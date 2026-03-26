package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type PathScanner struct {
	rootPath        string
	reportChildDirs bool
	scanConcurrency int
}

func NewPathScanner(rootPath string, reportChildDirs bool, scanConcurrency int) *PathScanner {
	if scanConcurrency < 1 {
		scanConcurrency = 1
	}

	return &PathScanner{
		rootPath:        filepath.Clean(rootPath),
		reportChildDirs: reportChildDirs,
		scanConcurrency: scanConcurrency,
	}
}

func ValidatePath(target string) error {
	_, err := statPath(filepath.Clean(target))
	return err
}

func (s *PathScanner) Scan(ctx context.Context) ([]usage.PathUsage, error) {
	rootStats, err := statPath(s.rootPath)
	if err != nil {
		return nil, err
	}

	rootLabel := filepath.ToSlash(filepath.Clean(s.rootPath))
	if !s.reportChildDirs {
		var mu sync.Mutex
		if err := walkRegularFiles(ctx, s.rootPath, s.scanConcurrency, nil, func(size float64) {
			mu.Lock()
			rootStats.UsedBytes += size
			mu.Unlock()
		}); err != nil {
			return nil, err
		}

		return []usage.PathUsage{{
			Path:           rootLabel,
			CapacityBytes:  rootStats.CapacityBytes,
			AvailableBytes: rootStats.AvailableBytes,
			UsedBytes:      rootStats.UsedBytes,
		}}, nil
	}

	entries, err := os.ReadDir(s.rootPath)
	if err != nil {
		return nil, fmt.Errorf("read root directory %s: %w", s.rootPath, err)
	}

	childPaths := make(map[string]string)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childPaths[entry.Name()] = path.Join(rootLabel, entry.Name())
	}

	usedBytes := map[string]float64{
		rootLabel: 0,
	}
	for _, label := range childPaths {
		usedBytes[label] = 0
	}

	var mu sync.Mutex
	err = walkRegularFiles(ctx, s.rootPath, s.scanConcurrency, func(filePath string, size float64) error {
		mu.Lock()
		defer mu.Unlock()

		usedBytes[rootLabel] += size
		relativePath, err := filepath.Rel(s.rootPath, filePath)
		if err != nil {
			return fmt.Errorf("make relative path for %s: %w", filePath, err)
		}

		firstComponent := strings.Split(relativePath, string(filepath.Separator))[0]
		if label, ok := childPaths[firstComponent]; ok {
			usedBytes[label] += size
		}

		return nil
	}, nil)
	if err != nil {
		return nil, err
	}

	results := make([]usage.PathUsage, 0, len(childPaths)+1)
	results = append(results, usage.PathUsage{
		Path:           rootLabel,
		CapacityBytes:  rootStats.CapacityBytes,
		AvailableBytes: rootStats.AvailableBytes,
		UsedBytes:      usedBytes[rootLabel],
	})

	childNames := make([]string, 0, len(childPaths))
	for name := range childPaths {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	for _, childName := range childNames {
		localPath := filepath.Join(s.rootPath, childName)
		label := childPaths[childName]

		stats, err := statPath(localPath)
		if err != nil {
			if shouldIgnoreMissingPath(err) {
				continue
			}

			return nil, err
		}

		results = append(results, usage.PathUsage{
			Path:           label,
			CapacityBytes:  stats.CapacityBytes,
			AvailableBytes: stats.AvailableBytes,
			UsedBytes:      usedBytes[label],
		})
	}

	return results, nil
}

func walkRegularFiles(
	ctx context.Context,
	rootPath string,
	concurrency int,
	perFile func(filePath string, size float64) error,
	aggregate func(size float64),
) error {
	return walkRegularFilesWithIO(ctx, rootPath, concurrency, perFile, aggregate, os.ReadDir, os.Lstat)
}

type readDirFunc func(string) ([]os.DirEntry, error)
type lstatFunc func(string) (fs.FileInfo, error)

type walkJobKind int

const (
	walkJobDir walkJobKind = iota
	walkJobFile
)

type walkJob struct {
	kind walkJobKind
	path string
}

var errWalkQueueClosed = errors.New("walk queue closed")

type walkQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []walkJob
	head    int
	pending int
	closed  bool
}

func newWalkQueue() *walkQueue {
	queue := &walkQueue{}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *walkQueue) Enqueue(job walkJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return errWalkQueueClosed
	}

	q.jobs = append(q.jobs, job)
	q.pending++
	q.cond.Signal()
	return nil
}

func (q *walkQueue) Dequeue() (walkJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for q.head >= len(q.jobs) && !q.closed {
		q.cond.Wait()
	}

	if q.head >= len(q.jobs) {
		return walkJob{}, false
	}

	job := q.jobs[q.head]
	q.head++
	if q.head >= len(q.jobs) {
		q.jobs = nil
		q.head = 0
	}

	return job, true
}

func (q *walkQueue) Done() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.pending == 0 {
		return
	}

	q.pending--
	if q.pending == 0 {
		q.closed = true
		q.cond.Broadcast()
	}
}

func (q *walkQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return
	}

	q.closed = true
	q.cond.Broadcast()
}

func walkRegularFilesWithIO(
	ctx context.Context,
	rootPath string,
	concurrency int,
	perFile func(filePath string, size float64) error,
	aggregate func(size float64),
	readDir readDirFunc,
	lstat lstatFunc,
) error {
	if concurrency < 1 {
		concurrency = 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := newWalkQueue()
	go func() {
		<-ctx.Done()
		queue.Close()
	}()

	var errMu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		if err == nil {
			return
		}

		errMu.Lock()
		defer errMu.Unlock()

		if firstErr != nil {
			return
		}

		firstErr = err
		cancel()
		queue.Close()
	}

	processDir := func(dirPath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		entries, err := readDir(dirPath)
		if err != nil {
			if shouldIgnoreMissingPath(err) {
				return nil
			}

			return fmt.Errorf("read directory %s: %w", dirPath, err)
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}

			entryPath := filepath.Join(dirPath, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}

			jobKind := walkJobFile
			if entry.IsDir() {
				jobKind = walkJobDir
			}

			if err := queue.Enqueue(walkJob{
				kind: jobKind,
				path: entryPath,
			}); err != nil {
				if errors.Is(err, errWalkQueueClosed) {
					return ctx.Err()
				}

				return err
			}
		}

		return nil
	}

	processFile := func(filePath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		fileInfo, err := lstat(filePath)
		if err != nil {
			if shouldIgnoreMissingPath(err) {
				return nil
			}

			return fmt.Errorf("stat file %s: %w", filePath, err)
		}

		if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			return nil
		}

		size := float64(fileInfo.Size())
		if aggregate != nil {
			aggregate(size)
		}

		if perFile != nil {
			return perFile(filePath, size)
		}

		return nil
	}

	if err := queue.Enqueue(walkJob{
		kind: walkJobDir,
		path: rootPath,
	}); err != nil {
		return err
	}

	var workerWG sync.WaitGroup
	for range concurrency {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()

			for {
				job, ok := queue.Dequeue()
				if !ok {
					return
				}

				var err error
				switch job.kind {
				case walkJobDir:
					err = processDir(job.path)
				case walkJobFile:
					err = processFile(job.path)
				}

				queue.Done()

				if err == nil {
					continue
				}

				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					continue
				}

				recordErr(err)
			}
		}()
	}

	workerWG.Wait()

	if firstErr != nil {
		return firstErr
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return nil
}

func shouldIgnoreMissingPath(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func statPath(target string) (usage.PathUsage, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(target, &stats); err != nil {
		return usage.PathUsage{}, fmt.Errorf("stat filesystem for %s: %w", target, err)
	}

	blockSize := float64(stats.Bsize)
	return usage.PathUsage{
		CapacityBytes:  float64(stats.Blocks) * blockSize,
		AvailableBytes: float64(stats.Bavail) * blockSize,
	}, nil
}

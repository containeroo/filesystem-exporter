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

	"golang.org/x/sys/unix"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type PathScanner struct {
	rootPath        string
	reportChildDirs bool
}

func NewPathScanner(rootPath string, reportChildDirs bool) *PathScanner {
	return &PathScanner{
		rootPath:        filepath.Clean(rootPath),
		reportChildDirs: reportChildDirs,
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
		if err := walkRegularFiles(ctx, s.rootPath, nil, func(size float64) {
			rootStats.UsedBytes += size
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

	err = walkRegularFiles(ctx, s.rootPath, func(filePath string, size float64) error {
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
	perFile func(filePath string, size float64) error,
	aggregate func(size float64),
) error {
	return walkRegularFilesWithWalker(ctx, rootPath, perFile, aggregate, filepath.WalkDir)
}

func walkRegularFilesWithWalker(
	ctx context.Context,
	rootPath string,
	perFile func(filePath string, size float64) error,
	aggregate func(size float64),
	walkDir func(root string, fn fs.WalkDirFunc) error,
) error {
	return walkDir(rootPath, func(filePath string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if shouldIgnoreMissingPath(walkErr) {
				return nil
			}

			return walkErr
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if filePath == rootPath {
			return nil
		}

		if dirEntry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if dirEntry.IsDir() {
			return nil
		}

		fileInfo, err := dirEntry.Info()
		if err != nil {
			if shouldIgnoreMissingPath(err) {
				return nil
			}

			return fmt.Errorf("stat file %s: %w", filePath, err)
		}
		if !fileInfo.Mode().IsRegular() {
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
	})
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

package scanner

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

func TestPathScannerReportsOnlyRootByDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	mustMkdirAll(t, filepath.Join(root, "archive", "nested"))
	mustMkdirAll(t, filepath.Join(root, "uploads"))

	mustWriteFileWithSize(t, filepath.Join(root, "root.bin"), 11)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "a.bin"), 13)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "nested", "b.bin"), 17)
	mustWriteFileWithSize(t, filepath.Join(root, "uploads", "c.bin"), 19)

	if err := os.Symlink(filepath.Join(root, "archive", "a.bin"), filepath.Join(root, "archive", "link.bin")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	scanner := NewPathScanner(root, false)
	results, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 usage entry, got %d", len(results))
	}

	assertUsage(t, results, root, 60)
}

func TestPathScannerReportsRootAndDepthOneDirectoriesWhenEnabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	mustMkdirAll(t, filepath.Join(root, "archive", "nested"))
	mustMkdirAll(t, filepath.Join(root, "uploads"))

	mustWriteFileWithSize(t, filepath.Join(root, "root.bin"), 11)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "a.bin"), 13)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "nested", "b.bin"), 17)
	mustWriteFileWithSize(t, filepath.Join(root, "uploads", "c.bin"), 19)

	if err := os.Symlink(filepath.Join(root, "archive", "a.bin"), filepath.Join(root, "archive", "link.bin")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	scanner := NewPathScanner(root, true)
	results, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 usage entries, got %d", len(results))
	}

	assertUsage(t, results, root, 60)
	assertUsage(t, results, filepath.ToSlash(filepath.Join(root, "archive")), 30)
	assertUsage(t, results, filepath.ToSlash(filepath.Join(root, "uploads")), 19)

	for _, result := range results {
		if result.CapacityBytes <= 0 {
			t.Fatalf("expected capacity for %s to be > 0, got %v", result.Path, result.CapacityBytes)
		}
		if result.AvailableBytes < 0 {
			t.Fatalf("expected available bytes for %s to be >= 0, got %v", result.Path, result.AvailableBytes)
		}
	}
}

func TestWalkRegularFilesIgnoresMissingEntries(t *testing.T) {
	t.Parallel()

	var sizes []float64
	err := walkRegularFilesWithWalker(
		context.Background(),
		"/root",
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string, fn fs.WalkDirFunc) error {
			if err := fn(root, fakeDirEntry{fileInfo: fakeFileInfo{mode: fs.ModeDir}}, nil); err != nil {
				return err
			}

			if err := fn(filepath.Join(root, "gone.bin"), fakeDirEntry{}, fs.ErrNotExist); err != nil {
				return err
			}

			return fn(filepath.Join(root, "still-there.bin"), fakeDirEntry{
				fileInfo: fakeFileInfo{size: 19, mode: 0},
			}, nil)
		},
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithWalker() error = %v", err)
	}

	if len(sizes) != 1 || sizes[0] != 19 {
		t.Fatalf("expected counted sizes [19], got %v", sizes)
	}
}

func TestWalkRegularFilesIgnoresMissingInfoForEntries(t *testing.T) {
	t.Parallel()

	var sizes []float64
	err := walkRegularFilesWithWalker(
		context.Background(),
		"/root",
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string, fn fs.WalkDirFunc) error {
			if err := fn(root, fakeDirEntry{fileInfo: fakeFileInfo{mode: fs.ModeDir}}, nil); err != nil {
				return err
			}

			return fn(filepath.Join(root, "gone-after-readdir.bin"), fakeDirEntry{
				fileInfo: fakeFileInfo{size: 17, mode: 0},
				infoErr:  fs.ErrNotExist,
			}, nil)
		},
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithWalker() error = %v", err)
	}

	if len(sizes) != 0 {
		t.Fatalf("expected no counted sizes, got %v", sizes)
	}
}

func assertUsage(t *testing.T, results []usage.PathUsage, path string, wantUsed float64) {
	t.Helper()

	for _, result := range results {
		if result.Path == path {
			if result.UsedBytes != wantUsed {
				t.Fatalf("usage for %s = %v, want %v", path, result.UsedBytes, wantUsed)
			}
			return
		}
	}

	t.Fatalf("usage for %s not found", path)
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", dir, err)
	}
}

func mustWriteFileWithSize(t *testing.T, filePath string, size int) {
	t.Helper()

	if err := os.WriteFile(filePath, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", filePath, err)
	}
}

type fakeDirEntry struct {
	fileInfo fakeFileInfo
	infoErr  error
}

func (d fakeDirEntry) Name() string               { return "" }
func (d fakeDirEntry) IsDir() bool                { return d.fileInfo.mode.IsDir() }
func (d fakeDirEntry) Type() fs.FileMode          { return d.fileInfo.mode.Type() }
func (d fakeDirEntry) Info() (fs.FileInfo, error) { return d.fileInfo, d.infoErr }

type fakeFileInfo struct {
	size int64
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

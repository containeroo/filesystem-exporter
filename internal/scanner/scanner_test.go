package scanner

import (
	"context"
	"fmt"
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

	scanner := NewPathScanner(root, false, 1)
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

	scanner := NewPathScanner(root, true, 4)
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
	err := walkRegularFilesWithIO(
		context.Background(),
		"/root",
		4,
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string) ([]os.DirEntry, error) {
			switch root {
			case "/root":
				return []os.DirEntry{
					fakeDirEntry{name: "gone.bin"},
					fakeDirEntry{name: "still-there.bin"},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected ReadDir path %s", root)
			}
		},
		func(path string) (fs.FileInfo, error) {
			switch path {
			case "/root/gone.bin":
				return nil, fs.ErrNotExist
			case "/root/still-there.bin":
				return fakeFileInfo{size: 19, mode: 0}, nil
			default:
				return nil, fmt.Errorf("unexpected Lstat path %s", path)
			}
		},
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithIO() error = %v", err)
	}

	if len(sizes) != 1 || sizes[0] != 19 {
		t.Fatalf("expected counted sizes [19], got %v", sizes)
	}
}

func TestWalkRegularFilesIgnoresMissingInfoForEntries(t *testing.T) {
	t.Parallel()

	var sizes []float64
	err := walkRegularFilesWithIO(
		context.Background(),
		"/root",
		4,
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string) ([]os.DirEntry, error) {
			switch root {
			case "/root":
				return []os.DirEntry{
					fakeDirEntry{name: "gone-after-readdir.bin"},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected ReadDir path %s", root)
			}
		},
		func(path string) (fs.FileInfo, error) {
			switch path {
			case "/root/gone-after-readdir.bin":
				return nil, fs.ErrNotExist
			default:
				return nil, fmt.Errorf("unexpected Lstat path %s", path)
			}
		},
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithIO() error = %v", err)
	}

	if len(sizes) != 0 {
		t.Fatalf("expected no counted sizes, got %v", sizes)
	}
}

func TestWalkRegularFilesIgnoresMissingDirectories(t *testing.T) {
	t.Parallel()

	var sizes []float64
	err := walkRegularFilesWithIO(
		context.Background(),
		"/root",
		4,
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string) ([]os.DirEntry, error) {
			switch root {
			case "/root":
				return []os.DirEntry{
					fakeDirEntry{name: "gone-dir", mode: fs.ModeDir},
					fakeDirEntry{name: "still-there.bin"},
				}, nil
			case "/root/gone-dir":
				return nil, fs.ErrNotExist
			default:
				return nil, fmt.Errorf("unexpected ReadDir path %s", root)
			}
		},
		func(path string) (fs.FileInfo, error) {
			switch path {
			case "/root/still-there.bin":
				return fakeFileInfo{size: 23, mode: 0}, nil
			default:
				return nil, fmt.Errorf("unexpected Lstat path %s", path)
			}
		},
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithIO() error = %v", err)
	}

	if len(sizes) != 1 || sizes[0] != 23 {
		t.Fatalf("expected counted sizes [23], got %v", sizes)
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
	name string
	mode fs.FileMode
}

func (d fakeDirEntry) Name() string               { return d.name }
func (d fakeDirEntry) IsDir() bool                { return d.mode.IsDir() }
func (d fakeDirEntry) Type() fs.FileMode          { return d.mode.Type() }
func (d fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{mode: d.mode}, nil }

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

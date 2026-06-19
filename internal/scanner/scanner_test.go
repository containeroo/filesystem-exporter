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
	result, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Usages) != 1 {
		t.Fatalf("expected 1 usage entry, got %d", len(result.Usages))
	}

	assertUsage(t, result.Usages, root, 60)
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
	result, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Usages) != 3 {
		t.Fatalf("expected 3 usage entries, got %d", len(result.Usages))
	}

	assertUsage(t, result.Usages, root, 60)
	assertUsage(t, result.Usages, filepath.ToSlash(filepath.Join(root, "archive")), 30)
	assertUsage(t, result.Usages, filepath.ToSlash(filepath.Join(root, "uploads")), 19)

	for _, usage := range result.Usages {
		if usage.CapacityBytes <= 0 {
			t.Fatalf("expected capacity for %s to be > 0, got %v", usage.Path, usage.CapacityBytes)
		}
		if usage.AvailableBytes < 0 {
			t.Fatalf("expected available bytes for %s to be >= 0, got %v", usage.Path, usage.AvailableBytes)
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
		nil,
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
		nil,
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
		nil,
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithIO() error = %v", err)
	}

	if len(sizes) != 1 || sizes[0] != 23 {
		t.Fatalf("expected counted sizes [23], got %v", sizes)
	}
}

func TestWalkRegularFilesDoesNotDeadlockWithSingleWorkerAndManyEntries(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var sizes []float64
	err := walkRegularFilesWithIO(
		ctx,
		"/root",
		1,
		nil,
		func(size float64) {
			sizes = append(sizes, size)
		},
		func(root string) ([]os.DirEntry, error) {
			switch root {
			case "/root":
				return []os.DirEntry{
					fakeDirEntry{name: "a.bin"},
					fakeDirEntry{name: "b.bin"},
					fakeDirEntry{name: "c.bin"},
					fakeDirEntry{name: "d.bin"},
					fakeDirEntry{name: "e.bin"},
					fakeDirEntry{name: "f.bin"},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected ReadDir path %s", root)
			}
		},
		func(path string) (fs.FileInfo, error) {
			switch path {
			case "/root/a.bin", "/root/b.bin", "/root/c.bin", "/root/d.bin", "/root/e.bin", "/root/f.bin":
				return fakeFileInfo{size: 1, mode: 0}, nil
			default:
				return nil, fmt.Errorf("unexpected Lstat path %s", path)
			}
		},
		nil,
	)
	if err != nil {
		t.Fatalf("walkRegularFilesWithIO() error = %v", err)
	}

	if len(sizes) != 6 {
		t.Fatalf("expected 6 counted sizes, got %v", sizes)
	}
}

func TestResolveMountUsesLongestMatchingMountPoint(t *testing.T) {
	t.Parallel()

	mount := resolveMount("/data/archive", []mountInfo{
		{mountPoint: "/", source: "overlay", fsType: "overlay"},
		{mountPoint: "/data", source: "nfs.example.com:/export", fsType: "nfs4"},
		{mountPoint: "/data/archive", source: "nfs.example.com:/archive", fsType: "nfs4"},
		{mountPoint: "/database", source: "nfs.example.com:/database", fsType: "nfs4"},
	})

	if mount.source != "nfs.example.com:/archive" || mount.fsType != "nfs4" {
		t.Fatalf("resolved mount = %#v", mount)
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

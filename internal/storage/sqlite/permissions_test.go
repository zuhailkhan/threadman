package sqlite

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// A new database and its directory are readable by their owner only, and so
// are the journal files SQLite creates beside it once it is written to.
func TestPrepareDatabaseFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes don't apply; the profile's access control lists keep it private")
	}
	path := filepath.Join(t.TempDir(), "share", "threadman", "threads.db")
	if err := PrepareDatabaseFile(path); err != nil {
		t.Fatal(err)
	}

	if _, err := NewRepository(path + "?_fk=1"); err != nil {
		t.Fatal(err)
	}

	if got := mode(t, filepath.Dir(path)); got != 0o700 {
		t.Errorf("directory mode %o, want 700", got)
	}
	if got := mode(t, path); got != 0o600 {
		t.Errorf("database mode %o, want 600", got)
	}
}

// An install made by an earlier version, with a world-readable directory and
// database, is tightened.
func TestPrepareDatabaseFileTightensAnExistingInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes don't apply; the profile's access control lists keep it private")
	}
	dir := filepath.Join(t.TempDir(), "threadman")
	path := filepath.Join(dir, "threads.db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil { // in case the umask narrowed it
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := PrepareDatabaseFile(path); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, dir); got != 0o700 {
		t.Errorf("directory mode %o, want 700", got)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if got := mode(t, p); got != 0o600 {
			t.Errorf("%s mode %o, want 600", filepath.Base(p), got)
		}
	}
}

func TestPrepareDatabaseFileCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "threads.db")
	if err := PrepareDatabaseFile(path); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}

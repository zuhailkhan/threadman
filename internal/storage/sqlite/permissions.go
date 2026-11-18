package sqlite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// PrepareDatabaseFile creates the directory that holds the database at path
// and makes it, the database and SQLite's companion files readable by the
// current user only. The database holds every conversation threadman has
// imported, so another user on the machine must not be able to read it.
//
// The database file is created empty before SQLite opens it, because SQLite
// would create it with 0644, and its journal and WAL files take the
// database's mode. Directories and files left readable by earlier versions
// are tightened.
//
// On Windows the user's profile is already private to them through its access
// control lists, and Unix mode bits don't apply, so only the directory is
// created.
func PrepareDatabaseFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, p := range []string{path, path + "-journal", path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

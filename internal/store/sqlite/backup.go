package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Backup writes a consistent snapshot of the database to path, which must not
// exist. The snapshot holds the chat history, so path is created with mode
// 0600 before anything is written to it; a failed backup removes it again.
func (db *DB) Backup(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("backup: %s already exists", path)
	}
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	err = f.Chmod(0o600)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_, err = db.r.ExecContext(ctx, `VACUUM INTO ?`, path)
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

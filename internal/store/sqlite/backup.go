package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Backup writes a consistent snapshot of the database to path, which must not exist.
func (db *DB) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("backup: %s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: %w", err)
	}
	if _, err := db.r.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

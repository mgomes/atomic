package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"

	"modernc.org/sqlite"

	"github.com/mgomes/ressik/internal/fsname"
)

const (
	applicationID   = 0x5253494b
	schemaVersion   = 1
	backupStepPages = 64

	// DefaultMaxBytes is the default plaintext catalog size limit.
	DefaultMaxBytes int64 = 64 << 20
)

//go:embed schema.sql
var schemaSQL string

type backuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

type serializer interface {
	Serialize() ([]byte, error)
	Deserialize([]byte) error
}

// Encode validates snapshot and materializes it as an immutable SQLite image.
// The caller owns the returned plaintext bytes and should clear them after use.
func Encode(ctx context.Context, snapshot Snapshot, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkMaxBytes(maxBytes); err != nil {
		return nil, err
	}
	if err := Validate(snapshot); err != nil {
		return nil, fmt.Errorf("validate snapshot: %w", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open catalog database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire catalog connection: %w", err)
	}
	defer conn.Close()
	if err := initialize(ctx, conn, maxBytes); err != nil {
		return nil, err
	}
	if err := insertSnapshot(ctx, conn, snapshot); err != nil {
		return nil, err
	}
	if err := checkEstimatedSize(ctx, conn, maxBytes); err != nil {
		return nil, err
	}

	image, err := backupImage(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("materialize catalog: %w", err)
	}
	if int64(len(image)) > maxBytes {
		clear(image)
		return nil, fmt.Errorf("catalog has %d bytes, maximum is %d", len(image), maxBytes)
	}
	return image, nil
}

func initialize(ctx context.Context, conn *sql.Conn, maxBytes int64) error {
	setup := fmt.Sprintf(`
		PRAGMA foreign_keys = ON;
		PRAGMA application_id = %d;
		PRAGMA user_version = %d;
	`, applicationID, schemaVersion)
	if _, err := conn.ExecContext(ctx, setup); err != nil {
		return fmt.Errorf("configure catalog database: %w", err)
	}
	if err := setPageLimit(ctx, conn, maxBytes); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create catalog schema: %w", err)
	}
	return nil
}

func setPageLimit(ctx context.Context, conn *sql.Conn, maxBytes int64) error {
	var pageSize int64
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fmt.Errorf("read catalog page size: %w", err)
	}
	if pageSize <= 0 {
		return fmt.Errorf("catalog page size is invalid: %d", pageSize)
	}
	pageLimit := maxBytes / pageSize
	if pageLimit == 0 {
		return fmt.Errorf("catalog maximum %d is smaller than one %d-byte page", maxBytes, pageSize)
	}
	var applied int64
	pragma := fmt.Sprintf("PRAGMA max_page_count = %d", pageLimit)
	if err := conn.QueryRowContext(ctx, pragma).Scan(&applied); err != nil {
		return fmt.Errorf("limit catalog pages: %w", err)
	}
	if applied > pageLimit {
		return fmt.Errorf("catalog already requires %d pages, maximum is %d", applied, pageLimit)
	}
	return nil
}

func insertSnapshot(ctx context.Context, conn *sql.Conn, snapshot Snapshot) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog transaction: %w", err)
	}
	defer tx.Rollback()

	createdAt := snapshot.CreatedAt.UTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO snapshot (
			singleton, snapshot_id, configuration_id, plan_id, plan_name,
			created_at_seconds, created_at_nanoseconds, root_digest, chunk_size,
			files, directories, symlinks, plaintext_bytes, new_blocks,
			reused_blocks, stored_bytes
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		snapshot.ID[:], snapshot.ConfigurationID, snapshot.PlanID, snapshot.PlanName,
		createdAt.Unix(), createdAt.Nanosecond(), snapshot.Root[:], snapshot.ChunkSize,
		snapshot.Statistics.Files, snapshot.Statistics.Directories,
		snapshot.Statistics.Symlinks, snapshot.Statistics.PlaintextBytes,
		snapshot.Statistics.NewBlocks, snapshot.Statistics.ReusedBlocks,
		snapshot.Statistics.StoredBytes,
	)
	if err != nil {
		return fmt.Errorf("insert catalog snapshot: %w", err)
	}

	for _, source := range snapshot.Sources {
		if err := insertSource(ctx, tx, source); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog transaction: %w", err)
	}
	return nil
}

func insertSource(ctx context.Context, tx *sql.Tx, source Source) error {
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO sources (source_id, original_path, digest) VALUES (?, ?, ?)",
		source.ID,
		source.OriginalPath,
		source.Digest[:],
	); err != nil {
		return fmt.Errorf("insert catalog source %q: %w", source.ID, err)
	}
	for _, entry := range source.Entries {
		if err := insertEntry(ctx, tx, source.ID, entry); err != nil {
			return err
		}
	}
	return nil
}

func insertEntry(ctx context.Context, tx *sql.Tx, sourceID string, entry Entry) error {
	modifiedAt := entry.ModifiedAt.UTC()
	var parent any
	if entry.Path != "." {
		parent = parentPath(entry.Path)
	}
	linkTarget, linkKind, changeToken := entryValues(entry)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO entries (
			source_id, path, folded_path, parent_path, kind, mode,
			modified_at_seconds, modified_at_nanoseconds, size, digest,
			link_target, link_kind, change_token
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sourceID, entry.Path, fsname.Fold(entry.Path), parent, string(entry.Kind),
		int64(entry.Mode), modifiedAt.Unix(), modifiedAt.Nanosecond(), entry.Size,
		entry.Digest[:], linkTarget, linkKind, changeToken,
	)
	if err != nil {
		return fmt.Errorf("insert catalog path %q in source %q: %w", entry.Path, sourceID, err)
	}
	for ordinal, ref := range entry.Blocks {
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO blocks (block_id, plaintext_length) VALUES (?, ?) ON CONFLICT (block_id) DO NOTHING",
			ref.ID[:],
			ref.Length,
		); err != nil {
			return fmt.Errorf("insert catalog block %s: %w", ref.ID, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO entry_blocks (source_id, path, ordinal, block_id) VALUES (?, ?, ?, ?)",
			sourceID,
			entry.Path,
			ordinal,
			ref.ID[:],
		); err != nil {
			return fmt.Errorf("insert block %d for catalog path %q: %w", ordinal, entry.Path, err)
		}
	}
	return nil
}

func entryValues(entry Entry) (linkTarget, linkKind, changeToken any) {
	switch entry.Kind {
	case FileEntry:
		changeToken = entry.ChangeToken
	case SymlinkEntry:
		linkTarget = entry.LinkTarget
		linkKind = string(entry.LinkKind)
	}
	return linkTarget, linkKind, changeToken
}

func checkEstimatedSize(ctx context.Context, conn *sql.Conn, maxBytes int64) error {
	var pageCount, pageSize int64
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return fmt.Errorf("read catalog page count: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fmt.Errorf("read catalog page size: %w", err)
	}
	if pageSize <= 0 {
		return fmt.Errorf("catalog page size is invalid: %d", pageSize)
	}
	if pageCount > maxBytes/pageSize {
		return fmt.Errorf("catalog requires at least %d bytes, maximum is %d", pageCount*pageSize, maxBytes)
	}
	return nil
}

func backupImage(ctx context.Context, conn *sql.Conn) (image []byte, finalErr error) {
	err := conn.Raw(func(raw any) error {
		source, ok := raw.(backuper)
		if !ok {
			return errors.New("SQLite driver does not support online backup")
		}
		backup, err := source.NewBackup(":memory:")
		if err != nil {
			return err
		}
		consumed := false
		defer func() {
			if !consumed {
				finalErr = errors.Join(finalErr, backup.Finish())
			}
		}()

		for more := true; more; {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err = backup.Step(backupStepPages)
			if err != nil {
				return err
			}
		}

		consumed = true
		destination, err := backup.Commit()
		if err != nil {
			return err
		}
		defer func() {
			finalErr = errors.Join(finalErr, destination.Close())
		}()

		exec, ok := destination.(driver.ExecerContext)
		if !ok {
			return errors.New("SQLite driver does not support direct execution")
		}
		if _, err := exec.ExecContext(ctx, "VACUUM", nil); err != nil {
			return fmt.Errorf("normalize catalog journal mode: %w", err)
		}
		snapshot, ok := destination.(serializer)
		if !ok {
			return errors.New("SQLite driver does not support serialization")
		}
		image, err = snapshot.Serialize()
		return err
	})
	if err != nil {
		return nil, errors.Join(err, finalErr)
	}
	if finalErr != nil {
		clear(image)
		return nil, finalErr
	}
	if len(image) < 20 || image[18] != 1 || image[19] != 1 {
		clear(image)
		return nil, errors.New("serialized catalog retained an unsupported journal mode")
	}
	return image, nil
}

func checkMaxBytes(maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("catalog maximum must be positive: %d", maxBytes)
	}
	if uint64(maxBytes) > uint64(^uint(0)>>1) {
		return fmt.Errorf("catalog maximum %d exceeds platform capacity", maxBytes)
	}
	return nil
}

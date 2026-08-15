package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mgomes/atomic/internal/chunk"
	"github.com/mgomes/atomic/internal/fsname"
)

const digestSize = 32

// ErrNotFound reports that a source-relative path is absent from a catalog.
var ErrNotFound = errors.New("catalog entry not found")

// Reader queries one immutable catalog image.
type Reader struct {
	mu     sync.Mutex
	db     *sql.DB
	conn   *sql.Conn
	closed bool
}

// Open validates image and opens it as an immutable catalog.
func Open(ctx context.Context, image []byte, maxBytes int64) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkMaxBytes(maxBytes); err != nil {
		return nil, err
	}
	if int64(len(image)) > maxBytes {
		return nil, fmt.Errorf("catalog has %d bytes, maximum is %d", len(image), maxBytes)
	}
	if len(image) < 100 {
		return nil, errors.New("catalog image is truncated")
	}
	if image[18] != 1 || image[19] != 1 {
		return nil, fmt.Errorf("catalog has unsupported journal header %d/%d", image[18], image[19])
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open catalog reader: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("acquire catalog reader connection: %w", err)
	}

	reader := &Reader{db: db, conn: conn}
	if err := conn.Raw(func(raw any) error {
		destination, ok := raw.(serializer)
		if !ok {
			return errors.New("SQLite driver does not support deserialization")
		}
		return destination.Deserialize(image)
	}); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("deserialize catalog: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON; PRAGMA foreign_keys = ON"); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("make catalog immutable: %w", err)
	}
	if err := reader.validate(ctx); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

// Close releases the catalog reader. It is safe to call Close more than once.
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return errors.Join(r.conn.Close(), r.db.Close())
}

// Snapshot loads and validates the complete logical snapshot.
func (r *Reader) Snapshot(ctx context.Context) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(ctx); err != nil {
		return Snapshot{}, err
	}

	snapshot, err := r.metadata(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	sources, err := r.sources(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Sources = sources
	if err := Validate(snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("validate catalog snapshot: %w", err)
	}
	return snapshot, nil
}

// Entry returns one source-relative entry and its ordered block references.
func (r *Reader) Entry(ctx context.Context, sourceID, path string) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(ctx); err != nil {
		return Entry{}, err
	}
	entry, err := r.entry(ctx, sourceID, path)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return entry, err
}

// Subtree returns an entry and all of its descendants in bytewise path order.
func (r *Reader) Subtree(ctx context.Context, sourceID, path string) ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := r.conn.QueryContext(ctx, `
		WITH RECURSIVE tree(path) AS (
			SELECT path FROM entries WHERE source_id = ? AND path = ?
			UNION
			SELECT child.path
			FROM entries AS child
			JOIN tree AS parent
			  ON child.source_id = ? AND child.parent_path = parent.path
		)
		SELECT e.path, e.kind, e.mode, e.modified_at_seconds,
		       e.modified_at_nanoseconds, e.size, e.digest,
		       e.link_target, e.link_kind, e.change_token
		FROM entries AS e
		JOIN tree ON tree.path = e.path
		WHERE e.source_id = ?
		ORDER BY e.path COLLATE BINARY
	`, sourceID, path, sourceID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("query catalog subtree %q: %w", path, err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, ErrNotFound
	}
	if err := r.loadSubtreeBlocks(ctx, sourceID, path, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Blocks returns each logical block reachable from the snapshot once.
func (r *Reader) Blocks(ctx context.Context) ([]BlockRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := r.conn.QueryContext(ctx, `
		SELECT blocks.block_id, blocks.plaintext_length
		FROM blocks
		WHERE EXISTS (
			SELECT 1 FROM entry_blocks WHERE entry_blocks.block_id = blocks.block_id
		)
		ORDER BY blocks.block_id
	`)
	if err != nil {
		return nil, fmt.Errorf("query catalog blocks: %w", err)
	}
	defer rows.Close()

	var blocks []BlockRef
	for rows.Next() {
		var rawID []byte
		var length int64
		if err := rows.Scan(&rawID, &length); err != nil {
			return nil, fmt.Errorf("scan catalog block: %w", err)
		}
		ref, err := blockRef(rawID, length)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog blocks: %w", err)
	}
	return blocks, nil
}

func (r *Reader) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed {
		return errors.New("catalog reader is closed")
	}
	return nil
}

func (r *Reader) validate(ctx context.Context) error {
	var gotApplicationID, gotVersion int
	if err := r.conn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&gotApplicationID); err != nil {
		return fmt.Errorf("read catalog application ID: %w", err)
	}
	if gotApplicationID != applicationID {
		return fmt.Errorf("catalog application ID is %#x, want %#x", gotApplicationID, applicationID)
	}
	if err := r.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&gotVersion); err != nil {
		return fmt.Errorf("read catalog schema version: %w", err)
	}
	if gotVersion != schemaVersion {
		return fmt.Errorf("catalog schema version is %d, want %d", gotVersion, schemaVersion)
	}

	var quickCheck string
	if err := r.conn.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil {
		return fmt.Errorf("check catalog integrity: %w", err)
	}
	if quickCheck != "ok" {
		return fmt.Errorf("catalog integrity check failed: %s", quickCheck)
	}
	if err := r.validateForeignKeys(ctx); err != nil {
		return err
	}
	if err := r.validateSchema(ctx); err != nil {
		return err
	}
	if err := r.validateRelationships(ctx); err != nil {
		return err
	}
	if err := r.validateStoredValues(ctx); err != nil {
		return err
	}
	return nil
}

func (r *Reader) validateForeignKeys(ctx context.Context) error {
	rows, err := r.conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check catalog foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("catalog contains a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check catalog foreign keys: %w", err)
	}
	return nil
}

func (r *Reader) validateSchema(ctx context.Context) error {
	want, err := expectedSchemaDefinitions()
	if err != nil {
		return err
	}
	rows, err := r.conn.QueryContext(ctx, `
		SELECT type, name, sql
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name
	`)
	if err != nil {
		return fmt.Errorf("read catalog schema: %w", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			return fmt.Errorf("scan catalog schema: %w", err)
		}
		key := kind + ":" + name
		expected, exists := want[key]
		if !exists {
			return fmt.Errorf("catalog contains unexpected schema object %q", key)
		}
		if normalizeSQL(definition) != expected {
			return fmt.Errorf("catalog schema object %q does not match version %d", key, schemaVersion)
		}
		got = append(got, key)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog schema: %w", err)
	}
	wantKeys := make([]string, 0, len(want))
	for key := range want {
		wantKeys = append(wantKeys, key)
	}
	slices.Sort(wantKeys)
	if !slices.Equal(got, wantKeys) {
		return fmt.Errorf("catalog schema objects are %v, want %v", got, wantKeys)
	}
	return nil
}

func expectedSchemaDefinitions() (map[string]string, error) {
	definitions := make(map[string]string)
	for statement := range strings.SplitSeq(schemaSQL, ";") {
		normalized := normalizeSQL(statement)
		if normalized == "" {
			continue
		}
		fields := strings.Fields(normalized)
		if len(fields) < 3 || fields[0] != "CREATE" || (fields[1] != "TABLE" && fields[1] != "INDEX") {
			return nil, fmt.Errorf("catalog schema contains an unsupported statement %q", normalized)
		}
		key := strings.ToLower(fields[1]) + ":" + fields[2]
		if _, exists := definitions[key]; exists {
			return nil, fmt.Errorf("catalog schema defines %q more than once", key)
		}
		definitions[key] = normalized
	}
	return definitions, nil
}

func normalizeSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

func (r *Reader) validateRelationships(ctx context.Context) error {
	checks := []struct {
		name  string
		query string
	}{
		{
			name:  "snapshot singleton",
			query: "SELECT count(*) FROM snapshot HAVING count(*) <> 1",
		},
		{
			name:  "sources",
			query: "SELECT count(*) FROM sources HAVING count(*) = 0",
		},
		{
			name: "source roots",
			query: `
				SELECT count(*)
				FROM sources
				LEFT JOIN entries
				  ON entries.source_id = sources.source_id AND entries.path = '.'
				WHERE entries.path IS NULL OR entries.digest <> sources.digest
				HAVING count(*) <> 0
			`,
		},
		{
			name: "entry sources",
			query: `
				SELECT count(*)
				FROM entries
				LEFT JOIN sources USING (source_id)
				WHERE sources.source_id IS NULL
				HAVING count(*) <> 0
			`,
		},
		{
			name: "directory parents",
			query: `
				SELECT count(*)
				FROM entries AS child
				LEFT JOIN entries AS parent
				  ON parent.source_id = child.source_id AND parent.path = child.parent_path
				WHERE child.path <> '.' AND (parent.path IS NULL OR parent.kind <> 'directory')
				HAVING count(*) <> 0
			`,
		},
		{
			name: "block owners",
			query: `
				SELECT count(*)
				FROM entry_blocks
				LEFT JOIN entries USING (source_id, path)
				WHERE entries.path IS NULL OR entries.kind <> 'file'
				HAVING count(*) <> 0
			`,
		},
		{
			name: "block references",
			query: `
				SELECT count(*)
				FROM entry_blocks
				LEFT JOIN blocks USING (block_id)
				WHERE blocks.block_id IS NULL
				HAVING count(*) <> 0
			`,
		},
		{
			name: "block ordinals",
			query: `
				SELECT count(*) FROM (
					SELECT source_id, path
					FROM entry_blocks
					GROUP BY source_id, path
					HAVING min(ordinal) <> 0 OR max(ordinal) <> count(*) - 1
					    OR count(DISTINCT ordinal) <> count(*)
				) HAVING count(*) <> 0
			`,
		},
		{
			name: "file lengths",
			query: `
				SELECT count(*) FROM (
					SELECT entries.source_id, entries.path
					FROM entries
					LEFT JOIN entry_blocks USING (source_id, path)
					LEFT JOIN blocks USING (block_id)
					WHERE entries.kind = 'file'
					GROUP BY entries.source_id, entries.path, entries.size
					HAVING coalesce(sum(blocks.plaintext_length), 0) <> entries.size
				) HAVING count(*) <> 0
			`,
		},
		{
			name: "unreachable blocks",
			query: `
				SELECT count(*)
				FROM blocks
				LEFT JOIN entry_blocks USING (block_id)
				WHERE entry_blocks.block_id IS NULL
				HAVING count(*) <> 0
			`,
		},
	}
	for _, check := range checks {
		var count int
		err := r.conn.QueryRowContext(ctx, check.query).Scan(&count)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return fmt.Errorf("validate catalog %s: %w", check.name, err)
		default:
			return fmt.Errorf("catalog has %d %s violations", count, check.name)
		}
	}
	return nil
}

func (r *Reader) validateStoredValues(ctx context.Context) error {
	snapshot, err := r.metadata(ctx)
	if err != nil {
		return err
	}
	if err := validateSnapshotMetadata(snapshot); err != nil {
		return fmt.Errorf("validate catalog metadata: %w", err)
	}
	if err := r.validateStoredSources(ctx); err != nil {
		return err
	}
	if err := r.validateStoredEntries(ctx); err != nil {
		return err
	}
	if err := r.validateFoldedPaths(ctx); err != nil {
		return err
	}
	if err := r.validateStoredBlocks(ctx); err != nil {
		return err
	}

	var actual Stats
	if err := r.conn.QueryRowContext(ctx, `
		SELECT
			coalesce(sum(CASE WHEN kind = 'file' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN kind = 'directory' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN kind = 'symlink' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN kind = 'file' THEN size ELSE 0 END), 0)
		FROM entries
	`).Scan(
		&actual.Files,
		&actual.Directories,
		&actual.Symlinks,
		&actual.PlaintextBytes,
	); err != nil {
		return fmt.Errorf("read catalog statistics: %w", err)
	}
	if actual.Files != snapshot.Statistics.Files ||
		actual.Directories != snapshot.Statistics.Directories ||
		actual.Symlinks != snapshot.Statistics.Symlinks ||
		actual.PlaintextBytes != snapshot.Statistics.PlaintextBytes {
		return errors.New("catalog statistics do not match its entries")
	}
	return nil
}

func (r *Reader) validateStoredSources(ctx context.Context) error {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT source_id, original_path, digest
		FROM sources ORDER BY source_id COLLATE BINARY
	`)
	if err != nil {
		return fmt.Errorf("query catalog sources for validation: %w", err)
	}
	defer rows.Close()

	count := 0
	var previousID string
	for rows.Next() {
		var sourceID, originalPath string
		var digest []byte
		if err := rows.Scan(&sourceID, &originalPath, &digest); err != nil {
			return fmt.Errorf("scan catalog source for validation: %w", err)
		}
		if !sourceIDPattern.MatchString(sourceID) {
			return fmt.Errorf("catalog source ID %q must match %s", sourceID, sourceIDPattern)
		}
		if err := fsname.Component(sourceID); err != nil {
			return fmt.Errorf("catalog source ID %q is not portable: %w", sourceID, err)
		}
		if originalPath == "" || !utf8.ValidString(originalPath) {
			return fmt.Errorf("catalog source %q has an invalid original path", sourceID)
		}
		if len(digest) != digestSize {
			return fmt.Errorf("catalog source %q has a %d-byte digest", sourceID, len(digest))
		}
		if count > 0 && sourceID == previousID {
			return fmt.Errorf("catalog source ID %q is duplicated", sourceID)
		}
		previousID = sourceID
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog sources for validation: %w", err)
	}
	if count == 0 {
		return errors.New("catalog contains no sources")
	}
	return nil
}

func (r *Reader) validateStoredEntries(ctx context.Context) error {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT source_id, path, folded_path, parent_path, kind, mode,
		       modified_at_seconds, modified_at_nanoseconds, size, digest,
		       link_target, link_kind, change_token
		FROM entries
		ORDER BY source_id COLLATE BINARY, path COLLATE BINARY
	`)
	if err != nil {
		return fmt.Errorf("query catalog entries for validation: %w", err)
	}
	defer rows.Close()

	var previousSource, previousPath string
	havePrevious := false
	for rows.Next() {
		var sourceID, path, folded, kind string
		var parent, linkTarget, linkKind, changeToken sql.NullString
		var mode, modifiedSeconds, modifiedNanoseconds, size int64
		var digest []byte
		if err := rows.Scan(
			&sourceID, &path, &folded, &parent, &kind, &mode,
			&modifiedSeconds, &modifiedNanoseconds, &size, &digest,
			&linkTarget, &linkKind, &changeToken,
		); err != nil {
			return fmt.Errorf("scan catalog entry for validation: %w", err)
		}
		if err := validateEntryPath(path); err != nil {
			return err
		}
		if folded != fsname.Fold(path) {
			return fmt.Errorf("catalog path %q has an invalid folded path", path)
		}
		if path == "." {
			if parent.Valid {
				return fmt.Errorf("catalog source %q root has a parent", sourceID)
			}
		} else if !parent.Valid || parent.String != parentPath(path) {
			return fmt.Errorf("catalog path %q has invalid parent %q", path, parent.String)
		}
		if mode < 0 || mode > math.MaxUint32 {
			return fmt.Errorf("catalog path %q has invalid mode %d", path, mode)
		}
		if modifiedNanoseconds < 0 || modifiedNanoseconds >= int64(time.Second) {
			return fmt.Errorf("catalog path %q has invalid modification nanoseconds %d", path, modifiedNanoseconds)
		}
		if time.Unix(modifiedSeconds, modifiedNanoseconds).UTC().IsZero() {
			return fmt.Errorf("catalog path %q has an empty modification time", path)
		}
		if len(digest) != digestSize {
			return fmt.Errorf("catalog path %q has a %d-byte digest", path, len(digest))
		}
		if havePrevious && sourceID == previousSource && path == previousPath {
			return fmt.Errorf("catalog source %q contains duplicate path %q", sourceID, path)
		}
		previousSource, previousPath = sourceID, path
		havePrevious = true
		fileMode := os.FileMode(mode)
		switch EntryKind(kind) {
		case DirectoryEntry:
			if !fileMode.IsDir() || size < 0 || linkTarget.Valid || linkKind.Valid || changeToken.Valid {
				return fmt.Errorf("catalog directory %q contains file data", path)
			}
		case FileEntry:
			if !fileMode.IsRegular() || size < 0 || linkTarget.Valid || linkKind.Valid || !changeToken.Valid || changeToken.String == "" || !utf8.ValidString(changeToken.String) {
				return fmt.Errorf("catalog file %q contains invalid metadata", path)
			}
		case SymlinkEntry:
			if fileMode&os.ModeSymlink == 0 || size < 0 || !linkTarget.Valid || linkTarget.String == "" || !utf8.ValidString(linkTarget.String) || !linkKind.Valid || changeToken.Valid {
				return fmt.Errorf("catalog symlink %q contains invalid metadata", path)
			}
			switch SymlinkTargetKind(linkKind.String) {
			case UnknownSymlinkTarget, FileSymlinkTarget, DirectorySymlinkTarget:
			default:
				return fmt.Errorf("catalog symlink %q has invalid target kind %q", path, linkKind.String)
			}
		default:
			return fmt.Errorf("catalog path %q has unknown kind %q", path, kind)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog entries for validation: %w", err)
	}
	return nil
}

func (r *Reader) validateFoldedPaths(ctx context.Context) error {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT source_id, folded_path, path
		FROM entries
		ORDER BY source_id COLLATE BINARY, folded_path COLLATE BINARY, path COLLATE BINARY
	`)
	if err != nil {
		return fmt.Errorf("query folded catalog paths: %w", err)
	}
	defer rows.Close()

	var previousSource, previousFolded, previousPath string
	havePrevious := false
	for rows.Next() {
		var sourceID, folded, path string
		if err := rows.Scan(&sourceID, &folded, &path); err != nil {
			return fmt.Errorf("scan folded catalog path: %w", err)
		}
		if havePrevious && sourceID == previousSource && folded == previousFolded {
			return fmt.Errorf("catalog source %q paths %q and %q collide on case-insensitive filesystems", sourceID, previousPath, path)
		}
		previousSource, previousFolded, previousPath = sourceID, folded, path
		havePrevious = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate folded catalog paths: %w", err)
	}
	return nil
}

func (r *Reader) validateStoredBlocks(ctx context.Context) error {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT block_id, plaintext_length FROM blocks ORDER BY block_id
	`)
	if err != nil {
		return fmt.Errorf("query catalog blocks for validation: %w", err)
	}
	defer rows.Close()
	var previousID string
	havePrevious := false
	for rows.Next() {
		var rawID []byte
		var length int64
		if err := rows.Scan(&rawID, &length); err != nil {
			return fmt.Errorf("scan catalog block for validation: %w", err)
		}
		ref, err := blockRef(rawID, length)
		if err != nil {
			return err
		}
		if ref.ID.IsZero() {
			return errors.New("catalog contains an empty block ID")
		}
		encodedID := string(ref.ID[:])
		if havePrevious && encodedID == previousID {
			return fmt.Errorf("catalog block ID %s is duplicated", ref.ID)
		}
		previousID = encodedID
		havePrevious = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog blocks for validation: %w", err)
	}
	return nil
}

func (r *Reader) metadata(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	var rawID, rawRoot []byte
	var createdSeconds, createdNanoseconds int64
	err := r.conn.QueryRowContext(ctx, `
		SELECT snapshot_id, configuration_id, plan_id, plan_name,
		       created_at_seconds, created_at_nanoseconds, root_digest, chunk_size,
		       files, directories, symlinks, plaintext_bytes, new_blocks,
		       reused_blocks, stored_bytes
		FROM snapshot WHERE singleton = 1
	`).Scan(
		&rawID, &snapshot.ConfigurationID, &snapshot.PlanID, &snapshot.PlanName,
		&createdSeconds, &createdNanoseconds, &rawRoot, &snapshot.ChunkSize,
		&snapshot.Statistics.Files, &snapshot.Statistics.Directories,
		&snapshot.Statistics.Symlinks, &snapshot.Statistics.PlaintextBytes,
		&snapshot.Statistics.NewBlocks, &snapshot.Statistics.ReusedBlocks,
		&snapshot.Statistics.StoredBytes,
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read catalog snapshot: %w", err)
	}
	if len(rawID) != len(snapshot.ID) || len(rawRoot) != len(snapshot.Root) {
		return Snapshot{}, errors.New("catalog snapshot contains a malformed ID or digest")
	}
	if createdNanoseconds < 0 || createdNanoseconds >= int64(time.Second) {
		return Snapshot{}, fmt.Errorf("catalog snapshot has invalid creation nanoseconds %d", createdNanoseconds)
	}
	copy(snapshot.ID[:], rawID)
	copy(snapshot.Root[:], rawRoot)
	snapshot.Version = Version
	snapshot.CreatedAt = time.Unix(createdSeconds, createdNanoseconds).UTC()
	return snapshot, nil
}

func (r *Reader) sources(ctx context.Context) ([]Source, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT source_id, original_path, digest
		FROM sources ORDER BY source_id COLLATE BINARY
	`)
	if err != nil {
		return nil, fmt.Errorf("query catalog sources: %w", err)
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var source Source
		var rawDigest []byte
		if err := rows.Scan(&source.ID, &source.OriginalPath, &rawDigest); err != nil {
			return nil, fmt.Errorf("scan catalog source: %w", err)
		}
		if len(rawDigest) != len(source.Digest) {
			return nil, fmt.Errorf("catalog source %q has a %d-byte digest", source.ID, len(rawDigest))
		}
		copy(source.Digest[:], rawDigest)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog sources: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close catalog sources: %w", err)
	}
	for i := range sources {
		entries, err := r.entries(ctx, sources[i].ID)
		if err != nil {
			return nil, err
		}
		sources[i].Entries = entries
	}
	return sources, nil
}

func (r *Reader) entries(ctx context.Context, sourceID string) ([]Entry, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT path, kind, mode, modified_at_seconds, modified_at_nanoseconds,
		       size, digest, link_target, link_kind, change_token
		FROM entries WHERE source_id = ? ORDER BY path COLLATE BINARY
	`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("query entries for catalog source %q: %w", sourceID, err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	if err := r.loadSourceBlocks(ctx, sourceID, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *Reader) entry(ctx context.Context, sourceID, path string) (Entry, error) {
	row := r.conn.QueryRowContext(ctx, `
		SELECT path, kind, mode, modified_at_seconds, modified_at_nanoseconds,
		       size, digest, link_target, link_kind, change_token
		FROM entries WHERE source_id = ? AND path = ?
	`, sourceID, path)
	entry, err := scanEntry(row)
	if err != nil {
		return Entry{}, err
	}
	blocks, err := r.blocks(ctx, sourceID, path)
	if err != nil {
		return Entry{}, err
	}
	entry.Blocks = blocks
	return entry, nil
}

type scanner interface {
	Scan(...any) error
}

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog entries: %w", err)
	}
	return entries, nil
}

func scanEntry(row scanner) (Entry, error) {
	var entry Entry
	var kind string
	var mode int64
	var modifiedSeconds, modifiedNanoseconds int64
	var rawDigest []byte
	var linkTarget, linkKind, changeToken sql.NullString
	if err := row.Scan(
		&entry.Path, &kind, &mode, &modifiedSeconds, &modifiedNanoseconds,
		&entry.Size, &rawDigest, &linkTarget, &linkKind, &changeToken,
	); err != nil {
		return Entry{}, err
	}
	if mode < 0 || mode > math.MaxUint32 {
		return Entry{}, fmt.Errorf("catalog path %q has invalid mode %d", entry.Path, mode)
	}
	if len(rawDigest) != len(entry.Digest) {
		return Entry{}, fmt.Errorf("catalog path %q has a %d-byte digest", entry.Path, len(rawDigest))
	}
	entry.Kind = EntryKind(kind)
	entry.Mode = uint32(mode)
	entry.ModifiedAt = time.Unix(modifiedSeconds, modifiedNanoseconds).UTC()
	copy(entry.Digest[:], rawDigest)
	entry.LinkTarget = linkTarget.String
	entry.LinkKind = SymlinkTargetKind(linkKind.String)
	entry.ChangeToken = changeToken.String
	return entry, nil
}

func (r *Reader) loadSourceBlocks(ctx context.Context, sourceID string, entries []Entry) error {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT entry_blocks.path, entry_blocks.ordinal,
		       blocks.block_id, blocks.plaintext_length
		FROM entry_blocks
		JOIN blocks USING (block_id)
		WHERE entry_blocks.source_id = ?
		ORDER BY entry_blocks.path COLLATE BINARY, entry_blocks.ordinal
	`, sourceID)
	if err != nil {
		return fmt.Errorf("query blocks for catalog source %q: %w", sourceID, err)
	}
	return mergeBlocks(rows, entries)
}

func (r *Reader) loadSubtreeBlocks(
	ctx context.Context,
	sourceID, root string,
	entries []Entry,
) error {
	rows, err := r.conn.QueryContext(ctx, `
		WITH RECURSIVE tree(path) AS (
			SELECT path FROM entries WHERE source_id = ? AND path = ?
			UNION
			SELECT child.path
			FROM entries AS child
			JOIN tree AS parent
			  ON child.source_id = ? AND child.parent_path = parent.path
		)
		SELECT entry_blocks.path, entry_blocks.ordinal,
		       blocks.block_id, blocks.plaintext_length
		FROM entry_blocks
		JOIN tree USING (path)
		JOIN blocks USING (block_id)
		WHERE entry_blocks.source_id = ?
		ORDER BY entry_blocks.path COLLATE BINARY, entry_blocks.ordinal
	`, sourceID, root, sourceID, sourceID)
	if err != nil {
		return fmt.Errorf("query blocks for catalog subtree %q: %w", root, err)
	}
	return mergeBlocks(rows, entries)
}

func mergeBlocks(rows *sql.Rows, entries []Entry) error {
	defer rows.Close()
	entryByPath := make(map[string]*Entry, len(entries))
	for i := range entries {
		entryByPath[entries[i].Path] = &entries[i]
	}
	for rows.Next() {
		var path string
		var ordinal int
		var rawID []byte
		var length int64
		if err := rows.Scan(&path, &ordinal, &rawID, &length); err != nil {
			return fmt.Errorf("scan catalog block: %w", err)
		}
		entry, exists := entryByPath[path]
		if !exists {
			return fmt.Errorf("catalog block refers to missing path %q", path)
		}
		if ordinal != len(entry.Blocks) {
			return fmt.Errorf("catalog path %q block ordinal is %d, want %d", path, ordinal, len(entry.Blocks))
		}
		ref, err := blockRef(rawID, length)
		if err != nil {
			return err
		}
		entry.Blocks = append(entry.Blocks, ref)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog blocks: %w", err)
	}
	return nil
}

func (r *Reader) blocks(ctx context.Context, sourceID, path string) ([]BlockRef, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT entry_blocks.ordinal, blocks.block_id, blocks.plaintext_length
		FROM entry_blocks
		JOIN blocks USING (block_id)
		WHERE entry_blocks.source_id = ? AND entry_blocks.path = ?
		ORDER BY entry_blocks.ordinal
	`, sourceID, path)
	if err != nil {
		return nil, fmt.Errorf("query blocks for catalog path %q: %w", path, err)
	}
	defer rows.Close()

	var blocks []BlockRef
	for rows.Next() {
		var ordinal int
		var rawID []byte
		var length int64
		if err := rows.Scan(&ordinal, &rawID, &length); err != nil {
			return nil, fmt.Errorf("scan block for catalog path %q: %w", path, err)
		}
		if ordinal != len(blocks) {
			return nil, fmt.Errorf("catalog path %q block ordinal is %d, want %d", path, ordinal, len(blocks))
		}
		ref, err := blockRef(rawID, length)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blocks for catalog path %q: %w", path, err)
	}
	return blocks, nil
}

func blockRef(rawID []byte, length int64) (BlockRef, error) {
	var ref BlockRef
	if len(rawID) != len(ref.ID) {
		return ref, fmt.Errorf("catalog block ID has %d bytes, want %d", len(rawID), len(ref.ID))
	}
	if length <= 0 || length > chunk.DefaultSize {
		return ref, fmt.Errorf("catalog block length is invalid: %d", length)
	}
	copy(ref.ID[:], rawID)
	ref.Length = uint32(length)
	return ref, nil
}

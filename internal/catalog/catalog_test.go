package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/mgomes/ressik/internal/chunk"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/merkle"
)

func TestEncodeRoundTripAndQueries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	want := testSnapshot()

	image, err := Encode(ctx, want, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Encode() returned error: %v", err)
	}
	if !bytes.HasPrefix(image, []byte("SQLite format 3\x00")) {
		t.Fatalf("Encode() returned a non-SQLite image: %q", image[:16])
	}
	if image[18] != 1 || image[19] != 1 {
		t.Fatalf("Encode() journal header is %d/%d, want 1/1", image[18], image[19])
	}

	reader, err := Open(ctx, image, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	clear(image)
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
	})

	got, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() returned error: %v", err)
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Snapshot() mismatch (-want +got):\n%s", diff)
	}

	entry, err := reader.Entry(ctx, "docs", "nested/story.txt")
	if err != nil {
		t.Fatalf("Entry() returned error: %v", err)
	}
	if diff := cmp.Diff(want.Sources[0].Entries[4], entry, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Entry() mismatch (-want +got):\n%s", diff)
	}
	if _, err := reader.Entry(ctx, "docs", "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Entry() error = %v, want ErrNotFound", err)
	}

	subtree, err := reader.Subtree(ctx, "docs", "nested")
	if err != nil {
		t.Fatalf("Subtree() returned error: %v", err)
	}
	paths := make([]string, len(subtree))
	for i, entry := range subtree {
		paths[i] = entry.Path
	}
	if diff := cmp.Diff([]string{"nested", "nested/link", "nested/story.txt"}, paths); diff != "" {
		t.Errorf("Subtree() paths mismatch (-want +got):\n%s", diff)
	}
	if _, err := reader.Subtree(ctx, "docs", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Subtree() error = %v, want ErrNotFound", err)
	}

	blocks, err := reader.Blocks(ctx)
	if err != nil {
		t.Fatalf("Blocks() returned error: %v", err)
	}
	wantBlocks := []BlockRef{
		{ID: filledID(0x11), Length: 4},
		{ID: filledID(0x22), Length: 3},
	}
	if diff := cmp.Diff(wantBlocks, blocks); diff != "" {
		t.Errorf("Blocks() mismatch (-want +got):\n%s", diff)
	}

	if _, err := reader.conn.ExecContext(ctx, "DELETE FROM entries"); err == nil {
		t.Fatal("read-only catalog accepted a write")
	}
}

func TestEncodeSizeLimit(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	image, err := Encode(context.Background(), snapshot, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Encode() returned error: %v", err)
	}

	exact, err := Encode(context.Background(), snapshot, int64(len(image)))
	if err != nil {
		t.Fatalf("Encode() at exact limit returned error: %v", err)
	}
	if got, want := len(exact), len(image); got != want {
		t.Fatalf("Encode() size = %d, want %d", got, want)
	}
	if _, err := Encode(context.Background(), snapshot, int64(len(image)-1)); err == nil {
		t.Fatal("Encode() below required size returned nil error")
	}
	if _, err := Open(context.Background(), image, int64(len(image)-1)); err == nil {
		t.Fatal("Open() below image size returned nil error")
	}
	for _, limit := range []int64{0, -1} {
		if _, err := Encode(context.Background(), snapshot, limit); err == nil {
			t.Errorf("Encode(maxBytes=%d) returned nil error", limit)
		}
	}
}

func TestCancellationAndClose(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Encode(canceled, testSnapshot(), DefaultMaxBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("Encode() error = %v, want context.Canceled", err)
	}

	image, err := Encode(context.Background(), testSnapshot(), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Encode() returned error: %v", err)
	}
	if _, err := Open(canceled, image, DefaultMaxBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", err)
	}
	reader, err := Open(context.Background(), image, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	if _, err := reader.Entry(canceled, "docs", "."); !errors.Is(err, context.Canceled) {
		t.Fatalf("Entry() error = %v, want context.Canceled", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close() returned error: %v", err)
	}
	if _, err := reader.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() on closed reader returned nil error")
	}
}

func TestOpenRejectsInvalidCatalogs(t *testing.T) {
	t.Parallel()
	image, err := Encode(context.Background(), testSnapshot(), DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Encode() returned error: %v", err)
	}

	tests := []struct {
		name string
		sql  string
	}{
		{name: "application_id", sql: "PRAGMA application_id = 0"},
		{name: "schema_version", sql: "PRAGMA user_version = 99"},
		{name: "missing_index", sql: "DROP INDEX entries_by_parent"},
		{
			name: "foreign_key",
			sql:  "PRAGMA foreign_keys = OFF; DELETE FROM sources WHERE source_id = 'docs'",
		},
		{
			name: "ordinal_gap",
			sql:  "UPDATE entry_blocks SET ordinal = 2 WHERE source_id = 'docs' AND path = 'nested/story.txt' AND ordinal = 1",
		},
		{
			name: "folded_path",
			sql:  "UPDATE entries SET folded_path = 'wrong' WHERE source_id = 'docs' AND path = 'empty.txt'",
		},
		{
			name: "zero_block_id",
			sql: `
				PRAGMA foreign_keys = OFF;
				UPDATE entry_blocks SET block_id = zeroblob(32) WHERE block_id = (SELECT min(block_id) FROM blocks);
				UPDATE blocks SET block_id = zeroblob(32) WHERE block_id = (SELECT min(block_id) FROM blocks);
			`,
		},
		{
			name: "directory_mode",
			sql:  "UPDATE entries SET mode = 0 WHERE source_id = 'docs' AND path = '.'",
		},
		{
			name: "nonportable_path",
			sql:  `UPDATE entries SET path = 'bad\name', folded_path = 'bad\name' WHERE source_id = 'docs' AND path = 'empty.txt'`,
		},
		{
			name: "statistics",
			sql:  "UPDATE snapshot SET files = 99",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutated := mutateImage(t, image, test.sql)
			if _, err := Open(context.Background(), mutated, DefaultMaxBytes); err == nil {
				t.Fatal("Open() returned nil error")
			}
		})
	}

	if _, err := Open(context.Background(), []byte("SQLite format 3\x00"), DefaultMaxBytes); err == nil {
		t.Fatal("Open() accepted a truncated image")
	}
}

func TestFileAndSymlinkSourceRoots(t *testing.T) {
	t.Parallel()
	modified := time.Unix(1_700_000_000, 42).UTC()
	fileDigest := filledDigest(0x31)
	linkDigest := filledDigest(0x32)
	snapshot := Snapshot{
		Version:         Version,
		ConfigurationID: "00112233445566778899aabbccddeeff",
		ID:              filledID(0x78),
		PlanID:          "roots",
		PlanName:        "Root kinds",
		CreatedAt:       modified,
		Root:            filledDigest(0x33),
		ChunkSize:       chunk.DefaultSize,
		Sources: []Source{
			{
				ID:           "file",
				OriginalPath: "/tmp/file.txt",
				Digest:       fileDigest,
				Entries: []Entry{{
					Path: ".", Kind: FileEntry, Mode: 0o600, ModifiedAt: modified,
					Size: 4, Digest: fileDigest,
					Blocks:      []BlockRef{{ID: filledID(0x41), Length: 4}},
					ChangeToken: "file-v1",
				}},
			},
			{
				ID:           "link",
				OriginalPath: "/tmp/link",
				Digest:       linkDigest,
				Entries: []Entry{{
					Path: ".", Kind: SymlinkEntry, Mode: uint32(os.ModeSymlink | 0o777),
					ModifiedAt: modified, Size: 8, Digest: linkDigest,
					LinkTarget: "file.txt", LinkKind: FileSymlinkTarget,
				}},
			},
		},
		Statistics: Stats{Files: 1, Symlinks: 1, PlaintextBytes: 4, NewBlocks: 1, StoredBytes: 4},
	}
	image, err := Encode(context.Background(), snapshot, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Encode() returned error: %v", err)
	}
	reader, err := Open(context.Background(), image, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer reader.Close()
	got, err := reader.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() returned error: %v", err)
	}
	if diff := cmp.Diff(snapshot, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Snapshot() mismatch (-want +got):\n%s", diff)
	}
}

func TestOpenRejectsSchemaWithoutParentForeignKey(t *testing.T) {
	t.Parallel()
	const parentForeignKey = `    FOREIGN KEY (source_id, parent_path)
        REFERENCES entries(source_id, path)
        DEFERRABLE INITIALLY DEFERRED,

`
	normalizedSchema := strings.ReplaceAll(schemaSQL, "\r\n", "\n")
	alteredSchema := strings.Replace(normalizedSchema, parentForeignKey, "", 1)
	if alteredSchema == normalizedSchema {
		t.Fatal("test did not remove the parent foreign key")
	}
	image := encodeWithSchema(t, alteredSchema, testSnapshot())
	mutated := mutateImage(t, image, "DELETE FROM entries WHERE source_id = 'docs' AND path = 'nested'")
	if _, err := Open(context.Background(), mutated, DefaultMaxBytes); err == nil {
		t.Fatal("Open() accepted a dangling parent without a declared foreign key")
	}
}

func TestOpenRejectsDuplicateOrdinalsWithoutPrimaryKey(t *testing.T) {
	t.Parallel()
	normalizedSchema := strings.ReplaceAll(schemaSQL, "\r\n", "\n")
	alteredSchema := strings.Replace(
		normalizedSchema,
		"    PRIMARY KEY (source_id, path, ordinal),\n",
		"",
		1,
	)
	const entryBlocksTail = `    FOREIGN KEY (block_id) REFERENCES blocks(block_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX entry_blocks_by_id`
	const alteredTail = `    FOREIGN KEY (block_id) REFERENCES blocks(block_id)
) STRICT;

CREATE INDEX entry_blocks_by_id`
	alteredSchema = strings.Replace(alteredSchema, entryBlocksTail, alteredTail, 1)
	if alteredSchema == normalizedSchema || strings.Contains(alteredSchema, "PRIMARY KEY (source_id, path, ordinal)") {
		t.Fatal("test did not remove the entry-block primary key")
	}
	image := encodeWithSchema(t, alteredSchema, testSnapshot())
	mutated := mutateImage(t, image, `
		INSERT INTO entry_blocks (source_id, path, ordinal, block_id)
		SELECT source_id, path, 1, block_id
		FROM entry_blocks
		WHERE source_id = 'docs' AND path = 'nested/story.txt' AND ordinal = 1;
		INSERT INTO entry_blocks (source_id, path, ordinal, block_id)
		SELECT source_id, path, 3, block_id
		FROM entry_blocks
		WHERE source_id = 'docs' AND path = 'nested/story.txt' AND ordinal = 0;
		UPDATE entries
		SET size = 14
		WHERE source_id = 'docs' AND path = 'nested/story.txt';
		UPDATE snapshot SET plaintext_bytes = 18;
	`)
	if _, err := Open(context.Background(), mutated, DefaultMaxBytes); err == nil {
		t.Fatal("Open() accepted duplicate block ordinals without a declared primary key")
	}
}

func TestOpenRejectsEmptyMetadataWithoutChecks(t *testing.T) {
	t.Parallel()
	normalizedSchema := strings.ReplaceAll(schemaSQL, "\r\n", "\n")
	alteredSchema := strings.Replace(
		normalizedSchema,
		"change_token IS NOT NULL AND change_token <> ''",
		"change_token IS NOT NULL",
		1,
	)
	alteredSchema = strings.Replace(
		alteredSchema,
		"link_target IS NOT NULL AND link_target <> '' AND",
		"link_target IS NOT NULL AND",
		1,
	)
	if alteredSchema == normalizedSchema {
		t.Fatal("test did not weaken the entry metadata checks")
	}
	image := encodeWithSchema(t, alteredSchema, testSnapshot())

	tests := []struct {
		name      string
		statement string
	}{
		{
			name:      "file_change_token",
			statement: "UPDATE entries SET change_token = '' WHERE source_id = 'docs' AND path = 'empty.txt'",
		},
		{
			name:      "symlink_target",
			statement: "UPDATE entries SET link_target = '' WHERE source_id = 'docs' AND path = 'nested/link'",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutated := mutateImage(t, image, test.statement)
			if _, err := Open(context.Background(), mutated, DefaultMaxBytes); err == nil {
				t.Fatal("Open() accepted empty entry metadata without a schema check")
			}
		})
	}
}

func TestBackupImageIncludesUncheckpointedWAL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "working.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() returned error: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
	})
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("connection Close() returned error: %v", err)
		}
	})
	if _, err := conn.ExecContext(ctx, `
		PRAGMA journal_mode = WAL;
		PRAGMA wal_autocheckpoint = 0;
		CREATE TABLE rows_in_wal (value INTEGER PRIMARY KEY);
	`); err != nil {
		t.Fatalf("initialize WAL database: %v", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() returned error: %v", err)
	}
	for i := range 4000 {
		if _, err := tx.ExecContext(ctx, "INSERT INTO rows_in_wal VALUES (?)", i); err != nil {
			t.Fatalf("insert WAL row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() returned error: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("Stat(WAL) returned error: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("WAL is empty before backup")
	}

	image, err := backupImage(ctx, conn)
	if err != nil {
		t.Fatalf("backupImage() returned error: %v", err)
	}
	if image[18] != 1 || image[19] != 1 {
		t.Fatalf("backupImage() journal header is %d/%d, want 1/1", image[18], image[19])
	}

	restored, restoredConn := deserializeForTest(t, image)
	defer restored.Close()
	defer restoredConn.Close()
	var count int
	if err := restoredConn.QueryRowContext(ctx, "SELECT count(*) FROM rows_in_wal").Scan(&count); err != nil {
		t.Fatalf("query restored WAL rows: %v", err)
	}
	if count != 4000 {
		t.Fatalf("restored row count = %d, want 4000", count)
	}
}

func TestValidateRejectsConflictingBlockLengths(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	snapshot.Sources[1].Entries[1].Blocks[0].Length = 3
	snapshot.Sources[1].Entries[1].Size = 3
	snapshot.Statistics.PlaintextBytes--
	if err := Validate(snapshot); err == nil {
		t.Fatal("Validate() returned nil error")
	}
}

func TestValidateRejectsInvalidChangeToken(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	snapshot.Sources[0].Entries[1].ChangeToken = string([]byte{0xff})
	if err := Validate(snapshot); err == nil {
		t.Fatal("Validate() returned nil error")
	}
}

func mutateImage(t *testing.T, image []byte, statement string) []byte {
	t.Helper()
	db, conn := deserializeForTest(t, image)
	defer db.Close()
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("mutate catalog: %v", err)
	}
	var mutated []byte
	if err := conn.Raw(func(raw any) error {
		var err error
		mutated, err = raw.(serializer).Serialize()
		return err
	}); err != nil {
		t.Fatalf("serialize mutated catalog: %v", err)
	}
	return mutated
}

func encodeWithSchema(t *testing.T, schema string, snapshot Snapshot) []byte {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() returned error: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn() returned error: %v", err)
	}
	defer conn.Close()
	setup := fmt.Sprintf(`
		PRAGMA foreign_keys = ON;
		PRAGMA application_id = %d;
		PRAGMA user_version = %d;
	`, applicationID, schemaVersion)
	if _, err := conn.ExecContext(context.Background(), setup); err != nil {
		t.Fatalf("configure catalog: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), schema); err != nil {
		t.Fatalf("create altered schema: %v", err)
	}
	if err := insertSnapshot(context.Background(), conn, snapshot); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	image, err := backupImage(context.Background(), conn)
	if err != nil {
		t.Fatalf("backupImage() returned error: %v", err)
	}
	return image
}

func deserializeForTest(t *testing.T, image []byte) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() returned error: %v", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		t.Fatalf("Conn() returned error: %v", err)
	}
	if err := conn.Raw(func(raw any) error {
		return raw.(serializer).Deserialize(image)
	}); err != nil {
		conn.Close()
		db.Close()
		t.Fatalf("Deserialize() returned error: %v", err)
	}
	return db, conn
}

func testSnapshot() Snapshot {
	modified := time.Unix(-123456789, 987654321).UTC()
	blockOne := BlockRef{ID: filledID(0x11), Length: 4}
	blockTwo := BlockRef{ID: filledID(0x22), Length: 3}
	docsRoot := filledDigest(0xa1)
	photosRoot := filledDigest(0xb2)
	return Snapshot{
		Version:         Version,
		ConfigurationID: "00112233445566778899aabbccddeeff",
		ID:              filledID(0x77),
		PlanID:          "home",
		PlanName:        "Home — nightly",
		CreatedAt:       time.Unix(1_800_000_000, 123456789).UTC(),
		Root:            filledDigest(0xcc),
		ChunkSize:       chunk.DefaultSize,
		Sources: []Source{
			{
				ID:           "docs",
				OriginalPath: "/Users/example/My Documents",
				Digest:       docsRoot,
				Entries: []Entry{
					{Path: ".", Kind: DirectoryEntry, Mode: uint32(os.ModeDir | 0o755), ModifiedAt: modified, Digest: docsRoot},
					{Path: "empty.txt", Kind: FileEntry, Mode: 0o644, ModifiedAt: modified, Digest: filledDigest(0x01), ChangeToken: "empty-v1"},
					{Path: "nested", Kind: DirectoryEntry, Mode: uint32(os.ModeDir | 0o700), ModifiedAt: modified, Digest: filledDigest(0x02)},
					{Path: "nested/link", Kind: SymlinkEntry, Mode: uint32(os.ModeSymlink | 0o777), ModifiedAt: modified, Size: 9, Digest: filledDigest(0x03), LinkTarget: "story.txt", LinkKind: FileSymlinkTarget},
					{Path: "nested/story.txt", Kind: FileEntry, Mode: 0o640, ModifiedAt: modified, Size: 7, Digest: filledDigest(0x04), Blocks: []BlockRef{blockOne, blockTwo}, ChangeToken: "story-v1"},
				},
			},
			{
				ID:           "photos",
				OriginalPath: "C:\\Users\\example\\Bilder",
				Digest:       photosRoot,
				Entries: []Entry{
					{Path: ".", Kind: DirectoryEntry, Mode: uint32(os.ModeDir | 0o755), ModifiedAt: modified, Digest: photosRoot},
					{Path: "café.jpg", Kind: FileEntry, Mode: 0o600, ModifiedAt: modified, Size: 4, Digest: filledDigest(0x05), Blocks: []BlockRef{blockOne}, ChangeToken: "photo-v1"},
				},
			},
		},
		Statistics: Stats{
			Files:          3,
			Directories:    3,
			Symlinks:       1,
			PlaintextBytes: 11,
			NewBlocks:      2,
			ReusedBlocks:   1,
			StoredBytes:    7,
		},
	}
}

func filledID(value byte) object.ID {
	var id object.ID
	for i := range id {
		id[i] = value
	}
	return id
}

func filledDigest(value byte) merkle.Digest {
	var digest merkle.Digest
	for i := range digest {
		digest[i] = value
	}
	return digest
}

func ExampleReader_Entry() {
	image, err := Encode(context.Background(), testSnapshot(), DefaultMaxBytes)
	if err != nil {
		panic(err)
	}
	defer clear(image)
	reader, err := Open(context.Background(), image, DefaultMaxBytes)
	if err != nil {
		panic(err)
	}
	defer reader.Close()
	entry, err := reader.Entry(context.Background(), "docs", "nested/story.txt")
	if err != nil {
		panic(err)
	}
	fmt.Println(entry.Path, len(entry.Blocks))
	// Output: nested/story.txt 2
}

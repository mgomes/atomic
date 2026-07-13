CREATE TABLE snapshot (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    snapshot_id BLOB NOT NULL CHECK (length(snapshot_id) = 32),
    configuration_id TEXT NOT NULL CHECK (length(configuration_id) = 32),
    plan_id TEXT NOT NULL CHECK (length(plan_id) BETWEEN 1 AND 63),
    plan_name TEXT NOT NULL CHECK (length(CAST(plan_name AS BLOB)) BETWEEN 1 AND 256),
    created_at_seconds INTEGER NOT NULL,
    created_at_nanoseconds INTEGER NOT NULL CHECK (created_at_nanoseconds BETWEEN 0 AND 999999999),
    root_digest BLOB NOT NULL CHECK (length(root_digest) = 32),
    chunk_size INTEGER NOT NULL CHECK (chunk_size = 4194304),
    files INTEGER NOT NULL CHECK (files >= 0),
    directories INTEGER NOT NULL CHECK (directories >= 0),
    symlinks INTEGER NOT NULL CHECK (symlinks >= 0),
    plaintext_bytes INTEGER NOT NULL CHECK (plaintext_bytes >= 0),
    new_blocks INTEGER NOT NULL CHECK (new_blocks >= 0),
    reused_blocks INTEGER NOT NULL CHECK (reused_blocks >= 0),
    stored_bytes INTEGER NOT NULL CHECK (stored_bytes >= 0)
) STRICT;

CREATE TABLE sources (
    source_id TEXT COLLATE BINARY PRIMARY KEY,
    original_path TEXT NOT NULL CHECK (original_path <> ''),
    digest BLOB NOT NULL CHECK (length(digest) = 32)
) STRICT, WITHOUT ROWID;

CREATE TABLE entries (
    source_id TEXT COLLATE BINARY NOT NULL,
    path TEXT COLLATE BINARY NOT NULL CHECK (path <> ''),
    folded_path TEXT COLLATE BINARY NOT NULL CHECK (folded_path <> ''),
    parent_path TEXT COLLATE BINARY,
    kind TEXT NOT NULL CHECK (kind IN ('directory', 'file', 'symlink')),
    mode INTEGER NOT NULL CHECK (mode BETWEEN 0 AND 4294967295),
    modified_at_seconds INTEGER NOT NULL,
    modified_at_nanoseconds INTEGER NOT NULL CHECK (modified_at_nanoseconds BETWEEN 0 AND 999999999),
    size INTEGER NOT NULL CHECK (size >= 0),
    digest BLOB NOT NULL CHECK (length(digest) = 32),
    link_target TEXT,
    link_kind TEXT CHECK (link_kind IS NULL OR link_kind IN ('unknown', 'file', 'directory')),
    change_token TEXT,

    PRIMARY KEY (source_id, path),
    UNIQUE (source_id, folded_path),

    FOREIGN KEY (source_id) REFERENCES sources(source_id),
    FOREIGN KEY (source_id, parent_path)
        REFERENCES entries(source_id, path)
        DEFERRABLE INITIALLY DEFERRED,

    CHECK (
        (path = '.' AND parent_path IS NULL) OR
        (path <> '.' AND parent_path IS NOT NULL AND parent_path <> '')
    ),
    CHECK (parent_path IS NULL OR parent_path <> path),
    CHECK (
        (kind = 'directory' AND link_target IS NULL AND link_kind IS NULL AND change_token IS NULL) OR
        (kind = 'file' AND link_target IS NULL AND link_kind IS NULL AND change_token IS NOT NULL AND change_token <> '') OR
        (kind = 'symlink' AND link_target IS NOT NULL AND link_target <> '' AND link_kind IS NOT NULL AND change_token IS NULL)
    )
) STRICT, WITHOUT ROWID;

CREATE INDEX entries_by_parent ON entries(source_id, parent_path, path);

CREATE TABLE blocks (
    block_id BLOB PRIMARY KEY CHECK (length(block_id) = 32),
    plaintext_length INTEGER NOT NULL CHECK (plaintext_length BETWEEN 1 AND 4194304)
) STRICT, WITHOUT ROWID;

CREATE TABLE entry_blocks (
    source_id TEXT COLLATE BINARY NOT NULL,
    path TEXT COLLATE BINARY NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    block_id BLOB NOT NULL CHECK (length(block_id) = 32),

    PRIMARY KEY (source_id, path, ordinal),
    FOREIGN KEY (source_id, path)
        REFERENCES entries(source_id, path)
        ON DELETE CASCADE,
    FOREIGN KEY (block_id) REFERENCES blocks(block_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX entry_blocks_by_id ON entry_blocks(block_id, source_id, path, ordinal);

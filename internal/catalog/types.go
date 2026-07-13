package catalog

import (
	"time"

	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/merkle"
)

// EntryKind identifies a filesystem entry stored in a snapshot.
type EntryKind string

const (
	// DirectoryEntry represents a directory.
	DirectoryEntry EntryKind = "directory"
	// FileEntry represents a regular file.
	FileEntry EntryKind = "file"
	// SymlinkEntry represents a symbolic link that is not followed.
	SymlinkEntry EntryKind = "symlink"
)

// SymlinkTargetKind identifies the filesystem kind a symbolic link targets.
type SymlinkTargetKind string

const (
	// UnknownSymlinkTarget represents a missing or unsupported link target.
	UnknownSymlinkTarget SymlinkTargetKind = "unknown"
	// FileSymlinkTarget represents a link to a regular file.
	FileSymlinkTarget SymlinkTargetKind = "file"
	// DirectorySymlinkTarget represents a link to a directory.
	DirectorySymlinkTarget SymlinkTargetKind = "directory"
)

// BlockRef points to one plaintext block and records its original length.
type BlockRef struct {
	ID     object.ID `json:"id"`
	Length uint32    `json:"length"`
}

// Entry describes one source-relative filesystem entry.
type Entry struct {
	Path        string            `json:"path"`
	Kind        EntryKind         `json:"kind"`
	Mode        uint32            `json:"mode"`
	ModifiedAt  time.Time         `json:"modified_at"`
	Size        int64             `json:"size"`
	Digest      merkle.Digest     `json:"digest"`
	Blocks      []BlockRef        `json:"blocks,omitempty"`
	LinkTarget  string            `json:"link_target,omitempty"`
	LinkKind    SymlinkTargetKind `json:"link_target_kind,omitempty"`
	ChangeToken string            `json:"change_token,omitempty"`
}

// Source contains one named source tree.
type Source struct {
	ID           string        `json:"id"`
	OriginalPath string        `json:"original_path"`
	Digest       merkle.Digest `json:"digest"`
	Entries      []Entry       `json:"entries"`
}

// Stats summarizes work performed for one snapshot.
type Stats struct {
	Files          int   `json:"files"`
	Directories    int   `json:"directories"`
	Symlinks       int   `json:"symlinks"`
	PlaintextBytes int64 `json:"plaintext_bytes"`
	NewBlocks      int   `json:"new_blocks"`
	ReusedBlocks   int   `json:"reused_blocks"`
	StoredBytes    int64 `json:"stored_bytes"`
}

// Snapshot is the complete logical description stored in one catalog.
type Snapshot struct {
	Version         int           `json:"version"`
	ConfigurationID string        `json:"configuration_id"`
	ID              object.ID     `json:"id"`
	PlanID          string        `json:"plan_id"`
	PlanName        string        `json:"plan_name"`
	CreatedAt       time.Time     `json:"created_at"`
	Root            merkle.Digest `json:"root"`
	ChunkSize       int           `json:"chunk_size"`
	Sources         []Source      `json:"sources"`
	Statistics      Stats         `json:"statistics"`
}

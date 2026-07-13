package repository

import (
	"time"

	"github.com/mgomes/ressik/internal/catalog"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/merkle"
)

// EntryKind identifies a filesystem entry stored in a manifest.
type EntryKind = catalog.EntryKind

const (
	// DirectoryEntry represents a directory.
	DirectoryEntry = catalog.DirectoryEntry
	// FileEntry represents a regular file.
	FileEntry = catalog.FileEntry
	// SymlinkEntry represents a symbolic link that is not followed.
	SymlinkEntry = catalog.SymlinkEntry
)

// SymlinkTargetKind identifies the filesystem kind a symbolic link targets.
type SymlinkTargetKind = catalog.SymlinkTargetKind

const (
	// UnknownSymlinkTarget represents a missing or unsupported link target.
	UnknownSymlinkTarget = catalog.UnknownSymlinkTarget
	// FileSymlinkTarget represents a link to a regular file.
	FileSymlinkTarget = catalog.FileSymlinkTarget
	// DirectorySymlinkTarget represents a link to a directory.
	DirectorySymlinkTarget = catalog.DirectorySymlinkTarget
)

// BlockRef points to one plaintext block and records its original length.
type BlockRef = catalog.BlockRef

// Entry describes one source-relative filesystem entry.
type Entry = catalog.Entry

// Source contains one named source tree.
type Source = catalog.Source

// Stats summarizes work performed for one snapshot.
type Stats = catalog.Stats

// Manifest is the encrypted description of one complete snapshot.
type Manifest = catalog.Snapshot

// Summary is the user-facing metadata for one committed snapshot.
type Summary struct {
	ID              object.ID
	ConfigurationID string
	PlanID          string
	PlanName        string
	CreatedAt       time.Time
	Root            merkle.Digest
	Statistics      Stats
}

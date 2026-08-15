package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mgomes/atomic/internal/chunk"
	"github.com/mgomes/atomic/internal/fsname"
	"github.com/mgomes/atomic/internal/object"
)

// Version is the logical schema version stored in every catalog snapshot.
const Version = 1

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var configurationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Validate checks every logical invariant required by a snapshot catalog.
func Validate(snapshot Snapshot) error {
	if err := validateSnapshotMetadata(snapshot); err != nil {
		return err
	}
	if len(snapshot.Sources) == 0 {
		return errors.New("snapshot contains no sources")
	}

	sourceIDs := make(map[string]bool, len(snapshot.Sources))
	blockLengths := make(map[object.ID]uint32)
	var actual Stats
	for _, source := range snapshot.Sources {
		if err := validateSource(source, sourceIDs, blockLengths, &actual); err != nil {
			return err
		}
	}
	if actual.Files != snapshot.Statistics.Files ||
		actual.Directories != snapshot.Statistics.Directories ||
		actual.Symlinks != snapshot.Statistics.Symlinks ||
		actual.PlaintextBytes != snapshot.Statistics.PlaintextBytes {
		return errors.New("snapshot statistics do not match its entries")
	}
	return nil
}

func validateSnapshotMetadata(snapshot Snapshot) error {
	if snapshot.Version != Version {
		return fmt.Errorf("snapshot version is %d, want %d", snapshot.Version, Version)
	}
	if snapshot.ID.IsZero() {
		return errors.New("snapshot ID is empty")
	}
	if !configurationIDPattern.MatchString(snapshot.ConfigurationID) {
		return fmt.Errorf("snapshot configuration ID %q must match %s", snapshot.ConfigurationID, configurationIDPattern)
	}
	if !sourceIDPattern.MatchString(snapshot.PlanID) {
		return fmt.Errorf("snapshot plan ID %q must match %s", snapshot.PlanID, sourceIDPattern)
	}
	if snapshot.PlanName == "" || len(snapshot.PlanName) > 256 || !utf8.ValidString(snapshot.PlanName) {
		return errors.New("snapshot plan name is empty, invalid UTF-8, or exceeds 256 bytes")
	}
	if snapshot.CreatedAt.IsZero() {
		return errors.New("snapshot creation time is empty")
	}
	if snapshot.ChunkSize != chunk.DefaultSize {
		return fmt.Errorf("snapshot chunk size is %d, want %d", snapshot.ChunkSize, chunk.DefaultSize)
	}
	if err := validateStats(snapshot.Statistics); err != nil {
		return err
	}
	return nil
}

func validateSource(
	source Source,
	sourceIDs map[string]bool,
	blockLengths map[object.ID]uint32,
	actual *Stats,
) error {
	if !sourceIDPattern.MatchString(source.ID) {
		return fmt.Errorf("snapshot source ID %q must match %s", source.ID, sourceIDPattern)
	}
	if err := fsname.Component(source.ID); err != nil {
		return fmt.Errorf("snapshot source ID %q is not portable: %w", source.ID, err)
	}
	if sourceIDs[source.ID] {
		return fmt.Errorf("snapshot source ID %q is duplicated", source.ID)
	}
	sourceIDs[source.ID] = true
	if source.OriginalPath == "" || !utf8.ValidString(source.OriginalPath) {
		return fmt.Errorf("snapshot source %q has an invalid original path", source.ID)
	}

	paths := make(map[string]Entry, len(source.Entries))
	foldedPaths := make(map[string]string, len(source.Entries))
	for _, entry := range source.Entries {
		if err := validateEntry(source.ID, entry, paths, foldedPaths, blockLengths, actual); err != nil {
			return err
		}
	}
	root, exists := paths["."]
	if !exists {
		return fmt.Errorf("snapshot source %q has no root entry", source.ID)
	}
	if source.Digest != root.Digest {
		return fmt.Errorf("snapshot source %q digest does not match its root entry", source.ID)
	}
	for path := range paths {
		if path == "." {
			continue
		}
		parent := parentPath(path)
		parentEntry, exists := paths[parent]
		if !exists || parentEntry.Kind != DirectoryEntry {
			return fmt.Errorf("snapshot path %q has no directory parent %q", path, parent)
		}
	}
	return nil
}

func validateEntry(
	sourceID string,
	entry Entry,
	paths map[string]Entry,
	foldedPaths map[string]string,
	blockLengths map[object.ID]uint32,
	actual *Stats,
) error {
	if err := validateEntryPath(entry.Path); err != nil {
		return fmt.Errorf("snapshot source %q: %w", sourceID, err)
	}
	if _, exists := paths[entry.Path]; exists {
		return fmt.Errorf("snapshot source %q contains duplicate path %q", sourceID, entry.Path)
	}
	folded := fsname.Fold(entry.Path)
	if previous, exists := foldedPaths[folded]; exists {
		return fmt.Errorf("snapshot source %q paths %q and %q collide on case-insensitive filesystems", sourceID, previous, entry.Path)
	}
	foldedPaths[folded] = entry.Path
	if entry.ModifiedAt.IsZero() {
		return fmt.Errorf("snapshot path %q has an empty modification time", entry.Path)
	}
	paths[entry.Path] = entry

	switch entry.Kind {
	case DirectoryEntry:
		if !os.FileMode(entry.Mode).IsDir() || entry.Size < 0 || len(entry.Blocks) != 0 || entry.LinkTarget != "" || entry.LinkKind != "" || entry.ChangeToken != "" {
			return fmt.Errorf("snapshot directory %q contains file data", entry.Path)
		}
		actual.Directories++
	case FileEntry:
		if !os.FileMode(entry.Mode).IsRegular() || entry.Size < 0 || entry.LinkTarget != "" || entry.LinkKind != "" || entry.ChangeToken == "" || !utf8.ValidString(entry.ChangeToken) {
			return fmt.Errorf("snapshot file %q contains invalid metadata", entry.Path)
		}
		var size int64
		for _, ref := range entry.Blocks {
			if ref.ID.IsZero() || ref.Length == 0 || ref.Length > chunk.DefaultSize {
				return fmt.Errorf("snapshot path %q has invalid block reference %s with length %d", entry.Path, ref.ID, ref.Length)
			}
			if previous, exists := blockLengths[ref.ID]; exists && previous != ref.Length {
				return fmt.Errorf("snapshot block %s has conflicting lengths %d and %d", ref.ID, previous, ref.Length)
			}
			blockLengths[ref.ID] = ref.Length
			size += int64(ref.Length)
		}
		if size != entry.Size {
			return fmt.Errorf("snapshot path %q block bytes are %d, want %d", entry.Path, size, entry.Size)
		}
		actual.Files++
		actual.PlaintextBytes += entry.Size
	case SymlinkEntry:
		if os.FileMode(entry.Mode)&os.ModeSymlink == 0 || entry.Size < 0 || len(entry.Blocks) != 0 || entry.LinkTarget == "" || !utf8.ValidString(entry.LinkTarget) || entry.ChangeToken != "" {
			return fmt.Errorf("snapshot symlink %q contains invalid data", entry.Path)
		}
		switch entry.LinkKind {
		case UnknownSymlinkTarget, FileSymlinkTarget, DirectorySymlinkTarget:
		default:
			return fmt.Errorf("snapshot symlink %q has invalid target kind %q", entry.Path, entry.LinkKind)
		}
		actual.Symlinks++
	default:
		return fmt.Errorf("snapshot path %q has unknown kind %q", entry.Path, entry.Kind)
	}
	return nil
}

func validateStats(stats Stats) error {
	if stats.Files < 0 || stats.Directories < 0 || stats.Symlinks < 0 ||
		stats.PlaintextBytes < 0 || stats.NewBlocks < 0 || stats.ReusedBlocks < 0 ||
		stats.StoredBytes < 0 {
		return errors.New("snapshot statistics contain negative values")
	}
	return nil
}

func validateEntryPath(path string) error {
	if path == "" {
		return errors.New("snapshot entry path is empty")
	}
	if strings.Contains(path, `\`) {
		return fmt.Errorf("snapshot path %q contains a backslash", path)
	}
	if strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return fmt.Errorf("snapshot path %q is absolute", path)
	}
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' || strings.HasPrefix(path, "//") {
		return fmt.Errorf("snapshot path %q is a drive or UNC path", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned != path || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("snapshot path %q is not canonical", path)
	}
	if err := fsname.Path(path); err != nil {
		return fmt.Errorf("snapshot path %q is not portable: %w", path, err)
	}
	return nil
}

func parentPath(path string) string {
	if separator := strings.LastIndexByte(path, '/'); separator >= 0 {
		return path[:separator]
	}
	return "."
}

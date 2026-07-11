package repository

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zeebo/blake3"

	"github.com/mgomes/ressik/internal/chunk"
	"github.com/mgomes/ressik/internal/fsname"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/merkle"
)

const (
	manifestVersion     = 1
	commitVersion       = 1
	maxManifestSize     = 64 << 20
	maxManifestObject   = maxManifestSize + 1024
	maxCommitSize       = 4 << 10
	maxCommitObjectSize = maxCommitSize + 1024
)

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var configurationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type commitRecord struct {
	Version         int           `json:"version"`
	ConfigurationID string        `json:"configuration_id"`
	ManifestDigest  merkle.Digest `json:"manifest_digest"`
	PlanID          string        `json:"plan_id"`
	PlanName        string        `json:"plan_name"`
	CreatedAt       time.Time     `json:"created_at"`
	Root            merkle.Digest `json:"root"`
	Statistics      Stats         `json:"statistics"`
}

// Commit writes an encrypted manifest and then its completion marker. Callers
// must hold the exclusive repository lock.
func (r *Repository) Commit(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest.Version != manifestVersion {
		return fmt.Errorf("manifest version is %d, want %d", manifest.Version, manifestVersion)
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	plaintext, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if len(plaintext) > maxManifestSize {
		return fmt.Errorf("manifest has %d bytes, maximum is %d", len(plaintext), maxManifestSize)
	}
	sealed, err := r.codec.Seal(object.Manifest, manifest.ID, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt manifest: %w", err)
	}
	created, err := putFile(r.manifestPath(manifest.ID), sealed)
	if err != nil {
		return fmt.Errorf("store manifest: %w", err)
	}
	if !created {
		return fmt.Errorf("snapshot %s already exists", manifest.ID)
	}

	digest := blake3.Sum256(sealed)
	record := commitRecord{
		Version:         commitVersion,
		ConfigurationID: manifest.ConfigurationID,
		ManifestDigest:  merkle.Digest(digest),
		PlanID:          manifest.PlanID,
		PlanName:        manifest.PlanName,
		CreatedAt:       manifest.CreatedAt,
		Root:            manifest.Root,
		Statistics:      manifest.Statistics,
	}
	commitPlaintext, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode commit marker: %w", err)
	}
	if len(commitPlaintext) > maxCommitSize {
		return fmt.Errorf("commit marker has %d bytes, maximum is %d", len(commitPlaintext), maxCommitSize)
	}
	commit, err := r.codec.Seal(object.Commit, manifest.ID, commitPlaintext)
	if err != nil {
		return fmt.Errorf("encrypt commit marker: %w", err)
	}
	created, err = putFile(r.commitPath(manifest.ID), commit)
	if err != nil {
		return fmt.Errorf("store commit marker: %w", err)
	}
	if !created {
		return fmt.Errorf("snapshot %s commit marker already exists", manifest.ID)
	}
	return nil
}

// Load authenticates and returns one committed snapshot manifest.
func (r *Repository) Load(ctx context.Context, id object.ID) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	record, err := r.loadCommit(ctx, id)
	if err != nil {
		return Manifest{}, err
	}

	manifestObject, err := readFile(r.manifestPath(id), maxManifestObject)
	if err != nil {
		return Manifest{}, fmt.Errorf("read snapshot %s manifest: %w", id, err)
	}
	digest := blake3.Sum256(manifestObject)
	if subtle.ConstantTimeCompare(record.ManifestDigest[:], digest[:]) != 1 {
		return Manifest{}, fmt.Errorf("snapshot %s manifest does not match commit marker", id)
	}
	plaintext, err := r.codec.Open(object.Manifest, id, manifestObject)
	if err != nil {
		return Manifest{}, fmt.Errorf("open snapshot %s manifest: %w", id, err)
	}

	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode snapshot %s manifest: %w", id, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("snapshot %s manifest contains trailing data", id)
	}
	if manifest.ID != id {
		return Manifest{}, fmt.Errorf("snapshot %s manifest contains ID %s", id, manifest.ID)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, fmt.Errorf("validate snapshot %s manifest: %w", id, err)
	}
	if err := record.matches(manifest); err != nil {
		return Manifest{}, fmt.Errorf("validate snapshot %s commit marker: %w", id, err)
	}
	return manifest, nil
}

// List returns committed snapshots newest first across all configuration
// namespaces. An empty planID lists all plans.
func (r *Repository) List(ctx context.Context, planID string) ([]Summary, error) {
	return r.list(ctx, "", planID)
}

// ListConfig returns committed snapshots for one configuration namespace and
// optional plan, newest first.
func (r *Repository) ListConfig(ctx context.Context, configurationID, planID string) ([]Summary, error) {
	return r.list(ctx, configurationID, planID)
}

func (r *Repository) list(ctx context.Context, configurationID, planID string) ([]Summary, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, "commits"))
	if err != nil {
		return nil, fmt.Errorf("list snapshot commits: %w", err)
	}
	summaries := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".commit") {
			continue
		}
		id, err := object.ParseID(strings.TrimSuffix(entry.Name(), ".commit"))
		if err != nil {
			return nil, fmt.Errorf("parse snapshot filename %q: %w", entry.Name(), err)
		}
		if info, err := os.Stat(r.manifestPath(id)); err != nil {
			return nil, fmt.Errorf("inspect snapshot %s manifest: %w", id, err)
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot %s manifest is not a regular file", id)
		}
		record, err := r.loadCommit(ctx, id)
		if err != nil {
			return nil, err
		}
		if (configurationID == "" || record.ConfigurationID == configurationID) &&
			(planID == "" || record.PlanID == planID) {
			summaries = append(summaries, record.summary(id))
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].ID.String() < summaries[j].ID.String()
		}
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	return summaries, nil
}

func (r *Repository) loadCommit(ctx context.Context, id object.ID) (commitRecord, error) {
	if err := ctx.Err(); err != nil {
		return commitRecord{}, err
	}
	commitObject, err := readFile(r.commitPath(id), maxCommitObjectSize)
	if err != nil {
		return commitRecord{}, fmt.Errorf("read snapshot %s commit marker: %w", id, err)
	}
	plaintext, err := r.codec.Open(object.Commit, id, commitObject)
	if err != nil {
		return commitRecord{}, fmt.Errorf("open snapshot %s commit marker: %w", id, err)
	}
	var record commitRecord
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return commitRecord{}, fmt.Errorf("decode snapshot %s commit marker: %w", id, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return commitRecord{}, fmt.Errorf("snapshot %s commit marker contains trailing data", id)
	}
	if err := record.validate(); err != nil {
		return commitRecord{}, fmt.Errorf("validate snapshot %s commit marker: %w", id, err)
	}
	return record, nil
}

func (r commitRecord) validate() error {
	if r.Version != commitVersion {
		return fmt.Errorf("version is %d, want %d", r.Version, commitVersion)
	}
	if !configurationIDPattern.MatchString(r.ConfigurationID) {
		return fmt.Errorf("configuration ID %q must match %s", r.ConfigurationID, configurationIDPattern)
	}
	if !sourceIDPattern.MatchString(r.PlanID) {
		return fmt.Errorf("plan ID %q must match %s", r.PlanID, sourceIDPattern)
	}
	if r.PlanName == "" || len(r.PlanName) > 256 {
		return errors.New("plan name is empty or exceeds 256 bytes")
	}
	if r.CreatedAt.IsZero() {
		return errors.New("creation time is empty")
	}
	return validateStats(r.Statistics)
}

func (r commitRecord) summary(id object.ID) Summary {
	return Summary{
		ID:              id,
		ConfigurationID: r.ConfigurationID,
		PlanID:          r.PlanID,
		PlanName:        r.PlanName,
		CreatedAt:       r.CreatedAt,
		Root:            r.Root,
		Statistics:      r.Statistics,
	}
}

func (r commitRecord) matches(manifest Manifest) error {
	if r.ConfigurationID != manifest.ConfigurationID ||
		r.PlanID != manifest.PlanID || r.PlanName != manifest.PlanName ||
		!r.CreatedAt.Equal(manifest.CreatedAt) || r.Root != manifest.Root ||
		r.Statistics != manifest.Statistics {
		return errors.New("summary does not match manifest")
	}
	return nil
}

// Remove deletes a snapshot commit marker before its encrypted manifest.
// Callers must hold the exclusive repository lock.
func (r *Repository) Remove(id object.ID) error {
	if err := os.Remove(r.commitPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove snapshot %s commit marker: %w", id, err)
	}
	if err := syncDir(filepath.Join(r.root, "commits")); err != nil {
		return fmt.Errorf("sync snapshot commits: %w", err)
	}
	if err := os.Remove(r.manifestPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove snapshot %s manifest: %w", id, err)
	}
	if err := syncDir(filepath.Join(r.root, "manifests")); err != nil {
		return fmt.Errorf("sync snapshot manifests: %w", err)
	}
	return nil
}

func (r *Repository) manifestPath(id object.ID) string {
	return filepath.Join(r.root, "manifests", id.String()+".manifest")
}

func (r *Repository) commitPath(id object.ID) string {
	return filepath.Join(r.root, "commits", id.String()+".commit")
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != manifestVersion {
		return fmt.Errorf("manifest version is %d, want %d", manifest.Version, manifestVersion)
	}
	if !configurationIDPattern.MatchString(manifest.ConfigurationID) {
		return fmt.Errorf("manifest configuration ID %q must match %s", manifest.ConfigurationID, configurationIDPattern)
	}
	if !sourceIDPattern.MatchString(manifest.PlanID) {
		return fmt.Errorf("manifest plan ID %q must match %s", manifest.PlanID, sourceIDPattern)
	}
	if manifest.PlanName == "" || len(manifest.PlanName) > 256 {
		return errors.New("manifest plan name is empty or exceeds 256 bytes")
	}
	if manifest.CreatedAt.IsZero() {
		return errors.New("manifest creation time is empty")
	}
	if manifest.ChunkSize != chunk.DefaultSize {
		return fmt.Errorf("manifest chunk size is %d, want %d", manifest.ChunkSize, chunk.DefaultSize)
	}
	if err := validateStats(manifest.Statistics); err != nil {
		return err
	}
	if len(manifest.Sources) == 0 {
		return errors.New("manifest contains no sources")
	}
	sourceIDs := make(map[string]bool, len(manifest.Sources))
	var actual Stats
	for _, source := range manifest.Sources {
		if !sourceIDPattern.MatchString(source.ID) {
			return fmt.Errorf("manifest source ID %q must match %s", source.ID, sourceIDPattern)
		}
		if err := fsname.Component(source.ID); err != nil {
			return fmt.Errorf("manifest source ID %q is not portable: %w", source.ID, err)
		}
		if sourceIDs[source.ID] {
			return fmt.Errorf("manifest source ID %q is duplicated", source.ID)
		}
		sourceIDs[source.ID] = true
		if source.OriginalPath == "" || !utf8.ValidString(source.OriginalPath) {
			return fmt.Errorf("manifest source %q has an invalid original path", source.ID)
		}
		paths := make(map[string]Entry, len(source.Entries))
		foldedPaths := make(map[string]string, len(source.Entries))
		for _, entry := range source.Entries {
			if err := validateEntryPath(entry.Path); err != nil {
				return fmt.Errorf("manifest source %q: %w", source.ID, err)
			}
			if _, exists := paths[entry.Path]; exists {
				return fmt.Errorf("manifest source %q contains duplicate path %q", source.ID, entry.Path)
			}
			folded := fsname.Fold(entry.Path)
			if previous, exists := foldedPaths[folded]; exists {
				return fmt.Errorf("manifest source %q paths %q and %q collide on case-insensitive filesystems", source.ID, previous, entry.Path)
			}
			foldedPaths[folded] = entry.Path
			if entry.ModifiedAt.IsZero() {
				return fmt.Errorf("manifest path %q has an empty modification time", entry.Path)
			}
			paths[entry.Path] = entry
			switch entry.Kind {
			case DirectoryEntry:
				if !os.FileMode(entry.Mode).IsDir() || entry.Size < 0 || len(entry.Blocks) != 0 || entry.LinkTarget != "" || entry.LinkKind != "" || entry.ChangeToken != "" {
					return fmt.Errorf("manifest directory %q contains file data", entry.Path)
				}
				actual.Directories++
			case FileEntry:
				if !os.FileMode(entry.Mode).IsRegular() || entry.Size < 0 || entry.LinkTarget != "" || entry.LinkKind != "" || entry.ChangeToken == "" {
					return fmt.Errorf("manifest file %q contains invalid metadata", entry.Path)
				}
				var size int64
				for _, ref := range entry.Blocks {
					if ref.Length == 0 || ref.Length > chunk.DefaultSize {
						return fmt.Errorf("manifest path %q has invalid block length %d", entry.Path, ref.Length)
					}
					size += int64(ref.Length)
				}
				if size != entry.Size {
					return fmt.Errorf("manifest path %q block bytes are %d, want %d", entry.Path, size, entry.Size)
				}
				actual.Files++
				actual.PlaintextBytes += entry.Size
			case SymlinkEntry:
				if os.FileMode(entry.Mode)&os.ModeSymlink == 0 || entry.Size < 0 || len(entry.Blocks) != 0 || entry.LinkTarget == "" || !utf8.ValidString(entry.LinkTarget) || entry.ChangeToken != "" {
					return fmt.Errorf("manifest symlink %q contains invalid data", entry.Path)
				}
				switch entry.LinkKind {
				case UnknownSymlinkTarget, FileSymlinkTarget, DirectorySymlinkTarget:
				default:
					return fmt.Errorf("manifest symlink %q has invalid target kind %q", entry.Path, entry.LinkKind)
				}
				actual.Symlinks++
			default:
				return fmt.Errorf("manifest path %q has unknown kind %q", entry.Path, entry.Kind)
			}
		}
		root, exists := paths["."]
		if !exists {
			return fmt.Errorf("manifest source %q has no root entry", source.ID)
		}
		if source.Digest != root.Digest {
			return fmt.Errorf("manifest source %q digest does not match its root entry", source.ID)
		}
		for path := range paths {
			if path == "." {
				continue
			}
			parent := "."
			if separator := strings.LastIndexByte(path, '/'); separator >= 0 {
				parent = path[:separator]
			}
			parentEntry, exists := paths[parent]
			if !exists || parentEntry.Kind != DirectoryEntry {
				return fmt.Errorf("manifest path %q has no directory parent %q", path, parent)
			}
		}
	}
	if actual.Files != manifest.Statistics.Files ||
		actual.Directories != manifest.Statistics.Directories ||
		actual.Symlinks != manifest.Statistics.Symlinks ||
		actual.PlaintextBytes != manifest.Statistics.PlaintextBytes {
		return errors.New("manifest statistics do not match its entries")
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
		return errors.New("manifest entry path is empty")
	}
	if strings.Contains(path, `\`) {
		return fmt.Errorf("manifest path %q contains a backslash", path)
	}
	if strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return fmt.Errorf("manifest path %q is absolute", path)
	}
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' || strings.HasPrefix(path, "//") {
		return fmt.Errorf("manifest path %q is a drive or UNC path", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned != path || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("manifest path %q is not canonical", path)
	}
	if err := fsname.Path(path); err != nil {
		return fmt.Errorf("manifest path %q is not portable: %w", path, err)
	}
	return nil
}

package backup

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mgomes/ressik/internal/chunk"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/fsname"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/pathcheck"
	"github.com/mgomes/ressik/internal/repository"
	"github.com/mgomes/ressik/internal/retention"
	"github.com/mgomes/ressik/merkle"
)

// Engine captures and restores snapshots in one repository.
type Engine struct {
	repository *repository.Repository
	splitter   *chunk.Fixed
	now        func() time.Time
}

const standaloneConfigurationID = "00000000000000000000000000000000"

// New returns a backup engine using the version-one chunk size.
func New(repo *repository.Repository) (*Engine, error) {
	splitter, err := chunk.NewFixed(chunk.DefaultSize)
	if err != nil {
		return nil, err
	}
	return &Engine{repository: repo, splitter: splitter, now: time.Now}, nil
}

// Backup captures one complete plan, commits it atomically, then applies its
// local retention policy.
func (e *Engine) Backup(ctx context.Context, planID string, plan config.Plan) (repository.Summary, error) {
	return e.BackupConfig(ctx, standaloneConfigurationID, planID, plan)
}

// BackupFull captures a plan without reusing prior file metadata.
func (e *Engine) BackupFull(ctx context.Context, planID string, plan config.Plan) (repository.Summary, error) {
	return e.BackupConfigFull(ctx, standaloneConfigurationID, planID, plan)
}

// BackupConfig captures a plan within one persistent configuration namespace.
func (e *Engine) BackupConfig(
	ctx context.Context,
	configurationID string,
	planID string,
	plan config.Plan,
) (repository.Summary, error) {
	return e.backup(ctx, configurationID, planID, plan, true)
}

// BackupConfigFull captures a plan without reusing prior file metadata.
func (e *Engine) BackupConfigFull(
	ctx context.Context,
	configurationID string,
	planID string,
	plan config.Plan,
) (repository.Summary, error) {
	return e.backup(ctx, configurationID, planID, plan, false)
}

func (e *Engine) backup(
	ctx context.Context,
	configurationID string,
	planID string,
	plan config.Plan,
	reusePrevious bool,
) (repository.Summary, error) {
	var summary repository.Summary
	err := e.repository.Exclusive(ctx, func() (workErr error) {
		if err := e.validatePlanPaths(planID, plan); err != nil {
			return err
		}
		protected, err := protectedRepositoryDirectories(e.repository.Root())
		if err != nil {
			return err
		}
		committed := false
		var manifest repository.Manifest
		defer func() {
			if committed || ctx.Err() != nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if _, err := e.repository.Collect(cleanupCtx); err != nil {
				workErr = errors.Join(workErr, fmt.Errorf("collect blocks from failed backup: %w", err))
			}
		}()

		var previous *repository.Manifest
		if reusePrevious {
			loadedPrevious, err := e.previousManifest(ctx, configurationID, planID)
			if err != nil {
				return err
			}
			previous = loadedPrevious
		}
		id, err := object.RandomID()
		if err != nil {
			return err
		}
		manifest = repository.Manifest{
			Version:         1,
			ConfigurationID: configurationID,
			ID:              id,
			PlanID:          planID,
			PlanName:        plan.Name,
			CreatedAt:       e.now().UTC(),
			ChunkSize:       chunk.DefaultSize,
		}

		sourceIDs := make([]string, 0, len(plan.Sources))
		for sourceID := range plan.Sources {
			sourceIDs = append(sourceIDs, sourceID)
		}
		sort.Strings(sourceIDs)
		manifest.Sources = make([]repository.Source, 0, len(sourceIDs))
		planLeaves := make([]merkle.Digest, 0, len(sourceIDs))
		for _, sourceID := range sourceIDs {
			source, err := e.scanSource(
				ctx,
				sourceID,
				plan.Sources[sourceID].Path,
				previousSource(previous, sourceID, plan.Sources[sourceID].Path),
				protected,
				&manifest.Statistics,
			)
			if err != nil {
				return fmt.Errorf("capture source %q: %w", sourceID, err)
			}
			manifest.Sources = append(manifest.Sources, source)
			root, ok := entryAt(source.Entries, ".")
			if !ok {
				return fmt.Errorf("capture source %q: root entry is missing", sourceID)
			}
			planLeaves = append(planLeaves, merkle.Leaf(sourceLeaf(sourceID, root)))
		}
		for _, source := range manifest.Sources {
			if err := verifySource(ctx, source); err != nil {
				return fmt.Errorf("verify source %q: %w", source.ID, err)
			}
		}
		manifest.Root = merkle.Root(planLeaves)
		if err := verifyManifestTree(ctx, manifest, make(map[object.ID]repository.BlockRef)); err != nil {
			return fmt.Errorf("validate captured Merkle tree: %w", err)
		}
		if err := e.repository.Commit(ctx, manifest); err != nil {
			return err
		}
		committed = true
		summary = repository.Summary{
			ID:              manifest.ID,
			ConfigurationID: manifest.ConfigurationID,
			PlanID:          manifest.PlanID,
			PlanName:        manifest.PlanName,
			CreatedAt:       manifest.CreatedAt,
			Root:            manifest.Root,
			Statistics:      manifest.Statistics,
		}
		if err := e.prune(ctx, manifest, plan.Retention); err != nil {
			return fmt.Errorf("snapshot %s committed but retention failed: %w", manifest.ID, err)
		}
		return nil
	})
	return summary, err
}

func (e *Engine) validatePlanPaths(planID string, plan config.Plan) error {
	sourceIDs := make([]string, 0, len(plan.Sources))
	for sourceID := range plan.Sources {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	for _, sourceID := range sourceIDs {
		source := plan.Sources[sourceID]
		info, err := os.Lstat(source.Path)
		if err != nil {
			return fmt.Errorf("inspect plan %q source %q: %w", planID, sourceID, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		overlaps, err := pathcheck.Overlap(e.repository.Root(), source.Path)
		if err != nil {
			return fmt.Errorf("compare plan %q source %q with repository: %w", planID, sourceID, err)
		}
		if overlaps {
			return fmt.Errorf("plan %q source %q physically overlaps the Ressik repository", planID, sourceID)
		}
	}
	return nil
}

func (e *Engine) previousManifest(
	ctx context.Context,
	configurationID string,
	planID string,
) (*repository.Manifest, error) {
	snapshots, err := e.repository.ListConfig(ctx, configurationID, planID)
	if err != nil {
		return nil, fmt.Errorf("list prior snapshots: %w", err)
	}
	if len(snapshots) == 0 {
		return nil, nil
	}
	manifest, err := e.repository.Load(ctx, snapshots[0].ID)
	if err != nil {
		return nil, fmt.Errorf("load prior snapshot %s: %w", snapshots[0].ID, err)
	}
	if manifest.ChunkSize != chunk.DefaultSize {
		return nil, nil
	}
	return &manifest, nil
}

func previousSource(manifest *repository.Manifest, sourceID, path string) *repository.Source {
	if manifest == nil {
		return nil
	}
	for i := range manifest.Sources {
		source := &manifest.Sources[i]
		if source.ID == sourceID && source.OriginalPath == path {
			return source
		}
	}
	return nil
}

func (e *Engine) scanSource(
	ctx context.Context,
	sourceID string,
	path string,
	previous *repository.Source,
	protected *pathcheck.IdentitySet,
	stats *repository.Stats,
) (repository.Source, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return repository.Source{}, err
	}
	previousEntries := make(map[string]repository.Entry)
	if previous != nil {
		previousEntries = make(map[string]repository.Entry, len(previous.Entries))
		for _, entry := range previous.Entries {
			previousEntries[entry.Path] = entry
		}
	}
	root, entries, err := e.scanEntry(ctx, path, ".", info, previousEntries, protected, stats)
	if err != nil {
		return repository.Source{}, err
	}
	return repository.Source{
		ID:           sourceID,
		OriginalPath: path,
		Digest:       root.Digest,
		Entries:      entries,
	}, nil
}

func (e *Engine) scanEntry(
	ctx context.Context,
	path string,
	relative string,
	before fs.FileInfo,
	previous map[string]repository.Entry,
	protected *pathcheck.IdentitySet,
	stats *repository.Stats,
) (repository.Entry, []repository.Entry, error) {
	if err := ctx.Err(); err != nil {
		return repository.Entry{}, nil, err
	}

	switch {
	case before.Mode().IsRegular():
		return e.scanFile(ctx, path, relative, before, previous, stats)
	case before.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return repository.Entry{}, nil, err
		}
		target = portableLinkTarget(target)
		if !utf8.ValidString(target) {
			return repository.Entry{}, nil, fmt.Errorf("symlink target contains invalid UTF-8")
		}
		if err := validateSymlinkTarget(target); err != nil {
			return repository.Entry{}, nil, fmt.Errorf("symlink target %q is not portable: %w", target, err)
		}
		linkKind := symlinkTargetKind(path)
		digest := merkle.Leaf(symlinkLeaf(target, linkKind))
		stats.Symlinks++
		entry := repository.Entry{
			Path:       relative,
			Kind:       repository.SymlinkEntry,
			Mode:       uint32(before.Mode()),
			ModifiedAt: before.ModTime().UTC(),
			Size:       before.Size(),
			Digest:     digest,
			LinkTarget: target,
			LinkKind:   linkKind,
		}
		return entry, []repository.Entry{entry}, nil
	case before.IsDir():
		if protected.Matches(before) {
			return repository.Entry{}, nil, errors.New("source contains an alias of the Ressik repository")
		}
		return e.scanDirectory(ctx, path, relative, before, previous, protected, stats)
	default:
		return repository.Entry{}, nil, fmt.Errorf("unsupported filesystem mode %s", before.Mode())
	}
}

func validateSymlinkTarget(target string) error {
	if target == "" {
		return errors.New("target is empty")
	}
	if strings.Contains(target, `\`) {
		return errors.New("target contains a backslash")
	}
	if strings.HasPrefix(target, "/") {
		return errors.New("absolute targets are platform-specific")
	}
	components := strings.Split(target, "/")
	for _, component := range components {
		if component == "." || component == ".." {
			continue
		}
		if err := fsname.Component(component); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) scanDirectory(
	ctx context.Context,
	path string,
	relative string,
	before fs.FileInfo,
	previous map[string]repository.Entry,
	protected *pathcheck.IdentitySet,
	stats *repository.Stats,
) (repository.Entry, []repository.Entry, error) {
	children, err := os.ReadDir(path)
	if err != nil {
		return repository.Entry{}, nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })

	entries := make([]repository.Entry, 0, len(children)+1)
	leaves := make([]merkle.Digest, 0, len(children))
	foldedNames := make(map[string]string, len(children))
	for _, child := range children {
		if err := fsname.Component(child.Name()); err != nil {
			return repository.Entry{}, nil, fmt.Errorf("filename %q is not portable: %w", child.Name(), err)
		}
		folded := fsname.Fold(child.Name())
		if previous, exists := foldedNames[folded]; exists {
			return repository.Entry{}, nil, fmt.Errorf(
				"filenames %q and %q collide on case-insensitive filesystems",
				previous,
				child.Name(),
			)
		}
		foldedNames[folded] = child.Name()
		childPath := filepath.Join(path, child.Name())
		info, err := os.Lstat(childPath)
		if err != nil {
			return repository.Entry{}, nil, err
		}
		childRelative := child.Name()
		if relative != "." {
			childRelative = relative + "/" + child.Name()
		}
		root, childEntries, err := e.scanEntry(ctx, childPath, childRelative, info, previous, protected, stats)
		if err != nil {
			return repository.Entry{}, nil, fmt.Errorf("scan %q: %w", childRelative, err)
		}
		leaves = append(leaves, merkle.Leaf(directoryLeaf(child.Name(), root)))
		entries = append(entries, childEntries...)
	}

	after, err := os.Lstat(path)
	if err != nil {
		return repository.Entry{}, nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return repository.Entry{}, nil, errors.New("directory changed while it was being captured")
	}
	digest := merkle.Root(leaves)
	stats.Directories++
	root := repository.Entry{
		Path:       relative,
		Kind:       repository.DirectoryEntry,
		Mode:       uint32(before.Mode()),
		ModifiedAt: before.ModTime().UTC(),
		Size:       before.Size(),
		Digest:     digest,
	}
	entries = append(entries, root)
	return root, entries, nil
}

func (e *Engine) scanFile(
	ctx context.Context,
	path string,
	relative string,
	before fs.FileInfo,
	previous map[string]repository.Entry,
	stats *repository.Stats,
) (repository.Entry, []repository.Entry, error) {
	changeToken, err := fileChangeToken(path, before)
	if err != nil {
		return repository.Entry{}, nil, fmt.Errorf("read file change token: %w", err)
	}
	if entry, ok := previous[relative]; ok && unchangedFile(entry, before, changeToken) {
		present, err := e.blocksPresent(ctx, entry.Blocks)
		if err != nil {
			return repository.Entry{}, nil, err
		}
		if present {
			entry.Blocks = append([]repository.BlockRef(nil), entry.Blocks...)
			stats.Files++
			stats.PlaintextBytes += entry.Size
			stats.ReusedBlocks += len(entry.Blocks)
			return entry, []repository.Entry{entry}, nil
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return repository.Entry{}, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return repository.Entry{}, nil, err
	}
	if !os.SameFile(before, opened) {
		return repository.Entry{}, nil, errors.New("file was replaced before capture")
	}

	var (
		builder merkle.Builder
		refs    []repository.BlockRef
		index   uint64
	)
	err = e.splitter.Split(ctx, file, func(block []byte) error {
		ref, created, err := e.repository.PutBlock(ctx, block)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
		builder.Add(merkle.Leaf(blockLeaf(index, ref)))
		index++
		if created {
			stats.NewBlocks++
			stats.StoredBytes += int64(ref.Length)
		} else {
			stats.ReusedBlocks++
		}
		return nil
	})
	if err != nil {
		return repository.Entry{}, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return repository.Entry{}, nil, err
	}
	pathAfter, err := os.Lstat(path)
	if err != nil {
		return repository.Entry{}, nil, err
	}
	if !os.SameFile(before, pathAfter) || before.Size() != after.Size() ||
		before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return repository.Entry{}, nil, errors.New("file changed while it was being captured")
	}
	afterChangeToken, err := fileChangeToken(path, pathAfter)
	if err != nil {
		return repository.Entry{}, nil, fmt.Errorf("reread file change token: %w", err)
	}
	if changeToken != afterChangeToken {
		return repository.Entry{}, nil, errors.New("file identity changed while it was being captured")
	}

	digest := builder.Digest()
	stats.Files++
	stats.PlaintextBytes += before.Size()
	entry := repository.Entry{
		Path:        relative,
		Kind:        repository.FileEntry,
		Mode:        uint32(before.Mode()),
		ModifiedAt:  before.ModTime().UTC(),
		Size:        before.Size(),
		Digest:      digest,
		Blocks:      refs,
		ChangeToken: changeToken,
	}
	return entry, []repository.Entry{entry}, nil
}

func (e *Engine) blocksPresent(ctx context.Context, refs []repository.BlockRef) (bool, error) {
	for _, ref := range refs {
		present, err := e.repository.HasBlock(ctx, ref)
		if err != nil {
			return false, err
		}
		if !present {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) prune(
	ctx context.Context,
	manifest repository.Manifest,
	policy config.Retention,
) error {
	snapshots, err := e.repository.ListConfig(ctx, manifest.ConfigurationID, manifest.PlanID)
	if err != nil {
		return err
	}
	remove := retention.Select(snapshots, retention.Policy{
		KeepLast: policy.KeepLast,
		KeepFor:  policy.KeepFor.DurationValue(),
		Protect:  manifest.ID,
	}, e.now())
	if len(remove) > 0 {
		stored, err := e.repository.Load(ctx, manifest.ID)
		if err != nil {
			return fmt.Errorf("reload retained snapshot: %w", err)
		}
		if err := verifyManifestTree(ctx, stored, make(map[object.ID]repository.BlockRef)); err != nil {
			return fmt.Errorf("verify retained snapshot Merkle tree: %w", err)
		}
		if err := e.authenticateManifestBlocks(ctx, stored); err != nil {
			return fmt.Errorf("authenticate retained snapshot blocks: %w", err)
		}
	}
	for _, snapshot := range remove {
		if err := e.repository.Remove(snapshot.ID); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if _, err := e.repository.Collect(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) authenticateManifestBlocks(ctx context.Context, manifest repository.Manifest) error {
	blocks := make(map[object.ID]repository.BlockRef)
	for _, source := range manifest.Sources {
		for _, entry := range source.Entries {
			for _, ref := range entry.Blocks {
				if previous, exists := blocks[ref.ID]; exists && previous.Length != ref.Length {
					return fmt.Errorf("block %s has conflicting lengths", ref.ID)
				}
				blocks[ref.ID] = ref
			}
		}
	}
	for _, ref := range blocks {
		if _, err := e.repository.ReadBlock(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}

func blockLeaf(index uint64, ref repository.BlockRef) []byte {
	payload := make([]byte, 1+8+4+len(ref.ID))
	payload[0] = 0x01
	binary.BigEndian.PutUint64(payload[1:9], index)
	binary.BigEndian.PutUint32(payload[9:13], ref.Length)
	copy(payload[13:], ref.ID[:])
	return payload
}

func directoryLeaf(name string, entry repository.Entry) []byte {
	return metadataLeaf(0x02, name, entry)
}

func sourceLeaf(id string, root repository.Entry) []byte {
	return metadataLeaf(0x03, id, root)
}

func metadataLeaf(domain byte, name string, entry repository.Entry) []byte {
	kind := string(entry.Kind)
	payload := make([]byte, 1+4+len(name)+4+len(kind)+4+8+8+len(entry.Digest))
	payload[0] = domain
	binary.BigEndian.PutUint32(payload[1:5], uint32(len(name)))
	offset := 5
	copy(payload[offset:], name)
	offset += len(name)
	binary.BigEndian.PutUint32(payload[offset:offset+4], uint32(len(kind)))
	offset += 4
	copy(payload[offset:], kind)
	offset += len(kind)
	binary.BigEndian.PutUint32(payload[offset:offset+4], entry.Mode)
	offset += 4
	binary.BigEndian.PutUint64(payload[offset:offset+8], uint64(entry.Size))
	offset += 8
	binary.BigEndian.PutUint64(payload[offset:offset+8], uint64(entry.ModifiedAt.UnixNano()))
	offset += 8
	copy(payload[offset:], entry.Digest[:])
	return payload
}

func symlinkLeaf(target string, kind repository.SymlinkTargetKind) []byte {
	payload := make([]byte, 1+4+len(kind)+len(target))
	payload[0] = 0x04
	binary.BigEndian.PutUint32(payload[1:5], uint32(len(kind)))
	copy(payload[5:], kind)
	copy(payload[5+len(kind):], target)
	return payload
}

func unchangedFile(entry repository.Entry, info fs.FileInfo, changeToken string) bool {
	return entry.Kind == repository.FileEntry &&
		entry.ChangeToken != "" &&
		entry.ChangeToken == changeToken &&
		entry.Size == info.Size() &&
		entry.Mode == uint32(info.Mode()) &&
		entry.ModifiedAt.Equal(info.ModTime())
}

func symlinkTargetKind(path string) repository.SymlinkTargetKind {
	info, err := os.Stat(path)
	if err != nil {
		return repository.UnknownSymlinkTarget
	}
	switch {
	case info.Mode().IsRegular():
		return repository.FileSymlinkTarget
	case info.IsDir():
		return repository.DirectorySymlinkTarget
	default:
		return repository.UnknownSymlinkTarget
	}
}

func entryAt(entries []repository.Entry, path string) (repository.Entry, bool) {
	for _, entry := range entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return repository.Entry{}, false
}

func verifySource(ctx context.Context, source repository.Source) error {
	for _, entry := range source.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := source.OriginalPath
		if entry.Path != "." {
			path = filepath.Join(path, filepath.FromSlash(entry.Path))
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect %q after capture: %w", entry.Path, err)
		}
		if uint32(info.Mode()) != entry.Mode || info.Size() != entry.Size || !info.ModTime().Equal(entry.ModifiedAt) {
			return fmt.Errorf("path %q metadata changed after capture", entry.Path)
		}
		switch entry.Kind {
		case repository.FileEntry:
			if !info.Mode().IsRegular() || info.Size() != entry.Size {
				return fmt.Errorf("file %q changed after capture", entry.Path)
			}
			changeToken, err := fileChangeToken(path, info)
			if err != nil {
				return fmt.Errorf("read file %q change token after capture: %w", entry.Path, err)
			}
			if entry.ChangeToken != "" && changeToken != entry.ChangeToken {
				return fmt.Errorf("file %q identity changed after capture", entry.Path)
			}
		case repository.DirectoryEntry:
			if !info.IsDir() {
				return fmt.Errorf("directory %q changed type after capture", entry.Path)
			}
		case repository.SymlinkEntry:
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("symlink %q changed type after capture", entry.Path)
			}
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read symlink %q after capture: %w", entry.Path, err)
			}
			target = portableLinkTarget(target)
			if target != entry.LinkTarget {
				return fmt.Errorf("symlink %q target changed after capture", entry.Path)
			}
			if kind := symlinkTargetKind(path); kind != entry.LinkKind {
				return fmt.Errorf("symlink %q target kind changed after capture", entry.Path)
			}
		}
	}
	return nil
}

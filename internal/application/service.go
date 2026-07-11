package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mgomes/ressik/internal/backup"
	"github.com/mgomes/ressik/internal/config"
	"github.com/mgomes/ressik/internal/object"
	"github.com/mgomes/ressik/internal/pathcheck"
	"github.com/mgomes/ressik/internal/repository"
)

// ErrPlanNotFound reports that a requested plan is absent from configuration.
var ErrPlanNotFound = errors.New("backup plan not found")

// Service coordinates configuration and repository operations.
type Service struct {
	configPath string
}

// New returns an application service for one absolute or relative config path.
func New(configPath string) *Service {
	return &Service{configPath: configPath}
}

// Initialize creates an empty configuration and its encrypted local
// repository.
func (s *Service) Initialize() (*config.Loaded, error) {
	path, err := config.Path(s.configPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("config already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect config path: %w", err)
	}

	cfg := config.New()
	configurationID, err := object.RandomID()
	if err != nil {
		return nil, err
	}
	cfg.ConfigurationID = configurationID.String()[:32]
	repositoryPath := filepath.Join(filepath.Dir(path), "repository")
	if s.configPath == "" && os.Getenv("RESSIK_CONFIG") == "" {
		repositoryPath, err = config.DefaultRepository()
		if err != nil {
			return nil, err
		}
	}
	cfg.Repository = repositoryPath
	if _, err := repository.Initialize(repositoryPath); err != nil {
		return nil, err
	}
	if err := config.SaveNew(path, cfg); err != nil {
		return nil, err
	}
	return config.Load(path)
}

// Config loads the current strict configuration.
func (s *Service) Config() (*config.Loaded, error) {
	path, err := config.Path(s.configPath)
	if err != nil {
		return nil, err
	}
	return config.Load(path)
}

// Check verifies configuration, repository access, and every source path.
func (s *Service) Check(live bool) (*config.Loaded, error) {
	loaded, err := s.Config()
	if err != nil {
		return nil, err
	}
	if !live {
		return loaded, nil
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return nil, err
	}
	if err := validateLivePaths(repo.Root(), loaded.Config.Plans); err != nil {
		return nil, err
	}
	return loaded, nil
}

// CheckRepository verifies configuration and repository access without
// requiring every configured source to be online.
func (s *Service) CheckRepository() (*config.Loaded, error) {
	loaded, err := s.Config()
	if err != nil {
		return nil, err
	}
	if _, err := repository.Open(loaded.Config.Repository); err != nil {
		return nil, err
	}
	return loaded, nil
}

// Run captures one plan immediately, even when automatic scheduling is
// disabled for that plan.
func (s *Service) Run(ctx context.Context, planID string) (repository.Summary, error) {
	return s.run(ctx, planID, false)
}

// RunFull captures one plan without the incremental metadata shortcut.
func (s *Service) RunFull(ctx context.Context, planID string) (repository.Summary, error) {
	return s.run(ctx, planID, true)
}

func (s *Service) run(ctx context.Context, planID string, full bool) (repository.Summary, error) {
	loaded, plan, err := s.plan(planID)
	if err != nil {
		return repository.Summary{}, err
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return repository.Summary{}, err
	}
	if err := validateLivePaths(repo.Root(), map[string]config.Plan{planID: plan}); err != nil {
		return repository.Summary{}, err
	}
	engine, err := backup.New(repo)
	if err != nil {
		return repository.Summary{}, err
	}
	if full {
		return engine.BackupConfigFull(ctx, loaded.ID, planID, plan)
	}
	return engine.BackupConfig(ctx, loaded.ID, planID, plan)
}

// Collect removes repository data that is not reachable from a committed
// snapshot.
func (s *Service) Collect(ctx context.Context) error {
	_, err := s.GarbageCollect(ctx)
	return err
}

// GarbageCollect removes repository data that is not reachable from a
// committed snapshot and returns the number of removed blocks.
func (s *Service) GarbageCollect(ctx context.Context) (int, error) {
	loaded, err := s.Config()
	if err != nil {
		return 0, err
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return 0, err
	}
	var removed int
	if err := repo.Exclusive(ctx, func() error {
		var err error
		removed, err = repo.Collect(ctx)
		return err
	}); err != nil {
		return 0, err
	}
	return removed, nil
}

// Verify authenticates all committed repository data and recomputes its
// Merkle trees.
func (s *Service) Verify(ctx context.Context) (backup.Verification, error) {
	loaded, err := s.Config()
	if err != nil {
		return backup.Verification{}, err
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return backup.Verification{}, err
	}
	engine, err := backup.New(repo)
	if err != nil {
		return backup.Verification{}, err
	}
	return engine.Verify(ctx)
}

// Snapshots returns committed snapshots newest first.
func (s *Service) Snapshots(ctx context.Context, planID string) ([]repository.Summary, error) {
	loaded, err := s.Config()
	if err != nil {
		return nil, err
	}
	if planID != "" {
		if _, exists := loaded.Config.Plans[planID]; !exists {
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
		}
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return nil, err
	}
	var snapshots []repository.Summary
	if err := repo.Exclusive(ctx, func() error {
		var err error
		snapshots, err = repo.ListConfig(ctx, loaded.ID, planID)
		return err
	}); err != nil {
		return nil, err
	}
	return snapshots, nil
}

// Restore materializes a snapshot under destination.
func (s *Service) Restore(
	ctx context.Context,
	snapshotID string,
	options backup.RestoreOptions,
) error {
	loaded, err := s.Config()
	if err != nil {
		return err
	}
	id, err := object.ParseID(snapshotID)
	if err != nil {
		return err
	}
	repo, err := repository.Open(loaded.Config.Repository)
	if err != nil {
		return err
	}
	engine, err := backup.New(repo)
	if err != nil {
		return err
	}
	return engine.Restore(ctx, id, options)
}

// PlanIDs returns sorted configured plan identifiers.
func (s *Service) PlanIDs() ([]string, error) {
	loaded, err := s.Config()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(loaded.Config.Plans))
	for id := range loaded.Config.Plans {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Service) plan(planID string) (*config.Loaded, config.Plan, error) {
	loaded, err := s.Config()
	if err != nil {
		return nil, config.Plan{}, err
	}
	plan, exists := loaded.Config.Plans[planID]
	if !exists {
		return nil, config.Plan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
	}
	return loaded, plan, nil
}

func validateLivePaths(repositoryPath string, plans map[string]config.Plan) error {
	for planID, plan := range plans {
		for sourceID, source := range plan.Sources {
			info, err := os.Lstat(source.Path)
			if err != nil {
				return fmt.Errorf("inspect plan %q source %q: %w", planID, sourceID, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			overlaps, err := pathcheck.Overlap(repositoryPath, source.Path)
			if err != nil {
				return fmt.Errorf("compare plan %q source %q with repository: %w", planID, sourceID, err)
			}
			if overlaps {
				return fmt.Errorf("plan %q source %q physically overlaps the Ressik repository", planID, sourceID)
			}
		}
	}
	return nil
}

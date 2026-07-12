package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zeebo/blake3"
)

const configEnvironment = "RESSIK_CONFIG"

// Path resolves the configuration path from an explicit flag, the
// RESSIK_CONFIG environment variable, or the platform configuration folder.
func Path(explicit string) (string, error) {
	path := explicit
	if path == "" {
		path = os.Getenv(configEnvironment)
	}
	if path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(root, "ressik", "config.yaml")
	}
	path, err := expandHome(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

// DefaultRepository returns the platform-local repository path used by init.
func DefaultRepository() (string, error) {
	root, err := dataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ressik", "repository"), nil
}

// DefaultCredentialDir returns the owner-only credential directory for one
// canonical configuration path.
func DefaultCredentialDir(configPath string) (string, error) {
	resolved, err := Path(configPath)
	if err != nil {
		return "", err
	}
	if physical, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = physical
	}
	identity := filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := blake3.Sum256([]byte(identity))

	root, err := dataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(
		root,
		"ressik",
		"credentials",
		"instances",
		fmt.Sprintf("%x", digest[:16]),
	), nil
}

// DefaultStateDir returns the platform-local daemon state path.
func DefaultStateDir() (string, error) {
	var root string
	var err error
	switch runtime.GOOS {
	case "darwin":
		root, err = os.UserConfigDir()
	case "windows":
		root, err = os.UserCacheDir()
	default:
		root = os.Getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(root) {
			var home string
			home, err = os.UserHomeDir()
			root = filepath.Join(home, ".local", "state")
		}
	}
	if err != nil {
		return "", fmt.Errorf("find user state directory: %w", err)
	}
	return filepath.Join(root, "ressik"), nil
}

func (l *Loaded) resolve() error {
	base := filepath.Dir(l.Path)
	if l.Config.Repository == "" {
		l.RepositoryDefaulted = true
		path, err := DefaultRepository()
		if err != nil {
			return fmt.Errorf("resolve default repository: %w", err)
		}
		l.Config.Repository = path
	} else {
		l.HomeDependent = l.HomeDependent || usesHome(l.Config.Repository)
		path, err := resolvePath(base, l.Config.Repository)
		if err != nil {
			return fmt.Errorf("resolve repository: %w", err)
		}
		l.Config.Repository = path
	}

	for planID, plan := range l.Config.Plans {
		for sourceID, source := range plan.Sources {
			l.HomeDependent = l.HomeDependent || usesHome(source.Path)
			path, err := resolvePath(base, source.Path)
			if err != nil {
				return fmt.Errorf("resolve plan %q source %q: %w", planID, sourceID, err)
			}
			source.Path = path
			plan.Sources[sourceID] = source
		}
		l.Config.Plans[planID] = plan
	}
	return nil
}

func resolvePath(base, path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	expanded = filepath.FromSlash(expanded)
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(base, expanded)
	}
	resolved, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return filepath.Join(home, filepath.FromSlash(strings.TrimLeft(path[1:], `/\`))), nil
	}
	if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("home directories for other users are not supported: %q", path)
	}
	return path, nil
}

func usesHome(path string) bool {
	return path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`)
}

func dataRoot() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		root, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("find user application data directory: %w", err)
		}
		return root, nil
	case "windows":
		root, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("find local application data directory: %w", err)
		}
		return root, nil
	default:
		if root := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(root) {
			return root, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		return filepath.Join(home, ".local", "share"), nil
	}
}

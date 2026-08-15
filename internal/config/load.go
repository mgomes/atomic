package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zeebo/blake3"
	"go.yaml.in/yaml/v3"

	"github.com/mgomes/atomic/internal/fsdurable"
)

// Loaded contains a validated configuration and its resolved paths.
type Loaded struct {
	Path   string
	ID     string
	Config Config
	// HomeDependent reports that a configured path used leading-tilde expansion.
	HomeDependent bool
	// RepositoryDefaulted reports that repository was omitted from YAML.
	RepositoryDefaulted bool
}

// Load strictly decodes, resolves, and validates a configuration file.
func Load(path string) (*Loaded, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve config aliases: %w", err)
	}

	data, err := readBounded(resolved)
	if err != nil {
		return nil, err
	}
	if err := validateYAML(data); err != nil {
		return nil, fmt.Errorf("validate YAML: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode config: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("decode trailing config data: %w", err)
	}

	loaded := &Loaded{Path: resolved, Config: cfg}
	if err := loaded.resolve(); err != nil {
		return nil, err
	}
	if err := loaded.Validate(); err != nil {
		return nil, err
	}
	loaded.ID = loaded.Config.ConfigurationID
	if loaded.ID == "" {
		identity := resolved
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(identity)
		}
		digest := blake3.Sum256([]byte(identity))
		loaded.ID = fmt.Sprintf("%x", digest[:16])
	}
	return loaded, nil
}

// SaveNew creates a configuration without overwriting an existing file.
func SaveNew(path string, cfg Config) error {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if err := fsdurable.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	file, err := os.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("config already exists: %s", resolved)
		}
		return fmt.Errorf("create config: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(resolved)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := fsdurable.SyncDir(filepath.Dir(resolved)); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	complete = true
	return nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("config exceeds %d bytes", maxConfigSize)
	}
	return data, nil
}

func validateYAML(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	return walkYAML(&document)
}

func walkYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("aliases and anchors are not allowed")
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return fmt.Errorf("custom YAML tag %q is not allowed", node.Tag)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for pair := range len(node.Content) / 2 {
			key := node.Content[pair*2]
			if key.Kind != yaml.ScalarNode {
				return errors.New("YAML mapping keys must be scalars")
			}
			if key.Value == "<<" {
				return errors.New("YAML merge keys are not allowed")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML key %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := walkYAML(child); err != nil {
			return err
		}
	}
	return nil
}

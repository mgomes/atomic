package config

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	// CurrentVersion is the configuration schema understood by this build.
	CurrentVersion = 1
	maxConfigSize  = 1 << 20
)

var (
	idPattern              = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	configurationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	durationPattern        = regexp.MustCompile(`^([1-9][0-9]*)(h|d|w)$`)
)

// Config is the versioned, desired configuration for Ressik.
type Config struct {
	Version         int                    `yaml:"version"`
	ConfigurationID string                 `yaml:"configuration_id,omitempty"`
	Repository      string                 `yaml:"repository,omitempty"`
	Ignore          []string               `yaml:"ignore,omitempty"`
	Destinations    map[string]Destination `yaml:"destinations,omitempty"`
	Plans           map[string]Plan        `yaml:"plans"`
}

// Plan describes one independently scheduled backup.
type Plan struct {
	Name         string            `yaml:"name"`
	Enabled      bool              `yaml:"enabled"`
	Sources      map[string]Source `yaml:"sources"`
	Schedule     *Schedule         `yaml:"schedule,omitempty"`
	Retention    Retention         `yaml:"retention,omitempty"`
	Destinations []string          `yaml:"destinations,omitempty"`
}

// Source names one file or directory included in a plan.
type Source struct {
	Path string `yaml:"path"`
}

// Schedule describes when the daemon should run a plan.
type Schedule struct {
	Kind     string   `yaml:"kind"`
	Days     []string `yaml:"days,omitempty"`
	At       string   `yaml:"at"`
	Timezone string   `yaml:"timezone,omitempty"`
}

// Retention combines minimum-copy and maximum-age rules. A snapshot is kept
// while either configured rule protects it.
type Retention struct {
	KeepLast int      `yaml:"keep_last,omitempty"`
	KeepFor  Duration `yaml:"keep_for,omitempty"`
}

// Destination reserves the provider-neutral destination schema for a future
// release. This build rejects configured destinations.
type Destination struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name,omitempty"`
}

// Duration is a configuration duration supporting hours, days, and weeks.
type Duration time.Duration

// DurationValue returns the standard-library duration value.
func (d Duration) DurationValue() time.Duration {
	return time.Duration(d)
}

// MarshalYAML renders a duration using the largest exact supported unit.
func (d Duration) MarshalYAML() (any, error) {
	value := time.Duration(d)
	switch {
	case value == 0:
		return "", nil
	case value%(7*24*time.Hour) == 0:
		return fmt.Sprintf("%dw", value/(7*24*time.Hour)), nil
	case value%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", value/(24*time.Hour)), nil
	case value%time.Hour == 0:
		return fmt.Sprintf("%dh", value/time.Hour), nil
	default:
		return nil, fmt.Errorf("duration must be a whole number of hours: %s", value)
	}
}

// UnmarshalYAML parses a positive duration such as 12h, 30d, or 8w.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got YAML kind %d", node.Kind)
	}
	if node.Tag == "!!null" {
		*d = 0
		return nil
	}
	if node.Value == "" {
		*d = 0
		return nil
	}

	matches := durationPattern.FindStringSubmatch(node.Value)
	if matches == nil {
		return fmt.Errorf("duration %q must match <number><h|d|w>", node.Value)
	}

	count, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", node.Value, err)
	}

	unit := time.Hour
	switch matches[2] {
	case "d":
		unit = 24 * time.Hour
	case "w":
		unit = 7 * 24 * time.Hour
	}
	if count > int64((1<<63-1)/unit) {
		return fmt.Errorf("duration %q is too large", node.Value)
	}

	*d = Duration(time.Duration(count) * unit)
	return nil
}

// New returns an empty version-one configuration.
func New() Config {
	return Config{
		Version:      CurrentVersion,
		Destinations: make(map[string]Destination),
		Plans:        make(map[string]Plan),
	}
}

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// Config is abacus's own small settings file. It holds only choices the user (or a confirmed
// setup step) made: which actor to lease as, which tracker directory Tasks reads, and which
// Lambe config Queues reads.
type Config struct {
	Actor       string `json:"actor,omitempty"`
	TrackerDir  string `json:"tracker_dir,omitempty"`
	LambeConfig string `json:"lambe_config,omitempty"`
}

var actorRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ConfigKeys are the settable keys, in display order.
var ConfigKeys = []string{"actor", "tracker_dir", "lambe_config"}

// Set validates and sets one key; on error the config is unchanged.
func (c *Config) Set(key, val string) error {
	switch key {
	case "actor":
		if !actorRE.MatchString(val) {
			return errors.New("actor must be 1-64 characters: letters, digits, . _ @ - (no spaces)")
		}
		c.Actor = val
	case "tracker_dir", "lambe_config":
		if val == "" { // clearing is how a saved path is dropped on purpose
			if key == "tracker_dir" {
				c.TrackerDir = ""
			} else {
				c.LambeConfig = ""
			}
			return nil
		}
		if !filepath.IsAbs(val) || Sanitize(val) != val {
			return fmt.Errorf("%s must be an absolute path", key)
		}
		if key == "tracker_dir" {
			c.TrackerDir = filepath.Clean(val)
		} else {
			c.LambeConfig = filepath.Clean(val)
		}
	default:
		return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(ConfigKeys, ", "))
	}
	return nil
}

// Get returns one key's value.
func (c Config) Get(key string) (string, error) {
	switch key {
	case "actor":
		return c.Actor, nil
	case "tracker_dir":
		return c.TrackerDir, nil
	case "lambe_config":
		return c.LambeConfig, nil
	}
	return "", fmt.Errorf("unknown setting %q", key)
}

// ConfigStore reads and writes the config file at Path.
type ConfigStore struct{ Path string }

// Load reads the config; a missing file is an empty config, an unreadable one an error (abacus
// does not guess around a file it cannot parse, and Update never overwrites one).
func (s ConfigStore) Load() (Config, error) {
	var c Config
	if s.Path == "" {
		return c, nil
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("config %s is not valid JSON: %w", s.Path, err)
	}
	return c, nil
}

// Update loads the config, applies fn and writes it back atomically (temp file in the same
// directory, then rename). If fn fails nothing is written.
func (s ConfigStore) Update(fn func(*Config) error) error {
	if !filepath.IsAbs(s.Path) {
		return errors.New("config: no usable path (no home directory?)")
	}
	c, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&c); err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "config.json.tmp*")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmp.Name())
	b, _ := json.MarshalIndent(c, "", "  ")
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// xdgDir is $<env>/abacus when env is an absolute path, else $HOME/<fallback>/abacus.
func xdgDir(env, fallback string) (string, error) {
	if x := os.Getenv(env); filepath.IsAbs(x) {
		return filepath.Join(x, brand.Name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("no home directory")
	}
	return filepath.Join(home, fallback, brand.Name), nil
}

// DefaultConfigPath is $XDG_CONFIG_HOME/abacus/config.json or ~/.config/abacus/config.json.
func DefaultConfigPath() (string, error) {
	d, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// DefaultDataDir is $XDG_DATA_HOME/abacus or ~/.local/share/abacus.
func DefaultDataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
}

// DefaultWorkspaceDir is the managed fleet workspace abacus offers to create.
func DefaultWorkspaceDir() (string, error) {
	d, err := DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "workspace"), nil
}

// DefaultLambeConfigPath is the abacus-owned Lambe config that setup writes.
func DefaultLambeConfigPath() (string, error) {
	d, err := xdgDir("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "lambe.yaml"), nil
}

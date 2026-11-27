package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	BoardURL    string `json:"board_url,omitempty"`
	// SearchHistory is "off" to keep no search history (queries are stored in plain text).
	SearchHistory string `json:"search_history,omitempty"`
	// AgentCommand is the argv "work on this" runs in a new terminal: the program and its
	// arguments, with {task_id} and {title} filled in as separate arguments (see agentcmd.go).
	AgentCommand []string `json:"agent_command,omitempty"`
}

var actorRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ConfigKeys are the settable keys, in display order.
var ConfigKeys = []string{"actor", "tracker_dir", "lambe_config", "board_url", "search_history", "agent_command"}

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
	case "board_url":
		if val != "" {
			if err := ValidateBoardURL(val); err != nil {
				return err
			}
		}
		c.BoardURL = val
	case "search_history":
		switch val {
		case "on", "":
			c.SearchHistory = ""
		case "off":
			c.SearchHistory = "off"
		default:
			return errors.New("search_history must be on or off")
		}
	case "agent_command":
		argv, err := ParseAgentCommand(val)
		if err != nil {
			return err
		}
		c.AgentCommand = argv
	default:
		return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(ConfigKeys, ", "))
	}
	return nil
}

// ConfigListKeys are the keys whose value is an argv: they may be given as a list of words as well as text.
var ConfigListKeys = []string{"agent_command"}

// ConfigListValue is the text form of a list value for an argv key, checked by the parser of that key (so a list
// and the same JSON text are one value).
func ConfigListValue(key string, words []string) (string, error) {
	if !slices.Contains(ConfigListKeys, key) {
		return "", fmt.Errorf("%s takes a text value, not a list", key)
	}
	if len(words) == 0 {
		return "", errors.New(`agent_command is an empty list: only "" clears it`)
	}
	val := agentCommandJSON(words)
	if _, err := ParseAgentCommand(val); err != nil {
		return "", err
	}
	return val, nil
}

// agentCommandJSON is the one text form of an agent command: compact JSON, no HTML escaping, "" for none.
func agentCommandJSON(words []string) string {
	if len(words) == 0 {
		return ""
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(words)
	return strings.TrimSuffix(b.String(), "\n")
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
	case "board_url":
		return c.BoardURL, nil
	case "search_history":
		if c.SearchHistory == "off" {
			return "off", nil
		}
		return "on", nil
	case "agent_command":
		if len(c.AgentCommand) == 0 {
			return "", nil
		}
		return agentCommandJSON(c.AgentCommand), nil
	}
	return "", fmt.Errorf("unknown setting %q", key)
}

// ConfigStore reads and writes the config file at Path.
type ConfigStore struct{ Path string }

// errCorrupt is a config file that exists but is not valid JSON.
type errCorrupt struct {
	path string
	err  error
}

func (e *errCorrupt) Error() string {
	return fmt.Sprintf("config %s is not valid JSON: %v", e.path, e.err)
}
func (e *errCorrupt) Unwrap() error { return e.err }

// Load reads the config; a missing file is an empty config and an unreadable one an error.
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
		return Config{}, &errCorrupt{s.Path, err}
	}
	return c, nil
}

// Update is UpdateNote without the note.
func (s ConfigStore) Update(fn func(*Config) error) error {
	_, err := s.UpdateNote(fn)
	return err
}

// UpdateNote loads the config under a lock, applies fn and writes it back atomically (temp file in
// the same directory, fsynced, then renamed, then the directory is fsynced). If fn fails nothing
// is written. A file that is not valid JSON is kept as config.json.corrupt (mode 0600) and the
// change starts from an empty config; the note says so, for the step result.
func (s ConfigStore) UpdateNote(fn func(*Config) error) (note string, err error) {
	if !filepath.IsAbs(s.Path) {
		return "", errors.New("config: no usable path (no home directory?)")
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	unlock, err := lockConfig(s.Path)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	defer unlock()
	c, err := s.Load()
	var corrupt *errCorrupt
	isCorrupt := errors.As(err, &corrupt)
	switch {
	case isCorrupt:
		c = Config{}
	case err != nil:
		return "", err
	}
	if err := fn(&c); err != nil {
		return "", err
	}
	if isCorrupt {
		kept := s.Path + ".corrupt"
		if err := os.Rename(s.Path, kept); err != nil {
			return "", fmt.Errorf("config: could not set the unreadable file aside: %w", err)
		}
		_ = os.Chmod(kept, 0o600)
		note = "the old config file was not valid JSON; kept as " + kept + " and started from empty"
	}
	tmp, err := os.CreateTemp(dir, "config.json.tmp*")
	if err != nil {
		return note, fmt.Errorf("config: %w", err)
	}
	defer os.Remove(tmp.Name())
	b, _ := json.MarshalIndent(c, "", "  ")
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return note, fmt.Errorf("config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return note, fmt.Errorf("config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return note, fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return note, fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return note, fmt.Errorf("config: %w", err)
	}
	if d, err := os.Open(dir); err == nil { // make the rename itself durable
		_ = d.Sync()
		d.Close()
	}
	return note, nil
}

// ConfigCheck is a module that only exists to put the config file's health in Doctor.
type ConfigCheck struct{ store ConfigStore }

// NewConfigCheck returns the module that reports on abacus's own settings file.
func NewConfigCheck(s ConfigStore) *ConfigCheck { return &ConfigCheck{store: s} }

// ID implements Module.
func (*ConfigCheck) ID() string { return "config" }

// Name implements Module.
func (*ConfigCheck) Name() string { return brand.Name + " config" }

// Detect implements Module; the settings file needs nothing installed, so it is always ready.
func (*ConfigCheck) Detect(context.Context) Detection { return Detection{State: Ready} }

// Status implements Module; the settings file has no live status.
func (*ConfigCheck) Status(context.Context) Status { return Status{} }

// Quiet marks a module the overview and `version` leave out of the tool list.
func (*ConfigCheck) Quiet() bool { return true }

// Doctor reports whether the settings file can be read.
func (m *ConfigCheck) Doctor(context.Context) []Check {
	if _, err := m.store.Load(); err != nil {
		fix := "fix or delete " + m.store.Path
		var c *errCorrupt
		if errors.As(err, &c) {
			fix += " (the next setup step keeps a copy as " + filepath.Base(m.store.Path) + ".corrupt and starts clean)"
		}
		return []Check{{Name: "settings", Status: StatusWarn, Detail: Sanitize(err.Error()), Fix: Sanitize(fix)}}
	}
	return []Check{{Name: "settings", Status: StatusOK, Detail: m.store.Path}}
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

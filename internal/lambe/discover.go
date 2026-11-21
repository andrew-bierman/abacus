package lambe

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

var (
	// ErrNoDaemon means no usable descriptor: API disabled, daemon down, or nothing matched.
	ErrNoDaemon = errors.New("no live daemon found (state API disabled or daemon not running)")
	// ErrAmbiguous means several live daemons matched and no selector narrowed them.
	ErrAmbiguous = errors.New("several live daemons found; pass --lambe-config to choose one")
)

// Selector narrows discovery to one daemon by instance name or config path.
type Selector struct{ Instance, ConfigPath string }

// DiscoverOptions injects the process probes so tests never touch real processes.
type DiscoverOptions struct {
	StateDir string
	Selector Selector
	Identity func(pid int) (string, error)
	PidAlive func(pid int) bool
}

// Discover follows the algorithm in Lambe's state-api doc: enumerate descriptors, skip
// unreadable, unknown-schema, stale, impostor and non-loopback entries, then select.
func Discover(o DiscoverOptions) (Descriptor, error) {
	if o.Identity == nil {
		o.Identity = ProcessIdentity
	}
	if o.PidAlive == nil {
		o.PidAlive = pidAlive
	}
	paths, _ := filepath.Glob(filepath.Join(o.StateDir, "live", "stateapi-*.json"))
	sort.Strings(paths)
	var live []Descriptor
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var d Descriptor
		if json.Unmarshal(b, &d) != nil || d.SchemaVersion != 1 || d.Instance == "" || d.PID <= 0 {
			continue
		}
		if o.Selector.Instance != "" && d.Instance != o.Selector.Instance {
			continue
		}
		if o.Selector.ConfigPath != "" && d.ConfigPath != o.Selector.ConfigPath {
			continue
		}
		if !loopback(d.Addr) || !o.PidAlive(d.PID) {
			continue
		}
		if d.Identity != "" {
			// Fail closed: a descriptor that claims an identity we cannot verify is skipped.
			got, err := o.Identity(d.PID)
			if err != nil || got != d.Identity {
				continue
			}
		}
		live = append(live, d)
	}
	switch len(live) {
	case 0:
		return Descriptor{}, ErrNoDaemon
	case 1:
		return live[0], nil
	}
	return Descriptor{}, ErrAmbiguous
}

// loopback reports whether addr is host:port with the literal host 127.0.0.1.
func loopback(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	// A numeric port keeps userinfo or path tricks (127.0.0.1:80@evil) out of the base URL.
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n > 0
}

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

func configRun(t *testing.T, path string, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(append([]string{"config"}, args...), cli.Deps{Out: &o, Err: &e, Version: "t", Config: core.ConfigStore{Path: path}})
	return code, o.String(), e.String()
}

// set, get, show and path round-trip through the settings file; typing mistakes are usage errors (2) with
// nothing written, and a file that cannot be read is a failure (1).
func TestConfigVerbsRoundTripAndExitCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if code, out, _ := configRun(t, path, "path"); code != 0 || strings.TrimSpace(out) != path {
		t.Fatalf("path: %d %q", code, out)
	}
	if code, out, _ := configRun(t, path, "set", "actor", "ada"); code != 0 || out != "actor set\n" {
		t.Fatalf("set: %d %q", code, out)
	}
	if code, out, _ := configRun(t, path, "get", "actor"); code != 0 || out != "ada\n" {
		t.Fatalf("get: %d %q", code, out)
	}
	code, out, _ := configRun(t, path, "show")
	if code != 0 || !strings.Contains(out, "actor") || !strings.Contains(out, "ada") || !strings.Contains(out, "(not set)") {
		t.Fatalf("show: %d %q", code, out)
	}
	code, out, _ = configRun(t, path, "show", "--json")
	var v map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v["path"] != path || v["actor"] != "ada" {
		t.Fatalf("show --json: %d %q", code, out)
	}
	for _, c := range [][]string{{}, {"--json"}, {"frob"}, {"get"}, {"get", "nope"}, {"set", "nope", "x"}, {"set", "actor"}, {"path", "extra"}} {
		if code, out, e := configRun(t, path, c...); code != 2 || (out != "") != (len(c) > 0 && c[0] == "--json") || e == "" {
			t.Errorf("%v: code=%d out=%q err=%q", c, code, out, e)
		}
	}
	// a value that was refused is not written
	if _, out, _ := configRun(t, path, "get", "actor"); out != "ada\n" {
		t.Fatalf("a refused command changed the file: %q", out)
	}
	// a damaged settings file: reading is a failure (1), and a set keeps the old file as .corrupt and says so
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]string{{"show"}, {"get", "actor"}} {
		if code, out, _ := configRun(t, bad, c...); code != 1 || out != "" {
			t.Errorf("%v on a damaged file: code=%d out=%q", c, code, out)
		}
	}
	if code, _, e := configRun(t, bad, "set", "actor", "x"); code != 0 || e == "" {
		t.Fatalf("set on a damaged file: code=%d err=%q", code, e)
	}
	if kept, err := os.ReadFile(bad + ".corrupt"); err != nil || string(kept) != "{not json" {
		t.Fatalf("the damaged file was not kept: %q %v", kept, err)
	}
}

// Demo mode never reads the real settings file: show, get and path answer from an empty demo store with a demo
// path marker, whether demo is asked for by the flag or the environment.
func TestConfigDemoNeverReadsTheRealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-home", "c.json")
	if err := (core.ConfigStore{Path: path}).Update(func(c *core.Config) error { return c.Set("actor", "real-actor-name") }); err != nil {
		t.Fatal(err)
	}
	for _, how := range []struct {
		name string
		env  map[string]string
		flag []string
	}{{"flag", nil, []string{"--demo"}}, {"env", map[string]string{"ABACUS_DEMO": "1"}, nil}} {
		for _, verb := range [][]string{{"show"}, {"show", "--json"}, {"get", "actor"}, {"path"}} {
			var o, e bytes.Buffer
			args := append(append([]string{"config", verb[0]}, how.flag...), verb[1:]...) // flags come before the positionals
			code := cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: "t", Config: core.ConfigStore{Path: path}, Getenv: func(k string) string { return how.env[k] }})
			out := o.String() + e.String()
			if code != 0 || strings.Contains(out, "real-actor-name") || strings.Contains(out, "secret-home") {
				t.Errorf("%s %v: %d %q", how.name, verb, code, out)
			}
			if verb[len(verb)-1] == "--json" {
				var v map[string]any
				if json.Unmarshal(o.Bytes(), &v) != nil || v["demo"] != true || v["path"] != core.DemoConfigPath {
					t.Errorf("%s show --json: %s", how.name, o.String())
				}
			}
			if verb[0] == "path" && strings.TrimSpace(o.String()) != core.DemoConfigPath {
				t.Errorf("%s path: %q", how.name, o.String())
			}
		}
	}
}

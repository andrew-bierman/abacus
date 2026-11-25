package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrew-bierman/abacus/internal/agents"
	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

// fakeBinDir writes fake bd, lambe and beads-lease that report the given version tag.
func fakeBinDir(t *testing.T, tag string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("bd", `echo '{"version":"`+tag+`","schema_version":1}'`)
	write("lambe", `case "$1" in
  version) echo '{"version":"`+tag+`","api_version":1}' ;;
  doctor) echo '[{"name":"config","status":"fail","detail":"no config"}]'; exit 1 ;;
esac`)
	write("beads-lease", `echo '{"api_version":1,"version":"`+tag+`"}'`)
	return dir
}

func env(t *testing.T, vars map[string]string) Env {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // lambe's default state dir, kept off the real one
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	root := t.TempDir()
	return Env{Getenv: func(k string) string { return vars[k] }, Cwd: cwd,
		Config: core.ConfigStore{Path: filepath.Join(root, "c", "config.json")}, Workspace: filepath.Join(root, "ws"), Agents: hermeticAgents(),
		Monitor: &agents.MonitorOptions{LookPath: func(string) (string, error) { return "", errors.New("no monitor on PATH in tests") }}}
}

// hermeticAgents is the Agents domain's sources with nothing in them: no processes, no session files,
// no terminals. A test never reads the real machine.
func hermeticAgents() *agents.Options {
	return &agents.Options{FS: fstest.MapFS{}, Home: "/nonexistent",
		Procs: func(context.Context) ([]agents.Proc, error) { return nil, nil },
		Panes: func(context.Context) ([]agents.Pane, error) { return nil, nil }}
}

func detect(t *testing.T, e Env) map[string]core.Detection {
	t.Helper()
	out := map[string]core.Detection{}
	for _, en := range Build(cli.Options{}, e).Detect(context.Background()) {
		out[en.Module.ID()] = en.Detection
	}
	return out
}

func TestBinaryPrecedenceIsOverrideThenBundledThenPATH(t *testing.T) {
	override, bundled, onPath := fakeBinDir(t, "override"), fakeBinDir(t, "bundled"), fakeBinDir(t, "path")
	t.Setenv("PATH", onPath+string(os.PathListSeparator)+"/usr/bin:/bin")
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return []string{bundled} }
	defer func() { core.SidecarDirs = old }()
	defer core.CleanupSidecarEnv()

	// Nothing set: the verified bundled copy wins over PATH, for all three tools.
	got := detect(t, env(t, nil))
	for _, id := range []string{"bd", "lambe", "beads-lease"} {
		if got[id].Source != core.SourceBundled || !strings.Contains(got[id].Version, "bundled") {
			t.Errorf("%s: want the bundled copy, got %+v", id, got[id])
		}
	}
	// An explicit ABACUS_*_BIN beats both.
	got = detect(t, env(t, map[string]string{"ABACUS_BD_BIN": filepath.Join(override, "bd")}))
	if got["bd"].Source != core.SourceOverride || got["bd"].Version != "override" {
		t.Errorf("override: %+v", got["bd"])
	}
	if got["lambe"].Source != core.SourceBundled {
		t.Errorf("an override for one tool must not change another: %+v", got["lambe"])
	}
	// With no bundled copy, PATH is used.
	core.SidecarDirs = func() []string { return nil }
	if got = detect(t, env(t, nil)); got["bd"].Source != core.SourcePATH || got["bd"].Version != "path" {
		t.Errorf("PATH fallback: %+v", got["bd"])
	}
}

func guiderFor(t *testing.T, e Env, id string) core.Guider {
	t.Helper()
	for _, en := range Build(cli.Options{}, e).Detect(context.Background()) {
		if en.Module.ID() == id {
			return en.Module.(core.Guider)
		}
	}
	t.Fatalf("no module %s", id)
	return nil
}

func TestASavedLambeConfigIsAdoptedOnlyWhileTheFileExists(t *testing.T) {
	bin := fakeBinDir(t, "x")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return nil }
	defer func() { core.SidecarDirs = old }()

	e := env(t, nil)
	if guiderFor(t, e, "lambe").Guide(context.Background()) == nil {
		t.Fatal("no config anywhere: Queues setup is offered")
	}
	cfgFile := filepath.Join(t.TempDir(), "lambe.yaml")
	if err := os.WriteFile(cfgFile, []byte("harness: pi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Config.Update(func(c *core.Config) error { return c.Set("lambe_config", cfgFile) }); err != nil {
		t.Fatal(err)
	}
	if guiderFor(t, e, "lambe").Guide(context.Background()) != nil {
		t.Fatal("the saved config exists: it is adopted, nothing more is offered")
	}
	os.Remove(cfgFile) // gone: back to offering setup
	if guiderFor(t, e, "lambe").Guide(context.Background()) == nil {
		t.Fatal("a saved path that is gone is not adopted")
	}
	// The user's own choices are never redirected to a starter config.
	if g := guiderFor(t, env(t, map[string]string{"ABACUS_LAMBE_CONFIG": "/my/own.yaml"}), "lambe"); g.Guide(context.Background()) != nil {
		t.Fatal("ABACUS_LAMBE_CONFIG set")
	}
	if g := guiderFor(t, env(t, map[string]string{"ABACUS_LAMBE_STATE_DIR": t.TempDir()}), "lambe"); g.Guide(context.Background()) != nil {
		t.Fatal("ABACUS_LAMBE_STATE_DIR set")
	}
}

func TestASavedTrackerThatWentMissingIsReportedAndNeverOfferedForReplacement(t *testing.T) {
	bin := fakeBinDir(t, "x")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return nil }
	defer func() { core.SidecarDirs = old }()
	e := env(t, nil)
	gone := filepath.Join(t.TempDir(), "gone")
	if err := e.Config.Update(func(c *core.Config) error { return c.Set("tracker_dir", gone) }); err != nil {
		t.Fatal(err)
	}
	var bd core.TaskProvider
	for _, en := range Build(cli.Options{}, e).Detect(context.Background()) {
		if en.Module.ID() == "bd" {
			bd = en.Module.(core.TaskProvider)
			if en.Module.(core.Guider).Guide(context.Background()) != nil {
				t.Fatal("no workspace may be offered while a saved tracker_dir is set")
			}
		}
	}
	_, err := bd.Tasks(context.Background())
	var nc *core.NotConnectedError
	if !asNotConnected(err, &nc) || !strings.Contains(nc.Reason, gone) || !strings.Contains(nc.HowToConnect, `tracker_dir ""`) {
		t.Fatalf("the plain problem must name the path and the fix: %v", err)
	}
}

func TestInitNeverReplacesASavedTrackerDir(t *testing.T) {
	st := core.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := st.Update(func(c *core.Config) error { return c.Set("tracker_dir", "/saved/one") }); err != nil {
		t.Fatal(err)
	}
	save := saveTracker(st)
	if err := save("/new/workspace"); err == nil || !strings.Contains(err.Error(), "/saved/one") {
		t.Fatalf("%v", err)
	}
	if c, _ := st.Load(); c.TrackerDir != "/saved/one" {
		t.Fatalf("overwritten: %+v", c)
	}
	if err := save("/saved/one"); err != nil { // the same path is fine
		t.Fatal(err)
	}
	st2 := core.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	if err := saveTracker(st2)("/new/workspace"); err != nil {
		t.Fatal(err)
	}
}

func asNotConnected(err error, out **core.NotConnectedError) bool {
	nc, ok := err.(*core.NotConnectedError)
	*out = nc
	return ok
}

func TestAnUnsafeBundledBdFallsBackToPATHForReadingButNeverForWriting(t *testing.T) {
	bundled, onPath := fakeBinDir(t, "bundled"), fakeBinDir(t, "path")
	if err := os.Chmod(filepath.Join(bundled, "bd"), 0o777); err != nil { // world-writable: refused
		t.Fatal(err)
	}
	t.Setenv("PATH", onPath+string(os.PathListSeparator)+"/usr/bin:/bin")
	old := core.SidecarDirs
	core.SidecarDirs = func() []string { return []string{bundled} }
	defer func() { core.SidecarDirs = old }()
	defer core.CleanupSidecarEnv()

	e := env(t, nil)
	reg := Build(cli.Options{}, e)
	entries := reg.Detect(context.Background())
	var bd core.Entry
	for _, en := range entries {
		if en.Module.ID() == "bd" {
			bd = en
		}
	}
	d := bd.Detection
	if d.Source != core.SourcePATH || d.Version != "path" || !strings.Contains(d.Ignored, "world-writable") {
		t.Fatalf("reading may use PATH, and says why: %+v", d)
	}
	ov := core.BuildOverview(context.Background(), entries)
	var note string
	for _, tl := range ov.Tools {
		if tl.ID == "bd" {
			note = tl.Note
		}
	}
	if !strings.HasPrefix(note, "the copy next to abacus was not used (world-writable") {
		t.Fatalf("Doctor must not be silent about it: %q", note)
	}
	w := bd.Module.(core.TaskWriter)
	if _, err := w.BuildTaskArgv(core.TaskChange{Op: core.TaskCreate, Title: "x"}); err == nil || !strings.Contains(err.Error(), "not writing") {
		t.Fatalf("a write must not fall back to the PATH bd: %v", err)
	}
	if _, err := w.RunTask(context.Background(), []string{"bd", "create", "--json", "--title=x"}); err == nil || !strings.Contains(err.Error(), "not writing") {
		t.Fatalf("RunTask must refuse too: %v", err)
	}
}

func TestBoardBinaryNeedsAnIdentityCheckAndTheURLPrecedenceIsEnvThenConfigThenDefault(t *testing.T) {
	dir := t.TempDir()
	good, wrong := filepath.Join(dir, "board"), filepath.Join(dir, "vb")
	if err := os.WriteFile(good, []byte("#!/bin/sh\nprintf '%s\\n' \"board 0.54.0: your Vercel team's message board, for people and agents\" '' 'usage: board <command> [options]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrong, []byte("#!/bin/sh\necho 'vb 1.0.2 an unrelated tool'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := env(t, map[string]string{"ABACUS_BOARD_BIN": good, "PATH": ""})
	if d := detect(t, e)["board"]; d.State != core.Ready || d.Version != "0.54.0" || d.Source != core.SourceOverride {
		t.Fatalf("%+v", d)
	}
	e = env(t, map[string]string{"ABACUS_BOARD_BIN": wrong})
	if d := detect(t, e)["board"]; d.State != core.Incompatible || !strings.Contains(d.Detail, "not Vercel Board") {
		t.Fatalf("a wrong package named vb must read as not Vercel Board: %+v", d)
	}

	const a, b = "http://127.0.0.1:4100/api/board", "https://example.com/api/board"
	for _, c := range []struct {
		abacus, cfg, boardEnv string
		want, src             string
		bad                   bool
	}{
		{want: ""},
		{cfg: b, want: b, src: "board_url (config)"},
		{abacus: a, cfg: b, want: a, src: "ABACUS_BOARD_URL"},
		{boardEnv: b, want: b, src: "BOARD_URL"},
		{cfg: b, boardEnv: a, want: b, src: "board_url (config)"}, // abacus's own settings win over the shell's BOARD_URL
		{abacus: "ftp://nope", cfg: b, bad: true},                 // a bad value is an error, not a silent fall back
		{boardEnv: "http://example.com/api/board", bad: true},     // BOARD_URL is validated like the others
		{boardEnv: "https://example.com/x?token=s", bad: true},
	} {
		e := Env{Getenv: func(k string) string {
			switch k {
			case "ABACUS_BOARD_URL":
				return c.abacus
			case "BOARD_URL":
				return c.boardEnv
			}
			return ""
		}}
		o := boardOptions(e, core.Config{BoardURL: c.cfg})
		if o.URL != c.want || o.URLSource != c.src || (o.URLErr != "") != c.bad {
			t.Errorf("%+v -> %+v", c, o)
		}
		if c.bad && strings.Contains(o.URLErr, "token=") {
			t.Errorf("an error must not echo a credential: %q", o.URLErr)
		}
	}
}

// --demo registers providers for every domain, so every tab has data, and none of them can offer
// a setup step or open a real tool.
func TestDemoRegistryProvidesEveryDomain(t *testing.T) {
	reg := Build(cli.Options{Demo: true}, Env{Getenv: func(string) string { return "" }})
	p := core.ResolveProviders(reg.Detect(context.Background()))
	if p.Tasks == nil || p.Leases == nil || p.Queues == nil || p.Board == nil || p.Insights == nil || p.Writer == nil || p.Reader == nil {
		t.Fatalf("a demo needs a provider for every domain: %+v", p)
	}
	for d, g := range p.Guiders {
		if guide := g.Guide(context.Background()); guide != nil {
			t.Fatalf("a demo must offer no first-run setup, %s offers %+v", d, guide)
		}
	}
	// the demo board can plan a post (--dry-run, the TUI confirm) and "applies" it to nothing
	if p.BoardWriter == nil {
		t.Fatal("a demo board can plan a post")
	}
	if res, err := p.BoardWriter.RunBoard(context.Background(), nil, core.BoardChange{Op: core.BoardPost, Text: "x"}); err != nil || res.ID != "demo-1" {
		t.Fatalf("a demo post is applied to nothing: %+v %v", res, err)
	}
}

// A monitor on PATH is never run by a test: the hermetic Env finds none.
func TestNoRealMonitorIsRunFromPathInTests(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(dir, "abtop"), []byte("#!/bin/sh\ntouch "+marker+"\necho '{\"sessions\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Cleanup(core.SetEmbeddedForTest(nil))
	e := env(t, nil)
	core.ReadAgents(context.Background(), Build(cli.Options{}, e).Detect(context.Background()))
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a test ran a monitor found on PATH")
	}
}

// --tracker wins over ABACUS_BD_DIR, a saved tracker_dir and the working directory; without it nothing changes.
func TestTrackerFlagWinsOverEnvAndConfig(t *testing.T) {
	e := env(t, map[string]string{"ABACUS_BD_DIR": "/from/env"})
	if err := e.Config.Update(func(c *core.Config) error { return c.Set("tracker_dir", "/from/config") }); err != nil {
		t.Fatal(err)
	}
	dirOf := func(o cli.Options) string {
		for _, en := range Build(o, e).Detect(context.Background()) {
			if td, ok := en.Module.(interface{ TrackerDir() string }); ok && en.Module.ID() == "bd" {
				return td.TrackerDir()
			}
		}
		t.Fatal("no bd module")
		return ""
	}
	if got := dirOf(cli.Options{}); got != "/from/env" {
		t.Errorf("without the flag: %q", got)
	}
	if got := dirOf(cli.Options{Tracker: "/from/flag"}); got != "/from/flag" {
		t.Errorf("--tracker must win, got %q", got)
	}
}

func TestNativeClaudeQuotaConfigDir(t *testing.T) {
	const (
		custom   = "/fixture/custom"
		fallback = `{"five_hour":{"used_percentage":20},"updated_at":1767268740}`
		fresh    = `{"five_hour":{"used_percentage":70},"updated_at":1767268800}`
		old      = `{"five_hour":{"used_percentage":70},"updated_at":1767267900}`
		unknown  = `{"five_hour":{"used_percentage":70}}`
	)
	ctx := context.Background()
	for _, tc := range []struct {
		name, dir, customJSON, defaultJSON string
		want                               float64
		stale                              bool
	}{
		{"configured only", custom, fresh, "", 70, false},
		{"configured newer", custom, fresh, fallback, 70, false},
		{"default newer", custom, old, fallback, 20, false},
		{"configured wins tie", custom, fresh, strings.Replace(fresh, "70", "20", 1), 70, false},
		{"default with no override", "", fresh, fallback, 20, false},
		{"missing override file", custom, "", fallback, 20, false},
		{"malformed override file", custom, "{", fallback, 20, false},
		{"oversized override file", custom, fresh + strings.Repeat(" ", 64<<10), fallback, 20, false},
		{"relative override ignored", "fixture/custom", fresh, fallback, 20, false},
		{"stale configured reading", custom, old, "", 70, true},
		{"undated configured reading", custom, unknown, "", 70, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			if tc.customJSON != "" {
				fsys["fixture/custom/abtop-rate-limits.json"] = &fstest.MapFile{Data: []byte(tc.customJSON)}
			}
			if tc.defaultJSON != "" {
				fsys["fixture/home/.claude/abtop-rate-limits.json"] = &fstest.MapFile{Data: []byte(tc.defaultJSON)}
			}
			e := env(t, map[string]string{"CLAUDE_CONFIG_DIR": tc.dir})
			e.Agents.FS = fsys
			e.Agents.Home = "/fixture/home"
			e.Agents.Now = func() time.Time { return time.Unix(1767268800, 0) }
			feed := agentFeeds(e, false)[0].(*agents.Local)
			_, extra, err := feed.Agents(ctx)
			if err != nil || len(extra.RateLimits) != 1 {
				t.Fatalf("quota = %+v, err = %v", extra.RateLimits, err)
			}
			got := extra.RateLimits[0]
			if got.Harness != core.HarnessClaude || got.FiveHourPct == nil || *got.FiveHourPct != tc.want || got.Stale != tc.stale {
				t.Fatalf("quota = %+v, want Claude %g%% with stale=%v", got, tc.want, tc.stale)
			}
		})
	}
}

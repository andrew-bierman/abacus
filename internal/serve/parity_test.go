package serve_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andrew-bierman/abacus/internal/cli"
	"github.com/andrew-bierman/abacus/internal/core"
)

var update = flag.Bool("update", false, "rewrite the golden transcripts in testdata")

// cliRun runs one command over the fixture's providers, audit, config, setup and clock.
func cliRun(t *testing.T, f *fixture, args ...string) (int, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := cli.Run(args, cli.Deps{Out: &o, Err: &e, Version: f.cfg.Version, Audit: f.audit, Config: f.cfg.Config, Setup: f.cfg.Setup,
		Now: f.cfg.Now, Build: func(cli.Options) *core.Registry { return f.cfg.Build() }})
	return code, o.String()
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return b.String()
}

// Every read method returns the bytes the command prints with --json, on one line: the same typed
// report, from the same core code, not a second implementation.
func TestReadMethodsReturnWhatTheCommandsPrint(t *testing.T) {
	c, f := readyClient(t)
	for _, tc := range []struct {
		method string
		params any
		args   []string
	}{
		{"version", nil, []string{"version", "--json"}},
		{"doctor", nil, []string{"doctor", "--json"}},
		{"status", nil, []string{"status", "--json"}},
		{"board", nil, []string{"board", "--json"}},
		{"leases", nil, []string{"leases", "--json"}},
		{"queues", nil, []string{"queues", "--json"}},
		{"agents", nil, []string{"agents", "--json"}},
		{"agents", map[string]any{"state": "running", "harness": "claude-code"}, []string{"agents", "--state", "running", "--harness", "claude-code", "--json"}},
		{"tasks.summary", nil, []string{"tasks", "--json"}},
		{"tasks.list", nil, []string{"tasks", "list", "--json"}},
		{"tasks.list", map[string]any{"query": "label:ui", "sort": "priority", "reverse": true, "preset": "all"}, []string{"tasks", "list", "--query", "label:ui", "--sort", "priority", "--reverse", "--preset", "all", "--json"}},
		{"tasks.tree", nil, []string{"tasks", "list", "--tree", "--json"}},
		{"tasks.board", nil, []string{"tasks", "board", "--json"}},
		{"tasks.board", map[string]any{"group": "priority", "closed": true}, []string{"tasks", "board", "--group", "priority", "--closed", "--json"}},
		{"tasks.show", map[string]any{"id": "ac-12"}, []string{"tasks", "show", "ac-12", "--json"}},
		{"tasks.graph", map[string]any{"id": "ac-12", "depth": 3}, []string{"tasks", "graph", "ac-12", "--depth", "3", "--json"}},
		{"tasks.graph", map[string]any{"whole": true}, []string{"tasks", "graph", "--whole", "--json"}},
		{"tasks.history", map[string]any{"id": "ac-12", "limit": 5}, []string{"tasks", "history", "ac-12", "--limit", "5", "--json"}},
		{"search", map[string]any{"query": "lease", "limit": 3}, []string{"search", "lease", "--limit", "3", "--json"}},
		{"search", map[string]any{"query": "status:open", "domains": []string{"tasks"}}, []string{"search", "status:open", "--domain", "tasks", "--json"}},
		{"setup.state", nil, []string{"setup", "--json"}},
		{"config.show", nil, []string{"config", "show", "--json"}},
	} {
		code, want := cliRun(t, f, tc.args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", tc.args, code, want)
		}
		got := c.call(tc.method, tc.params)
		if got.Error != nil {
			t.Errorf("%s: %+v", tc.method, got.Error)
			continue
		}
		if string(got.Result) != compact(t, want) {
			t.Errorf("%s %v differs from %v:\nserve: %.300s\ncli:   %.300s", tc.method, tc.params, tc.args, got.Result, compact(t, want))
		}
	}
	// the workspace report is what status carries under domains.workspace
	_, st := cliRun(t, f, "status", "--json")
	var doc struct {
		Domains struct {
			Workspace json.RawMessage `json:"workspace"`
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(st), &doc); err != nil || len(doc.Domains.Workspace) == 0 {
		t.Fatalf("status has no workspace: %v", err)
	}
	var want, got map[string]json.RawMessage
	_ = json.Unmarshal(doc.Domains.Workspace, &want)
	m := c.call("workspace", nil)
	_ = json.Unmarshal(m.Result, &got)
	delete(got, "schema_version")
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if string(wb) != string(gb) {
		t.Errorf("workspace differs from status domains.workspace:\n%s\n%s", gb, wb)
	}
}

// A plan made over the protocol has the digest `--dry-run --json` prints for the same change, and the
// same display argv: a client can show one and a script can pass the other to --expect-digest.
func TestPlanDigestsMatchTheCommandLine(t *testing.T) {
	c, f := readyClient(t)
	for _, tc := range []struct {
		method string
		params any
		args   []string
	}{
		{"plan.task.create", map[string]any{"title": "new thing", "priority": 2, "type": "task", "description": "d"}, []string{"tasks", "create", "--title", "new thing", "--priority", "2", "--type", "task", "--description", "d"}},
		{"plan.task.update", map[string]any{"id": "ac-12", "status": "blocked"}, []string{"tasks", "update", "ac-12", "--status", "blocked"}},
		{"plan.task.close", map[string]any{"id": "ac-12", "reason": "done"}, []string{"tasks", "close", "ac-12", "--reason", "done"}},
		{"plan.task.reopen", map[string]any{"id": "ac-03"}, []string{"tasks", "reopen", "ac-03"}},
		{"plan.task.label-add", map[string]any{"id": "ac-12", "label": "ui"}, []string{"tasks", "label", "add", "ac-12", "ui"}},
		{"plan.task.assign", map[string]any{"id": "ac-12", "assignee": "mei"}, []string{"tasks", "assign", "ac-12", "mei"}},
		{"plan.task.assign", map[string]any{"id": "ac-12", "clear": true}, []string{"tasks", "assign", "ac-12", "--clear"}},
		{"plan.task.priority", map[string]any{"id": "ac-12", "priority": 3}, []string{"tasks", "priority", "ac-12", "3"}},
		{"plan.task.dep-add", map[string]any{"id": "ac-30", "blocked_by": "ac-33"}, []string{"tasks", "dep", "add", "ac-30", "--blocked-by", "ac-33"}},
		{"plan.task.dep-remove", map[string]any{"id": "ac-16", "blocked_by": "ac-15"}, []string{"tasks", "dep", "remove", "ac-16", "--blocked-by", "ac-15"}},
		{"plan.task.comment", map[string]any{"id": "ac-12", "text": "hello"}, []string{"tasks", "comment", "ac-12", "hello"}},
		{"plan.tasks.bulk", map[string]any{"ids": []string{"ac-30", "ac-33"}, "op": "priority", "value": "1"}, []string{"tasks", "bulk", "--ids", "ac-30,ac-33", "--op", "priority", "--value", "1"}},
		{"plan.board.post", map[string]any{"channel": "dev", "text": "hello"}, []string{"board", "post", "dev", "hello"}},
		{"plan.board.reply", map[string]any{"id": "2", "text": "thanks"}, []string{"board", "reply", "2", "thanks"}},
	} {
		code, out := cliRun(t, f, append(tc.args, "--dry-run", "--json")...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", tc.args, code, out)
		}
		var want struct {
			Digest string   `json:"plan_digest"`
			Argv   []string `json:"argv"`
		}
		_ = json.Unmarshal([]byte(out), &want)
		var got core.ServePlan
		c.call(tc.method, tc.params).into(t, &got)
		if got.PlanDigest != want.Digest || want.Digest == "" {
			t.Errorf("%s: digest %s, the command line says %s", tc.method, got.PlanDigest, want.Digest)
		}
		if strings.Join(got.Argv, " ") != strings.Join(want.Argv, " ") {
			t.Errorf("%s: argv %v vs %v", tc.method, got.Argv, want.Argv)
		}
	}
}

// ---- golden transcripts ----

type step struct {
	raw    string // a raw line, when the step is not a well formed request
	do     func() // something that happens between two requests (the clock moves)
	method string
	params any
}

var (
	planRE  = regexp.MustCompile(`plan_[0-9a-f]{32}`)
	batchRE = regexp.MustCompile(`"batch_id":"[^"]*"`)
)

// transcript runs the steps one at a time and compares the whole conversation (every request line and
// every response line, plan ids and batch ids renamed in order of appearance) with testdata/NAME.golden.
func transcript(t *testing.T, name string, f *fixture, steps []step) {
	t.Helper()
	c := startClient(t, f.cfg)
	ids := map[string]string{}
	norm := func(s string) string {
		s = planRE.ReplaceAllStringFunc(s, func(id string) string {
			if _, ok := ids[id]; !ok {
				ids[id] = fmt.Sprintf("plan_%032d", len(ids)+1)
			}
			return ids[id]
		})
		return batchRE.ReplaceAllString(s, `"batch_id":"<batch>"`)
	}
	var b strings.Builder
	for _, s := range steps {
		if s.do != nil {
			s.do()
			continue
		}
		if s.raw != "" {
			c.raw(s.raw)
			fmt.Fprintf(&b, "-> %s\n<- %s\n", s.raw, norm(c.anonResponse().Raw))
			continue
		}
		if m, ok := s.params.(map[string]any); ok { // a plan id the test learned is replaced by the real one
			for k, v := range m {
				if str, ok := v.(string); ok {
					for real, fake := range ids {
						if str == fake {
							m[k] = real
						}
					}
				}
			}
		}
		id := c.send(s.method, s.params)
		resp := c.await(id)
		rq := map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": s.method}
		if s.params != nil {
			rq["params"] = s.params
		}
		req, _ := json.Marshal(rq)
		fmt.Fprintf(&b, "-> %s\n<- %s\n", norm(string(req)), norm(resp.Raw))
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/serve -update writes it)", err)
	}
	if b.String() != string(want) {
		t.Errorf("%s differs from its golden (-update accepts):\n--- want\n%s\n--- got\n%s", name, want, b.String())
	}
}

func initStep() step {
	return step{method: "initialize", params: map[string]any{"protocol_version": 1, "client": map[string]any{"name": "golden", "version": "1"}}}
}

func TestTranscriptHandshakeAndMalformedInput(t *testing.T) {
	f := newFixture(t)
	f.cfg.Version = "0.0.0-golden"
	transcript(t, "handshake", f, []step{
		{method: "version"},
		{method: "initialize", params: map[string]any{"protocol_version": 9}},
		initStep(),
		{method: "workspace"},
		{method: "nosuch.method"},
		{raw: `{"jsonrpc":"2.0","id":3,`},
		{raw: `[]`},
		{method: "tasks.show", params: map[string]any{"id": "nosuch-1"}},
		{method: "tasks.list", params: map[string]any{"sort": "nosuch"}},
	})
}

func TestTranscriptPlanApply(t *testing.T) {
	f := newFixture(t)
	transcript(t, "plan-apply", f, []step{
		initStep(),
		{method: "plan.task.priority", params: map[string]any{"id": "ac-12", "priority": 3}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001", "expected_digest": strings.Repeat("0", 64)}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
		{method: "plan.tasks.bulk", params: map[string]any{"ids": []string{"ac-30", "ac-33"}, "op": "priority", "value": "1"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000002"}},
		{method: "plan.board.post", params: map[string]any{"channel": "dev", "text": "deploy done"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000003"}},
		{method: "plan.board.post", params: map[string]any{"channel": "dev", "text": "deploy done"}},
		{method: "plan.cancel", params: map[string]any{"plan_id": "plan_00000000000000000000000000000003"}},
	})
}

func TestTranscriptConfigSetupAndExpiry(t *testing.T) {
	f := newFixture(t)
	f.withActorGuide()
	transcript(t, "config-setup-expiry", f, []step{
		initStep(),
		{method: "plan.config.set", params: map[string]any{"key": "search_history", "value": "off"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
		{method: "plan.config.set", params: map[string]any{"key": "nosuch", "value": "x"}},
		{method: "plan.setup", params: map[string]any{"step": "actor"}},
		{do: func() { f.advance(11 * time.Minute) }},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000002"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000002"}},
		{method: "plan.setup", params: map[string]any{"step": "queues"}},
	})
}

func TestTranscriptDemoPlans(t *testing.T) {
	f := newFixture(t)
	f.cfg.Demo = true
	transcript(t, "demo-plans", f, []step{
		initStep(),
		{method: "plan.task.close", params: map[string]any{"id": "ac-12"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
		{method: "plan.config.set", params: map[string]any{"key": "actor", "value": "mei"}},
		{method: "plan.cancel", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
	})
}

func TestTranscriptUnknownOutcome(t *testing.T) {
	f := newFixture(t)
	f.tasks.runHook = func(_ context.Context, _ []string) error {
		return core.UnknownOutcome("bd timed out; refresh before retrying")
	}
	transcript(t, "unknown-outcome", f, []step{
		initStep(),
		{method: "plan.task.close", params: map[string]any{"id": "ac-12", "reason": "done"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
		{method: "apply", params: map[string]any{"plan_id": "plan_00000000000000000000000000000001"}},
	})
}

var _ = time.Now

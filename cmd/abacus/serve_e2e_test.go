package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests below run the real program: the test binary starts itself as abacus (see TestMain), with an
// isolated home, over the demo providers, so no tracker, hub, board or real state directory is touched.

func TestMain(m *testing.M) {
	if os.Getenv("ABACUS_TEST_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

type proc struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
	home   string
}

func start(t *testing.T, stdin any, args ...string) *proc {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = home
	cmd.Env = []string{"ABACUS_TEST_AS_MAIN=1", "HOME=" + home, "XDG_STATE_HOME=" + home + "/s", "XDG_CONFIG_HOME=" + home + "/c", "XDG_DATA_HOME=" + home + "/d",
		"PATH=/usr/bin:/bin"}
	p := &proc{cmd: cmd, stderr: &bytes.Buffer{}, home: home}
	cmd.Stderr = p.stderr
	var err error
	switch v := stdin.(type) {
	case nil:
		p.in, err = cmd.StdinPipe()
	case *os.File:
		cmd.Stdin = v
	}
	if err != nil {
		t.Fatal(err)
	}
	outR, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.out = bufio.NewReaderSize(outR, 1<<20)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return p
}

func (p *proc) send(line string) {
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		panic(err)
	}
}

func (p *proc) read(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	done := make(chan string, 1)
	go func() { l, _ := p.out.ReadString('\n'); done <- l }()
	select {
	case l := <-done:
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdout carries something that is not a protocol line: %q (stderr: %s)", l, p.stderr)
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatalf("no line from the server (stderr: %s)", p.stderr)
		return nil
	}
}

func (p *proc) wait(t *testing.T) *os.ProcessState {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the process did not exit")
	}
	return p.cmd.ProcessState
}

const initReq = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1}}`

func TestServeOverARealPipe(t *testing.T) {
	p := start(t, nil, "serve", "--stdio", "--demo")
	p.send(initReq)
	if r := p.read(t); r["error"] != nil || r["result"] == nil {
		t.Fatalf("%v", r)
	}
	p.send(`{"jsonrpc":"2.0","id":2,"method":"board"}`)
	if r := p.read(t); !strings.Contains(string(r["result"]), `"channels"`) {
		t.Fatalf("%s", r["result"])
	}
	// demo mode makes plans, flagged demo, and refuses to apply them (settings and setup steps too): nothing is audited
	p.send(`{"jsonrpc":"2.0","id":3,"method":"plan.task.close","params":{"id":"ac-12"}}`)
	r := p.read(t)
	var plan struct {
		PlanID string `json:"plan_id"`
		Demo   bool   `json:"demo"`
	}
	if err := json.Unmarshal(r["result"], &plan); err != nil || !plan.Demo || plan.PlanID == "" {
		t.Fatalf("%s %s", r["result"], r["error"])
	}
	p.send(`{"jsonrpc":"2.0","id":4,"method":"apply","params":{"plan_id":"` + plan.PlanID + `"}}`)
	if r := p.read(t); !strings.Contains(string(r["error"]), `"code":"refused"`) {
		t.Fatalf("%s", r["error"])
	}
	// the settings and the setup steps are demo plans too, and applying them is refused
	for i, call := range []string{`"plan.config.set","params":{"key":"actor","value":"mei"}`, `"plan.setup","params":{"step":"actor"}`} {
		p.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(6+i*10) + `,"method":` + call + `}`)
		r := p.read(t)
		var pl struct {
			PlanID string `json:"plan_id"`
			Demo   bool   `json:"demo"`
		}
		if err := json.Unmarshal(r["result"], &pl); err != nil || !pl.Demo || pl.PlanID == "" {
			t.Fatalf("%s: %s %s", call, r["result"], r["error"])
		}
		p.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(7+i*10) + `,"method":"apply","params":{"plan_id":"` + pl.PlanID + `"}}`)
		if r := p.read(t); !strings.Contains(string(r["error"]), `"code":"refused"`) {
			t.Fatalf("%s", r["error"])
		}
	}
	if audit, _ := filepath.Glob(filepath.Join(p.home, "s", "abacus", "*")); len(audit) != 0 {
		t.Errorf("demo mode audited: %v", audit)
	}
	p.send(`{"jsonrpc":"2.0","id":5,"method":"shutdown"}`)
	p.read(t)
	if st := p.wait(t); st.ExitCode() != 0 {
		t.Fatalf("exit %d: %s", st.ExitCode(), p.stderr)
	}
	// nothing but the protocol went to stdout, and the log never carries a request
	if rest, _ := io.ReadAll(p.out); len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("extra stdout: %q", rest)
	}
	if strings.Contains(p.stderr.String(), "ac-12") || strings.Contains(p.stderr.String(), "plan_") {
		t.Errorf("the log carries request data: %s", p.stderr)
	}
}

func TestServeStopsWhenItsInputCloses(t *testing.T) {
	p := start(t, nil, "serve", "--stdio", "--demo")
	p.send(initReq)
	p.read(t)
	p.in.Close()
	if st := p.wait(t); st.ExitCode() != 0 {
		t.Fatalf("exit %d: %s", st.ExitCode(), p.stderr)
	}
}

func TestServeStopsOnSIGTERM(t *testing.T) {
	p := start(t, nil, "serve", "--stdio", "--demo")
	p.send(initReq)
	p.read(t)
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if st := p.wait(t); st.ExitCode() != 0 {
		t.Fatalf("exit %d: %s", st.ExitCode(), p.stderr)
	}
}

func TestServeStopsOnSIGHUP(t *testing.T) {
	p := start(t, nil, "serve", "--stdio", "--demo")
	p.send(initReq)
	p.read(t)
	_ = p.cmd.Process.Signal(syscall.SIGHUP)
	if st := p.wait(t); st.ExitCode() != 0 {
		t.Fatalf("exit %d: %s", st.ExitCode(), p.stderr)
	}
}

// A client that closes its end of stdout is an error to write to, not a signal that kills the process.
func TestServeIgnoresSIGPIPE(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "serve", "--stdio", "--demo")
	cmd.Dir = home
	cmd.Env = []string{"ABACUS_TEST_AS_MAIN=1", "HOME=" + home, "XDG_STATE_HOME=" + home + "/s", "XDG_CONFIG_HOME=" + home + "/c", "XDG_DATA_HOME=" + home + "/d", "PATH=/usr/bin:/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, _ := cmd.StdinPipe()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = outW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	outW.Close()
	outR.Close() // the client's read end is gone before the first response
	io.WriteString(in, initReq+"\n")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the server did not notice the closed pipe")
	}
	st := cmd.ProcessState
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		t.Fatalf("killed by %v: SIGPIPE must be ignored", ws.Signal())
	}
	if st.ExitCode() != 1 || !strings.Contains(stderr.String(), "stopped reading") {
		t.Errorf("exit %d: %s", st.ExitCode(), &stderr)
	}
}

func TestServeRefusesWhatIsNotAPipe(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	file := filepath.Join(t.TempDir(), "in.jsonl")
	if err := os.WriteFile(file, []byte(initReq+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, _ := os.Open(file)
	defer regular.Close()
	for name, in := range map[string]*os.File{"/dev/null": devnull, "a regular file": regular} {
		p := start(t, in, "serve", "--stdio", "--demo")
		st := p.wait(t)
		out, _ := io.ReadAll(p.out)
		if st.ExitCode() != 2 || len(out) != 0 || !strings.Contains(p.stderr.String(), "pipe or a socket") {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, st.ExitCode(), out, p.stderr)
		}
	}
}

func TestServeNeedsStdioAndTakesNoWords(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"serve", "--demo"}, {"serve", "--stdio", "extra"}, {"serve", "--port", "8080"}, {"serve", "--stdio", "--listen", ":1"}} {
		p := start(t, nil, args...)
		st := p.wait(t)
		if st.ExitCode() != 2 {
			t.Errorf("%v: exit %d", args, st.ExitCode())
		}
	}
}

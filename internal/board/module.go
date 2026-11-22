// Package board is the Board provider. It runs the Vercel Board CLI (github.com/vercel/board,
// npm package @vercel/board, bins `board` and `vb`) as a subprocess only, behind an argv
// allowlist, with a scrubbed environment. It never starts `serve`, `tail` or `wait`: every
// read is bounded by --limit and a deadline.
//
// `vb` on npm is an unrelated package, so a binary is trusted only after its own --help says
// it is Vercel Board.
package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Bounds of one Board read: a few channels, a few threads each, one page of open tasks.
const (
	maxChannels       = 50
	readChannels      = 3
	threadsPerChannel = 5
	maxTasks          = 20
)

// Options configure the module; every process boundary is injectable.
type Options struct {
	Bin      string // ABACUS_BOARD_BIN
	URL      string // board_url / ABACUS_BOARD_URL, passed as --url; empty means the CLI's own default
	URLErr   string // why the configured URL is unusable: every read and write then refuses
	LookPath func(string) (string, error)
	Runner   core.SplitRunner
	Getenv   func(string) string // for redacting secrets from text
	Timeout  time.Duration
}

type Module struct {
	core.Locator
	o Options

	mu      sync.Mutex
	lastErr string
}

func New(o Options) *Module {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Scrub: core.ScrubBoardEnv}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Timeout <= 0 {
		o.Timeout = core.DefaultExecTimeout
	}
	return &Module{o: o}
}

func (m *Module) ID() string   { return "board" }
func (m *Module) Name() string { return "Vercel Board" }

// clean is the one door for text from the CLI or the board: secrets redacted, then sanitized.
func (m *Module) clean(s string) string {
	for _, k := range core.BoardSecretEnv {
		if v := m.o.Getenv(k); len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return core.Sanitize(s)
}

var identityRE = regexp.MustCompile(`^board (\d+\.\d+\.\d+[0-9A-Za-z.+-]*): your Vercel team's message board`)

// identify runs `--help` on a candidate and says whether it is Vercel Board, and its version.
func (m *Module) identify(ctx context.Context, bin string) (version, why string) {
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, _, exit, err := m.o.Runner.RunSplit(ctx, bin, "--help")
	if err != nil || exit != 0 {
		return "", "did not answer --help"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	sm := identityRE.FindStringSubmatch(strings.TrimSpace(first))
	if sm == nil || !strings.Contains(string(out), "usage: board <command>") {
		return "", "its --help does not identify Vercel Board"
	}
	return sm[1], ""
}

// Detect looks for `board` (preferred) then `vb`, or only ABACUS_BOARD_BIN when set, and trusts
// a binary only when identify says it is Vercel Board.
func (m *Module) Detect(ctx context.Context) core.Detection {
	names := []string{"board", "vb"}
	if m.o.Bin != "" {
		names = []string{"board"}
	}
	var rejected []string
	for _, name := range names {
		f, absent := core.LocateFound(name, m.o.Bin, m.o.LookPath)
		if absent != "" {
			continue
		}
		ver, why := m.identify(ctx, f.Bin)
		if why == "" {
			m.SetFound(f)
			return core.Detection{State: core.Ready, Version: ver}
		}
		rejected = append(rejected, fmt.Sprintf("%s at %s: %s", name, f.Bin, why))
	}
	if len(rejected) > 0 {
		return core.Detection{State: core.Incompatible, Detail: m.clean("not Vercel Board (" + strings.Join(rejected, "; ") + "). The npm package named vb is a different project: install @vercel/board")}
	}
	return core.Detection{State: core.Absent, Detail: "the Vercel Board CLI (board or vb) was not found on PATH"}
}

func (m *Module) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastErr = ""
	if err != nil {
		m.lastErr = m.clean(err.Error())
	}
}

func (m *Module) Doctor(context.Context) []core.Check {
	m.mu.Lock()
	last := m.lastErr
	m.mu.Unlock()
	if last != "" {
		return []core.Check{{Name: "board", Status: core.StatusWarn, Detail: "the last board read failed: " + last, Fix: "check the board URL (abacus config get board_url) or sign in, then press r"}}
	}
	return []core.Check{{Name: "installation", Status: core.StatusOK, Detail: "found at " + m.clean(m.Bin())}}
}

func (m *Module) Status(ctx context.Context) core.Status {
	r, err := m.Board(ctx)
	var nc *core.NotConnectedError
	switch {
	case errors.As(err, &nc):
		return core.Status{Unavailable: nc.Reason, HowToConnect: nc.HowToConnect}
	case err != nil:
		return core.Status{Unavailable: m.clean(err.Error())}
	}
	return core.Status{Summary: r.Summarize(), Source: core.SourceLive}
}

// ---- argv allowlist ----

// allowedRead is the whole set of read argvs, matched element by element (after the optional
// trailing `--url U`):
//
//	--help | --version
//	channels list --json --limit N
//	read <channel> --json --limit N
//	read <id> --json
//	tasks --json --limit N
//
// Nothing else is run for a read: no serve, tail, wait, inspect, whoami or any write verb.
func allowedRead(a []string) bool {
	a = stripURL(a)
	n := func(s string) bool {
		v, err := strconv.Atoi(s)
		return err == nil && v >= 1 && v <= 100 && strconv.Itoa(v) == s
	}
	switch {
	case len(a) == 1:
		return a[0] == "--help" || a[0] == "--version"
	case len(a) == 5 && a[0] == "channels" && a[1] == "list" && a[2] == "--json" && a[3] == "--limit":
		return n(a[4])
	case len(a) == 5 && a[0] == "read" && a[2] == "--json" && a[3] == "--limit":
		return core.BoardChannelRE.MatchString(a[1]) && n(a[4])
	case len(a) == 3 && a[0] == "read" && a[2] == "--json":
		return core.BoardIDRE.MatchString(a[1])
	case len(a) == 4 && a[0] == "tasks" && a[1] == "--json" && a[2] == "--limit":
		return n(a[3])
	}
	return false
}

// stripURL drops a trailing `--url U` (the only global option abacus adds).
func stripURL(a []string) []string {
	if len(a) >= 2 && a[len(a)-2] == "--url" && core.ValidateBoardURL(a[len(a)-1]) == nil {
		return a[:len(a)-2]
	}
	return a
}

// allowedWrite is the whole set of write argvs: post and reply, each with --json, an optional
// --url, an idempotency key, and the text after `--` so it can never be read as an option.
//
//	post <channel> --json [--url U] --idempotency-key K -- <text>
//	reply <id> --json [--url U] --idempotency-key K -- <text>
func allowedWrite(a []string) bool {
	if len(a) < 7 || a[2] != "--json" {
		return false
	}
	rest := a[3:]
	if rest[0] == "--url" {
		if core.ValidateBoardURL(rest[1]) != nil {
			return false
		}
		rest = rest[2:]
	}
	if len(rest) != 4 || rest[0] != "--idempotency-key" || !keyRE.MatchString(rest[1]) || rest[2] != "--" || rest[3] == "" {
		return false
	}
	switch a[0] {
	case "post":
		return core.BoardChannelRE.MatchString(a[1])
	case "reply":
		return core.BoardIDRE.MatchString(a[1])
	}
	return false
}

var keyRE = regexp.MustCompile(`^abacus-[0-9a-f]{16}$`)

func (m *Module) argv(a ...string) []string {
	if m.o.URL != "" {
		a = append(a, "--url", m.o.URL)
	}
	return a
}

// ---- running ----

// failure is the CLI's own error document (stderr, with --json): reason is what a program
// branches on, next says what to run.
type failure struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// run runs one allowlisted argv and decodes stdout into into (nil: ignore it). A non-zero exit
// becomes an error classified from the failure document.
func (m *Module) run(ctx context.Context, argv []string, into any, ok func([]string) bool) error {
	if !ok(argv) {
		return fmt.Errorf("refused: %v is not an allowed argv", argv)
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, errOut, exit, err := m.o.Runner.RunSplit(ctx, m.Bin(), argv...)
	if err != nil {
		if errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled) {
			return err
		}
		return fmt.Errorf("could not run: %s", m.clean(err.Error()))
	}
	if exit != 0 {
		return m.classify(exit, errOut)
	}
	if into != nil && json.Unmarshal(out, into) != nil {
		return errors.New("unreadable output")
	}
	return nil
}

// classify turns a failed run into the right state: no board / not signed in is "not
// connected" with the exact next step; anything else is a plain error (degraded).
func (m *Module) classify(exit int, stderr []byte) error {
	var f failure
	if json.Unmarshal(stderr, &f) != nil || f.Reason == "" {
		return fmt.Errorf("exited %d", exit)
	}
	switch f.Reason {
	case "missing_scope":
		return &core.NotConnectedError{Reason: core.BoardNotSetUpReason, HowToConnect: core.BoardNotSetUp}
	case "login_required":
		return &core.NotConnectedError{Reason: core.BoardNotSignedInReason, HowToConnect: core.BoardNotSignedIn}
	case "network_error", "timeout":
		return errors.New("could not reach the board; check board_url and the network, then press r")
	}
	msg := f.Message
	if msg == "" {
		msg = f.Reason
	}
	if f.Hint != "" {
		msg += " " + f.Hint
	}
	return errors.New(m.clean(msg))
}

// ---- decoding (the real shape of @vercel/board 0.54.0, see testdata/) ----

type authorJSON struct {
	Kind   string `json:"kind"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

type messageJSON struct {
	ID        string      `json:"id"`
	Channel   string      `json:"channel"`
	Text      string      `json:"text"`
	CreatedAt string      `json:"createdAt"`
	Deleted   bool        `json:"deleted"`
	Author    *authorJSON `json:"author"`
	Task      *struct {
		State string `json:"state"`
		Claim *struct {
			Actor string `json:"actor"`
			Until string `json:"until"`
		} `json:"claim"`
	} `json:"task"`
}

type threadJSON struct {
	Message messageJSON  `json:"message"`
	Replies int          `json:"replies"`
	Last    *messageJSON `json:"last"`
}

type channelsDoc struct {
	Channels *[]struct {
		Path      string `json:"path"`
		LastIndex int    `json:"lastIndex"`
		Messages  int    `json:"messages"`
		Threads   int    `json:"threads"`
	} `json:"channels"`
}

type threadsDoc struct {
	Threads *[]threadJSON `json:"threads"`
}

type threadDoc struct {
	Message *messageJSON  `json:"message"`
	Replies []messageJSON `json:"replies"`
}

func (m *Module) message(j messageJSON) core.BoardMessage {
	text := j.Text
	if j.Deleted {
		text = "(deleted)"
	}
	b := core.BoardMessage{ID: m.clean(j.ID), Channel: m.clean(j.Channel), At: m.clean(j.CreatedAt), Text: m.clean(text)}
	if a := j.Author; a != nil {
		b.Author = m.clean(a.Handle)
		if b.Author == "" {
			b.Author = m.clean(a.Name)
		}
	}
	return b
}

// claimant shows a claim's actor as the board's own docs do: "claude via julian".
func (m *Module) claimant(actor string) string {
	if i := strings.LastIndex(actor, ":"); i >= 0 {
		actor = actor[i+1:]
	}
	if who, agent, ok := strings.Cut(actor, "+"); ok {
		return m.clean(agent + " via " + who)
	}
	return m.clean(actor)
}

// Board reads channels, a few recent threads and open tasks. No channels read and no tasks
// read is an error; a part that fails alone is named in Reason (degraded).
func (m *Module) Board(ctx context.Context) (core.BoardReport, error) {
	if m.o.URLErr != "" {
		return core.BoardReport{}, &core.NotConnectedError{Reason: "board_url is not usable: " + m.clean(m.o.URLErr), HowToConnect: "Fix it with: abacus config set board_url <url> (or clear it with an empty value)"}
	}
	r := core.BoardReport{Header: core.Header{Source: core.SourceLive}}
	var cd channelsDoc
	if err := m.run(ctx, m.argv("channels", "list", "--json", "--limit", strconv.Itoa(maxChannels)), &cd, allowedRead); err != nil {
		m.setErr(err)
		return core.BoardReport{}, err
	}
	if cd.Channels == nil {
		err := errors.New("unreadable output")
		m.setErr(err)
		return core.BoardReport{}, err
	}
	chans := *cd.Channels
	// Newest activity first where the board says (a team board lists names only: keep its order).
	sort.SliceStable(chans, func(i, j int) bool { return chans[i].LastIndex > chans[j].LastIndex })
	for _, c := range chans {
		r.Channels = append(r.Channels, core.BoardChannel{Path: m.clean(c.Path), Threads: c.Threads, Messages: c.Messages})
	}
	var why []string
	for i, c := range chans {
		if i == readChannels {
			break
		}
		var td threadsDoc
		if err := m.run(ctx, m.argv("read", c.Path, "--json", "--limit", strconv.Itoa(threadsPerChannel)), &td, allowedRead); err != nil || td.Threads == nil {
			why = append(why, "channel "+m.clean(c.Path)+" could not be read")
			continue
		}
		for _, t := range *td.Threads {
			bt := core.BoardThread{Root: m.message(t.Message), Replies: t.Replies}
			if t.Last != nil {
				l := m.message(*t.Last)
				bt.Last = &l
			}
			r.Threads = append(r.Threads, bt)
		}
	}
	sort.SliceStable(r.Threads, func(i, j int) bool { return activity(r.Threads[i]) > activity(r.Threads[j]) })
	var tk threadsDocTasks
	if err := m.run(ctx, m.argv("tasks", "--json", "--limit", strconv.Itoa(maxTasks)), &tk, allowedRead); err != nil || tk.Tasks == nil {
		why = append(why, "tasks could not be read")
	} else {
		for _, t := range *tk.Tasks {
			bt := core.BoardTask{ID: m.clean(t.Message.ID), Channel: m.clean(t.Message.Channel), Text: m.clean(t.Message.Text), State: "unknown"}
			if x := t.Message.Task; x != nil {
				bt.State = m.clean(x.State)
				if x.Claim != nil {
					bt.Claimant, bt.Until = m.claimant(x.Claim.Actor), m.clean(x.Claim.Until)
				}
			}
			r.Tasks = append(r.Tasks, bt)
		}
	}
	m.setErr(nil)
	if len(why) > 0 {
		r.Reason = strings.Join(why, "; ")
		m.setErr(errors.New(r.Reason))
	}
	return r, nil
}

type threadsDocTasks struct {
	Tasks *[]threadJSON `json:"tasks"`
}

func activity(t core.BoardThread) string {
	if t.Last != nil && t.Last.At != "" {
		return t.Last.At
	}
	return t.Root.At
}

// Thread re-reads one thread by message id (the root and every reply), bounded by the deadline.
func (m *Module) readDoc(ctx context.Context, id string) (threadDoc, error) {
	var d threadDoc
	if err := m.run(ctx, m.argv("read", id, "--json"), &d, allowedRead); err != nil {
		return d, err
	}
	if d.Message == nil {
		return d, errors.New("unreadable output")
	}
	return d, nil
}

func (m *Module) Thread(ctx context.Context, id string) (core.BoardThread, []core.BoardMessage, error) {
	d, err := m.readDoc(ctx, id)
	if err != nil {
		return core.BoardThread{}, nil, err
	}
	t := core.BoardThread{Root: m.message(*d.Message), Replies: len(d.Replies)}
	var all []core.BoardMessage
	for _, x := range d.Replies {
		all = append(all, m.message(x))
	}
	if n := len(all); n > 0 {
		t.Last = &all[n-1]
	}
	return t, all, nil
}

// ---- writes (post, reply) ----

// BuildBoardArgv is the write allowlist: post and reply only, text after `--`.
func (m *Module) BuildBoardArgv(ch core.BoardChange, key string) ([]string, error) {
	if m.o.URLErr != "" {
		return nil, errors.New("board_url is not usable: " + m.clean(m.o.URLErr))
	}
	var a []string
	switch ch.Op {
	case core.BoardPost:
		a = []string{"post", ch.Channel, "--json"}
	case core.BoardReply:
		a = []string{"reply", ch.ID, "--json"}
	default:
		return nil, fmt.Errorf("unsupported operation %q", ch.Op)
	}
	if m.o.URL != "" {
		a = append(a, "--url", m.o.URL)
	}
	a = append(a, "--idempotency-key", key, "--", ch.Text)
	if !allowedWrite(a) {
		return nil, fmt.Errorf("refused: %q is not an allowed board write", ch.Op)
	}
	return a, nil
}

// Target says where a write goes, for the audit line: the host of board_url, never a credential.
func (m *Module) Target() string {
	if m.o.URL == "" {
		return "the default board"
	}
	return m.o.URL
}

type writeDoc struct {
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	Outcome string `json:"outcome"`
}

// RunBoard runs a planned write, then re-reads the new message by id: "applied" means the board
// has it with the text that was planned. A timeout or cancel means the outcome is unknown.
func (m *Module) RunBoard(ctx context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	if !allowedWrite(argv) {
		return core.BoardResult{}, fmt.Errorf("refused: %v is not an allowed argv", argv)
	}
	var w writeDoc
	if err := m.run(ctx, argv, &w, allowedWrite); err != nil {
		if errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled) {
			return core.BoardResult{}, core.UnknownOutcome("the board did not answer in time; it may have been posted. Refresh the Board before trying again")
		}
		return core.BoardResult{}, err
	}
	if w.Message == nil || !core.BoardIDRE.MatchString(w.Message.ID) {
		return core.BoardResult{}, core.UnknownOutcome("the board answered without a message id; refresh the Board to check")
	}
	id := w.Message.ID
	d, err := m.readDoc(ctx, id)
	if err != nil {
		return core.BoardResult{}, core.UnknownOutcome(fmt.Sprintf("posted as %s but it could not be re-read to confirm (%s)", m.clean(id), m.clean(err.Error())))
	}
	got := d.Message
	for i := range d.Replies {
		if d.Replies[i].ID == id {
			got = &d.Replies[i]
		}
	}
	if got.ID != id || got.Text != ch.Text || (ch.Op == core.BoardPost && got.Channel != ch.Channel) {
		return core.BoardResult{}, core.UnknownOutcome(fmt.Sprintf("the board has message %s but it does not match what was planned; refresh the Board to check", m.clean(id)))
	}
	return core.BoardResult{ID: m.clean(id), Channel: ch.Channel, Text: ch.Text}, nil
}

// InspectCmd is the handoff to the CLI's local web inspector (`inspect` serves the board's web
// UI on 127.0.0.1 and opens a browser until Ctrl-C). It is never run by the module itself: the
// TUI runs it only from an explicit keypress, with the same scrubbed environment as every read.
func (m *Module) InspectCmd() *exec.Cmd {
	a := []string{"inspect"}
	if m.o.URL != "" {
		a = append(a, "--url", m.o.URL)
	}
	c := exec.Command(m.Bin(), a...)
	c.Env = core.ScrubBoardEnv(os.Environ())
	return c
}

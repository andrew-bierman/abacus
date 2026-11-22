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
	maxStdout         = 4 << 20 // a read answer larger than this is cut off and reported
)

// minVersion is the oldest CLI whose --json shapes were verified (see testdata/README.md).
var minVersion = [3]int{0, 54, 0}

// Options configure the module; every process boundary is injectable.
type Options struct {
	Bin       string // ABACUS_BOARD_BIN
	URL       string // the validated board URL, passed as --url; empty means the CLI's own default
	URLSource string // where URL came from, for the confirm step (ABACUS_BOARD_URL, board_url (config), BOARD_URL)
	URLErr    string // why the configured URL is unusable: every read and write then refuses
	LookPath  func(string) (string, error)
	Runner    core.SplitRunner    // reads and writes
	Probe     core.SplitRunner    // the identity probe: no secrets (defaults to Runner when Runner is injected)
	Getenv    func(string) string // for redacting secrets from text
	Timeout   time.Duration
}

type Module struct {
	core.Locator
	o Options

	mu      sync.Mutex
	lastErr string
}

func New(o Options) *Module {
	if o.Runner == nil {
		o.Runner = core.ExecRunner{Scrub: core.ScrubBoardEnvFor(o.URL), MaxOut: maxStdout}
		if o.Probe == nil {
			o.Probe = core.ExecRunner{Scrub: core.ScrubBoardProbeEnv, MaxOut: maxStdout}
		}
	}
	if o.Probe == nil {
		o.Probe = o.Runner
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

// redact replaces secret values; it runs after sanitizing so an invisible character cannot
// split a secret and hide it from the match.
func (m *Module) redact(s string) string {
	for _, k := range core.BoardSecretEnv {
		if v := m.o.Getenv(k); len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// clean is the one door for one-line text from the CLI or the board.
func (m *Module) clean(s string) string { return m.redact(core.Sanitize(s)) }

// cleanText is clean for message text: line breaks are kept (renderers flatten per line).
func (m *Module) cleanText(s string) string { return m.redact(core.SanitizeText(s)) }

var identityRE = regexp.MustCompile(`^board (\d+\.\d+\.\d+[0-9A-Za-z.+-]*): your Vercel team's message board`)

// identify runs `--help` on a candidate and says whether it is Vercel Board, and its version.
func (m *Module) identify(ctx context.Context, bin string) (version, why string) {
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	out, _, exit, err := m.o.Probe.RunSplit(ctx, bin, "--help")
	if err != nil || exit != 0 {
		return "", "did not answer --help"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	sm := identityRE.FindStringSubmatch(strings.TrimSpace(first))
	if sm == nil || !strings.Contains(string(out), "usage: board <command>") {
		return "", "its --help does not identify Vercel Board"
	}
	if !versionAtLeast(sm[1], minVersion) {
		return sm[1], "too old"
	}
	return sm[1], ""
}

var semverRE = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

func versionAtLeast(v string, min [3]int) bool {
	sm := semverRE.FindStringSubmatch(v)
	if sm == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		n, _ := strconv.Atoi(sm[i+1])
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
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
		if why == "too old" {
			return core.Detection{State: core.Incompatible, Version: m.clean(ver), Detail: m.clean(fmt.Sprintf("Vercel Board %s is too old: abacus needs 0.54.0 or newer. Update it with: npm i -g @vercel/board", ver))}
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
	if m.o.URLErr != "" {
		return []core.Check{{Name: "board url", Status: core.StatusWarn, Detail: "the board URL is not used: " + m.clean(m.o.URLErr), Fix: "set a valid one with: abacus config set board_url <url>"}}
	}
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

// call runs one allowlisted argv and returns the raw answer. A runner error is returned as is.
func (m *Module) call(ctx context.Context, argv []string, ok func([]string) bool) (out, errOut []byte, exit int, err error) {
	if !ok(argv) {
		return nil, nil, 0, fmt.Errorf("refused: %v is not an allowed argv", argv)
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
	defer cancel()
	return m.o.Runner.RunSplit(ctx, m.Bin(), argv...)
}

// run runs one allowlisted read argv and decodes stdout into into (nil: ignore it). A non-zero
// exit becomes an error classified from the failure document.
func (m *Module) run(ctx context.Context, argv []string, into any, ok func([]string) bool) error {
	out, errOut, exit, err := m.call(ctx, argv, ok)
	switch {
	case errors.Is(err, core.ErrOutputTooLarge):
		return errors.New("the answer was too large and was cut off")
	case err != nil:
		if errors.Is(err, core.ErrTimedOut) || errors.Is(err, core.ErrCanceled) || strings.HasPrefix(err.Error(), "refused:") {
			return err
		}
		return fmt.Errorf("could not run: %s", m.clean(err.Error()))
	case exit != 0:
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
	Next *string `json:"next"`
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
	b := core.BoardMessage{ID: m.clean(j.ID), Channel: m.clean(j.Channel), At: m.clean(j.CreatedAt), Text: m.cleanText(text)}
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

// Board reads channels, a few recent threads and open tasks. A channel list that cannot be read
// is an error; a part that fails alone is named in Reason (degraded). Truncation (more than the
// bounds shown) is named in Truncated and is not a failure.
func (m *Module) Board(ctx context.Context) (core.BoardReport, error) {
	if m.o.URLErr != "" {
		return core.BoardReport{}, &core.NotConnectedError{Reason: "board_url is not usable: " + m.clean(m.o.URLErr), HowToConnect: "Fix it with: abacus config set board_url <url> (or clear it with an empty value)"}
	}
	r := core.BoardReport{Header: core.Header{Source: core.SourceLive}}
	var cd channelsDoc
	err := m.run(ctx, m.argv("channels", "list", "--json", "--limit", strconv.Itoa(maxChannels)), &cd, allowedRead)
	if err == nil {
		err = validChannels(cd)
	}
	if err != nil {
		m.setErr(err)
		return core.BoardReport{}, err
	}
	chans := *cd.Channels
	// Newest activity first where the board says (a team board lists names only: keep its order).
	sort.SliceStable(chans, func(i, j int) bool { return chans[i].LastIndex > chans[j].LastIndex })
	var trunc []string
	if cd.Next != nil {
		trunc = append(trunc, fmt.Sprintf("only the first %d channels are listed", maxChannels))
	}
	if len(chans) > readChannels {
		trunc = append(trunc, fmt.Sprintf("threads are shown for the %d most recently active channels", readChannels))
	}
	for _, c := range chans {
		r.Channels = append(r.Channels, core.BoardChannel{Path: m.clean(c.Path), Threads: c.Threads, Messages: c.Messages})
	}
	var why []string
	for i, c := range chans {
		if i == readChannels {
			break
		}
		var td threadsDoc
		err := m.run(ctx, m.argv("read", c.Path, "--json", "--limit", strconv.Itoa(threadsPerChannel)), &td, allowedRead)
		switch {
		case err != nil && strings.Contains(err.Error(), "too large"):
			why = append(why, "channel "+m.clean(c.Path)+": the answer was too large and was cut off")
			continue
		case err != nil || td.Threads == nil || !threadsValid(*td.Threads):
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
	if err := m.run(ctx, m.argv("tasks", "--json", "--limit", strconv.Itoa(maxTasks)), &tk, allowedRead); err != nil || tk.Tasks == nil || !threadsValid(*tk.Tasks) {
		why = append(why, "tasks could not be read")
	} else {
		for _, t := range *tk.Tasks {
			bt := core.BoardTask{ID: m.clean(t.Message.ID), Channel: m.clean(t.Message.Channel), Text: m.cleanText(t.Message.Text), State: "unknown"}
			if x := t.Message.Task; x != nil {
				bt.State = m.clean(x.State)
				if x.Claim != nil {
					bt.Claimant, bt.Until = m.claimant(x.Claim.Actor), m.clean(x.Claim.Until)
				}
			}
			r.Tasks = append(r.Tasks, bt)
		}
		if len(*tk.Tasks) >= maxTasks {
			trunc = append(trunc, fmt.Sprintf("only the first %d tasks are shown", maxTasks))
		}
	}
	m.setErr(nil)
	r.Truncated = strings.Join(trunc, "; ")
	if len(why) > 0 {
		r.Reason = strings.Join(why, "; ")
		m.setErr(errors.New(r.Reason))
	}
	return r, nil
}

// validChannels is the shape check: a channel list with a blank path is not a board.
func validChannels(d channelsDoc) error {
	if d.Channels == nil {
		return errors.New("unreadable output")
	}
	for _, c := range *d.Channels {
		if strings.TrimSpace(c.Path) == "" {
			return errors.New("unreadable output")
		}
	}
	return nil
}

// threadsValid rejects a page where any root has no id: a blank row is never shown.
func threadsValid(ts []threadJSON) bool {
	for _, t := range ts {
		if strings.TrimSpace(t.Message.ID) == "" {
			return false
		}
	}
	return true
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

// readDoc reads one thread by message id (the root and every reply), bounded by the deadline.
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

// Target is where a write goes: scheme://host/path only, never a credential, query or fragment.
func (m *Module) Target() string { return core.BoardTarget(m.o.URL) }

// TargetSource says where the target came from, for the confirm step.
func (m *Module) TargetSource() string {
	if m.o.URL == "" {
		return "no board_url is set: the CLI's own default"
	}
	if m.o.URLSource == "" {
		return "board_url"
	}
	return m.o.URLSource
}

// Describe is the read-only, bounded context for the confirm step. A failed read is a line of
// its own and never blocks the write.
func (m *Module) Describe(ctx context.Context, ch core.BoardChange) []string {
	const still = " You can still confirm."
	switch ch.Op {
	case core.BoardReply:
		d, err := m.readDoc(ctx, ch.ID)
		if err != nil {
			return []string{"could not read message " + ch.ID + " to show what you are replying to (" + m.clean(err.Error()) + ")." + still}
		}
		got := d.Message
		for i := range d.Replies {
			if d.Replies[i].ID == ch.ID {
				got = &d.Replies[i]
			}
		}
		msg := m.message(*got)
		first, _, _ := strings.Cut(msg.Text, "\n")
		return []string{fmt.Sprintf("replying to message %s in channel %s, by %s: %q", msg.ID, orUnknown(msg.Channel), orUnknown(msg.Author), first)}
	case core.BoardPost:
		var cd channelsDoc
		if err := m.run(ctx, m.argv("channels", "list", "--json", "--limit", strconv.Itoa(maxChannels)), &cd, allowedRead); err != nil || validChannels(cd) != nil {
			return []string{"could not check whether channel " + ch.Channel + " exists." + still}
		}
		for _, c := range *cd.Channels {
			if c.Path == ch.Channel {
				return []string{"existing channel " + ch.Channel}
			}
		}
		if cd.Next != nil {
			return []string{"channel " + ch.Channel + " is not among the first " + strconv.Itoa(maxChannels) + " channels: it may be new."}
		}
		return []string{"NEW channel " + ch.Channel + ": this post creates it"}
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// ambiguous is the unknown outcome of a write that may or may not have landed.
func ambiguous(why string) error {
	return core.UnknownOutcome(why + "; it may have been posted. Refresh the Board before trying again")
}

type writeDoc struct {
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	Outcome string `json:"outcome"`
}

// RunBoard runs a planned write, then re-reads the new message by id: "applied" means the board
// has it with the text that was planned. Anything that leaves it unclear whether the write landed
// (a timeout, a cancel, a runner error, a network error, undecodable output, an exit with no
// failure document) is an unknown outcome. Only a definite refusal is "not applied".
func (m *Module) RunBoard(ctx context.Context, argv []string, ch core.BoardChange) (core.BoardResult, error) {
	out, errOut, exit, err := m.call(ctx, argv, allowedWrite)
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "refused:"):
		return core.BoardResult{}, err
	case err != nil:
		return core.BoardResult{}, ambiguous("the board did not answer (" + m.clean(err.Error()) + ")")
	case exit != 0:
		var f failure
		if json.Unmarshal(errOut, &f) != nil || f.Reason == "" || f.Reason == "network_error" || f.Reason == "timeout" {
			return core.BoardResult{}, ambiguous(fmt.Sprintf("the CLI failed without a clear answer (exit %d)", exit))
		}
		return core.BoardResult{}, m.classify(exit, errOut)
	}
	var w writeDoc
	if json.Unmarshal(out, &w) != nil || w.Message == nil || !core.BoardIDRE.MatchString(w.Message.ID) {
		return core.BoardResult{}, ambiguous("the board's answer could not be read")
	}
	id := w.Message.ID
	d, err := m.readDoc(ctx, id)
	if err != nil {
		return core.BoardResult{}, core.UnknownOutcome(fmt.Sprintf("posted as %s but it could not be re-read to confirm (%s); refresh the Board to check", m.clean(id), m.clean(err.Error())))
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
	c.Env = core.ScrubBoardEnvFor(m.o.URL)(os.Environ())
	return c
}

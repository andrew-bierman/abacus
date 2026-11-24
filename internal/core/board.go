package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// HowToConnectBoard is shown when the Board tab has no board to read yet: the Board CLI is built
// into abacus, so the only thing missing is which board.
const HowToConnectBoard = BoardNotSetUp

// The two "built in but not usable yet" states of the Board CLI: the reason, and the exact next step.
const (
	BoardNotSetUpReason = "No board is selected yet."
	BoardNotSetUp       = "Point abacus at a board with: abacus config set board_url <url> (or set BOARD_URL). A team board needs `vercel login` first."

	BoardNotSignedInReason = "Not signed in to the board."
	BoardNotSignedIn       = "Run `vercel login`, or set BOARD_TOKEN, then press r."
)

// boardEnv is everything the Vercel Board CLI may inherit: where it runs (HOME, PATH, XDG
// dirs: it keeps a local board's token and claims under ~/.config/board), its own documented
// settings, proxies and a CA bundle. Nothing else (no BEADS_*, no cloud keys, no NODE_OPTIONS).
// BOARD_URL is never passed: abacus validates the board URL itself and passes it as --url.
var boardEnv = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true, "TERM": true, "NO_COLOR": true,
	"XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	"BOARD_NAME": true, "BOARD_TOKEN": true,
	"VERCEL_AUTH_TOKEN": true, "VERCEL_ORG_ID": true, "VERCEL_AUTOMATION_BYPASS_SECRET": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true,
	"NODE_EXTRA_CA_CERTS": true, "SSL_CERT_FILE": true,
}

// boardSecrets are the variables that carry a credential or pick a team.
var boardSecrets = map[string]bool{"BOARD_TOKEN": true, "VERCEL_AUTH_TOKEN": true, "VERCEL_AUTOMATION_BYPASS_SECRET": true, "VERCEL_ORG_ID": true}

// BoardSecretEnv are the variables whose values must never appear in any text abacus prints.
var BoardSecretEnv = []string{"BOARD_TOKEN", "VERCEL_AUTH_TOKEN", "VERCEL_AUTOMATION_BYPASS_SECRET"}

// ScrubBoardEnvFor is the environment filter for a run against the board at url ("" is the
// CLI's own default): an allowlist, unlike the Beads scrub. BOARD_URL never passes; BOARD_NAME
// passes only without a url (the CLI treats it as misuse beside --url); credentials pass only
// when the target is https or loopback, so a token is never sent over plain http to a remote host.
func ScrubBoardEnvFor(url string) func([]string) []string {
	trusted := url == "" || boardTransportOK(url)
	return func(env []string) []string {
		out := make([]string, 0, len(boardEnv))
		for _, e := range env {
			name, _, _ := strings.Cut(e, "=")
			switch {
			case !boardEnv[name] && !strings.HasPrefix(name, "LC_"):
			case name == "BOARD_NAME" && url != "":
			case boardSecrets[name] && !trusted:
			default:
				out = append(out, e)
			}
		}
		return out
	}
}

// boardProbe is all the identity probe may see: where it runs, and nothing about any board.
var boardProbe = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true, "TERM": true, "NO_COLOR": true,
	"XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
}

// ScrubBoardProbeEnv is the environment of the `--help` identity probe: it runs a binary that is
// not yet trusted, so it gets no token, no team and no board setting.
func ScrubBoardProbeEnv(env []string) []string {
	out := make([]string, 0, len(boardProbe))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if boardProbe[name] || strings.HasPrefix(name, "LC_") {
			out = append(out, e)
		}
	}
	return out
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// boardTransportOK is https anywhere, or plain http to a loopback host only.
func boardTransportOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && loopbackHost(u.Hostname()))
}

// ValidateBoardURL accepts an https URL, or an http one for a loopback host, with a host and no
// embedded credentials, query, fragment or control characters: it goes to the CLI as --url, and a
// token in a URL (or sent over plain http to a remote host) would leak.
func ValidateBoardURL(raw string) error {
	if Sanitize(raw) != raw || strings.ContainsAny(raw, " \\") {
		return errors.New("board_url must be a plain URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("board_url must be an https URL, or http for a local board, for example http://127.0.0.1:4100/api/board")
	}
	if u.User != nil {
		return errors.New("board_url must not contain a username or password")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("board_url must not contain a query or fragment")
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return errors.New("board_url must use https unless the board is on this machine (localhost, 127.0.0.1 or ::1)")
	}
	return nil
}

// BoardTarget is where a write goes, for people and the audit log: scheme://host/path only,
// never a credential, query or fragment ("the CLI's default board" when no URL is set).
func BoardTarget(raw string) string {
	if raw == "" {
		return "the CLI's default board"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "an unreadable board URL"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// ---- Board writes: post and reply ----

// BoardOp is a Board write verb. Only these two exist: no edit, delete, react or task verbs.
type BoardOp string

const (
	BoardPost  BoardOp = "post"
	BoardReply BoardOp = "reply"
)

// BoardChange is what a user asked for: a new thread in a channel, or a reply to a message.
type BoardChange struct {
	Op      BoardOp
	Channel string // post only
	ID      string // reply only: a message in the thread
	Text    string
	// Nonce names one draft (the TUI makes a random one when a draft is opened and keeps it while it is
	// edited). With it the idempotency key depends on the draft, not on the clock: the same draft
	// retried after an unknown outcome reuses the key at any time, and a change of text, channel or
	// target is a new key. Without it (the command line) the key is bucketed by the clock.
	Nonce string
}

// BoardMaxText bounds a message, in runes: what a post may hold, and the most of a message the
// Board shows (a longer one read from the board is cut and marked BoardMessage.Cut).
const BoardMaxText = 4000

// BoardCursorRE is the paging cursor a channel list ends a page with (a channel path on the
// local board, an opaque token elsewhere). It never starts with "-", so it cannot be read as an option.
var BoardCursorRE = boardPath{regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/=+-]*$`), 160}

// boardPath is a pattern for a name the Board CLI reads as a path (a channel, or a cursor that is
// one): beyond the character set it never has an empty segment ("//", a leading or trailing "/") or
// a ".." in it. Checked against the local board (0.54.0): "a//b", "a/../b", "/a", "a/", "./a",
// "a..b" and "a/-b" are all refused as "invalid channel path".
type boardPath struct {
	re  *regexp.Regexp
	max int // most bytes
}

func (p boardPath) MatchString(s string) bool {
	return len(s) <= p.max && p.re.MatchString(s) && !strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.HasSuffix(s, "/")
}

var (
	// BoardChannelRE is a channel path: segments of letters, digits, "." "_" "-" that start and end
	// with a letter or digit (a segment may end in "_" or "-"), joined by single "/". The local board
	// itself takes only lower-case letters, digits, "_" and "-"; a name it refuses is "not applied".
	BoardChannelRE = boardPath{regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?(?:/[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?)*$`), 128}
	// BoardIDRE is a message id as the board prints it: a number, or msg_ from Vercel's API.
	BoardIDRE = regexp.MustCompile(`^(?:[0-9]{1,18}|msg_[A-Za-z0-9_.-]{1,120})$`)
)

// BoardResult is the message a write created, as re-read from the board.
type BoardResult struct {
	ID      string `json:"id"`
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text,omitempty"`
}

// BoardWriter is what the Board provider offers for writes. BuildBoardArgv refuses anything
// outside the provider's write allowlist; RunBoard re-checks it, runs it, and re-reads the new
// message by id (so "applied" means it is on the board).
type BoardWriter interface {
	BuildBoardArgv(BoardChange, string) ([]string, error)
	RunBoard(ctx context.Context, argv []string, ch BoardChange) (BoardResult, error)
	// Target is where the write goes: scheme://host/path only, never a credential.
	Target() string
	// TargetSource says where the target came from (ABACUS_BOARD_URL, board_url, BOARD_URL, default).
	TargetSource() string
	// Describe is read-only, bounded context for the confirm step (what a reply answers, whether
	// a channel is new). Best effort: a failed read is itself a line, and never blocks the write.
	Describe(ctx context.Context, ch BoardChange) []string
}

// BoardPlan is a validated change with its exact argv. It cannot be applied until Confirm.
type BoardPlan struct {
	id        uint64
	Change    BoardChange
	Argv      []string
	Key       string // the idempotency key in Argv: a retry of the same change gets the same one
	Lines     []string
	confirmed bool
}

// Confirm returns the plan marked as confirmed by the user; Apply refuses a plan that was not.
func (p BoardPlan) Confirm() BoardPlan { p.confirmed = true; return p }

func validateBoard(ch BoardChange) error {
	switch ch.Op {
	case BoardPost:
		if !BoardChannelRE.MatchString(ch.Channel) {
			return fmt.Errorf("invalid channel %q", ch.Channel)
		}
	case BoardReply:
		if !BoardIDRE.MatchString(ch.ID) {
			return fmt.Errorf("invalid message id %q", ch.ID)
		}
	default:
		return fmt.Errorf("unsupported operation %q", ch.Op)
	}
	if strings.TrimSpace(ch.Text) == "" {
		return errors.New("the message text is empty")
	}
	if n := len([]rune(ch.Text)); n > BoardMaxText {
		return fmt.Errorf("the message is %d characters; the limit is %d", n, BoardMaxText)
	}
	for _, r := range ch.Text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return errors.New("the message contains a control character")
		}
	}
	return nil
}

var (
	boardClockMu sync.Mutex
	boardClock   = time.Now
)

// SetBoardClockForTest swaps the clock the idempotency key buckets on and returns a restore func.
func SetBoardClockForTest(f func() time.Time) (restore func()) {
	boardClockMu.Lock()
	old := boardClock
	boardClock = f
	boardClockMu.Unlock()
	return func() { boardClockMu.Lock(); boardClock = old; boardClockMu.Unlock() }
}

// BoardKeyWindow is the clock bucket of a key made without a Nonce (the command line, which keeps no
// draft between runs). It is best effort: a retry that falls in the next bucket is a new post.
const BoardKeyWindow = 10 * time.Minute

// boardKey derives the idempotency key from (target, op, channel or id, text) and either the draft's
// Nonce or, without one, the clock bucket: nothing is persisted.
func boardKey(target string, ch BoardChange) string {
	dest := ch.Channel
	if ch.Op == BoardReply {
		dest = ch.ID
	}
	salt := ch.Nonce
	if salt == "" {
		boardClockMu.Lock()
		now := boardClock()
		boardClockMu.Unlock()
		salt = fmt.Sprint(now.Unix() / int64(BoardKeyWindow/time.Second))
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", target, ch.Op, dest, ch.Text, salt)))
	return "abacus-" + hex.EncodeToString(sum[:8])
}

// PlanBoardChange validates a change and builds its argv and description without side effects.
func PlanBoardChange(w BoardWriter, ch BoardChange) (BoardPlan, error) {
	if err := validateBoard(ch); err != nil {
		return BoardPlan{}, err
	}
	key := boardKey(w.Target(), ch)
	argv, err := w.BuildBoardArgv(ch, key)
	if err != nil {
		return BoardPlan{}, err
	}
	lines := []string{"post a new thread to channel " + ch.Channel}
	if ch.Op == BoardReply {
		lines = []string{"reply in the thread of message " + ch.ID}
	}
	why := "a retry of this same draft reuses it, so the board takes it at most once"
	if ch.Nonce == "" {
		why = "a retry of the same text a moment later usually reuses it, but not across a ten-minute clock boundary"
	}
	lines = append(lines, fmt.Sprintf("text: %q", ch.Text), "board: "+w.Target()+" ("+w.TargetSource()+")",
		fmt.Sprintf("idempotency key: %s (%s)", key, why))
	return BoardPlan{id: planSeq.Add(1), Change: ch, Argv: argv, Key: key, Lines: lines}, nil
}

// BoardApplier is the one door to a Board write: the confirm step, then the shared gate
// (single use, audit before and after), then the provider.
type BoardApplier struct {
	w BoardWriter
	g *gate
}

// NewBoardApplier returns an applier that writes through w and records every attempt in a.
func NewBoardApplier(w BoardWriter, a Audit) *BoardApplier { return &BoardApplier{w: w, g: newGate(a)} }

// Apply runs a confirmed board plan once. A timeout, cancel or unreadable answer is reported as an unknown result, never as not applied.
func (a *BoardApplier) Apply(ctx context.Context, p BoardPlan) (BoardResult, error) {
	if !p.confirmed {
		return BoardResult{}, ErrNotConfirmed
	}
	e := AuditEntry{Domain: string(DomainBoard), Verb: string(p.Change.Op), Argv: p.Argv, Tracker: a.w.Target()}
	var res BoardResult
	err := a.g.do(p.id, e, func() error {
		var err error
		res, err = a.w.RunBoard(ctx, p.Argv, p.Change)
		return err
	})
	return res, err
}

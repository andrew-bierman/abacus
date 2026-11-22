package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// HowToConnectBoard is shown when the Vercel Board CLI is missing or is not Vercel Board. It
// names the real package on purpose: `vb` on npm is an unrelated project.
const HowToConnectBoard = "The Vercel Board CLI was not found. Install it with: npm i -g @vercel/board (never `npm i vb` or `npx vb`: that is a different package). Docs: https://github.com/vercel/board"

// The two "installed but not usable yet" states of a Board CLI: the reason, and the exact next step.
const (
	BoardNotSetUpReason = "No board is selected yet."
	BoardNotSetUp       = "Point abacus at one with: abacus config set board_url <url> (a local board from `vb serve` prints its URL), or set BOARD_URL. A team board needs `vercel login` first."

	BoardNotSignedInReason = "Not signed in to the board."
	BoardNotSignedIn       = "Run `vercel login`, or set BOARD_TOKEN, then press r."
)

// boardEnv is everything the Vercel Board CLI may inherit: where it runs (HOME, PATH, XDG
// dirs: it keeps a local board's token and claims under ~/.config/board), its own documented
// settings, proxies and a CA bundle. Nothing else (no BEADS_*, no cloud keys, no NODE_OPTIONS).
var boardEnv = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "LANG": true, "TERM": true, "NO_COLOR": true,
	"XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	"BOARD_URL": true, "BOARD_NAME": true, "BOARD_TOKEN": true,
	"VERCEL_AUTH_TOKEN": true, "VERCEL_ORG_ID": true, "VERCEL_AUTOMATION_BYPASS_SECRET": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true,
	"NODE_EXTRA_CA_CERTS": true, "SSL_CERT_FILE": true,
}

// BoardSecretEnv are the variables whose values must never appear in any text abacus prints.
var BoardSecretEnv = []string{"BOARD_TOKEN", "VERCEL_AUTH_TOKEN", "VERCEL_AUTOMATION_BYPASS_SECRET"}

// ScrubBoardEnv keeps only what the Vercel Board CLI needs (an allowlist, unlike the Beads
// scrub): everything else inherited from the user's shell is dropped.
func ScrubBoardEnv(env []string) []string {
	out := make([]string, 0, len(boardEnv))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if boardEnv[name] || strings.HasPrefix(name, "LC_") {
			out = append(out, e)
		}
	}
	return out
}

// ValidateBoardURL accepts an http(s) URL with a host and no embedded credentials or control
// characters: it goes to the CLI as --url, and a token in a URL would end up in logs.
func ValidateBoardURL(raw string) error {
	if Sanitize(raw) != raw || strings.ContainsAny(raw, " \\") {
		return errors.New("board_url must be a plain URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("board_url must be an http(s) URL, for example http://127.0.0.1:4100/api/board")
	}
	if u.User != nil {
		return errors.New("board_url must not contain a username or password")
	}
	return nil
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
}

// BoardMaxText bounds a message, in runes.
const BoardMaxText = 4000

var (
	BoardChannelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
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
	Target() string // where the write goes, for the audit line (never a credential)
}

// BoardPlan is a validated change with its exact argv. It cannot be applied until Confirm.
type BoardPlan struct {
	id        uint64
	Change    BoardChange
	Argv      []string
	Lines     []string
	confirmed bool
}

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

// PlanBoardChange validates a change and builds its argv and description without side effects.
// The idempotency key is made here, once, so a retried apply of the same plan posts at most once.
func PlanBoardChange(w BoardWriter, ch BoardChange) (BoardPlan, error) {
	if err := validateBoard(ch); err != nil {
		return BoardPlan{}, err
	}
	var k [8]byte
	_, _ = rand.Read(k[:])
	argv, err := w.BuildBoardArgv(ch, "abacus-"+hex.EncodeToString(k[:]))
	if err != nil {
		return BoardPlan{}, err
	}
	lines := []string{"post a new thread to channel " + ch.Channel}
	if ch.Op == BoardReply {
		lines = []string{"reply in the thread of message " + ch.ID}
	}
	lines = append(lines, fmt.Sprintf("text: %q", ch.Text), "board: "+w.Target())
	return BoardPlan{id: planSeq.Add(1), Change: ch, Argv: argv, Lines: lines}, nil
}

// BoardApplier is the one door to a Board write: the confirm step, then the shared gate
// (single use, audit before and after), then the provider.
type BoardApplier struct {
	w BoardWriter
	g *gate
}

func NewBoardApplier(w BoardWriter, a Audit) *BoardApplier { return &BoardApplier{w: w, g: newGate(a)} }

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

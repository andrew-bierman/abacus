package agents

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// Limits on what a scan reads. Session files grow without bound, so only the tail of each (and
// the head of a Codex file, where its metadata line is) is ever read, and one scan stops at a
// total budget however many files there are.
const (
	tailBytes      = 512 << 10
	headBytes      = 128 << 10
	scanBudget     = 24 << 20 // all files of one scan together
	maxFilesParsed = 64
	maxListed      = 5000 // directory entries looked at in one scan
)

// endKind is what the last meaningful record of a session says happened last.
type endKind int

const (
	endNone       endKind = iota
	endPrompt             // a person's prompt: the model should be working on it
	endToolResult         // a tool answered: the model should be working on it
	endToolUse            // the model asked for a tool: running it, or waiting for a permission
	endDone               // the model finished its turn
	endError              // an API error ended the turn
)

// session is what a transcript's tail says. Zero Tokens means unknown (nil), never zero.
type session struct {
	ID, Cwd, Model, Task, Branch string
	Tokens                       *int64
	End                          endKind
	ModTime                      time.Time
	// Context is what the last model call read in, and Window the model's limit when the file says
	// it; Turns is one point per model response in the part of the file that was read (cache reads
	// left out); Quota is the account rate limit the file recorded last. All are nil/empty when the
	// harness does not write them.
	Context, Window *int64
	Turns           []int64
	Quota           *quotaRead
	Cost            *float64
}

// maxTurns bounds what one read keeps of the per-turn series (core keeps the newest 120 anyway).
const maxTurns = 200

// push appends one per-turn point, dropping the oldest past the bound.
func (s *session) push(v int64) {
	s.Turns = append(s.Turns, v)
	if len(s.Turns) > maxTurns {
		s.Turns = s.Turns[len(s.Turns)-maxTurns:]
	}
}

// reader is a file the scan can read the head and tail of.
type reader interface {
	io.ReaderAt
	io.Closer
}

func openAt(fsys fs.FS, name string) (reader, int64, error) {
	f, fi, err := openRegular(fsys, name)
	if err != nil {
		return nil, 0, err
	}
	ra, ok := f.(reader)
	if !ok {
		f.Close()
		return nil, 0, fs.ErrInvalid
	}
	return ra, fi.Size(), nil
}

type budget struct{ left int64 }

func (b *budget) take(n int64) bool {
	if n > b.left {
		return false
	}
	b.left -= n
	return true
}

// tailLines returns the complete lines in the last tailBytes of a file.
func tailLines(fsys fs.FS, name string, b *budget) ([][]byte, time.Time, error) {
	ra, size, err := openAt(fsys, name)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer ra.Close()
	fi, _ := fs.Stat(fsys, name)
	var mod time.Time
	if fi != nil {
		mod = fi.ModTime()
	}
	n := min(size, tailBytes)
	if !b.take(n) {
		return nil, mod, fs.ErrInvalid
	}
	buf := make([]byte, n)
	got, err := ra.ReadAt(buf, size-n)
	if err != nil && err != io.EOF {
		return nil, mod, err
	}
	buf = buf[:got]
	if size > n { // the first line is cut
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return nil, mod, nil
		}
	}
	return bytes.Split(buf, []byte("\n")), mod, nil
}

// headLine returns the first line of a file when it fits in headBytes.
func headLine(fsys fs.FS, name string, b *budget) []byte {
	ra, size, err := openAt(fsys, name)
	if err != nil {
		return nil
	}
	defer ra.Close()
	n := min(size, headBytes)
	if !b.take(n) {
		return nil
	}
	buf := make([]byte, n)
	got, _ := ra.ReadAt(buf, 0)
	buf = buf[:got]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return buf[:i]
	}
	if size <= headBytes {
		return buf
	}
	return nil
}

// ---- Claude Code ----

type claudeLine struct {
	Type        string `json:"type"`
	Cwd         string `json:"cwd"`
	SessionID   string `json:"sessionId"`
	GitBranch   string `json:"gitBranch"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input       int64 `json:"input_tokens"`
			Output      int64 `json:"output_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// blocksOf reads message content that is a string or a list of blocks.
func blocksOf(raw json.RawMessage) []block {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	_ = json.Unmarshal(raw, &bs)
	return bs
}

// systemish is a prompt a person did not type (a slash command echo, a caveat, a reminder).
func systemish(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "Caveat:") || strings.HasPrefix(s, "[Request interrupted")
}

func parseClaude(lines [][]byte) session {
	var s session
	lastMsg := "" // Claude writes one line per content block of a response, each repeating its usage
	for _, raw := range lines {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var l claudeLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if l.SessionID != "" {
			s.ID = l.SessionID
		}
		if l.Cwd != "" && !l.IsSidechain {
			s.Cwd = l.Cwd
		}
		if l.GitBranch != "" && !l.IsSidechain {
			s.Branch = l.GitBranch
		}
		if l.IsSidechain || (l.Type != "user" && l.Type != "assistant") {
			continue
		}
		bs := blocksOf(l.Message.Content)
		switch l.Type {
		case "user":
			if l.IsMeta {
				continue
			}
			tool, text := false, ""
			for _, b := range bs {
				switch b.Type {
				case "tool_result":
					tool = true
				case "text":
					text += b.Text + " "
				}
			}
			switch {
			case tool:
				s.End = endToolResult
			case !systemish(text):
				s.End, s.Task = endPrompt, strings.TrimSpace(text)
			}
		case "assistant":
			if m := l.Message.Model; m != "" && !strings.HasPrefix(m, "<") {
				s.Model = m
			}
			if u := l.Message.Usage; u != nil && u.Input+u.Output+u.CacheRead+u.CacheCreate > 0 {
				ctx := u.Input + u.CacheRead + u.CacheCreate
				s.Context = &ctx
				pt := u.Input + u.Output + u.CacheCreate
				if l.Message.ID != "" && l.Message.ID == lastMsg && len(s.Turns) > 0 {
					s.Turns[len(s.Turns)-1] = pt
				} else {
					s.push(pt)
				}
				lastMsg = l.Message.ID
			}
			switch {
			case l.IsAPIError:
				s.End = endError
			case len(bs) > 0 && bs[len(bs)-1].Type == "tool_use":
				s.End = endToolUse
			default:
				s.End = endDone
			}
		}
	}
	return s
}

// ---- Codex ----

type codexLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Cwd     string `json:"cwd"`
		Model   string `json:"model"`
		Message string `json:"message"`
		Role    string `json:"role"`
		Git     struct {
			Branch string `json:"branch"`
		} `json:"git"`
		Info struct {
			Total struct {
				Total *int64 `json:"total_tokens"`
			} `json:"total_token_usage"`
			Last struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"last_token_usage"`
			Window int64 `json:"model_context_window"`
		} `json:"info"`
		RateLimits *codexLimits `json:"rate_limits"`
	} `json:"payload"`
}

func parseCodex(head []byte, lines [][]byte) session {
	var s session
	var prevTotal *int64 // Codex repeats a token_count; only a changed total is a new response
	apply := func(raw []byte) {
		var l codexLine
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
			return
		}
		p := l.Payload
		switch l.Type {
		case "session_meta":
			s.ID, s.Cwd, s.Branch = p.ID, p.Cwd, p.Git.Branch
		case "turn_context":
			if p.Cwd != "" {
				s.Cwd = p.Cwd
			}
			if p.Model != "" {
				s.Model = p.Model
			}
		case "event_msg":
			switch p.Type {
			case "user_message":
				s.End = endPrompt
				if !systemish(p.Message) {
					s.Task = p.Message
				}
			case "task_started":
				s.End = endPrompt
			case "task_complete", "turn_complete", "turn_aborted":
				s.End = endDone
			case "error":
				s.End = endError
			case "token_count":
				t := p.Info.Total.Total
				if t != nil {
					s.Tokens = t
				}
				if w := p.Info.Window; w > 0 {
					s.Window = &w
				}
				// Compaction writes a token_count whose last usage is zero: a marker, not a response.
				if u := p.Info.Last; u.Input+u.Output > 0 {
					ctx := u.Input
					s.Context = &ctx
					if t == nil || prevTotal == nil || *t != *prevTotal {
						s.push(max(0, u.Input-u.Cached) + u.Output)
					}
				}
				if t != nil {
					prevTotal = t
				}
				if q, ok := p.RateLimits.read(l.Timestamp); ok {
					s.Quota = &q
				}
			}
		case "response_item":
			switch {
			case p.Type == "function_call" || p.Type == "custom_tool_call":
				s.End = endToolUse
			case strings.HasSuffix(p.Type, "_call_output"):
				s.End = endToolResult
			}
		}
	}
	if len(head) > 0 {
		apply(head)
	}
	for _, raw := range lines {
		apply(raw)
	}
	return s
}

// ---- reading both ----

// encodeClaudeDir is how Claude Code names a project's folder: every character that is not a
// letter or digit becomes "-".
var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func encodeClaudeDir(cwd string) string { return nonAlnum.ReplaceAllString(cwd, "-") }

type fileRef struct {
	name string
	mod  time.Time
}

// newest lists the .jsonl files directly in dir, newest first, at most n.
func newest(fsys fs.FS, dir string, n int) []fileRef {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var out []fileRef
	for i, e := range ents {
		if i >= maxListed {
			break
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, fileRef{path.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func readClaude(fsys fs.FS, f fileRef, b *budget) (session, bool) {
	lines, mod, err := tailLines(fsys, f.name, b)
	if err != nil || len(lines) == 0 {
		return session{}, false
	}
	s := parseClaude(lines)
	s.ModTime = mod
	if s.ID == "" { // the file name is the session id
		s.ID = strings.TrimSuffix(path.Base(f.name), ".jsonl")
	}
	return s, true
}

func readCodex(fsys fs.FS, f fileRef, b *budget) (session, bool) {
	head := headLine(fsys, f.name, b)
	lines, mod, err := tailLines(fsys, f.name, b)
	if err != nil {
		return session{}, false
	}
	s := parseCodex(head, lines)
	s.ModTime = mod
	if s.Cwd == "" {
		return session{}, false
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(path.Base(f.name), ".jsonl")
	}
	return s, true
}

// inferState is the state a session's last record and age suggest. It is a guess and is always
// labelled as one (Source is "transcript").
func inferState(s session, now time.Time) core.AgentState {
	age := now.Sub(s.ModTime)
	switch s.End {
	case endError:
		if age < 30*time.Minute {
			return core.AgentError
		}
		return core.AgentIdle
	case endPrompt, endToolResult:
		if age <= 3*time.Minute {
			return core.AgentRunning
		}
		return core.AgentUnknown
	case endToolUse:
		switch {
		case age <= 5*time.Second:
			return core.AgentRunning
		case age <= 30*time.Minute:
			return core.AgentWaiting
		}
		return core.AgentIdle
	case endDone:
		return core.AgentIdle
	}
	return core.AgentUnknown
}

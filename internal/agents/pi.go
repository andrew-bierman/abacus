package agents

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"path"
	"strings"
)

// pi (a coding agent that keeps one JSONL file per session) writes its sessions to
// ~/.pi/agent/sessions/--<directory>--/*.jsonl. The first line is the session header (id and
// directory); later lines are messages. Only the tail is read, like the other harnesses. A session
// folder set elsewhere through pi's own configuration is not looked at.

type piBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type piUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

type piLine struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Message *struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Provider   string          `json:"provider"`
		Model      string          `json:"model"`
		StopReason string          `json:"stopReason"`
		Usage      *piUsage        `json:"usage"`
	} `json:"message"`
}

// encodePiDir is how pi names a directory's session folder: one leading separator dropped, every
// separator and colon a hyphen, wrapped in "--". (Dots and underscores stay, unlike Claude's.)
func encodePiDir(cwd string) string {
	cwd = strings.TrimPrefix(strings.TrimPrefix(cwd, "/"), `\`)
	return "--" + strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, cwd) + "--"
}

// piText is the visible text of a user message: a string in older files, a list of blocks now.
func piText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []piBlock
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, " ")
}

func parsePi(head []byte, lines [][]byte) session {
	var s session
	apply := func(raw []byte) {
		var l piLine
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
			return
		}
		if l.Type == "session" {
			if s.ID == "" {
				s.ID = l.ID
			}
			if l.Cwd != "" {
				s.Cwd = l.Cwd
			}
			return
		}
		m := l.Message
		if l.Type != "message" || m == nil {
			return
		}
		switch m.Role {
		case "user":
			if text := piText(m.Content); !systemish(text) {
				s.End, s.Task = endPrompt, strings.TrimSpace(text)
			}
		case "toolResult":
			s.End = endToolResult
		case "assistant":
			if m.Model != "" {
				s.Model = m.Model
				if m.Provider != "" {
					s.Model = m.Provider + "/" + m.Model
				}
			}
			switch m.StopReason {
			case "toolUse":
				s.End = endToolUse
			case "error":
				s.End = endError
			case "deferred": // the provider will answer later: the turn is still open
				s.End = endPrompt
			default:
				s.End = endDone
			}
			// A response that never reached the provider reports zero usage: not a turn.
			if u := m.Usage; u != nil && u.Input+u.Output+u.CacheRead+u.CacheWrite > 0 {
				ctx := u.Input + u.CacheRead + u.CacheWrite
				s.Context = &ctx
				s.push(u.Input + u.Output + u.CacheWrite)
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

func readPi(fsys fs.FS, f fileRef, b *budget) (session, bool) {
	head := headLine(fsys, f.name, b)
	lines, mod, err := tailLines(fsys, f.name, b)
	if err != nil {
		return session{}, false
	}
	s := parsePi(head, lines)
	s.ModTime = mod
	if s.ID == "" {
		s.ID = strings.TrimSuffix(path.Base(f.name), ".jsonl")
	}
	return s, true
}

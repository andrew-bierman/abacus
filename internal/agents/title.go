package agents

import "strings"

// promptTitle turns the first real prompt into a session title, dropping the markdown and
// image markers a pasted prompt carries. It is shared by every JSONL harness so a title reads
// the same whichever one wrote the transcript.
func promptTitle(text string) string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") {
			continue
		}
		parts = append(parts, line)
	}
	clean := strings.TrimSpace(dropImageMarkers(strings.Join(parts, " ")))
	return truncateRunes(clean, 50)
}

func dropImageMarkers(s string) string {
	for {
		start := strings.Index(s, "[Image")
		if start < 0 {
			return s
		}
		rest := s[start:]
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return s[:start]
		}
		s = strings.TrimLeft(s[:start]+rest[end+1:], " ")
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

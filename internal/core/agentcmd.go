package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The agent command is the one thing "work on this" runs: an argv the user configured
// (`config set agent_command '["claude","Work on {task_id}: {title}"]'`). Each word stays one
// argument end to end: placeholders are filled in inside a word, never split and never handed to a
// shell, so a hostile title cannot add an argument or a command.

const (
	maxAgentWords     = 16
	maxAgentWordRunes = 512
	maxAgentTitle     = 200
)

// ItemIDRE is an item id as it may be put in a command: the shapes a task, run, lease or
// thread id takes, and nothing that a shell, a flag or a path would read specially.
var ItemIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// placeholderRE finds every {word} in a template word, so an unknown one is a typo to report,
// not text to pass on.
var placeholderRE = regexp.MustCompile(`\{[A-Za-z_]*\}`)

// ParseAgentCommand reads the config value: empty clears it, otherwise a JSON array of words.
func ParseAgentCommand(val string) ([]string, error) {
	if strings.TrimSpace(val) == "" {
		return nil, nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(val), &argv); err != nil {
		return nil, errors.New(`agent_command must be a JSON list of words, for example ["claude","Work on {task_id}: {title}"]`)
	}
	if err := ValidateAgentCommand(argv); err != nil {
		return nil, err
	}
	return argv, nil
}

// ValidateAgentCommand checks a template without filling it in.
func ValidateAgentCommand(argv []string) error {
	if len(argv) == 0 {
		return errors.New("agent_command is empty")
	}
	if len(argv) > maxAgentWords {
		return fmt.Errorf("agent_command has %d words; the limit is %d", len(argv), maxAgentWords)
	}
	for i, w := range argv {
		switch {
		case w == "" && i == 0:
			return errors.New("agent_command: the program (first word) is empty")
		case utf8.RuneCountInString(w) > maxAgentWordRunes:
			return fmt.Errorf("agent_command word %d is longer than %d characters", i+1, maxAgentWordRunes)
		case !utf8.ValidString(w) || Sanitize(w) != w:
			return fmt.Errorf("agent_command word %d holds a control or invisible character", i+1)
		}
		for _, ph := range placeholderRE.FindAllString(w, -1) {
			if ph != "{task_id}" && ph != "{title}" {
				return fmt.Errorf("agent_command word %d: unknown placeholder %s (known: {task_id}, {title})", i+1, ph)
			}
			if i == 0 {
				return errors.New("agent_command: the program (first word) cannot hold a placeholder")
			}
		}
	}
	return nil
}

// ExpandAgentCommand fills the template for one item. The result has exactly as many words as the
// template. An id that is not a plain id, or a word that a filled-in title would turn into an
// option (it starts with "-" and the template word did not), is refused rather than passed on.
func ExpandAgentCommand(tpl []string, id, title string) ([]string, error) {
	if err := ValidateAgentCommand(tpl); err != nil {
		return nil, err
	}
	if !ItemIDRE.MatchString(id) {
		return nil, fmt.Errorf("%q is not an id that can be put in a command", ClipRunes(Sanitize(id), 40))
	}
	title = ClipRunes(strings.Join(strings.Fields(Sanitize(title)), " "), maxAgentTitle)
	if title == "" {
		title = id
	}
	out := make([]string, len(tpl))
	for i, w := range tpl {
		x := strings.ReplaceAll(strings.ReplaceAll(w, "{task_id}", id), "{title}", title)
		if x != w && strings.HasPrefix(x, "-") && !strings.HasPrefix(w, "-") {
			return nil, fmt.Errorf("agent_command word %d would start with \"-\" once filled in, so the program could read it as an option; put text before the placeholder", i+1)
		}
		out[i] = x
	}
	return out, nil
}

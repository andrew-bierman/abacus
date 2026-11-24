package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/andrew-bierman/abacus/internal/core"
)

// topCommands are the commands completion offers, in the order usage lists them.
var topCommands = []string{"board", "tasks", "leases", "queues", "agents", "tui", "search", "rex", "setup", "config", "version", "doctor", "status", "licenses", "completion", "help"}

// taskVerbs are the `tasks` subcommands, in the order the help lists them.
var taskVerbs = []string{"list", "show", "board", "graph", "history", "create", "update", "close", "reopen", "label", "assign", "priority", "dep", "comment", "bulk"}

const writeFlags = "[--yes] [--dry-run] [--json]"

// subHelp is the usage of each subcommand that has its own words; --help prints it (stdout, exit 0).
var subHelp = map[string]string{
	"tasks":          "usage: abacus tasks [--json]\n       abacus tasks " + strings.Join(taskVerbs, "|") + " ...\n\nWith no verb, print the task summary. Run `abacus tasks VERB --help` for a verb.",
	"tasks list":     "usage: abacus tasks list [--query Q] [--sort KEY] [--reverse] [--preset open|ready|blocked|closed|all] [--tree] [--json]",
	"tasks show":     "usage: abacus tasks show ID [--json]",
	"tasks board":    "usage: abacus tasks board [--query Q] [--group status|priority|assignee|type] [--closed] [--json]",
	"tasks graph":    "usage: abacus tasks graph [ID | --whole] [--depth 1-6] [--json]",
	"tasks history":  "usage: abacus tasks history ID [--limit N] [--json]",
	"tasks create":   "usage: abacus tasks create --title TITLE [--description TEXT] [--type TYPE] [--priority 0-4] " + writeFlags,
	"tasks update":   "usage: abacus tasks update ID [--title TITLE] [--description TEXT] [--status STATUS] [--priority 0-4] " + writeFlags,
	"tasks close":    "usage: abacus tasks close ID [--reason TEXT] " + writeFlags,
	"tasks reopen":   "usage: abacus tasks reopen ID [--reason TEXT] " + writeFlags,
	"tasks label":    "usage: abacus tasks " + editVerbs["label"] + " " + writeFlags,
	"tasks assign":   "usage: abacus tasks " + editVerbs["assign"] + " " + writeFlags,
	"tasks priority": "usage: abacus tasks " + editVerbs["priority"] + " " + writeFlags,
	"tasks dep":      "usage: abacus tasks " + editVerbs["dep"] + " " + writeFlags,
	"tasks comment":  "usage: abacus tasks " + editVerbs["comment"] + " " + writeFlags,
	"tasks bulk":     "usage: abacus " + bulkUsage + "\n\nChanges up to 25 tasks with one confirmation; a task that changed since it was read is skipped and reported.",
	"board":          "usage: abacus board [--json]\n       abacus board post CHANNEL TEXT " + writeFlags + "\n       abacus board reply ID TEXT " + writeFlags,
	"board post":     "usage: abacus board post CHANNEL TEXT [--text TEXT] " + writeFlags,
	"board reply":    "usage: abacus board reply ID TEXT [--text TEXT] " + writeFlags,
	"rex":            "usage: abacus rex layout [--new NAME] [--yes] [--dry-run] [--json]\n\nOpens the cockpit tabs in a terminal workspace; it only opens tabs.",
	"rex layout":     "usage: abacus rex layout [--new NAME] [--yes] [--dry-run] [--json]",
	"config":         "usage: abacus config show [--json] | get KEY | set KEY VALUE | path\nkeys: " + strings.Join(core.ConfigKeys, ", "),
	"licenses":       "usage: abacus licenses\n\nPrints the third-party notices and license texts carried in this file.",
	"completion":     "usage: abacus completion bash|zsh|fish\n\nPrints a shell completion script, for example: source <(abacus completion bash)",
	"help":           "usage: abacus help [command]",
}

func wantsHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	last := args[len(args)-1]
	return last == "-h" || last == "--help" || args[0] == "help"
}

// runHelp answers `help [command...]`, `-h` and `--help` anywhere a command ends: usage on stdout, exit 0.
// A command without its own text falls through to its flag listing (printed to stdout too).
func runHelp(args []string, d Deps) (int, bool) {
	words := args
	if len(words) > 0 && words[0] == "help" {
		words = words[1:]
	}
	for len(words) > 0 && (words[len(words)-1] == "-h" || words[len(words)-1] == "--help") {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		fprintUsage(d.Out)
		return core.ExitOK, true
	}
	for n := len(words); n > 0; n-- {
		if t, ok := subHelp[strings.Join(words[:n], " ")]; ok {
			fmt.Fprintln(d.Out, t)
			return core.ExitOK, true
		}
	}
	return 0, false
}

func fprintUsage(w io.Writer) {
	fmt.Fprint(w, usageText())
}

// complete lists the words that can follow `abacus` and `abacus tasks`, for the completion scripts.
func completionScript(shell string) (string, bool) {
	top := strings.Join(topCommands, " ")
	tv := strings.Join(taskVerbs, " ")
	switch shell {
	case "bash":
		return fmt.Sprintf("_abacus() {\n  local cur=${COMP_WORDS[COMP_CWORD]}\n  if [ \"$COMP_CWORD\" -eq 1 ]; then COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n  elif [ \"$COMP_CWORD\" -eq 2 ] && [ \"${COMP_WORDS[1]}\" = tasks ]; then COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"))\n  fi\n}\ncomplete -F _abacus abacus\n", top, tv), true
	case "zsh":
		return fmt.Sprintf("#compdef abacus\n_abacus() {\n  if (( CURRENT == 2 )); then compadd %s\n  elif (( CURRENT == 3 )) && [[ $words[2] == tasks ]]; then compadd %s\n  fi\n}\ncompdef _abacus abacus\n", top, tv), true
	case "fish":
		var b strings.Builder
		fmt.Fprintf(&b, "complete -c abacus -f -n '__fish_use_subcommand' -a '%s'\n", top)
		fmt.Fprintf(&b, "complete -c abacus -f -n '__fish_seen_subcommand_from tasks' -a '%s'\n", tv)
		return b.String(), true
	}
	return "", false
}

func runCompletion(args []string, d Deps) int {
	if len(args) != 1 {
		fmt.Fprintln(d.Err, subHelp["completion"])
		return core.ExitUsage
	}
	s, ok := completionScript(args[0])
	if !ok {
		fmt.Fprintf(d.Err, "completion: unknown shell %q (bash, zsh or fish)\n", core.Sanitize(args[0]))
		return core.ExitUsage
	}
	fmt.Fprint(d.Out, s)
	return core.ExitOK
}

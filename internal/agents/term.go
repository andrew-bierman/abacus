package agents

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var paneIDRE = regexp.MustCompile(`^%[0-9]+$`)
var targetRE = regexp.MustCompile(`^[A-Za-z0-9_.:@ -]{1,80}$`)

// tmuxPanes lists every pane of the running tmux server. The one fixed argv is read-only (it
// lists, never selects or sends keys), runs under a short deadline with a scrubbed environment,
// and an absent tmux or server is an error the caller treats as "no panes".
func tmuxPanes(ctx context.Context) ([]Pane, error) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "list-panes", "-a", "-F", "#{pane_pid}\t#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}")
	cmd.Env = scrubbedEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePanes(string(out)), nil
}

// parsePanes reads the list-panes output, dropping any row that is not exactly what tmux prints
// (a pane id is the one thing later put in a command line a person copies).
func parsePanes(out string) []Pane {
	var panes []Pane
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Split(ln, "\t")
		if len(f) != 3 || !paneIDRE.MatchString(f[1]) || !targetRE.MatchString(f[2]) {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 0 {
			continue
		}
		panes = append(panes, Pane{PID: pid, ID: f[1], Target: f[2]})
	}
	return panes
}

// envKeep are the only variables a helper (or the monitor) inherits: where things live, nothing
// else. Credentials and tool configuration in the environment never reach a child.
var envKeep = []string{"HOME", "PATH", "USER", "LOGNAME", "TMUX", "TMUX_TMPDIR", "XDG_RUNTIME_DIR", "TMPDIR", "LANG",
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"}

func scrubbedEnv() []string {
	var out []string
	for _, k := range envKeep {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

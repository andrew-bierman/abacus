package core

import (
	"context"
	"errors"
	"fmt"
)

// ConfigSetPlan is a validated `config set KEY VALUE`, planned so a front end can show it and apply it once
// (the serve protocol's plan.config.set). The command line's own `config set` is unchanged.
type ConfigSetPlan struct {
	Key, Value string
	// Before is the value the key has now: the state the plan was made against, checked again at apply.
	Before string
	Lines  []string
	// Argv is, for agent_command, what "work on this" will run (display only; it is also what the audit records).
	Argv      []string
	confirmed bool
}

// PlanConfigSet validates key and value on a scratch copy, so a bad one never depends on the file, and reads
// the current value. It writes nothing.
func PlanConfigSet(st ConfigStore, key, value string) (ConfigSetPlan, error) {
	var scratch Config
	if err := scratch.Set(key, value); err != nil {
		return ConfigSetPlan{}, err
	}
	if key == "agent_command" { // a list and the same words as text (any spacing) are one value: one digest, one stored form
		value = agentCommandJSON(scratch.AgentCommand)
	}
	cur, err := st.Load()
	var corrupt *errCorrupt
	if err != nil && !errors.As(err, &corrupt) { // an unreadable file is kept aside by the write, as the command does
		return ConfigSetPlan{}, err
	}
	before, _ := cur.Get(key)
	p := ConfigSetPlan{Key: key, Value: value, Before: before, Argv: scratch.AgentCommand}
	shown := Sanitize(value)
	if shown == "" {
		shown = "(not set)"
	}
	p.Lines = []string{fmt.Sprintf("set %s to %s", key, shown)}
	if key == "agent_command" {
		p.Lines = append(p.Lines, "work on this will run: "+DisplayArgv(scratch.AgentCommand))
	}
	return p, nil
}

// Digest is the plan digest of the change: the key, the new value and the value it was planned against.
func (p ConfigSetPlan) Digest() string {
	return PlanDigest("config", "set", []string{p.Key}, [][]string{{"set", p.Key, p.Value}}, map[string]string{"before": p.Before})
}

// Confirm marks the plan as explicitly approved.
func (p ConfigSetPlan) Confirm() ConfigSetPlan { p.confirmed = true; return p }

// ApplyConfigSet writes a confirmed plan once. It fails closed: a plan whose key changed since it was made
// is ErrChanged and nothing is audited or written; the attempt is recorded before the write (a log that
// cannot be written blocks it) and the outcome after. It returns the file's note, if any, and whether the
// write was started (false: refused before the attempt was recorded, so nothing happened).
func ApplyConfigSet(ctx context.Context, st ConfigStore, au Audit, p ConfigSetPlan) (note string, wrote bool, err error) {
	if !p.confirmed {
		return "", false, ErrNotConfirmed
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	cur, err := st.Load()
	var corrupt *errCorrupt
	if err != nil && !errors.As(err, &corrupt) {
		return "", false, err
	}
	if now, _ := cur.Get(p.Key); now != p.Before {
		return "", false, Stale(p.Key + " was changed since the plan was made")
	}
	// The record names the key, and for the few keys a person would want to trace, the new value (bounded,
	// control characters removed; none of them holds a secret: a board URL with credentials is refused).
	argv := []string{p.Key}
	switch p.Key {
	case "agent_command":
		argv = p.Argv
	case "tracker_dir", "board_url", "actor":
		v := Sanitize(p.Value)
		if len(v) > 120 {
			v = v[:120] + "..."
		}
		argv = append(argv, v)
	}
	err = newGate(au).do(setupSeq.Add(1), AuditEntry{Domain: "config", Verb: "set " + p.Key, Argv: argv}, func() error {
		var err error
		note, err = st.UpdateNote(func(c *Config) error {
			// again, under the file's lock: a change made since the check above (or the attempt line) is not overwritten
			if now, _ := c.Get(p.Key); now != p.Before {
				return Stale(p.Key + " was changed since the plan was made")
			}
			return c.Set(p.Key, p.Value)
		})
		return err
	})
	var ae *AuditError
	return note, !errors.As(err, &ae), err
}

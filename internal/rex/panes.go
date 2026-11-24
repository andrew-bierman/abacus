package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/andrew-bierman/abacus/internal/core"
)

// maxCapture bounds what one terminal read keeps: the end of the output, which is what a person
// looks at. A terminal holds up to 64 MiB of scrollback, so the rest is cut as it is read.
const maxCapture = 128 << 10

// readParallel bounds how many sessions have their panes read at once.
const readParallel = 4

type cachedBlocks struct {
	at time.Time
	by map[string][]block
}

// Invalidate drops what was read, so a front-end that just changed something sees it at once.
func (m *Module) Invalidate() { m.invalidate() }

// invalidate drops what was read, so the next read asks the server again. The change feed calls it.
func (m *Module) invalidate() {
	m.mu.Lock()
	m.cache, m.bcache = nil, nil
	m.mu.Unlock()
}

// parseBlocks reads `block ls --json`: {"blocks": null | [{"block_id": string, "label": string}, ...]}.
// Unknown fields are ignored; anything else is not a pane list. A pane whose id is not a pane id
// is dropped, since the id ends up in a command.
func parseBlocks(out []byte) ([]block, bool) {
	var top struct {
		Blocks []struct {
			ID    string `json:"block_id"`
			Label string `json:"label"`
		} `json:"blocks"`
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(out, &raw) != nil || raw == nil {
		return nil, false
	}
	if _, ok := raw["blocks"]; !ok || json.Unmarshal(out, &top) != nil {
		return nil, false
	}
	var bs []block
	for _, b := range top.Blocks {
		if len(bs) >= maxBlocks {
			break
		}
		if blockRE.MatchString(b.ID) {
			bs = append(bs, block{ID: b.ID, text: core.Sanitize(b.Label)})
		}
	}
	return bs, true
}

// readBlocks reads the panes of a host's sessions (at most maxBlockSessions, a few at a time) for
// one workspace read. It is best effort: a session whose panes cannot be read, or that did not
// answer within blockReadTime, simply has none, and the read still stands on the session names.
func (m *Module) readBlocks(ctx context.Context, host string, ss []session) map[string][]block {
	ttl := blockCacheTTL
	if m.live.Load() {
		ttl = liveCacheTTL
	}
	if m.o.CacheTTL > 0 {
		m.mu.Lock()
		c, ok := m.bcache[host]
		m.mu.Unlock()
		if ok && m.o.Now().Sub(c.at) < ttl {
			return c.by
		}
	}
	ctx, cancel := context.WithTimeout(ctx, blockReadTime)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		by  = map[string][]block{}
		sem = make(chan struct{}, readParallel)
	)
	for i, s := range ss {
		if i >= maxBlockSessions || !sessionRE.MatchString(s.ID) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out, exit, err := m.run.Run(ctx, m.Bin(), blocksArgv(host, s.ID)...)
			if err != nil || exit != 0 {
				return
			}
			if bs, ok := parseBlocks(out); ok {
				mu.Lock()
				by[s.ID] = bs
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if m.o.CacheTTL > 0 && ctx.Err() == nil { // a partial read is not kept
		m.mu.Lock()
		if m.bcache == nil {
			m.bcache = map[string]cachedBlocks{}
		}
		m.bcache[host] = cachedBlocks{at: m.o.Now(), by: by}
		m.mu.Unlock()
	}
	return by
}

// targetHost is the machine a write or terminal read for a placement goes to: this one (""), or a
// host in the configured list. Any other machine is never contacted.
func (m *Module) targetHost(host string) (string, error) {
	kind, h := m.classify(core.WorkspaceItem{Host: host})
	switch kind {
	case hostLocal, hostAllowed:
		return h, nil
	case hostNotAllowed:
		return "", errors.New("this machine is not in the configured list of machines abacus may contact")
	}
	return "", errors.New("this item's machine name is not safe to pass on")
}

// Capture reads the end of what a placement's terminal shows. It only reads: nothing is sent to
// the terminal. The text is cleaned by core before anyone sees it.
func (m *Module) Capture(ctx context.Context, p core.Placement) (core.WorkspaceCapture, error) {
	if p.Block == "" || p.Session == "" {
		return core.WorkspaceCapture{}, errors.New("this item has no terminal to read")
	}
	host, err := m.targetHost(p.Host)
	if err != nil {
		return core.WorkspaceCapture{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, m.o.CallTimeout)
	defer cancel()
	out, exit, err := m.crun.Run(ctx, m.Bin(), captureArgv(host, p.Session, p.Block)...)
	cut := false
	switch {
	case errors.Is(err, core.ErrOutputTruncated):
		cut, err = true, nil
	case err != nil:
		return core.WorkspaceCapture{}, fmt.Errorf("reading the terminal %s", runFailure(err))
	}
	switch exit {
	case 0:
	case exitNoServer:
		return core.WorkspaceCapture{}, errNoServer
	default:
		return core.WorkspaceCapture{}, fmt.Errorf("the terminal could not be read (exit %d)", exit)
	}
	return core.CleanCapture(out, cut), nil
}

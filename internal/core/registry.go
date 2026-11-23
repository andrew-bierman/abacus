package core

import (
	"context"
	"time"
)

// Entry pairs a module with its detection for this invocation.
type Entry struct {
	Module    Module
	Detection Detection
}

// Registry holds compiled-in modules in a fixed order.
type Registry struct{ mods []Module }

// NewRegistry returns a registry iterating modules in the given order.
func NewRegistry(mods ...Module) *Registry { return &Registry{mods: mods} }

// DetectDeadline bounds the whole startup detection, so slow probes (a cold bundled bd can take
// seconds to start) cannot add up one after another before the first frame.
var DetectDeadline = 8 * time.Second

// Detect probes every module exactly once, concurrently, and returns all entries in module
// order, absent ones included. A module that has not answered by DetectDeadline is reported
// broken ("detection timed out"), not waited for.
func (r *Registry) Detect(ctx context.Context) []Entry {
	ClearEmbeddedFailures() // a refresh tries a failed built-in tool again
	ctx, cancel := context.WithTimeout(ctx, DetectDeadline)
	defer cancel()
	chans := make([]chan Detection, len(r.mods))
	for i, m := range r.mods {
		chans[i] = make(chan Detection, 1)
		go func() { chans[i] <- m.Detect(ctx) }()
	}
	out := make([]Entry, 0, len(r.mods))
	for i, m := range r.mods {
		var d Detection
		select {
		case d = <-chans[i]:
		case <-ctx.Done():
			select { // an answer that arrived at the same moment still counts
			case d = <-chans[i]:
			default:
				d = Detection{State: Broken, Detail: "detection timed out"}
			}
		}
		// Version and Detail come from the tool; no front-end should ever see control characters.
		d.Version, d.Detail = Sanitize(d.Version), Sanitize(d.Detail)
		if l, ok := m.(Located); ok && d.State != Absent {
			f := l.Located()
			d.Path, d.Source, d.Ignored = Sanitize(f.Bin), f.Src, Sanitize(f.Ignored)
		}
		out = append(out, Entry{Module: m, Detection: d})
	}
	return out
}

// ReadyOnly filters entries to modules the umbrella can use.
func ReadyOnly(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Detection.State == Ready {
			out = append(out, e)
		}
	}
	return out
}

package core

import "context"

// Entry pairs a module with its detection for this invocation.
type Entry struct {
	Module    Module
	Detection Detection
}

// Registry holds compiled-in modules in a fixed order.
type Registry struct{ mods []Module }

// NewRegistry returns a registry iterating modules in the given order.
func NewRegistry(mods ...Module) *Registry { return &Registry{mods: mods} }

// Detect probes every module exactly once and returns all entries, absent ones included.
func (r *Registry) Detect(ctx context.Context) []Entry {
	out := make([]Entry, 0, len(r.mods))
	for _, m := range r.mods {
		d := m.Detect(ctx)
		// Version and Detail come from the tool; no front-end should ever see control characters.
		d.Version, d.Detail = Sanitize(d.Version), Sanitize(d.Detail)
		if l, ok := m.(Located); ok && d.State != Absent {
			d.Path, d.Source = l.Located()
			d.Path = Sanitize(d.Path)
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

package core

// SinceKind says how much a tasks.since answer is: a delta against the state the client named, or everything.
type SinceKind string

const (
	SinceDelta SinceKind = "delta" // Rows changed (new or different), Removed gone, since the fingerprint named
	SinceFull  SinceKind = "full"  // the fingerprint was not known (or the tracker is too big to compare): refetch the list
)

// SinceKinds is every SinceKind, in the order the schema and the docs list them.
var SinceKinds = []SinceKind{SinceDelta, SinceFull}

// SchemaValues makes the schema a closed enum (see SchemaEnum).
func (SinceKind) SchemaValues() []string {
	out := make([]string, len(SinceKinds))
	for i, k := range SinceKinds {
		out[i] = string(k)
	}
	return out
}

// TasksSince is the result of the serve method tasks.since: the task rows that changed since a fingerprint, so a
// client refetches incrementally. Fingerprint is the token of the state this answer describes: send it back next
// time (compare it, never parse it). With Kind "full" Rows and Removed are empty and the client reads the list
// again; the fingerprint is still the new baseline.
type TasksSince struct {
	Kind        SinceKind `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	// Rows are the tasks that are new or differ (every field of the row, as tasks.list shows it), sorted by id.
	Rows []TaskRow `json:"rows"`
	// Removed are the ids that were in the state named and are not now.
	Removed []string `json:"removed"`
	// Total is how many tasks the tracker holds now.
	Total   int    `json:"total"`
	Tracker string `json:"tracker,omitempty"`
	ReadAt  string `json:"read_at,omitempty"`
}

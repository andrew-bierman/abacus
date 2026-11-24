package lambe

// Wire DTOs for Lambe's state API v1 (docs/api/state-api.md in the Lambe repo).
// Unknown fields are ignored; absent optional fields decode as their zero value,
// which this package treats as "unknown". Lists accept null and [] alike.

// SupportedAPIVersion is the only `lambe version --json` api_version this module reads.
const SupportedAPIVersion = 1

// Daemon is the supervisor's own state in a snapshot.
type Daemon struct {
	PID        int    `json:"pid"`
	Identity   string `json:"identity"`
	Machine    string `json:"machine"`
	Version    string `json:"version"`
	ConfigPath string `json:"config_path"`
	Actor      string `json:"actor"`
}

// Run is one supervised run in a snapshot.
type Run struct {
	Issue          string `json:"issue"`
	Session        string `json:"session"`
	Attempt        int    `json:"attempt"`
	State          string `json:"state"`
	LastEventAge   string `json:"last_event_age"`
	LastEventAt    string `json:"last_event_at"`
	Phase          string `json:"phase"`
	Workflow       string `json:"workflow"`
	Machine        string `json:"machine"`
	Actor          string `json:"actor"`
	LastActivityAt string `json:"last_activity_at"`
	Stalled        bool   `json:"stalled"`
}

// NeedsHuman is one issue the supervisor has stopped on and a person must handle.
type NeedsHuman struct {
	Issue  string `json:"issue"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	Since  string `json:"since"`
}

// Snapshot is the supervisor's status document (schema version 1).
type Snapshot struct {
	SchemaVersion int          `json:"schema_version"`
	Daemon        Daemon       `json:"daemon"`
	Runs          []Run        `json:"runs"`
	NeedsHuman    []NeedsHuman `json:"needs_human"`
	RunsAsOf      string       `json:"runs_as_of"`
	TickAt        string       `json:"tick_at"`
}

// Blocker is one issue that blocks a queued issue.
type Blocker struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// QueueRow is one queued issue.
type QueueRow struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Reason   string    `json:"reason"`
	Blocked  bool      `json:"blocked"`
	Blockers []Blocker `json:"blockers"`
}

// Queue is the supervisor's queue document.
type Queue struct {
	SchemaVersion int        `json:"schema_version"`
	Daemon        Daemon     `json:"daemon"`
	AsOf          string     `json:"as_of"`
	Rows          []QueueRow `json:"rows"`
}

// HistoryRun is one finished run.
type HistoryRun struct {
	Issue     string `json:"issue"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Phase     string `json:"phase"`
	Outcome   string `json:"outcome"`
	Attempt   int    `json:"attempt"`
	UpdatedAt string `json:"updated_at"`
}

// History is the supervisor's run history document.
type History struct {
	SchemaVersion int          `json:"schema_version"`
	Daemon        Daemon       `json:"daemon"`
	AsOf          string       `json:"as_of"`
	Runs          []HistoryRun `json:"runs"`
}

type errorBody struct {
	SchemaVersion int    `json:"schema_version"`
	Error         string `json:"error"`
	Message       string `json:"message"`
}

// Descriptor is the per-daemon discovery file. BearerToken is secret: never log or print it.
type Descriptor struct {
	SchemaVersion int    `json:"schema_version"`
	Instance      string `json:"instance"`
	Addr          string `json:"addr"`
	BearerToken   string `json:"bearer_token"`
	PID           int    `json:"pid"`
	Identity      string `json:"identity"`
	Version       string `json:"version"`
	ConfigPath    string `json:"config_path"`
	StartedAt     string `json:"started_at"`
}

// SourceTracker labels a View read from the tracker with no daemon (`lambe status -json`).
const SourceTracker = "tracker subset"

// View is everything the status and TUI surfaces read in one poll. Source is empty for a
// live state-API read; SourceTracker marks the degraded subset, which has no queue.
type View struct {
	Snapshot Snapshot
	Queue    Queue
	History  History // best effort: empty when the history route is unavailable
	// HistoryErr is why the history could not be read ("" when it was); sanitized with the rest.
	HistoryErr string
	Source     string
}

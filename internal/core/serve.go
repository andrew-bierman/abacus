package core

// The typed messages of the serve protocol (docs/serve-protocol.md): what `abacus serve --stdio` returns
// for initialize, the write plans it holds and the applies it runs. Every result goes out through
// ReportJSONCompact, so it carries schema_version like any --json output. The reports of the read
// methods are the ones the commands print and are not repeated here.

// ServeProtocolVersion is the version of the line protocol (methods, framing, errors). It moves only
// for a breaking change; a new method, field or notification is additive.
const ServeProtocolVersion = 1

// ServeLimits are the bounds the server enforces, announced so a client can stay inside them.
type ServeLimits struct {
	MaxRequestBytes  int `json:"max_request_bytes"`
	MaxResponseBytes int `json:"max_response_bytes"`
	MaxDepth         int `json:"max_depth"`
	MaxInFlight      int `json:"max_in_flight"`
	// MaxQueued is how many requests past MaxInFlight wait their turn (in arrival order) before the
	// next is refused with "busy". Additive.
	MaxQueued         int `json:"max_queued"`
	MaxPlans          int `json:"max_plans"`
	PlanTTLSeconds    int `json:"plan_ttl_seconds"`
	DefaultDeadlineMS int `json:"default_deadline_ms"`
	MaxDeadlineMS     int `json:"max_deadline_ms"`
}

// ServeCapabilities says what this server offers.
type ServeCapabilities struct {
	Methods       []string    `json:"methods"`
	Notifications []string    `json:"notifications"`
	Topics        []string    `json:"topics"`
	Limits        ServeLimits `json:"limits"`
}

// ServeInitialize is the result of `initialize`: the one place a version mismatch is decided.
type ServeInitialize struct {
	ProtocolVersion int               `json:"protocol_version"`
	AbacusVersion   string            `json:"abacus_version"`
	Capabilities    ServeCapabilities `json:"capabilities"`
	// Contract is the contract section `version --json` carries, so a client needs no second process to
	// feature-detect. Additive.
	Contract *Contract `json:"contract,omitempty"`
}

// ServePlan is the result of every plan.* method: what would run, nothing run. The argv is for
// display only; the server never accepts one. Apply it with `apply {plan_id}`, once.
type ServePlan struct {
	PlanID string `json:"plan_id"`
	// PlanDigest is the same digest `--dry-run --json` prints (see PlanDigest).
	PlanDigest string `json:"plan_digest"`
	// Kind is task, bulk, board, setup or config.
	Kind  string   `json:"kind"`
	Op    string   `json:"op"`
	Lines []string `json:"lines"`
	// Context is read-only context for the confirm step.
	Context []string `json:"context,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	// Items and Skipped are a batch plan's tasks.
	Items    []BulkDryRunItem `json:"items,omitempty"`
	Skipped  []BulkSkip       `json:"skipped,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
	// ExpiresAt is when the plan stops being appliable (RFC 3339, UTC).
	ExpiresAt string `json:"expires_at"`
}

// ServeApplied is the result of an `apply` that ran. A failed apply is an error (see ServeErrorData).
// Exactly one of Task, Board, Bulk, Setup and Config is set, as Kind says. Outcome is applied, or for a batch partial
// when some task was not written (read Bulk.Items).
type ServeApplied struct {
	PlanID     string           `json:"plan_id"`
	PlanDigest string           `json:"plan_digest"`
	Kind       string           `json:"kind"`
	Outcome    string           `json:"outcome"`
	Task       *TaskResult      `json:"task,omitempty"`
	Board      *BoardResult     `json:"board,omitempty"`
	Bulk       *BulkReport      `json:"bulk,omitempty"`
	Setup      *ServeStepResult `json:"setup,omitempty"`
	Config     *ServeStepResult `json:"config,omitempty"`
}

// ServeStepResult is what an applied setup step or `config set` reports: what it was and the one-line result.
type ServeStepResult struct {
	// Step is the setup step (workspace, queues, actor, repair), or the config key.
	Step    string `json:"step"`
	Message string `json:"message"`
}

// ServeErrorData is the `data` of a JSON-RPC error: the error object any failing command prints, with
// the plan it concerns and the P3 outcome of a write (not_applied, unknown, applied_mismatch,
// already_applied, changed).
type ServeErrorData struct {
	ErrorBody
	PlanID     string `json:"plan_id,omitempty"`
	PlanDigest string `json:"plan_digest,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	// ExpiresAt is, for an expired plan, when it stopped being appliable (RFC 3339, UTC).
	ExpiresAt string `json:"expires_at,omitempty"`
	// RetryAfterMS is, for busy, how long to wait before sending the request again.
	RetryAfterMS int `json:"retry_after_ms,omitempty"`
}

// ServeSubscribed is the result of subscribe and unsubscribe: the topics now live, and why the others are not.
type ServeSubscribed struct {
	Active      []string          `json:"active"`
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// ServeNotice is the params of a notification. It says that something changed, and never carries the data:
// refetch. Kind says how much: "full" (refetch the whole topic, the default and what a client that ignores
// the other fields does) or "ids" (only the tasks or agents in ChangedIDs were added, changed or removed:
// refetch those, and drop one that no longer exists). A notice that merged several changes lists them all, or
// says "full" past a bound. Fingerprint is an opaque token of the state the notice is about (compare, never parse).
type ServeNotice struct {
	Topic       string   `json:"topic"`
	Seq         uint64   `json:"seq"`
	Reason      string   `json:"reason,omitempty"`
	Kind        string   `json:"kind"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	ChangedIDs  []string `json:"changed_ids,omitempty"`
}

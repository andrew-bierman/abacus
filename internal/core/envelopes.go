package core

// The JSON envelopes the write commands and setup print, as named types, so the contract schema
// (`abacus schema`) is derived from the same Go types the CLI encodes. ReportJSON adds schema_version.

// WriteDryRun is `--dry-run --json` of a single write: what would run, nothing run.
type WriteDryRun struct {
	DryRun  bool     `json:"dry_run"`
	Lines   []string `json:"change"`
	Context []string `json:"context,omitempty"`
	Argv    []string `json:"argv"`
	// Steps are the later commands of a plan with several steps.
	Steps [][]string `json:"steps,omitempty"`
	// PlanDigest identifies this plan: pass it as --expect-digest to apply exactly it (see PlanDigest).
	PlanDigest string `json:"plan_digest"`
}

// WriteApplied is `--yes --json` of a single write that was applied; Result is the TaskResult of a
// Tasks write or the BoardResult of a Board write.
type WriteApplied struct {
	Applied    bool   `json:"applied"`
	Result     any    `json:"result"`
	PlanDigest string `json:"plan_digest"`
}

// TaskApplied and BoardApplied are WriteApplied with its Result named, for the schema.
type TaskApplied struct {
	Applied    bool       `json:"applied"`
	Result     TaskResult `json:"result"`
	PlanDigest string     `json:"plan_digest"`
}

// BoardApplied is the applied envelope of a Board write.
type BoardApplied struct {
	Applied    bool        `json:"applied"`
	Result     BoardResult `json:"result"`
	PlanDigest string      `json:"plan_digest"`
}

// BulkDryRunItem is one task of a planned batch.
type BulkDryRunItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title,omitempty"`
	Change string   `json:"change"`
	Argv   []string `json:"argv"`
}

// BulkDryRun is `tasks bulk --dry-run --json`.
type BulkDryRun struct {
	SchemaVersion int              `json:"schema_version"`
	DryRun        bool             `json:"dry_run"`
	Op            string           `json:"op"`
	Items         []BulkDryRunItem `json:"items"`
	Skipped       []BulkSkip       `json:"skipped,omitempty"`
	PlanDigest    string           `json:"plan_digest"`
}

// SetupResult is the outcome of one setup step that was attempted.
type SetupResult struct {
	Step    StepID `json:"step"`
	Applied bool   `json:"applied"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SetupReport is `setup --json`: the steps (and what is left after applying), and what was attempted.
type SetupReport struct {
	Steps      []StepInfo    `json:"steps"`
	Results    []SetupResult `json:"results,omitempty"`
	PlanDigest string        `json:"plan_digest"`
}

// SetupRepairReport is `setup --repair --json`.
type SetupRepairReport struct {
	Results    []SetupResult `json:"results"`
	PlanDigest string        `json:"plan_digest,omitempty"`
}

// SchemaIndex is `schema --json`: the schemas `abacus schema` can print, and the file each one has with --out.
type SchemaIndex struct {
	Schemas []SchemaIndexEntry `json:"schemas"`
}

// SchemaIndexEntry names one schema and the commands whose --json output it describes.
type SchemaIndexEntry struct {
	Name     string `json:"name"`
	Commands string `json:"commands"`
	File     string `json:"file"`
}

// ConfigShow is `config show --json`: the settings file's path and its settings.
type ConfigShow struct {
	Path string `json:"path"`
	Config
}

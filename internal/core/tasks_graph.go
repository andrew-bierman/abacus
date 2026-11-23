package core

// The dependency graph view (plan unit U5). GraphView is the typed result of a pure, deterministic
// layout over the Tasks snapshot's blocking edges (internal/taskview.GraphIndex); the TUI only
// draws it. An edge "From blocks To" means To waits for From. Every string was sanitized when the
// snapshot was read.

// Graph modes.
const (
	GraphNeighborhood = "neighborhood" // the selected task, what blocks it and what it blocks
	GraphWhole        = "whole"        // every task that has a blocking link, in topological layers
)

// Outline sides.
const (
	OutlineUp    = "up"    // a blocker, directly or further up
	OutlineFocus = "focus" // the selected task
	OutlineDown  = "down"  // a task that waits for the focus, directly or further down
)

// GraphNode is one task in the view. Layer is signed in a neighborhood (negative: a blocker that
// many steps above the focus, 0: the focus, positive: a dependent) and a topological layer in the
// whole graph (0: nothing blocks it).
type GraphNode struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	Layer     int    `json:"layer"`
	Order     int    `json:"order"` // position inside its layer
	BlockedBy int    `json:"blocked_by,omitempty"`
	Blocks    int    `json:"blocks,omitempty"`
	Ready     bool   `json:"ready,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
	Focus     bool   `json:"focus,omitempty"`
	// OnChain marks the longest chain; InCycle a member of a dependency cycle; Bottleneck comes from
	// the graph-insights engine when its report was given (never on closed work).
	OnChain    bool `json:"on_chain,omitempty"`
	InCycle    bool `json:"in_cycle,omitempty"`
	Bottleneck bool `json:"bottleneck,omitempty"`
	SelfDep    bool `json:"self_dependency,omitempty"`
	// Missing are blocking dependencies on tasks that are not in this tracker.
	Missing []string `json:"missing,omitempty"`
}

// GraphEdge says From blocks To. Drawn is false for a link between layers that are not neighbors,
// which the view counts instead of routing.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Drawn bool   `json:"drawn"`
}

// GraphLayer is one layer of nodes in display order; More counts nodes collapsed into "+N more".
type GraphLayer struct {
	Layer int      `json:"layer"`
	Nodes []string `json:"nodes"`
	More  int      `json:"more,omitempty"`
}

// OutlineRow is one line of the indented outline. A row with More > 0 and no ID stands for "+N more"
// siblings; Repeat marks a task listed elsewhere (it is not expanded again). Every row with an ID
// carries its node, so the outline can be drawn without looking anything up in GraphView.Nodes
// (which holds only the tasks the boxes show). A task is listed at its shortest distance from the
// focus, so the outline and the boxes agree on who is within the depth.
type OutlineRow struct {
	ID     string     `json:"id,omitempty"`
	Side   string     `json:"side"`
	Depth  int        `json:"depth"`
	Repeat bool       `json:"repeat,omitempty"`
	More   int        `json:"more,omitempty"`
	Node   *GraphNode `json:"node,omitempty"`
}

// GraphView is the graph of one snapshot, laid out for a neighborhood or the whole tracker.
type GraphView struct {
	Header
	Mode  string `json:"mode"`
	Focus string `json:"focus,omitempty"`
	Depth int    `json:"depth,omitempty"`

	Nodes   []GraphNode  `json:"nodes"`
	Edges   []GraphEdge  `json:"edges,omitempty"`
	Layers  []GraphLayer `json:"layers,omitempty"`
	Outline []OutlineRow `json:"outline,omitempty"` // neighborhood only

	// Chain is the longest chain of unfinished tasks, blocker first: through the focus in a
	// neighborhood, in the whole tracker otherwise. Cycle members cannot be ordered, so they are never on it.
	Chain    []string   `json:"chain,omitempty"`
	Cycles   [][]string `json:"cycles,omitempty"`
	SelfDeps []string   `json:"self_dependencies,omitempty"`

	Total      int `json:"total"`                 // tasks in the tracker
	Components int `json:"components,omitempty"`  // connected groups of linked tasks
	Isolated   int `json:"isolated,omitempty"`    // tasks with no blocking link, left out of the whole graph
	Hidden     int `json:"hidden,omitempty"`      // linked tasks collapsed into "+N more"
	CrossLinks int `json:"cross_links,omitempty"` // links not drawn: between layers that are not neighbors, or sharing a bus ambiguously
	// MarksSource says where the Bottleneck marks came from: "engine" or "" (none were given).
	MarksSource string `json:"marks_source,omitempty"`
	// OpenBlockers and Unblocks describe the focus: unfinished blockers, and dependents that
	// would be freed when it closes.
	OpenBlockers int `json:"open_blockers,omitempty"`
	Unblocks     int `json:"unblocks,omitempty"`
}

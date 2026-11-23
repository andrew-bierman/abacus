package taskview

import (
	"slices"
	"sort"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The dependency graph layout (plan unit U5). Everything here is pure and deterministic: ids are
// sorted before anything else happens and every tie falls to the id, so the same snapshot lays out
// identically in any record order. Edges are the blocking dependencies exactly as Build derives
// them (blocking), kept when both ends are in the snapshot; a blocker outside the snapshot is a
// flag on the node, and a task that blocks itself is a flag, not a cycle.

const (
	defaultGraphDepth = 2
	maxGraphDepth     = 6
	defaultNearCap    = 5   // nodes kept per neighborhood layer
	defaultWholeCap   = 200 // nodes kept per whole-graph layer
	defaultOutlineFan = 8   // children listed per outline row
	maxOutlineRows    = 300
	engineMarks       = 5 // how many bottlenecks and keystones from the engine are highlighted
)

// GraphOptions choose the view. The zero value is a depth-2 neighborhood of the first linked task.
type GraphOptions struct {
	Focus string
	Depth int  // neighborhood depth, 1 to 6 (default 2)
	Whole bool // every linked task in topological layers instead of a neighborhood
	// MaxPerLayer collapses a layer's overflow into "+N more" (default 5, or 200 in the whole graph);
	// MaxFan does the same for the outline's children (default 8).
	MaxPerLayer int
	MaxFan      int
	// Insights is the graph engine's report for this snapshot, when there is one: its
	// bottlenecks and impact-depth leaders are marked on the nodes.
	Insights *core.InsightsReport
}

// GraphIndex is the graph of one View, indexed once so every focus, depth and mode is cheap.
// Build it per snapshot; View is safe to call repeatedly.
type GraphIndex struct {
	v        *View
	ids      []string // sorted: node n is ids[n]
	at       map[string]int
	in, out  [][]int // in: blockers of n, out: tasks n blocks, both ascending
	self     []bool
	missing  [][]string
	inCycle  []bool
	cycles   [][]string
	layer    []int // topological layer in the whole graph
	up, down []int // longest unfinished chain ending and starting at n (0: not eligible)
	prevBest []int
	nextBest []int
	chain    []int
	linked   []bool
	comps    int
	isolated int
}

// NewGraphIndex derives the blocking graph of v.
func NewGraphIndex(v *View) *GraphIndex {
	n := len(v.rows)
	g := &GraphIndex{v: v, at: make(map[string]int, n), ids: make([]string, 0, n)}
	for _, r := range v.rows {
		g.ids = append(g.ids, r.ID)
	}
	sort.Strings(g.ids)
	g.ids = slices.Compact(g.ids)
	n = len(g.ids)
	for i, id := range g.ids {
		g.at[id] = i
	}
	g.in, g.out = make([][]int, n), make([][]int, n)
	g.self, g.missing = make([]bool, n), make([][]string, n)
	for i, id := range g.ids {
		for _, b := range v.blkBy[id] {
			bi, ok := g.at[b]
			switch {
			case !ok:
				g.missing[i] = append(g.missing[i], b)
			case bi == i:
				g.self[i] = true
			default:
				g.in[i] = append(g.in[i], bi)
				g.out[bi] = append(g.out[bi], i)
			}
		}
		sort.Strings(g.missing[i])
		g.missing[i] = slices.Compact(g.missing[i])
	}
	for i := range g.ids {
		g.in[i], g.out[i] = sortedUnique(g.in[i]), sortedUnique(g.out[i])
	}
	g.layers()
	g.chains()
	g.components()
	return g
}

func sortedUnique(s []int) []int {
	sort.Ints(s)
	return slices.Compact(s)
}

// layers finds the cycles (strongly connected components of two or more tasks) and gives every
// task its topological layer: one past its deepest blocker, with a cycle sharing one layer.
func (g *GraphIndex) layers() {
	n := len(g.ids)
	index, low := make([]int, n), make([]int, n)
	on := make([]bool, n)
	comp := make([]int, n)
	for i := range index {
		index[i], comp[i] = -1, -1
	}
	var stack []int
	var members [][]int // in emission order: a component comes after everything it blocks
	next := 0
	var visit func(int)
	visit = func(u int) {
		index[u], low[u] = next, next
		next++
		stack, on[u] = append(stack, u), true
		for _, w := range g.out[u] {
			switch {
			case index[w] < 0:
				visit(w)
				low[u] = min(low[u], low[w])
			case on[w]:
				low[u] = min(low[u], index[w])
			}
		}
		if low[u] == index[u] {
			var c []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w], comp[w] = false, len(members)
				c = append(c, w)
				if w == u {
					break
				}
			}
			sort.Ints(c)
			members = append(members, c)
		}
	}
	for u := 0; u < n; u++ {
		if index[u] < 0 {
			visit(u)
		}
	}
	g.inCycle, g.layer = make([]bool, n), make([]int, n)
	compLayer := make([]int, len(members))
	for c := len(members) - 1; c >= 0; c-- { // blockers first
		l := 0
		for _, m := range members[c] {
			for _, p := range g.in[m] {
				if comp[p] != c {
					l = max(l, compLayer[comp[p]]+1)
				}
			}
		}
		compLayer[c] = l
		for _, m := range members[c] {
			g.layer[m] = l
		}
		if len(members[c]) > 1 {
			var ids []string
			for _, m := range members[c] {
				g.inCycle[m] = true
				ids = append(ids, g.ids[m])
			}
			g.cycles = append(g.cycles, ids)
		}
	}
	sort.Slice(g.cycles, func(a, b int) bool { return g.cycles[a][0] < g.cycles[b][0] })
}

// eligible is whether a task can be on a chain: unfinished and not part of a cycle (a cycle has no order).
func (g *GraphIndex) eligible(n int) bool {
	return g.v.rows[g.v.byID[g.ids[n]]].Status != "closed" && !g.inCycle[n]
}

// chains computes, for every eligible task, the longest chain of eligible tasks that ends at it and
// that starts at it, with the best neighbor on each side (the smaller id on a tie), and the longest
// chain of the whole tracker.
func (g *GraphIndex) chains() {
	n := len(g.ids)
	g.up, g.down = make([]int, n), make([]int, n)
	g.prevBest, g.nextBest = make([]int, n), make([]int, n)
	order := make([]int, n)
	for i := range order {
		order[i], g.prevBest[i], g.nextBest[i] = i, -1, -1
	}
	sort.SliceStable(order, func(a, b int) bool { return g.layer[order[a]] < g.layer[order[b]] })
	for _, u := range order {
		if !g.eligible(u) {
			continue
		}
		best := 0
		for _, p := range g.in[u] {
			if g.up[p] > best { // strictly greater: the first, smallest id keeps a tie
				best, g.prevBest[u] = g.up[p], p
			}
		}
		g.up[u] = best + 1
	}
	for i := n - 1; i >= 0; i-- {
		u := order[i]
		if !g.eligible(u) {
			continue
		}
		best := 0
		for _, w := range g.out[u] {
			if g.down[w] > best {
				best, g.nextBest[u] = g.down[w], w
			}
		}
		g.down[u] = best + 1
	}
	end, best := -1, 1
	for u := 0; u < n; u++ {
		if g.up[u] > best {
			end, best = u, g.up[u]
		}
	}
	for u := end; u >= 0; u = g.prevBest[u] {
		g.chain = append(g.chain, u)
	}
	slices.Reverse(g.chain)
}

// chainThrough is the longest eligible chain through u, blocker first; nil when u is not
// eligible or the chain is only u.
func (g *GraphIndex) chainThrough(u int) []int {
	if u < 0 || !g.eligible(u) || g.up[u]+g.down[u] < 3 {
		return nil
	}
	var c []int
	for p := u; p >= 0; p = g.prevBest[p] {
		c = append(c, p)
	}
	slices.Reverse(c)
	for w := g.nextBest[u]; w >= 0; w = g.nextBest[w] {
		c = append(c, w)
	}
	return c
}

// components counts the groups of linked tasks and the tasks with no link at all.
func (g *GraphIndex) components() {
	n := len(g.ids)
	g.linked = make([]bool, n)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for u := 0; u < n; u++ {
		g.linked[u] = len(g.in[u])+len(g.out[u]) > 0 || g.self[u] || len(g.missing[u]) > 0
		for _, w := range g.out[u] {
			parent[find(u)] = find(w)
		}
	}
	roots := map[int]bool{}
	for u := 0; u < n; u++ {
		if g.linked[u] {
			roots[find(u)] = true
		} else {
			g.isolated++
		}
	}
	g.comps = len(roots)
}

// View lays out the graph for o.
func (g *GraphIndex) View(o GraphOptions) core.GraphView {
	if o.Depth <= 0 {
		o.Depth = defaultGraphDepth
	}
	o.Depth = min(o.Depth, maxGraphDepth)
	if o.MaxFan <= 0 {
		o.MaxFan = defaultOutlineFan
	}
	if o.MaxPerLayer <= 0 {
		o.MaxPerLayer = defaultNearCap
		if o.Whole {
			o.MaxPerLayer = defaultWholeCap
		}
	}
	gv := core.GraphView{Header: core.Header{Domain: core.DomainTasks, Title: "Graph", State: core.Connected, Source: core.SourceLive},
		Mode: core.GraphNeighborhood, Total: len(g.ids), Components: g.comps, Isolated: g.isolated}
	for _, c := range g.cycles {
		gv.Cycles = append(gv.Cycles, slices.Clone(c))
	}
	for i, s := range g.self {
		if s {
			gv.SelfDeps = append(gv.SelfDeps, g.ids[i])
		}
	}
	marks := g.engineMarks(o.Insights)
	if o.Whole {
		gv.Mode = core.GraphWhole
		g.whole(&gv, o, marks)
		return gv
	}
	focus, ok := g.at[o.Focus]
	if o.Focus == "" {
		focus, ok = g.defaultFocus()
	}
	if !ok {
		gv.Reason = "no task has a blocking link"
		if o.Focus != "" {
			gv.Reason = "task " + core.Sanitize(o.Focus) + " is not in this tracker"
		}
		return gv
	}
	gv.Focus, gv.Depth = g.ids[focus], o.Depth
	g.neighborhood(&gv, focus, o, marks)
	return gv
}

func (g *GraphIndex) defaultFocus() (int, bool) {
	if len(g.chain) > 0 {
		return g.chain[0], true
	}
	for u := range g.ids {
		if g.linked[u] {
			return u, true
		}
	}
	return 0, false
}

type nodeMarks struct{ bottleneck, keystone map[int]bool }

func (g *GraphIndex) engineMarks(r *core.InsightsReport) nodeMarks {
	m := nodeMarks{map[int]bool{}, map[int]bool{}}
	if r == nil {
		return m
	}
	for _, b := range r.Bottlenecks[:min(len(r.Bottlenecks), engineMarks)] {
		if i, ok := g.at[b.ID]; ok && b.Value > 0 {
			m.bottleneck[i] = true
		}
	}
	for _, k := range r.CriticalPath[:min(len(r.CriticalPath), engineMarks)] {
		if i, ok := g.at[k.ID]; ok && k.Value > 0 {
			m.keystone[i] = true
		}
	}
	return m
}

func (g *GraphIndex) node(u, layer, order int, onChain map[int]bool, m nodeMarks) core.GraphNode {
	r := g.v.rows[g.v.byID[g.ids[u]]]
	return core.GraphNode{ID: r.ID, Title: r.Title, Status: r.Status, Priority: r.Priority, Layer: layer, Order: order,
		BlockedBy: r.BlockedBy, Blocks: r.Blocks, Ready: r.Ready, Blocked: r.Blocked,
		OnChain: onChain[u], InCycle: g.inCycle[u], Bottleneck: m.bottleneck[u], Keystone: m.keystone[u],
		SelfDep: g.self[u], Missing: slices.Clone(g.missing[u])}
}

func set(s []int) map[int]bool {
	m := make(map[int]bool, len(s))
	for _, x := range s {
		m[x] = true
	}
	return m
}

func (g *GraphIndex) ids2(s []int) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	for i, u := range s {
		out[i] = g.ids[u]
	}
	return out
}

// barycenter orders layer by the mean position of each node's neighbors in nbr (positions in pos),
// then by id. A node with no placed neighbor goes last. The comparison is exact integer arithmetic.
func (g *GraphIndex) barycenter(layer []int, nbr func(int) []int, pos map[int]int) {
	sum, cnt := map[int]int{}, map[int]int{}
	for _, u := range layer {
		for _, p := range nbr(u) {
			if at, ok := pos[p]; ok {
				sum[u] += at
				cnt[u]++
			}
		}
	}
	sort.SliceStable(layer, func(a, b int) bool {
		x, y := layer[a], layer[b]
		switch {
		case cnt[x] == 0 || cnt[y] == 0:
			if cnt[x] != cnt[y] {
				return cnt[x] > 0
			}
		case sum[x]*cnt[y] != sum[y]*cnt[x]:
			return sum[x]*cnt[y] < sum[y]*cnt[x]
		}
		return x < y
	})
}

func (g *GraphIndex) whole(gv *core.GraphView, o GraphOptions, m nodeMarks) {
	chain := set(g.chain)
	gv.Chain = g.ids2(g.chain)
	var byLayer [][]int
	for u := range g.ids {
		if !g.linked[u] {
			continue
		}
		for len(byLayer) <= g.layer[u] {
			byLayer = append(byLayer, nil)
		}
		byLayer[g.layer[u]] = append(byLayer[g.layer[u]], u)
	}
	pos := map[int]int{}
	shown := map[int]bool{}
	for l, nodes := range byLayer {
		if l > 0 {
			g.barycenter(nodes, func(u int) []int { return g.in[u] }, pos)
		}
		var kept []int
		for _, u := range nodes {
			if len(kept) < o.MaxPerLayer || chain[u] || g.inCycle[u] {
				kept = append(kept, u)
			}
		}
		for i, u := range kept {
			pos[u], shown[u] = i, true
			gv.Nodes = append(gv.Nodes, g.node(u, l, i, chain, m))
		}
		gv.Hidden += len(nodes) - len(kept)
		gv.Layers = append(gv.Layers, core.GraphLayer{Layer: l, Nodes: g.ids2(kept), More: len(nodes) - len(kept)})
	}
	for u := range g.ids {
		for _, w := range g.out[u] {
			if shown[u] && shown[w] {
				gv.Edges = append(gv.Edges, core.GraphEdge{From: g.ids[u], To: g.ids[w], Drawn: g.layer[w]-g.layer[u] == 1})
			}
		}
	}
}

// less ranks tasks when a layer has to be cut: the chain through the focus, then unfinished work,
// then priority, then the id.
func (g *GraphIndex) less(onChain map[int]bool) func(a, b int) bool {
	return func(a, b int) bool {
		if onChain[a] != onChain[b] {
			return onChain[a]
		}
		ra, rb := g.v.rows[g.v.byID[g.ids[a]]], g.v.rows[g.v.byID[g.ids[b]]]
		if (ra.Status == "closed") != (rb.Status == "closed") {
			return rb.Status == "closed"
		}
		if ra.Priority != rb.Priority {
			return ra.Priority < rb.Priority
		}
		return a < b
	}
}

// side walks outward from the focus along adj, a layer per step, keeping at most cap nodes per layer.
// A task found on the first side is not listed again on the other (a cycle).
func (g *GraphIndex) side(focus int, adj [][]int, depth, capN int, seen map[int]bool, less func(a, b int) bool) (layers [][]int, more []int) {
	frontier := []int{focus}
	for d := 1; d <= depth; d++ {
		var cands []int
		for _, u := range frontier {
			for _, w := range adj[u] {
				if !seen[w] {
					seen[w] = true
					cands = append(cands, w)
				}
			}
		}
		if len(cands) == 0 {
			break
		}
		sort.Slice(cands, func(a, b int) bool { return less(cands[a], cands[b]) })
		kept := cands[:min(len(cands), capN)]
		layers, more = append(layers, kept), append(more, len(cands)-len(kept))
		frontier = kept
	}
	return layers, more
}

func (g *GraphIndex) neighborhood(gv *core.GraphView, focus int, o GraphOptions, m nodeMarks) {
	through := g.chainThrough(focus)
	onChain := set(through)
	gv.Chain = g.ids2(through)
	less := g.less(onChain)
	seen := map[int]bool{focus: true}
	upL, upMore := g.side(focus, g.in, o.Depth, o.MaxPerLayer, seen, less)
	downL, downMore := g.side(focus, g.out, o.Depth, o.MaxPerLayer, seen, less)

	pos := map[int]int{focus: 0}
	layerOf := map[int]int{focus: 0}
	place := func(layers [][]int, sign int, nbr func(int) []int) [][]int {
		for d, nodes := range layers {
			g.barycenter(nodes, nbr, pos)
			for i, u := range nodes {
				pos[u], layerOf[u] = i, sign*(d+1)
			}
		}
		return layers
	}
	// a blocker's neighbors toward the focus are the tasks it blocks, a dependent's are its blockers
	place(upL, -1, func(u int) []int { return g.out[u] })
	place(downL, 1, func(u int) []int { return g.in[u] })

	add := func(nodes []int, layer, more int) {
		for i, u := range nodes {
			n := g.node(u, layer, i, onChain, m)
			n.Focus = u == focus
			gv.Nodes = append(gv.Nodes, n)
		}
		gv.Layers = append(gv.Layers, core.GraphLayer{Layer: layer, Nodes: g.ids2(nodes), More: more})
		gv.Hidden += more
	}
	for d := len(upL) - 1; d >= 0; d-- {
		add(upL[d], -(d + 1), upMore[d])
	}
	add([]int{focus}, 0, 0)
	for d, nodes := range downL {
		add(nodes, d+1, downMore[d])
	}
	for _, e := range gv.Nodes {
		u := g.at[e.ID]
		for _, w := range g.out[u] {
			if lw, ok := layerOf[w]; ok {
				drawn := lw-layerOf[u] == 1
				gv.Edges = append(gv.Edges, core.GraphEdge{From: e.ID, To: g.ids[w], Drawn: drawn})
				if !drawn {
					gv.CrossLinks++
				}
			}
		}
	}
	sort.Slice(gv.Edges, func(a, b int) bool {
		ea, eb := gv.Edges[a], gv.Edges[b]
		if ea.From != eb.From {
			return ea.From < eb.From
		}
		return ea.To < eb.To
	})
	r := g.v.rows[g.v.byID[g.ids[focus]]]
	if r.Status != "closed" {
		gv.OpenBlockers = r.BlockedBy
		for _, w := range g.out[focus] {
			if g.v.rows[g.v.byID[g.ids[w]]].Status != "closed" && g.v.openBlockers(g.ids[w]) == 1 {
				gv.Unblocks++
			}
		}
	}
	g.outline(gv, focus, o, less)
}

// outline lists the blockers nested above the focus and the dependents nested below it, each task
// once (a second sighting is a Repeat row that is not expanded), fan-out capped per parent and the
// whole list capped, with the overflow counted.
func (g *GraphIndex) outline(gv *core.GraphView, focus int, o GraphOptions, less func(a, b int) bool) {
	emit := func(side string, adj [][]int) {
		seen := map[int]bool{focus: true}
		listed := 0
		var rows []core.OutlineRow
		var walk func(u, depth int)
		walk = func(u, depth int) {
			if depth > o.Depth || len(rows) >= maxOutlineRows {
				return
			}
			kids := slices.Clone(adj[u])
			sort.Slice(kids, func(a, b int) bool { return less(kids[a], kids[b]) })
			for i, w := range kids {
				if len(rows) >= maxOutlineRows {
					return
				}
				if i == o.MaxFan {
					rows = append(rows, core.OutlineRow{Side: side, Depth: depth, More: len(kids) - i})
					return
				}
				if seen[w] {
					rows = append(rows, core.OutlineRow{ID: g.ids[w], Side: side, Depth: depth, Repeat: true})
					continue
				}
				seen[w] = true
				listed++
				rows = append(rows, core.OutlineRow{ID: g.ids[w], Side: side, Depth: depth})
				walk(w, depth+1)
			}
		}
		walk(focus, 1)
		if len(rows) >= maxOutlineRows { // say how much the cap hid
			if left := g.reach(focus, adj, o.Depth) - listed; left > 0 {
				rows = append(rows, core.OutlineRow{Side: side, Depth: 1, More: left})
			}
		}
		if side == core.OutlineUp {
			gv.Outline = append(gv.Outline, rows...)
			gv.Outline = append(gv.Outline, core.OutlineRow{ID: g.ids[focus], Side: core.OutlineFocus})
			return
		}
		gv.Outline = append(gv.Outline, rows...)
	}
	emit(core.OutlineUp, g.in)
	emit(core.OutlineDown, g.out)
}

// reach counts the tasks within depth steps of from along adj, not counting from.
func (g *GraphIndex) reach(from int, adj [][]int, depth int) int {
	seen := map[int]bool{from: true}
	frontier := []int{from}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []int
		for _, u := range frontier {
			for _, w := range adj[u] {
				if !seen[w] {
					seen[w] = true
					next = append(next, w)
				}
			}
		}
		frontier = next
	}
	return len(seen) - 1
}

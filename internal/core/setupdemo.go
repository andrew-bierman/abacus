package core

import (
	"slices"

	"github.com/andrew-bierman/abacus/internal/brand"
)

// Demo setup: the first-run steps of invented data, so a client can show its setup sheets with no tracker.
// Nothing here reads or plans against anything real, and nothing built from it can be applied (demo data
// cannot be changed): every step is planned as a demo plan, marked with a DemoDigest.

type demoStep struct {
	info StepInfo
	argv []string
}

// demoSetup is one of each state a client draws: a step that can be applied, one that waits on another
// (blocked) and a second one that can be applied.
func demoSetup() []demoStep {
	ws := Guide{Domain: DomainTasks, Step: StepWorkspace, Message: "No tracker here", Action: "create your fleet workspace"}
	wsArgv := []string{"bd", "init", "--quiet"}
	q := Guide{Domain: DomainQueues, Step: StepQueues, Message: "Queues need a supervisor config", Action: "set up"}
	ac := Guide{Domain: DomainLeases, Step: StepActor, Message: "Leases need an actor name", Action: "choose one"}
	acArgv := []string{brand.Name, "config", "set", "actor", "demo-user"}
	return []demoStep{
		{StepInfo{Step: StepWorkspace, Domain: DomainTasks, State: StepNeeded, Message: ws.CLILine(),
			Lines: []string{"create your fleet workspace (demo data: nothing is created)"}, Command: DisplayArgv(wsArgv)}, wsArgv},
		{StepInfo{Step: StepQueues, Domain: DomainQueues, State: StepBlocked,
			Message: q.Message + ": create your fleet workspace first (Tasks tab, press i, or run `" + brand.Name + " setup`)"}, nil},
		{StepInfo{Step: StepActor, Domain: DomainLeases, State: StepNeeded, Message: ac.CLILine(),
			Lines: []string{"set the actor name leases are held under: demo-user", "change it any time with: " + brand.Name + " config set actor NAME"}, Command: DisplayArgv(acArgv)}, acArgv},
	}
}

// DemoSetupSteps is `setup` of demo data: what Setup.Steps would report.
func DemoSetupSteps() []StepInfo {
	var out []StepInfo
	for _, d := range demoSetup() {
		out = append(out, d.info)
	}
	return out
}

// DemoSetupStepPlans is DemoSetupSteps as reviewed, for SetupDigest (wrap it in DemoDigest).
func DemoSetupStepPlans() []SetupStepPlan {
	var out []SetupStepPlan
	for _, d := range demoSetup() {
		sp := SetupStepPlan{Step: d.info.Step, State: d.info.State, Message: d.info.Message}
		if d.info.State == StepNeeded {
			sp.Argv = d.argv
		}
		out = append(out, sp)
	}
	return out
}

// DemoSetupPlan is the plan of one demo step and its digest (not yet demo-marked: wrap it in DemoDigest, as
// every demo digest is); false when the step has nothing to do now (not offered, as for a real one).
func DemoSetupPlan(step StepID) (lines, argv []string, digest string, ok bool) {
	for _, d := range demoSetup() {
		if d.info.Step == step && d.info.State == StepNeeded {
			return slices.Clone(d.info.Lines), slices.Clone(d.argv), PlanDigest("setup", string(step), []string{""}, [][]string{d.argv}, nil), true
		}
	}
	return nil, nil, "", false
}

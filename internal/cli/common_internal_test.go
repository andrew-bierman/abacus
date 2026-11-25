package cli

import (
	"testing"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The audit behind the demo guard: in demo mode no record can be written, so the gate refuses to run.
func TestDemoAuditBackstopBlocksTheGate(t *testing.T) {
	if err := auditFor(Deps{Out: &counter{demo: true}}).Record(core.AuditEntry{}); err == nil {
		t.Error("a demo run could record an audit entry")
	}
	if err := auditFor(Deps{Out: &counter{}, Audit: demoAudit{}}).Record(core.AuditEntry{}); err == nil {
		t.Error("an explicit audit is still used outside demo mode")
	}
}

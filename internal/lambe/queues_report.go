package lambe

import "github.com/andrew-bierman/abacus/internal/core"

// QueuesReportOf is the typed Queues report of a view already read, for a front-end that holds
// the view (the TUI keeps it between polls) and wants the same report the CLI prints.
func QueuesReportOf(v View, demo bool) core.QueuesReport { return queuesReport(v, demo) }

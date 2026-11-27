package agents

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// HostVitals is the machine's own load, independent of which agent is running. It is a read of
// the host, not of a session, so a front-end can show it even when no agent is alive.
type HostVitals struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	// LoadKnown is false where the platform has no load average. The zeroes are then absent
	// numbers rather than a measurement of an idle host.
	LoadKnown bool `json:"load_known"`
}

// Known reports whether the sample carries the memory reading every host has. A zero value is
// a monitor that has not read the host yet, not a machine with no memory.
func (v HostVitals) Known() bool { return v.MemTotal > 0 }

// NativeHost reads the host through gopsutil, which already carries the per-platform detail
// this package would otherwise reimplement. CPU is the delta since the previous call, so the
// first sample after process start covers a very short window; later samples follow the
// monitor's refresh interval. Load is optional because not every platform exposes it.
type NativeHost struct{}

// Vitals reads one host sample.
func (NativeHost) Vitals(ctx context.Context) (HostVitals, error) {
	pcts, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return HostVitals{}, err
	}
	v := HostVitals{}
	if len(pcts) > 0 {
		v.CPUPercent = clampPercent(pcts[0])
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return HostVitals{}, err
	}
	v.MemUsed, v.MemTotal = vm.Used, vm.Total
	if avg, err := load.AvgWithContext(ctx); err == nil && avg != nil {
		v.Load1, v.Load5, v.Load15, v.LoadKnown = avg.Load1, avg.Load5, avg.Load15, true
	}
	return v, nil
}

func clampPercent(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

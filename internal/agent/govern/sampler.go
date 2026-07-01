package govern

import "github.com/shirou/gopsutil/v4/cpu"

// CPUSampler reports system-wide CPU utilization percent.
type CPUSampler struct{}

func (CPUSampler) CPUPercent() float64 {
	pcts, err := cpu.Percent(0, false) // non-blocking: % since last call, aggregate
	if err != nil || len(pcts) == 0 {
		return 0
	}
	return pcts[0]
}

package procsrc

import (
	"fmt"

	"github.com/prometheus/procfs"
)

// SumTaskSchedstat walks /proc/<pid>/task/*/schedstat under hostProc and
// returns the sum of each thread's runqueue wait time, in nanoseconds. This
// captures per-vCPU-thread contention that QEMU's main-thread schedstat
// alone would miss.
func SumTaskSchedstat(hostProc string, pid int) (uint64, error) {
	fs, err := procfs.NewFS(hostProc)
	if err != nil {
		return 0, fmt.Errorf("procsrc: open procfs at %s: %w", hostProc, err)
	}
	threads, err := fs.AllThreads(pid)
	if err != nil {
		return 0, fmt.Errorf("procsrc: list threads for pid %d: %w", pid, err)
	}
	var total uint64
	for _, th := range threads {
		st, err := th.Schedstat()
		if err != nil {
			// A thread can exit between listing and reading; skip it rather
			// than fail the whole sum.
			continue
		}
		total += st.WaitingNanoseconds
	}
	return total, nil
}

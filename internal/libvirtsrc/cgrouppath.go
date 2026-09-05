package libvirtsrc

import (
	"fmt"

	"github.com/prometheus/procfs"
)

// ResolveCgroupPath returns the process's cgroup v2 path (relative to the
// cgroupfs mount root), read from /proc/<pid>/cgroup. Reading the kernel's
// own record avoids reimplementing systemd's unit-name escaping rules, and
// works whether libvirt placed the domain under machine.slice (systemd
// cgroup driver) or /machine (cgroupfs driver, used when libvirt can't
// reach systemd over D-Bus).
func ResolveCgroupPath(hostProc string, pid int) (string, error) {
	fs, err := procfs.NewFS(hostProc)
	if err != nil {
		return "", fmt.Errorf("libvirtsrc: open procfs at %s: %w", hostProc, err)
	}
	proc, err := fs.Proc(pid)
	if err != nil {
		return "", fmt.Errorf("libvirtsrc: proc %d not found under %s: %w", pid, hostProc, err)
	}
	cgroups, err := proc.Cgroups()
	if err != nil {
		return "", fmt.Errorf("libvirtsrc: read cgroup for pid %d: %w", pid, err)
	}
	for _, cg := range cgroups {
		// cgroup v2 unified hierarchy: hierarchy ID 0, no named controllers.
		if cg.HierarchyID == 0 && len(cg.Controllers) == 0 {
			return cg.Path, nil
		}
	}
	return "", fmt.Errorf("libvirtsrc: pid %d has no cgroup v2 unified hierarchy entry", pid)
}

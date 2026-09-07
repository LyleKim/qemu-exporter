package libvirtsrc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/prometheus/procfs"
)

// ResolveCgroupPath returns the cgroup v2 path (relative to the cgroupfs
// mount root) that a domain's resource metrics should be read from.
//
// It starts from the kernel's own record in /proc/<pid>/cgroup: reading that
// avoids reimplementing systemd's unit-name escaping and works whether
// libvirt used the systemd cgroup driver (domain under machine.slice) or the
// cgroupfs driver (/machine, used when libvirt can't reach systemd over
// D-Bus). The path is Clean'd first: when the exporter runs in its own
// cgroup namespace, the kernel prefixes a cgroup outside that namespace's
// root (QEMU's /machine/... always is) with "/../" sequences, and Clean
// collapses those back to the host-absolute path the cgroupfs bind mount
// expects.
//
// It then climbs out of libvirt's per-thread-class child cgroups. libvirt
// puts a domain's emulator / vCPU / IO threads in "threaded" sub-cgroups
// (emulator/, vcpuN/, iothreadN/) of the domain cgroup, and the QEMU
// process's group leader lands in emulator/. Those threaded children hold
// only part of the CPU accounting and carry no memory controller at all
// (memory.current does not exist there), so metrics must be read at the
// domain cgroup above them. The climb reads each level's cgroup.type and
// stops at the first one that is not "threaded" -- that is "domain" (systemd
// driver) or "domain threaded" (cgroupfs driver), i.e. the domain cgroup.
// An unreadable cgroup.type (cgroup v1, missing mount) also stops the climb,
// leaving the kernel-reported path unchanged.
func ResolveCgroupPath(hostProc, hostSysFsCgroup string, pid int) (string, error) {
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
			// When the exporter runs in its own cgroup namespace, the kernel
			// renders a target cgroup that lies outside that namespace's root
			// with a leading "/../" sequence (cgroup_namespaces(7)). QEMU's
			// /machine/... cgroup is by definition outside kubepods, so it
			// always gets this treatment. filepath.Clean collapses the "/../"
			// prefix back to the host-absolute path, which is what we need:
			// hostSysFsCgroup is a bind mount of the host's real cgroup root,
			// not the namespace-relative view.
			hostPath := filepath.Clean(cg.Path)
			return climbToDomainCgroup(hostSysFsCgroup, hostPath), nil
		}
	}
	return "", fmt.Errorf("libvirtsrc: pid %d has no cgroup v2 unified hierarchy entry", pid)
}

// climbToDomainCgroup walks up from one of libvirt's "threaded" child
// cgroups (emulator/, vcpuN/, iothreadN/) to the domain cgroup that holds
// the full CPU and memory accounting. Each level's cgroup.type is checked;
// anything other than "threaded" -- unreadable, "domain", "domain threaded"
// -- ends the climb and that path is returned.
func climbToDomainCgroup(hostSysFsCgroup, path string) string {
	for path != "/" && path != "." && path != "" {
		t, err := os.ReadFile(filepath.Join(hostSysFsCgroup, path, "cgroup.type"))
		if err != nil || strings.TrimSpace(string(t)) != "threaded" {
			return path
		}
		path = filepath.Dir(path)
	}
	return path
}

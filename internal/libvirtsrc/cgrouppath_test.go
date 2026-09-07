package libvirtsrc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeProcCgroup writes a fake /proc/<pid>/cgroup under hostProc.
func writeProcCgroup(t *testing.T, hostProc string, pid int, line string) {
	t.Helper()
	dir := filepath.Join(hostProc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCgroupType writes a cgroup.type file at hostSysFsCgroup/<cgPath>.
func writeCgroupType(t *testing.T, hostSysFsCgroup, cgPath, typ string) {
	t.Helper()
	dir := filepath.Join(hostSysFsCgroup, cgPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.type"), []byte(typ+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveCgroupPath(t *testing.T) {
	hostProc := t.TempDir()
	writeProcCgroup(t, hostProc, 999,
		"0::/machine.slice/machine-qemu\\x2d1\\x2dinstance\\x2d00000001.scope/libvirt\n")

	got, err := ResolveCgroupPath(hostProc, t.TempDir(), 999)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	want := "/machine.slice/machine-qemu\\x2d1\\x2dinstance\\x2d00000001.scope/libvirt"
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveCgroupPath_CgroupfsFallback(t *testing.T) {
	// When libvirt can't reach systemd over D-Bus, it falls back to the
	// cgroupfs driver and registers domains under /machine instead of
	// /machine.slice. The v2-unified-hierarchy line format is identical
	// either way, so no special-casing is needed here.
	hostProc := t.TempDir()
	writeProcCgroup(t, hostProc, 1000, "0::/machine/qemu-1-instance-00000001\n")

	got, err := ResolveCgroupPath(hostProc, t.TempDir(), 1000)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	if want := "/machine/qemu-1-instance-00000001"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveCgroupPath_ClimbsOutOfThreadedChild(t *testing.T) {
	// libvirt's cgroupfs driver puts the QEMU group leader in the threaded
	// emulator/ child of the domain cgroup. cpu.stat there is partial and
	// memory.current does not exist, so ResolveCgroupPath must climb to the
	// domain cgroup (cgroup.type "domain threaded").
	hostProc := t.TempDir()
	hostCg := t.TempDir()
	const domain = "/machine/qemu-1-instance-00000001.libvirt-qemu"

	writeProcCgroup(t, hostProc, 72472, "0::"+domain+"/emulator\n")
	writeCgroupType(t, hostCg, domain+"/emulator", "threaded")
	writeCgroupType(t, hostCg, domain, "domain threaded")

	got, err := ResolveCgroupPath(hostProc, hostCg, 72472)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	if got != domain {
		t.Errorf("path = %q, want %q", got, domain)
	}
}

func TestResolveCgroupPath_StripsCgroupNamespacePrefix(t *testing.T) {
	// The exporter runs in its own cgroup namespace, so /host/proc/<pid>/cgroup
	// renders QEMU's out-of-namespace /machine cgroup with a leading "/../"
	// run. That prefix must be collapsed, then the threaded climb applied.
	hostProc := t.TempDir()
	hostCg := t.TempDir()
	const domain = "/machine/qemu-1-instance-00000001.libvirt-qemu"

	writeProcCgroup(t, hostProc, 72472, "0::/../../../../machine/qemu-1-instance-00000001.libvirt-qemu/emulator\n")
	writeCgroupType(t, hostCg, domain+"/emulator", "threaded")
	writeCgroupType(t, hostCg, domain, "domain threaded")

	got, err := ResolveCgroupPath(hostProc, hostCg, 72472)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	if got != domain {
		t.Errorf("path = %q, want %q", got, domain)
	}
}

func TestResolveCgroupPath_NoClimbForDomainLeaf(t *testing.T) {
	// systemd driver: the process sits directly in the domain scope, whose
	// cgroup.type is "domain". No climb.
	hostProc := t.TempDir()
	hostCg := t.TempDir()
	const scope = "/machine.slice/machine-qemu-1-instance-00000001.scope"

	writeProcCgroup(t, hostProc, 555, "0::"+scope+"\n")
	writeCgroupType(t, hostCg, scope, "domain")

	got, err := ResolveCgroupPath(hostProc, hostCg, 555)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	if got != scope {
		t.Errorf("path = %q, want %q", got, scope)
	}
}

package libvirtsrc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestResolveCgroupPath(t *testing.T) {
	hostProc := t.TempDir()
	pid := 999
	dir := filepath.Join(hostProc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "0::/machine.slice/machine-qemu\\x2d1\\x2dinstance\\x2d00000001.scope/libvirt\n"
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCgroupPath(hostProc, pid)
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
	pid := 1000
	dir := filepath.Join(hostProc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("0::/machine/qemu-1-instance-00000001\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCgroupPath(hostProc, pid)
	if err != nil {
		t.Fatalf("ResolveCgroupPath: %v", err)
	}
	if want := "/machine/qemu-1-instance-00000001"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

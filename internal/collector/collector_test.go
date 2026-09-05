package collector

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/LyleKim/qemu-exporter/internal/libvirtsrc"
)

type fakeSource struct {
	domains []libvirtsrc.Domain
	err     error
}

func (f *fakeSource) Domains() ([]libvirtsrc.Domain, error) {
	return f.domains, f.err
}

// buildFakeHost writes a fake cgroup + proc tree under a temp directory and
// returns the (hostSysFsCgroup, hostProc) roots to point the collector at.
func buildFakeHost(t *testing.T, cgroupPath string, pid int) (hostSysFsCgroup, hostProc string) {
	t.Helper()
	hostSysFsCgroup = t.TempDir()
	hostProc = t.TempDir()

	cgroupDir := filepath.Join(hostSysFsCgroup, cgroupPath)
	if err := os.MkdirAll(cgroupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec 5000000\n",
		"memory.current": "104857600\n",
		"cpu.pressure":   "some avg10=0.00 avg60=0.00 avg300=0.00 total=2000000\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1000000\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(cgroupDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	taskDir := filepath.Join(hostProc, strconv.Itoa(pid), "task", strconv.Itoa(pid))
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "schedstat"), []byte("111 3000000000 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hostSysFsCgroup, hostProc
}

func TestCollector_Collect(t *testing.T) {
	pid := 4242
	cgroupPath := "/machine.slice/machine-qemu-1-instance-00000001.scope"
	hostSysFsCgroup, hostProc := buildFakeHost(t, cgroupPath, pid)

	source := &fakeSource{domains: []libvirtsrc.Domain{{
		UUID:       "e1e5c1c0-1234-5678-9abc-def012345678",
		Name:       "instance-00000001",
		Flavor:     "m1.large",
		ProjectID:  "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		PID:        pid,
		CgroupPath: cgroupPath,
	}}}

	c := New(source, hostSysFsCgroup, hostProc, "node1")

	expected := `
# HELP openstack_vm_cpu_usage_seconds_total Cumulative CPU time consumed by the VM's QEMU process (cgroup cpu.stat usage_usec).
# TYPE openstack_vm_cpu_usage_seconds_total counter
openstack_vm_cpu_usage_seconds_total{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 5
# HELP openstack_vm_memory_usage_bytes Current memory usage of the VM's QEMU process (cgroup memory.current).
# TYPE openstack_vm_memory_usage_bytes gauge
openstack_vm_memory_usage_bytes{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 1.048576e+08
# HELP openstack_vm_cpu_pressure_stall_seconds_total Cumulative CPU pressure stall time for the VM's cgroup (cpu.pressure 'some').
# TYPE openstack_vm_cpu_pressure_stall_seconds_total counter
openstack_vm_cpu_pressure_stall_seconds_total{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479",type="some"} 2
# HELP openstack_vm_sched_runqueue_wait_seconds_total Cumulative time the VM's vCPU threads spent waiting on a runqueue (schedstat field 2, summed over threads).
# TYPE openstack_vm_sched_runqueue_wait_seconds_total counter
openstack_vm_sched_runqueue_wait_seconds_total{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 3
# HELP qemu_exporter_scrape_errors_total Cumulative count of per-domain or connection failures encountered while scraping.
# TYPE qemu_exporter_scrape_errors_total counter
qemu_exporter_scrape_errors_total 0
# HELP qemu_exporter_vms_discovered Number of VMs successfully scraped in the most recent collection.
# TYPE qemu_exporter_vms_discovered gauge
qemu_exporter_vms_discovered 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected)); err != nil {
		t.Fatalf("unexpected collecting result:\n%s", err)
	}
}

func TestCollector_Collect_PSIUnsupported(t *testing.T) {
	pid := 4242
	cgroupPath := "/machine.slice/machine-qemu-1-instance-00000001.scope"
	hostSysFsCgroup, hostProc := buildFakeHost(t, cgroupPath, pid)
	// Drop cpu.pressure: simulates a kernel/cgroup hierarchy without PSI.
	if err := os.Remove(filepath.Join(hostSysFsCgroup, cgroupPath, "cpu.pressure")); err != nil {
		t.Fatal(err)
	}

	source := &fakeSource{domains: []libvirtsrc.Domain{{
		UUID:       "e1e5c1c0-1234-5678-9abc-def012345678",
		Name:       "instance-00000001",
		Flavor:     "m1.large",
		ProjectID:  "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		PID:        pid,
		CgroupPath: cgroupPath,
	}}}
	c := New(source, hostSysFsCgroup, hostProc, "node1")

	// The three non-pressure VM metrics must still be emitted with correct
	// values; the pressure metric must be absent; and this must NOT count as a
	// scrape error.
	expected := `
# HELP openstack_vm_cpu_usage_seconds_total Cumulative CPU time consumed by the VM's QEMU process (cgroup cpu.stat usage_usec).
# TYPE openstack_vm_cpu_usage_seconds_total counter
openstack_vm_cpu_usage_seconds_total{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 5
# HELP openstack_vm_memory_usage_bytes Current memory usage of the VM's QEMU process (cgroup memory.current).
# TYPE openstack_vm_memory_usage_bytes gauge
openstack_vm_memory_usage_bytes{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 1.048576e+08
# HELP openstack_vm_sched_runqueue_wait_seconds_total Cumulative time the VM's vCPU threads spent waiting on a runqueue (schedstat field 2, summed over threads).
# TYPE openstack_vm_sched_runqueue_wait_seconds_total counter
openstack_vm_sched_runqueue_wait_seconds_total{flavor="m1.large",instance_name="instance-00000001",instance_uuid="e1e5c1c0-1234-5678-9abc-def012345678",node="node1",project_id="f47ac10b-58cc-4372-a567-0e02b2c3d479"} 3
# HELP qemu_exporter_scrape_errors_total Cumulative count of per-domain or connection failures encountered while scraping.
# TYPE qemu_exporter_scrape_errors_total counter
qemu_exporter_scrape_errors_total 0
# HELP qemu_exporter_vms_discovered Number of VMs successfully scraped in the most recent collection.
# TYPE qemu_exporter_vms_discovered gauge
qemu_exporter_vms_discovered 1
`
	err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"openstack_vm_cpu_usage_seconds_total",
		"openstack_vm_memory_usage_bytes",
		"openstack_vm_sched_runqueue_wait_seconds_total",
		"openstack_vm_cpu_pressure_stall_seconds_total",
		"qemu_exporter_scrape_errors_total",
		"qemu_exporter_vms_discovered")
	if err != nil {
		t.Fatalf("unexpected collecting result:\n%s", err)
	}
}

func TestCollector_Collect_SkipsDomainOnParserFailure(t *testing.T) {
	// No fake cgroup/proc files written -- every parser call will fail, so
	// the VM should be skipped and counted as an error, not crash the scrape.
	hostSysFsCgroup, hostProc := t.TempDir(), t.TempDir()
	source := &fakeSource{domains: []libvirtsrc.Domain{{
		UUID: "uuid-1", Name: "broken-vm", PID: 1, CgroupPath: "/nonexistent",
	}}}
	c := New(source, hostSysFsCgroup, hostProc, "node1")

	expected := `
# HELP qemu_exporter_scrape_errors_total Cumulative count of per-domain or connection failures encountered while scraping.
# TYPE qemu_exporter_scrape_errors_total counter
qemu_exporter_scrape_errors_total 1
# HELP qemu_exporter_vms_discovered Number of VMs successfully scraped in the most recent collection.
# TYPE qemu_exporter_vms_discovered gauge
qemu_exporter_vms_discovered 0
`
	err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"qemu_exporter_scrape_errors_total", "qemu_exporter_vms_discovered")
	if err != nil {
		t.Fatalf("unexpected collecting result:\n%s", err)
	}
}

func TestCollector_Collect_DomainSourceError(t *testing.T) {
	source := &fakeSource{err: errors.New("libvirt connection refused")}
	c := New(source, t.TempDir(), t.TempDir(), "node1")

	expected := `
# HELP qemu_exporter_scrape_errors_total Cumulative count of per-domain or connection failures encountered while scraping.
# TYPE qemu_exporter_scrape_errors_total counter
qemu_exporter_scrape_errors_total 1
# HELP qemu_exporter_vms_discovered Number of VMs successfully scraped in the most recent collection.
# TYPE qemu_exporter_vms_discovered gauge
qemu_exporter_vms_discovered 0
`
	err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"qemu_exporter_scrape_errors_total", "qemu_exporter_vms_discovered")
	if err != nil {
		t.Fatalf("unexpected collecting result:\n%s", err)
	}
}

# qemu-exporter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a read-only Prometheus exporter that reads QEMU VM resource/contention metrics from host cgroupfs/procfs and correlates them with Nova identity via libvirt, so they show up outside `kubepods.slice`'s blind spot.

**Architecture:** Three-layer pipeline — `libvirtsrc` (identify running VMs + their PID/cgroup path via libvirt + pidfile + `/proc/<pid>/cgroup`) → `cgroupsrc`/`procsrc` (parse the 4 metric source files) → `collector` (assemble into a `prometheus.Collector`). `main.go` wires the three layers and serves `/metrics`.

**Tech Stack:** Go 1.22+, `github.com/prometheus/client_golang`, `github.com/prometheus/procfs`, `github.com/digitalocean/go-libvirt` (pure Go, no cgo), `log/slog`.

**Spec:** `docs/superpowers/specs/2026-09-05-qemu-exporter-design.md`

## Global Constraints

- No eBPF. All metrics come from cgroupfs/procfs files.
- No cgo. Every build in this plan uses `CGO_ENABLED=0`.
- Read-only. No file writes to any host path; libvirt connections use only the read-only socket (`libvirt-sock-ro`).
- Never infer a cgroup path from a systemd scope-name convention. Always resolve it by reading `/proc/<pid>/cgroup`.
- Never call Nova's REST API or Keystone. All identity metadata comes from the libvirt domain XML.
- Do not modify any openstack-helm chart, libvirt config, or Nova config.
- Module path: `github.com/LyleKim/qemu-exporter`.
- A single VM's collection failure must never fail the whole scrape (skip + log + count, keep going).
- All raw microsecond/nanosecond values are converted to seconds before being exposed.
- Build exactly the metrics in the table below. Nothing else.

| Metric | Type | Source |
|---|---|---|
| `openstack_vm_cpu_usage_seconds_total` | Counter | cgroup `cpu.stat` `usage_usec` |
| `openstack_vm_memory_usage_bytes` | Gauge | cgroup `memory.current` |
| `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}` | Counter | cgroup `cpu.pressure` `some` line `total` |
| `openstack_vm_sched_runqueue_wait_seconds_total` | Counter | `/proc/<pid>/task/*/schedstat` field 2, summed |
| `qemu_exporter_scrape_errors_total` | Counter | internal |
| `qemu_exporter_vms_discovered` | Gauge | internal |

Labels on the 4 VM metrics: `node`, `instance_uuid`, `instance_name`, `flavor`, `project_id`.

**Every code block below has already been compiled and its tests run successfully during planning** (Go 1.25.1 on darwin/arm64, with fake `/proc` and cgroup trees standing in for the real host). Copy it as-is unless a step says otherwise.

---

### Task 1: Project scaffolding

**Files:**
- Create: `go.mod`, `go.sum`
- Create: `cmd/qemu-exporter/main.go` (placeholder, replaced in Task 11)

**Interfaces:**
- Produces: module path `github.com/LyleKim/qemu-exporter`, so every later task's imports look like `github.com/LyleKim/qemu-exporter/internal/<pkg>`.

- [ ] **Step 1: Initialize the module**

```bash
go mod init github.com/LyleKim/qemu-exporter
```

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/prometheus/client_golang/prometheus
go get github.com/prometheus/client_golang/prometheus/promhttp
go get github.com/prometheus/client_golang/prometheus/testutil
go get github.com/prometheus/procfs
go get github.com/digitalocean/go-libvirt
```

- [ ] **Step 3: Write a placeholder main.go**

```go
package main

import "fmt"

func main() {
	fmt.Println("qemu-exporter (scaffolding)")
}
```

- [ ] **Step 4: Verify it builds**

Run: `CGO_ENABLED=0 go build ./...`
Expected: exits 0, no output.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum cmd/qemu-exporter/main.go
git commit -m "chore: scaffold qemu-exporter Go module"
```

---

### Task 2: cgroupsrc — cpu.stat parser

**Files:**
- Create: `internal/cgroupsrc/cpustat.go`
- Create: `internal/cgroupsrc/cpustat_test.go`
- Create: `internal/cgroupsrc/testdata/cpu.stat`

**Interfaces:**
- Produces: `cgroupsrc.ParseCPUStat(r io.Reader) (uint64, error)`, `cgroupsrc.ReadCPUStat(cgroupDir string) (uint64, error)` — both consumed by `collector` in Task 10.

- [ ] **Step 1: Write the failing tests**

`internal/cgroupsrc/testdata/cpu.stat`:
```
usage_usec 5000000
user_usec 4000000
system_usec 1000000
nr_periods 0
nr_throttled 0
throttled_usec 0
```

`internal/cgroupsrc/cpustat_test.go`:
```go
package cgroupsrc

import (
	"os"
	"strings"
	"testing"
)

func TestParseCPUStat(t *testing.T) {
	f, err := os.Open("testdata/cpu.stat")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ParseCPUStat(f)
	if err != nil {
		t.Fatalf("ParseCPUStat: %v", err)
	}
	if want := uint64(5000000); got != want {
		t.Errorf("usage_usec = %d, want %d", got, want)
	}
}

func TestParseCPUStat_MissingField(t *testing.T) {
	_, err := ParseCPUStat(strings.NewReader("user_usec 100\nsystem_usec 200\n"))
	if err == nil {
		t.Fatal("expected error for missing usage_usec, got nil")
	}
}

func TestReadCPUStat(t *testing.T) {
	got, err := ReadCPUStat("testdata")
	if err != nil {
		t.Fatalf("ReadCPUStat: %v", err)
	}
	if want := uint64(5000000); got != want {
		t.Errorf("usage_usec = %d, want %d", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cgroupsrc/... -run TestParseCPUStat -v`
Expected: FAIL (`ParseCPUStat` undefined).

- [ ] **Step 3: Implement**

`internal/cgroupsrc/cpustat.go`:
```go
package cgroupsrc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseCPUStat reads a cgroup v2 cpu.stat file and returns usage_usec, the
// cumulative CPU time consumed by the cgroup in microseconds.
func ParseCPUStat(r io.Reader) (usageUsec uint64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || fields[0] != "usage_usec" {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cgroupsrc: parse usage_usec %q: %w", fields[1], err)
		}
		return v, nil
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("cgroupsrc: read cpu.stat: %w", err)
	}
	return 0, fmt.Errorf("cgroupsrc: usage_usec not found in cpu.stat")
}

// ReadCPUStat opens <cgroupDir>/cpu.stat and parses it.
func ReadCPUStat(cgroupDir string) (uint64, error) {
	p := filepath.Join(cgroupDir, "cpu.stat")
	f, err := os.Open(p)
	if err != nil {
		return 0, fmt.Errorf("cgroupsrc: open %s: %w", p, err)
	}
	defer f.Close()
	return ParseCPUStat(f)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cgroupsrc/... -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/cgroupsrc/cpustat.go internal/cgroupsrc/cpustat_test.go internal/cgroupsrc/testdata/cpu.stat
git commit -m "feat: add cgroup cpu.stat parser"
```

---

### Task 3: cgroupsrc — memory.current parser

**Files:**
- Create: `internal/cgroupsrc/memcurrent.go`
- Create: `internal/cgroupsrc/memcurrent_test.go`
- Create: `internal/cgroupsrc/testdata/memory.current`

**Interfaces:**
- Produces: `cgroupsrc.ParseMemoryCurrent(r io.Reader) (uint64, error)`, `cgroupsrc.ReadMemoryCurrent(cgroupDir string) (uint64, error)` — consumed by `collector` in Task 10.

- [ ] **Step 1: Write the failing test**

`internal/cgroupsrc/testdata/memory.current`:
```
104857600
```

`internal/cgroupsrc/memcurrent_test.go`:
```go
package cgroupsrc

import (
	"os"
	"testing"
)

func TestParseMemoryCurrent(t *testing.T) {
	f, err := os.Open("testdata/memory.current")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := ParseMemoryCurrent(f)
	if err != nil {
		t.Fatalf("ParseMemoryCurrent: %v", err)
	}
	if want := uint64(104857600); got != want {
		t.Errorf("memory.current = %d, want %d", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cgroupsrc/... -run TestParseMemoryCurrent -v`
Expected: FAIL (`ParseMemoryCurrent` undefined).

- [ ] **Step 3: Implement**

`internal/cgroupsrc/memcurrent.go`:
```go
package cgroupsrc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseMemoryCurrent reads a cgroup v2 memory.current file and returns the
// current memory usage in bytes.
func ParseMemoryCurrent(r io.Reader) (uint64, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return 0, fmt.Errorf("cgroupsrc: read memory.current: %w", err)
		}
		return 0, fmt.Errorf("cgroupsrc: memory.current is empty")
	}
	line := strings.TrimSpace(sc.Text())
	v, err := strconv.ParseUint(line, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cgroupsrc: parse memory.current %q: %w", line, err)
	}
	return v, nil
}

// ReadMemoryCurrent opens <cgroupDir>/memory.current and parses it.
func ReadMemoryCurrent(cgroupDir string) (uint64, error) {
	p := filepath.Join(cgroupDir, "memory.current")
	f, err := os.Open(p)
	if err != nil {
		return 0, fmt.Errorf("cgroupsrc: open %s: %w", p, err)
	}
	defer f.Close()
	return ParseMemoryCurrent(f)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cgroupsrc/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cgroupsrc/memcurrent.go internal/cgroupsrc/memcurrent_test.go internal/cgroupsrc/testdata/memory.current
git commit -m "feat: add cgroup memory.current parser"
```

---

### Task 4: cgroupsrc — cpu.pressure (PSI) parser

**Files:**
- Create: `internal/cgroupsrc/psi.go`
- Create: `internal/cgroupsrc/psi_test.go`
- Create: `internal/cgroupsrc/testdata/cpu.pressure`

**Interfaces:**
- Produces: `cgroupsrc.ParsePSISome(r io.Reader) (uint64, error)`, `cgroupsrc.ReadCPUPressureSome(cgroupDir string) (uint64, error)`, `cgroupsrc.ErrPSIUnsupported` (sentinel error) — consumed by `collector` in Task 10.

- [ ] **Step 1: Write the failing tests**

`internal/cgroupsrc/testdata/cpu.pressure`:
```
some avg10=0.00 avg60=0.00 avg300=0.00 total=2000000
full avg10=0.00 avg60=0.00 avg300=0.00 total=1000000
```

`internal/cgroupsrc/psi_test.go`:
```go
package cgroupsrc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePSISome(t *testing.T) {
	f, err := os.Open("testdata/cpu.pressure")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := ParsePSISome(f)
	if err != nil {
		t.Fatalf("ParsePSISome: %v", err)
	}
	if want := uint64(2000000); got != want {
		t.Errorf("total = %d, want %d", got, want)
	}
}

func TestReadCPUPressureSome_Missing(t *testing.T) {
	_, err := ReadCPUPressureSome(filepath.Join(t.TempDir(), "nonexistent"))
	if !errors.Is(err, ErrPSIUnsupported) {
		t.Errorf("err = %v, want ErrPSIUnsupported", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cgroupsrc/... -run TestParsePSISome -v`
Expected: FAIL (`ParsePSISome` undefined).

- [ ] **Step 3: Implement**

`internal/cgroupsrc/psi.go`:
```go
package cgroupsrc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrPSIUnsupported indicates the pressure file itself is missing, meaning
// the kernel or cgroup hierarchy does not expose PSI -- not that the read
// failed for some other, transient reason.
var ErrPSIUnsupported = errors.New("cgroupsrc: cpu.pressure not available (kernel/cgroup lacks PSI support)")

// ParsePSISome reads a cgroup v2 cpu.pressure file and returns the "some"
// line's cumulative stall time in microseconds (the total= field).
func ParsePSISome(r io.Reader) (uint64, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "some" {
			continue
		}
		for _, f := range fields[1:] {
			k, v, ok := strings.Cut(f, "=")
			if !ok || k != "total" {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("cgroupsrc: parse cpu.pressure total %q: %w", v, err)
			}
			return n, nil
		}
		return 0, fmt.Errorf("cgroupsrc: cpu.pressure 'some' line missing total field")
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("cgroupsrc: read cpu.pressure: %w", err)
	}
	return 0, fmt.Errorf("cgroupsrc: cpu.pressure has no 'some' line")
}

// ReadCPUPressureSome opens <cgroupDir>/cpu.pressure and parses it. A
// missing file is reported as ErrPSIUnsupported so callers can distinguish
// "this environment lacks PSI" from other I/O failures.
func ReadCPUPressureSome(cgroupDir string) (uint64, error) {
	p := filepath.Join(cgroupDir, "cpu.pressure")
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrPSIUnsupported
		}
		return 0, fmt.Errorf("cgroupsrc: open %s: %w", p, err)
	}
	defer f.Close()
	return ParsePSISome(f)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cgroupsrc/... -v`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add internal/cgroupsrc/psi.go internal/cgroupsrc/psi_test.go internal/cgroupsrc/testdata/cpu.pressure
git commit -m "feat: add cgroup cpu.pressure (PSI) parser"
```

---

### Task 5: procsrc — runqueue wait time (schedstat)

Uses `prometheus/procfs`'s `FS.AllThreads` + `Proc.Schedstat()` directly (already parses `/proc/<pid>/task/*/schedstat`) instead of a hand-written parser — the library already covers this file format exactly.

**Files:**
- Create: `internal/procsrc/schedstat.go`
- Create: `internal/procsrc/schedstat_test.go`

**Interfaces:**
- Produces: `procsrc.SumTaskSchedstat(hostProc string, pid int) (uint64, error)` — consumed by `collector` in Task 10.

- [ ] **Step 1: Write the failing test**

`internal/procsrc/schedstat_test.go`:
```go
package procsrc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeFakeTask(t *testing.T, hostProc string, pid, tid int, waitNanos uint64) {
	t.Helper()
	dir := filepath.Join(hostProc, strconv.Itoa(pid), "task", strconv.Itoa(tid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := "111 " + strconv.FormatUint(waitNanos, 10) + " 3\n"
	if err := os.WriteFile(filepath.Join(dir, "schedstat"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSumTaskSchedstat(t *testing.T) {
	hostProc := t.TempDir()
	pid := 100
	writeFakeTask(t, hostProc, pid, 100, 1_000_000_000)
	writeFakeTask(t, hostProc, pid, 101, 2_000_000_000)

	got, err := SumTaskSchedstat(hostProc, pid)
	if err != nil {
		t.Fatalf("SumTaskSchedstat: %v", err)
	}
	if want := uint64(3_000_000_000); got != want {
		t.Errorf("sum = %d, want %d", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/procsrc/... -v`
Expected: FAIL (`SumTaskSchedstat` undefined).

- [ ] **Step 3: Implement**

`internal/procsrc/schedstat.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/procsrc/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/procsrc/schedstat.go internal/procsrc/schedstat_test.go
git commit -m "feat: add procfs-based schedstat runqueue-wait summing"
```

---

### Task 6: libvirtsrc — cgroup path resolution

Uses `prometheus/procfs`'s `Proc.Cgroups()` (already parses `/proc/<pid>/cgroup`) instead of a hand-written parser.

**Files:**
- Create: `internal/libvirtsrc/cgrouppath.go`
- Create: `internal/libvirtsrc/cgrouppath_test.go`

**Interfaces:**
- Produces: `libvirtsrc.ResolveCgroupPath(hostProc string, pid int) (string, error)` — consumed by `libvirtsrc.Cache` in Task 9.

- [ ] **Step 1: Write the failing tests**

`internal/libvirtsrc/cgrouppath_test.go`:
```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/libvirtsrc/... -v`
Expected: FAIL (`ResolveCgroupPath` undefined).

- [ ] **Step 3: Implement**

`internal/libvirtsrc/cgrouppath.go`:
```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/libvirtsrc/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libvirtsrc/cgrouppath.go internal/libvirtsrc/cgrouppath_test.go
git commit -m "feat: resolve QEMU cgroup path via procfs Cgroups()"
```

---

### Task 7: libvirtsrc — Nova metadata parsing + PID resolution

**Files:**
- Create: `internal/libvirtsrc/domain.go`
- Create: `internal/libvirtsrc/domain_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `parseNovaMetadata(domainXMLBytes []byte) (flavor, projectID string, err error)`, `resolveQEMUPID(libvirtRunDir, domainName string) (int, error)` (both unexported, used by `Cache` in Task 9 — same package).

- [ ] **Step 1: Write the failing tests**

`internal/libvirtsrc/domain_test.go`:
```go
package libvirtsrc

import (
	"os"
	"path/filepath"
	"testing"
)

const testDomainXML = `<domain type='qemu'>
  <name>instance-00000001</name>
  <uuid>e1e5c1c0-1234-5678-9abc-def012345678</uuid>
  <metadata>
    <nova:instance xmlns:nova="http://openstack.org/xmlns/libvirt/nova/1.1">
      <nova:flavor name="m1.large">
        <nova:memory>8192</nova:memory>
      </nova:flavor>
      <nova:owner>
        <nova:project uuid="f47ac10b-58cc-4372-a567-0e02b2c3d479">demo</nova:project>
      </nova:owner>
    </nova:instance>
  </metadata>
</domain>`

func TestParseNovaMetadata(t *testing.T) {
	flavor, projectID, err := parseNovaMetadata([]byte(testDomainXML))
	if err != nil {
		t.Fatalf("parseNovaMetadata: %v", err)
	}
	if flavor != "m1.large" {
		t.Errorf("flavor = %q, want m1.large", flavor)
	}
	if projectID != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Errorf("projectID = %q, want f47ac10b-58cc-4372-a567-0e02b2c3d479", projectID)
	}
}

func TestParseNovaMetadata_NoNovaMetadata(t *testing.T) {
	// Not every libvirt domain is Nova-managed; a domain XML with no
	// <nova:instance> block should parse cleanly with empty results, not error.
	const plain = `<domain type='qemu'><name>manual-vm</name><uuid>x</uuid></domain>`
	flavor, projectID, err := parseNovaMetadata([]byte(plain))
	if err != nil {
		t.Fatalf("parseNovaMetadata: %v", err)
	}
	if flavor != "" || projectID != "" {
		t.Errorf("flavor=%q projectID=%q, want both empty", flavor, projectID)
	}
}

func TestResolveQEMUPID(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "qemu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "qemu", "instance-00000001.pid"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveQEMUPID(runDir, "instance-00000001")
	if err != nil {
		t.Fatalf("resolveQEMUPID: %v", err)
	}
	if want := 4242; got != want {
		t.Errorf("pid = %d, want %d", got, want)
	}
}

func TestResolveQEMUPID_Missing(t *testing.T) {
	_, err := resolveQEMUPID(t.TempDir(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing pidfile, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/libvirtsrc/... -run TestParseNovaMetadata -v`
Expected: FAIL (`parseNovaMetadata` undefined).

- [ ] **Step 3: Implement**

`internal/libvirtsrc/domain.go`:
```go
package libvirtsrc

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// domainXML mirrors only the subset of libvirt's domain XML this project
// reads. Go's encoding/xml matches these tags against the namespaced
// <nova:...> elements by local name alone (verified: it does not require
// the tag to repeat the nova namespace URI).
type domainXML struct {
	Metadata domainMetadataXML `xml:"metadata"`
}

type domainMetadataXML struct {
	Instance *novaInstanceXML `xml:"instance"`
}

type novaInstanceXML struct {
	Flavor novaFlavorXML `xml:"flavor"`
	Owner  novaOwnerXML  `xml:"owner"`
}

type novaFlavorXML struct {
	Name string `xml:"name,attr"`
}

type novaOwnerXML struct {
	Project novaProjectXML `xml:"project"`
}

type novaProjectXML struct {
	UUID string `xml:"uuid,attr"`
}

// parseNovaMetadata extracts the flavor name and project UUID from a
// libvirt domain XML document's <nova:instance> metadata block. Name and
// UUID aren't parsed here because ConnectListAllDomains already provides
// them more cheaply (see cache.go). It returns empty strings, not an
// error, when the domain carries no such block -- not every libvirt
// domain is Nova-managed.
func parseNovaMetadata(domainXMLBytes []byte) (flavor, projectID string, err error) {
	var dx domainXML
	if err := xml.Unmarshal(domainXMLBytes, &dx); err != nil {
		return "", "", fmt.Errorf("libvirtsrc: parse domain XML: %w", err)
	}
	inst := dx.Metadata.Instance
	if inst == nil {
		return "", "", nil
	}
	return inst.Flavor.Name, inst.Owner.Project.UUID, nil
}

// resolveQEMUPID reads libvirt's pidfile for a running qemu domain.
// libvirt's public API has no "get PID" RPC (it deliberately abstracts the
// hypervisor), so this reads the same pidfile libvirt itself writes at
// <libvirtRunDir>/qemu/<domain-name>.pid -- the standard technique used by
// other libvirt-based exporters.
func resolveQEMUPID(libvirtRunDir, domainName string) (int, error) {
	p := filepath.Join(libvirtRunDir, "qemu", domainName+".pid")
	data, err := os.ReadFile(p)
	if err != nil {
		return 0, fmt.Errorf("libvirtsrc: read pidfile %s: %w", p, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("libvirtsrc: parse pidfile %s: %w", p, err)
	}
	return pid, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/libvirtsrc/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libvirtsrc/domain.go internal/libvirtsrc/domain_test.go
git commit -m "feat: parse Nova metadata from domain XML, resolve QEMU PID via pidfile"
```

---

### Task 8: libvirtsrc — libvirt connection

**Files:**
- Create: `internal/libvirtsrc/client.go`
- Create: `internal/libvirtsrc/client_test.go`

**Interfaces:**
- Produces: `libvirtsrc.Connect(sockPath string) (*libvirt.Libvirt, error)` — consumed by `main.go` in Task 11.

- [ ] **Step 1: Write the failing test**

`internal/libvirtsrc/client_test.go`:
```go
package libvirtsrc

import "testing"

func TestBuildURI(t *testing.T) {
	got := buildURI("/var/run/libvirt/libvirt-sock-ro").String()
	want := "qemu+unix:///system?socket=%2Fvar%2Frun%2Flibvirt%2Flibvirt-sock-ro"
	if got != want {
		t.Errorf("buildURI = %q, want %q", got, want)
	}
}
```

Note: `Connect` itself (the part that actually dials libvirtd) is not unit-tested here -- it needs a live libvirtd, which this environment doesn't have. It gets its first real exercise on the AWS host in a later phase. `buildURI` is the pure, testable part.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/libvirtsrc/... -run TestBuildURI -v`
Expected: FAIL (`buildURI` undefined).

- [ ] **Step 3: Implement**

`internal/libvirtsrc/client.go`:
```go
package libvirtsrc

import (
	"fmt"
	"net/url"

	"github.com/digitalocean/go-libvirt"
)

// buildURI constructs the libvirt connection URI for a specific unix
// socket path. Using qemu+unix with an explicit socket= query parameter
// lets us force the read-only socket rather than relying on
// qemu:///system's default socket selection.
func buildURI(sockPath string) *url.URL {
	u := &url.URL{Scheme: "qemu+unix", Path: "/system"}
	q := u.Query()
	q.Set("socket", sockPath)
	u.RawQuery = q.Encode()
	return u
}

// Connect dials libvirtd over the given read-only unix socket.
func Connect(sockPath string) (*libvirt.Libvirt, error) {
	l, err := libvirt.ConnectToURI(buildURI(sockPath))
	if err != nil {
		return nil, fmt.Errorf("libvirtsrc: connect to libvirtd at %s: %w", sockPath, err)
	}
	return l, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/libvirtsrc/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libvirtsrc/client.go internal/libvirtsrc/client_test.go
git commit -m "feat: add libvirt read-only unix socket connection"
```

---

### Task 9: libvirtsrc — DomainSource + Cache

This is the core identification-layer task: it turns a cheap "list active domains" RPC plus a cache into "the same VM's expensive metadata lookup happens once per VM lifecycle, not once per scrape."

**Files:**
- Create: `internal/libvirtsrc/source.go`
- Create: `internal/libvirtsrc/cache.go`
- Create: `internal/libvirtsrc/cache_test.go`

**Interfaces:**
- Consumes: `parseNovaMetadata`, `resolveQEMUPID` (Task 7), `ResolveCgroupPath` (Task 6).
- Produces: `type Domain struct{ UUID, Name, Flavor, ProjectID string; PID int; CgroupPath string }`, `type DomainSource interface{ Domains() ([]Domain, error) }`, `libvirtsrc.NewCache(connect func() (*libvirt.Libvirt, error), hostProc, libvirtRunDir string) *Cache` (implements `DomainSource`) — all consumed by `collector` (Task 10) and `main.go` (Task 11).

- [ ] **Step 1: Write source.go (no test needed -- pure type declarations)**

`internal/libvirtsrc/source.go`:
```go
package libvirtsrc

// Domain is the identity and location information this project correlates
// cgroup/procfs metrics against. Flavor and ProjectID are empty when the
// domain carries no Nova metadata -- not every libvirt domain is
// Nova-managed.
type Domain struct {
	UUID       string
	Name       string
	Flavor     string
	ProjectID  string
	PID        int
	CgroupPath string
}

// DomainSource lists the QEMU domains this exporter should scrape. Cache is
// the production implementation; tests substitute a fake so collector
// tests don't need a live libvirtd.
type DomainSource interface {
	Domains() ([]Domain, error)
}
```

- [ ] **Step 2: Write the failing tests for Cache**

`internal/libvirtsrc/cache_test.go`:
```go
package libvirtsrc

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/digitalocean/go-libvirt"
)

type fakeRPC struct {
	active       []libvirt.Domain
	xmlByName    map[string]string
	xmlErrByName map[string]error
	xmlDescCalls map[string]int
	listErr      error
}

func (f *fakeRPC) ConnectListAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.active, uint32(len(f.active)), nil
}

func (f *fakeRPC) DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
	if f.xmlDescCalls == nil {
		f.xmlDescCalls = map[string]int{}
	}
	f.xmlDescCalls[dom.Name]++
	if err, ok := f.xmlErrByName[dom.Name]; ok {
		return "", err
	}
	return f.xmlByName[dom.Name], nil
}

func writeFakeHost(t *testing.T, pid int, cgroupPath, domainName string) (hostProc, libvirtRunDir string) {
	t.Helper()
	hostProc = t.TempDir()
	libvirtRunDir = t.TempDir()

	pidDir := filepath.Join(hostProc, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte("0::"+cgroupPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(libvirtRunDir, "qemu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libvirtRunDir, "qemu", domainName+".pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hostProc, libvirtRunDir
}

func TestCache_Domains_BuildsAndCaches(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/machine-qemu-1-instance-00000001.scope", "instance-00000001")

	dom := libvirt.Domain{Name: "instance-00000001", ID: 1}
	copy(dom.UUID[:], []byte{0xe1, 0xe5, 0xc1, 0xc0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78})

	fake := &fakeRPC{
		active:    []libvirt.Domain{dom},
		xmlByName: map[string]string{"instance-00000001": testDomainXML},
	}

	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d domains, want 1", len(got))
	}
	want := Domain{
		UUID:       "e1e5c1c0-1234-5678-9abc-def012345678",
		Name:       "instance-00000001",
		Flavor:     "m1.large",
		ProjectID:  "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		PID:        pid,
		CgroupPath: "/machine.slice/machine-qemu-1-instance-00000001.scope",
	}
	if got[0] != want {
		t.Errorf("Domains()[0] = %+v, want %+v", got[0], want)
	}

	// Second call must not re-fetch XML for the same domain (cache hit).
	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains (2nd call): %v", err)
	}
	if n := fake.xmlDescCalls["instance-00000001"]; n != 1 {
		t.Errorf("DomainGetXMLDesc called %d times, want 1 (cache should avoid the second call)", n)
	}
}

func TestCache_Domains_EvictsStoppedDomain(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/x.scope", "instance-00000001")
	dom := libvirt.Domain{Name: "instance-00000001"}
	fake := &fakeRPC{active: []libvirt.Domain{dom}, xmlByName: map[string]string{"instance-00000001": testDomainXML}}
	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(c.domains) != 1 {
		t.Fatalf("cache has %d entries after first call, want 1", len(c.domains))
	}

	fake.active = nil // domain stopped
	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains (after stop): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Domains() after stop = %d entries, want 0", len(got))
	}
	if len(c.domains) != 0 {
		t.Errorf("cache has %d entries after eviction, want 0", len(c.domains))
	}
}

func TestCache_Domains_SkipsDomainOnBuildFailure(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/x.scope", "instance-00000001")
	good := libvirt.Domain{Name: "instance-00000001"}
	copy(good.UUID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	bad := libvirt.Domain{Name: "instance-broken"}
	copy(bad.UUID[:], []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	fake := &fakeRPC{
		active:       []libvirt.Domain{good, bad},
		xmlByName:    map[string]string{"instance-00000001": testDomainXML},
		xmlErrByName: map[string]error{"instance-broken": errors.New("boom")},
	}
	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(got) != 1 || got[0].Name != "instance-00000001" {
		t.Errorf("Domains() = %+v, want only instance-00000001", got)
	}
}

func TestCache_Domains_ReconnectsAfterFailure(t *testing.T) {
	hostProc, libvirtRunDir := t.TempDir(), t.TempDir()
	failing := &fakeRPC{listErr: errors.New("connection refused")}
	attempts := 0
	c := &Cache{
		connect: func() (rpcClient, error) {
			attempts++
			if attempts == 1 {
				return failing, nil
			}
			return &fakeRPC{}, nil
		},
		hostProc:      hostProc,
		libvirtRunDir: libvirtRunDir,
		domains:       make(map[string]Domain),
	}

	if _, err := c.Domains(); err == nil {
		t.Fatal("expected error on first call")
	}
	if c.rpc != nil {
		t.Error("rpc should be nil after a failed call, to force reconnect")
	}
	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains (after reconnect): %v", err)
	}
	if attempts != 2 {
		t.Errorf("connect called %d times, want 2", attempts)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/libvirtsrc/... -run TestCache -v`
Expected: FAIL (`Cache` undefined).

- [ ] **Step 4: Implement**

`internal/libvirtsrc/cache.go`:
```go
package libvirtsrc

import (
	"fmt"
	"sync"

	"github.com/digitalocean/go-libvirt"
)

// rpcClient is the subset of *libvirt.Libvirt this package calls, factored
// out so Cache's diff/evict logic can be tested without a live libvirtd.
type rpcClient interface {
	ConnectListAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error)
	DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error)
}

// Cache is a DomainSource backed by libvirt. Domains() re-derives the
// active domain set from a cheap ConnectListAllDomains call every time
// it's called, and only pays for the expensive GetXMLDesc + pidfile +
// cgroup lookups on a cache miss -- once per VM lifecycle, not once per
// scrape. There is no separate lifecycle-event subscription or timer: this
// per-scrape diff is the entire invalidation mechanism, and it evicts a
// stopped VM within one scrape cycle.
type Cache struct {
	connect       func() (rpcClient, error)
	hostProc      string
	libvirtRunDir string

	mu      sync.Mutex
	rpc     rpcClient
	domains map[string]Domain
}

// NewCache returns a Cache that connects to libvirtd lazily (and
// reconnects on failure) using connect. libvirtRunDir is the directory
// containing libvirt's per-domain qemu pidfiles (qemu/<name>.pid) --
// normally the directory holding LIBVIRT_SOCK.
func NewCache(connect func() (*libvirt.Libvirt, error), hostProc, libvirtRunDir string) *Cache {
	return &Cache{
		connect:       func() (rpcClient, error) { return connect() },
		hostProc:      hostProc,
		libvirtRunDir: libvirtRunDir,
		domains:       make(map[string]Domain),
	}
}

// Domains implements DomainSource.
func (c *Cache) Domains() ([]Domain, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.rpc == nil {
		rpc, err := c.connect()
		if err != nil {
			return nil, fmt.Errorf("libvirtsrc: connect: %w", err)
		}
		c.rpc = rpc
	}

	active, _, err := c.rpc.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		c.rpc = nil // force reconnect on next call
		return nil, fmt.Errorf("libvirtsrc: list active domains: %w", err)
	}

	seen := make(map[string]bool, len(active))
	result := make([]Domain, 0, len(active))
	for _, ld := range active {
		uuid := formatUUID(ld.UUID)
		seen[uuid] = true

		d, cached := c.domains[uuid]
		if !cached {
			built, err := c.buildDomain(ld, uuid)
			if err != nil {
				// Skip this VM for this scrape; a transient failure (e.g.
				// the pidfile not written yet) shouldn't block every other
				// domain.
				continue
			}
			d = built
			c.domains[uuid] = d
		}
		result = append(result, d)
	}

	for uuid := range c.domains {
		if !seen[uuid] {
			delete(c.domains, uuid)
		}
	}

	return result, nil
}

func (c *Cache) buildDomain(ld libvirt.Domain, uuid string) (Domain, error) {
	xmlDesc, err := c.rpc.DomainGetXMLDesc(ld, 0)
	if err != nil {
		return Domain{}, fmt.Errorf("get XML for domain %s: %w", ld.Name, err)
	}
	flavor, projectID, err := parseNovaMetadata([]byte(xmlDesc))
	if err != nil {
		return Domain{}, fmt.Errorf("parse metadata for domain %s: %w", ld.Name, err)
	}
	pid, err := resolveQEMUPID(c.libvirtRunDir, ld.Name)
	if err != nil {
		return Domain{}, fmt.Errorf("resolve PID for domain %s: %w", ld.Name, err)
	}
	cgroupPath, err := ResolveCgroupPath(c.hostProc, pid)
	if err != nil {
		return Domain{}, fmt.Errorf("resolve cgroup path for domain %s (pid %d): %w", ld.Name, pid, err)
	}
	return Domain{
		UUID:       uuid,
		Name:       ld.Name,
		Flavor:     flavor,
		ProjectID:  projectID,
		PID:        pid,
		CgroupPath: cgroupPath,
	}, nil
}

func formatUUID(b libvirt.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/libvirtsrc/... -v`
Expected: PASS (all tests in the package, including Tasks 6-8's).

- [ ] **Step 6: Commit**

```bash
git add internal/libvirtsrc/source.go internal/libvirtsrc/cache.go internal/libvirtsrc/cache_test.go
git commit -m "feat: add DomainSource cache with active-set diff invalidation"
```

---

### Task 10: collector — prometheus.Collector

**Files:**
- Create: `internal/collector/collector.go`
- Create: `internal/collector/collector_test.go`

**Interfaces:**
- Consumes: `libvirtsrc.DomainSource`, `libvirtsrc.Domain` (Task 9); `cgroupsrc.ReadCPUStat`, `cgroupsrc.ReadMemoryCurrent`, `cgroupsrc.ReadCPUPressureSome` (Tasks 2-4); `procsrc.SumTaskSchedstat` (Task 5).
- Produces: `collector.New(source libvirtsrc.DomainSource, hostSysFsCgroup, hostProc, node string) *Collector` (implements `prometheus.Collector`) — consumed by `main.go` in Task 11.

This test is Stage 2 of the local verification strategy: it exercises the whole pipeline (fake `DomainSource` + fake `/proc`/cgroup trees under `t.TempDir()`) and checks the real Prometheus exposition text via `testutil.CollectAndCompare`, with no libvirtd, Docker, or AWS involved.

- [ ] **Step 1: Write the failing tests**

`internal/collector/collector_test.go`:
```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/collector/... -v`
Expected: FAIL (`New`/`Collector` undefined).

- [ ] **Step 3: Implement**

`internal/collector/collector.go`:
```go
package collector

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LyleKim/qemu-exporter/internal/cgroupsrc"
	"github.com/LyleKim/qemu-exporter/internal/libvirtsrc"
	"github.com/LyleKim/qemu-exporter/internal/procsrc"
)

var labelNames = []string{"node", "instance_uuid", "instance_name", "flavor", "project_id"}

var (
	cpuUsageDesc = prometheus.NewDesc(
		"openstack_vm_cpu_usage_seconds_total",
		"Cumulative CPU time consumed by the VM's QEMU process (cgroup cpu.stat usage_usec).",
		labelNames, nil,
	)
	memoryUsageDesc = prometheus.NewDesc(
		"openstack_vm_memory_usage_bytes",
		"Current memory usage of the VM's QEMU process (cgroup memory.current).",
		labelNames, nil,
	)
	cpuPressureDesc = prometheus.NewDesc(
		"openstack_vm_cpu_pressure_stall_seconds_total",
		"Cumulative CPU pressure stall time for the VM's cgroup (cpu.pressure 'some').",
		labelNames, prometheus.Labels{"type": "some"},
	)
	runqueueWaitDesc = prometheus.NewDesc(
		"openstack_vm_sched_runqueue_wait_seconds_total",
		"Cumulative time the VM's vCPU threads spent waiting on a runqueue (schedstat field 2, summed over threads).",
		labelNames, nil,
	)
	scrapeErrorsDesc = prometheus.NewDesc(
		"qemu_exporter_scrape_errors_total",
		"Cumulative count of per-domain or connection failures encountered while scraping.",
		nil, nil,
	)
	vmsDiscoveredDesc = prometheus.NewDesc(
		"qemu_exporter_vms_discovered",
		"Number of VMs successfully scraped in the most recent collection.",
		nil, nil,
	)
)

// Collector implements prometheus.Collector for QEMU VM metrics.
type Collector struct {
	source          libvirtsrc.DomainSource
	hostSysFsCgroup string
	hostProc        string
	node            string

	errorsTotal atomic.Uint64
}

// New returns a Collector. source lists the VMs to scrape; hostSysFsCgroup
// and hostProc are the (possibly container-remapped) roots of the cgroupfs
// and procfs mounts; node is the value for the "node" label.
func New(source libvirtsrc.DomainSource, hostSysFsCgroup, hostProc, node string) *Collector {
	return &Collector{
		source:          source,
		hostSysFsCgroup: hostSysFsCgroup,
		hostProc:        hostProc,
		node:            node,
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cpuUsageDesc
	ch <- memoryUsageDesc
	ch <- cpuPressureDesc
	ch <- runqueueWaitDesc
	ch <- scrapeErrorsDesc
	ch <- vmsDiscoveredDesc
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	domains, err := c.source.Domains()
	if err != nil {
		slog.Warn("qemu-exporter: list domains failed", "error", err)
		c.errorsTotal.Add(1)
		ch <- prometheus.MustNewConstMetric(scrapeErrorsDesc, prometheus.CounterValue, float64(c.errorsTotal.Load()))
		ch <- prometheus.MustNewConstMetric(vmsDiscoveredDesc, prometheus.GaugeValue, 0)
		return
	}

	discovered := 0
	for _, d := range domains {
		if err := c.collectDomain(ch, d); err != nil {
			slog.Warn("qemu-exporter: collect domain failed", "domain", d.Name, "uuid", d.UUID, "error", err)
			c.errorsTotal.Add(1)
			continue
		}
		discovered++
	}

	ch <- prometheus.MustNewConstMetric(scrapeErrorsDesc, prometheus.CounterValue, float64(c.errorsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(vmsDiscoveredDesc, prometheus.GaugeValue, float64(discovered))
}

func (c *Collector) collectDomain(ch chan<- prometheus.Metric, d libvirtsrc.Domain) error {
	cgroupDir := filepath.Join(c.hostSysFsCgroup, d.CgroupPath)

	cpuUsage, err := cgroupsrc.ReadCPUStat(cgroupDir)
	if err != nil {
		return fmt.Errorf("cpu.stat: %w", err)
	}
	memUsage, err := cgroupsrc.ReadMemoryCurrent(cgroupDir)
	if err != nil {
		return fmt.Errorf("memory.current: %w", err)
	}
	pressure, err := cgroupsrc.ReadCPUPressureSome(cgroupDir)
	if err != nil {
		return fmt.Errorf("cpu.pressure: %w", err)
	}
	waitNanos, err := procsrc.SumTaskSchedstat(c.hostProc, d.PID)
	if err != nil {
		return fmt.Errorf("schedstat: %w", err)
	}

	labels := []string{c.node, d.UUID, d.Name, d.Flavor, d.ProjectID}

	ch <- prometheus.MustNewConstMetric(cpuUsageDesc, prometheus.CounterValue, float64(cpuUsage)/1e6, labels...)
	ch <- prometheus.MustNewConstMetric(memoryUsageDesc, prometheus.GaugeValue, float64(memUsage), labels...)
	ch <- prometheus.MustNewConstMetric(cpuPressureDesc, prometheus.CounterValue, float64(pressure)/1e6, labels...)
	ch <- prometheus.MustNewConstMetric(runqueueWaitDesc, prometheus.CounterValue, float64(waitNanos)/1e9, labels...)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/collector/... -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/collector/collector.go internal/collector/collector_test.go
git commit -m "feat: implement prometheus.Collector for QEMU VM metrics"
```

---

### Task 11: main.go wiring + local smoke test

**Files:**
- Modify: `cmd/qemu-exporter/main.go` (replaces Task 1's placeholder)

**Interfaces:**
- Consumes: `libvirtsrc.Connect`, `libvirtsrc.NewCache` (Task 8, 9), `collector.New` (Task 10).

- [ ] **Step 1: Replace main.go**

`cmd/qemu-exporter/main.go`:
```go
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/LyleKim/qemu-exporter/internal/collector"
	"github.com/LyleKim/qemu-exporter/internal/libvirtsrc"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9179", "address to listen on for telemetry")
	flag.Parse()

	hostProc := envOr("HOST_PROC", "/host/proc")
	hostSysFsCgroup := envOr("HOST_SYS_FS_CGROUP", "/host/sys/fs/cgroup")
	libvirtSock := envOr("LIBVIRT_SOCK", "/var/run/libvirt/libvirt-sock-ro")
	node := os.Getenv("NODE_NAME")
	if node == "" {
		slog.Warn("qemu-exporter: NODE_NAME is empty; the node label will be blank")
	}

	libvirtRunDir := filepath.Dir(libvirtSock)
	connect := func() (*libvirt.Libvirt, error) { return libvirtsrc.Connect(libvirtSock) }
	cache := libvirtsrc.NewCache(connect, hostProc, libvirtRunDir)
	col := collector.New(cache, hostSysFsCgroup, hostProc, node)
	prometheus.MustRegister(col)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: *listenAddr, Handler: mux}

	go func() {
		slog.Info("qemu-exporter: listening", "address", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("qemu-exporter: server failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	slog.Info("qemu-exporter: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("qemu-exporter: graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Build**

Run: `CGO_ENABLED=0 go build -o /tmp/qemu-exporter ./cmd/qemu-exporter`
Expected: exits 0.

- [ ] **Step 3: Manual local smoke test (Stage 1 of the local verification strategy -- no libvirtd, no AWS)**

```bash
/tmp/qemu-exporter --web.listen-address=:19179 &
sleep 1
curl -s http://localhost:19179/metrics | grep -E "^qemu_exporter"
kill %1
```

Expected output: the server logs a warning that it couldn't connect to libvirtd (there's no libvirtd on this machine -- that's expected), but stays up and serves:
```
qemu_exporter_scrape_errors_total 1
qemu_exporter_vms_discovered 0
```
This confirms the degrade-gracefully behavior (Global Constraints: a connection failure never crashes the process) and that the HTTP/promhttp wiring works, without needing any VM or libvirtd. `kill` sends SIGTERM, which should produce a "shutting down" log line and exit 0.

- [ ] **Step 4: Run the full test suite**

Run: `go vet ./... && go test ./...`
Expected: PASS for every package.

- [ ] **Step 5: Commit**

```bash
git add cmd/qemu-exporter/main.go
git commit -m "feat: wire main.go (flags, env config, graceful shutdown)"
```

---

### Task 12: Dockerfile + local container smoke test

**Files:**
- Create: `deploy/Dockerfile`

**Interfaces:**
- Consumes: the built binary from Tasks 1-11 (whole module).

- [ ] **Step 1: Write the Dockerfile**

`deploy/Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /qemu-exporter ./cmd/qemu-exporter

FROM scratch
COPY --from=build /qemu-exporter /qemu-exporter
ENTRYPOINT ["/qemu-exporter"]
```

- [ ] **Step 2: Build the image**

Run (from the repo root): `docker build -t qemu-exporter:dev -f deploy/Dockerfile .`
Expected: exits 0.

- [ ] **Step 3: Run it against a fake host tree (Stage 3 of the local verification strategy)**

```bash
mkdir -p /tmp/fake-cgroup/machine.slice/test.scope /tmp/fake-proc/1/task/1
cat > /tmp/fake-cgroup/machine.slice/test.scope/cpu.stat <<'EOF'
usage_usec 1000000
EOF
echo "52428800" > /tmp/fake-cgroup/machine.slice/test.scope/memory.current
cat > /tmp/fake-cgroup/machine.slice/test.scope/cpu.pressure <<'EOF'
some avg10=0.00 avg60=0.00 avg300=0.00 total=500000
EOF
echo "10 20 3" > /tmp/fake-proc/1/task/1/schedstat

docker run --rm -d --name qemu-exporter-smoke -p 19179:9179 \
  -v /tmp/fake-cgroup:/host/sys/fs/cgroup:ro \
  -v /tmp/fake-proc:/host/proc:ro \
  qemu-exporter:dev

sleep 1
curl -s http://localhost:19179/metrics | grep -E "^qemu_exporter"
docker stop qemu-exporter-smoke
```

Expected: the container starts, `/metrics` responds (no libvirtd inside the container either, so this still just confirms `qemu_exporter_scrape_errors_total 1` / `qemu_exporter_vms_discovered 0` -- the point of this step is proving the *binary and image* work, not exercising libvirt, which is verified for real only on the AWS host).

- [ ] **Step 4: Commit**

```bash
git add deploy/Dockerfile
git commit -m "feat: add multi-stage Dockerfile for qemu-exporter"
```

---

### Task 13: DaemonSet manifest + scrape config

**Files:**
- Create: `deploy/daemonset.yaml`
- Create: `deploy/scrape-config.md`

**Interfaces:**
- Consumes: the container image built in Task 12; the env vars read in Task 11's `main.go` (`NODE_NAME`, `HOST_PROC`, `HOST_SYS_FS_CGROUP`, `LIBVIRT_SOCK`).

- [ ] **Step 1: Write the DaemonSet manifest**

`deploy/daemonset.yaml`:
```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: qemu-exporter
  namespace: openstack
  labels:
    app: qemu-exporter
spec:
  selector:
    matchLabels:
      app: qemu-exporter
  template:
    metadata:
      labels:
        app: qemu-exporter
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9179"
    spec:
      hostPID: true
      # nodeSelector: add once the real compute-node label is confirmed on
      # Day 1 (this is a single-node cluster for now, so every node is a
      # compute node and no selector is required yet).
      containers:
        - name: qemu-exporter
          image: qemu-exporter:dev
          ports:
            - containerPort: 9179
              name: metrics
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
            - name: HOST_PROC
              value: /host/proc
            - name: HOST_SYS_FS_CGROUP
              value: /host/sys/fs/cgroup
            - name: LIBVIRT_SOCK
              value: /var/run/libvirt/libvirt-sock-ro
          volumeMounts:
            - name: proc
              mountPath: /host/proc
              readOnly: true
            - name: cgroup
              mountPath: /host/sys/fs/cgroup
              readOnly: true
            - name: libvirt-sock
              mountPath: /var/run/libvirt
              readOnly: true
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 64Mi
      volumes:
        - name: proc
          hostPath:
            path: /proc
        - name: cgroup
          hostPath:
            path: /sys/fs/cgroup
        - name: libvirt-sock
          hostPath:
            path: /var/run/libvirt
```

Note: no `privileged: true` and no `securityContext` capabilities are needed -- every mount is read-only and the process only reads files.

- [ ] **Step 2: Write the scrape config doc**

`deploy/scrape-config.md`:
```markdown
# Prometheus scrape config for qemu-exporter

qemu-exporter's `node` label must match the `node` label kube-state-metrics
puts on `kube_node_info`, so PromQL can join VM-level metrics with
node-level Kubernetes metrics via `on(node)`. The `node` label comes from
the `NODE_NAME` downward-API env var (`spec.nodeName`), which is already
exactly the node name Kubernetes itself uses -- no relabeling is needed for
that part. What *does* need relabel_configs is discovering the pods and
routing the scrape to the right port:

\`\`\`yaml
scrape_configs:
  - job_name: qemu-exporter
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: qemu-exporter
        action: keep
      - source_labels: [__address__]
        regex: '(.+):\d+'
        replacement: '${1}:9179'
        target_label: __address__
\`\`\`

## Example PromQL: joining VM pressure with node-level pod CPU usage

\`\`\`promql
openstack_vm_cpu_pressure_stall_seconds_total
  * on(node) group_left()
  sum(rate(container_cpu_usage_seconds_total{namespace!="openstack"}[5m])) by (node)
\`\`\`

This is illustrative -- the exact query used for the paper's Fig.3 will be
finalized once real contention data is captured.
```

- [ ] **Step 3: Commit**

```bash
git add deploy/daemonset.yaml deploy/scrape-config.md
git commit -m "feat: add DaemonSet manifest and Prometheus scrape config"
```

---

## Plan self-review

- **Spec coverage:** every row of the metrics table, both exporter self-metrics, the 3-stage local verification strategy, the cache invalidation approach, and the deploy manifests from the spec are each covered by exactly one task above.
- **Placeholder scan:** none of the code blocks contain TBD/TODO -- every one was compiled and its tests run during planning.
- **Type consistency:** `Domain`, `DomainSource`, `rpcClient`, and every parser signature are used identically across the tasks that produce and consume them (verified by literally building the whole module task-by-task).
- **What's deliberately deferred to the AWS phase, not this plan:** the real `libvirt.Connect` path (needs a live libvirtd), the real DaemonSet apply (needs the AWS cluster), and Fig.1/Fig.3 data collection (a separate, already-scoped-out set of experiment scripts per the spec).

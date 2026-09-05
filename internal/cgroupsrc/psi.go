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

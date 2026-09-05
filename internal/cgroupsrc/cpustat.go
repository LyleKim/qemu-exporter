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

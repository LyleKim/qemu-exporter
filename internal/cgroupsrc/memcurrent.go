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

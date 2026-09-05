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

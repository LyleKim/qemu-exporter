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

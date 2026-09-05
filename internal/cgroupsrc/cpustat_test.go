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

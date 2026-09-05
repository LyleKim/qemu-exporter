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

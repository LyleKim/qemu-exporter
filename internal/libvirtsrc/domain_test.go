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

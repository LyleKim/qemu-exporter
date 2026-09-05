package libvirtsrc

import "testing"

func TestBuildURI(t *testing.T) {
	got := buildURI("/var/run/libvirt/libvirt-sock-ro").String()
	want := "qemu+unix:///system?socket=%2Fvar%2Frun%2Flibvirt%2Flibvirt-sock-ro"
	if got != want {
		t.Errorf("buildURI = %q, want %q", got, want)
	}
}

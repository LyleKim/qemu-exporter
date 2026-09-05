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

package libvirtsrc

import (
	"fmt"
	"net/url"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
)

// libvirtDialTimeout bounds the initial unix-socket dial to libvirtd. Without
// it the dialer default is 15s, longer than a Prometheus scrape interval, so a
// libvirtd whose socket accepts but never completes the handshake would stall
// the first scrape. The per-call RPC round-trips are bounded separately in
// cache.go (go-libvirt has no per-call timeout of its own).
const libvirtDialTimeout = 3 * time.Second

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

// Connect dials libvirtd over the given read-only unix socket, with a bounded
// dial timeout.
func Connect(sockPath string) (*libvirt.Libvirt, error) {
	uri := buildURI(sockPath)
	dialer := dialers.NewLocal(
		dialers.WithSocket(sockPath),
		dialers.WithLocalTimeout(libvirtDialTimeout),
	)
	l := libvirt.NewWithDialer(dialer)
	if err := l.ConnectToURI(libvirt.RemoteURI(uri)); err != nil {
		return nil, fmt.Errorf("libvirtsrc: connect to libvirtd at %s: %w", sockPath, err)
	}
	return l, nil
}

package libvirtsrc

import (
	"fmt"
	"sync"

	"github.com/digitalocean/go-libvirt"
)

// rpcClient is the subset of *libvirt.Libvirt this package calls, factored
// out so Cache's diff/evict logic can be tested without a live libvirtd.
type rpcClient interface {
	ConnectListAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error)
	DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error)
}

// Cache is a DomainSource backed by libvirt. Domains() re-derives the
// active domain set from a cheap ConnectListAllDomains call every time
// it's called, and only pays for the expensive GetXMLDesc + pidfile +
// cgroup lookups on a cache miss -- once per VM lifecycle, not once per
// scrape. There is no separate lifecycle-event subscription or timer: this
// per-scrape diff is the entire invalidation mechanism, and it evicts a
// stopped VM within one scrape cycle.
type Cache struct {
	connect       func() (rpcClient, error)
	hostProc      string
	libvirtRunDir string

	mu      sync.Mutex
	rpc     rpcClient
	domains map[string]Domain
}

// NewCache returns a Cache that connects to libvirtd lazily (and
// reconnects on failure) using connect. libvirtRunDir is the directory
// containing libvirt's per-domain qemu pidfiles (qemu/<name>.pid) --
// normally the directory holding LIBVIRT_SOCK.
func NewCache(connect func() (*libvirt.Libvirt, error), hostProc, libvirtRunDir string) *Cache {
	return &Cache{
		connect:       func() (rpcClient, error) { return connect() },
		hostProc:      hostProc,
		libvirtRunDir: libvirtRunDir,
		domains:       make(map[string]Domain),
	}
}

// Domains implements DomainSource.
func (c *Cache) Domains() ([]Domain, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.rpc == nil {
		rpc, err := c.connect()
		if err != nil {
			return nil, fmt.Errorf("libvirtsrc: connect: %w", err)
		}
		c.rpc = rpc
	}

	active, _, err := c.rpc.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		c.rpc = nil // force reconnect on next call
		return nil, fmt.Errorf("libvirtsrc: list active domains: %w", err)
	}

	seen := make(map[string]bool, len(active))
	result := make([]Domain, 0, len(active))
	for _, ld := range active {
		uuid := formatUUID(ld.UUID)
		seen[uuid] = true

		d, cached := c.domains[uuid]
		if !cached {
			built, err := c.buildDomain(ld, uuid)
			if err != nil {
				// Skip this VM for this scrape; a transient failure (e.g.
				// the pidfile not written yet) shouldn't block every other
				// domain.
				continue
			}
			d = built
			c.domains[uuid] = d
		}
		result = append(result, d)
	}

	for uuid := range c.domains {
		if !seen[uuid] {
			delete(c.domains, uuid)
		}
	}

	return result, nil
}

func (c *Cache) buildDomain(ld libvirt.Domain, uuid string) (Domain, error) {
	xmlDesc, err := c.rpc.DomainGetXMLDesc(ld, 0)
	if err != nil {
		return Domain{}, fmt.Errorf("get XML for domain %s: %w", ld.Name, err)
	}
	flavor, projectID, err := parseNovaMetadata([]byte(xmlDesc))
	if err != nil {
		return Domain{}, fmt.Errorf("parse metadata for domain %s: %w", ld.Name, err)
	}
	pid, err := resolveQEMUPID(c.libvirtRunDir, ld.Name)
	if err != nil {
		return Domain{}, fmt.Errorf("resolve PID for domain %s: %w", ld.Name, err)
	}
	cgroupPath, err := ResolveCgroupPath(c.hostProc, pid)
	if err != nil {
		return Domain{}, fmt.Errorf("resolve cgroup path for domain %s (pid %d): %w", ld.Name, pid, err)
	}
	return Domain{
		UUID:       uuid,
		Name:       ld.Name,
		Flavor:     flavor,
		ProjectID:  projectID,
		PID:        pid,
		CgroupPath: cgroupPath,
	}, nil
}

func formatUUID(b libvirt.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

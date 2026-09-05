package libvirtsrc

import (
	"fmt"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// libvirtRPCTimeout bounds a single Domains() call's libvirt RPC round-trips.
// digitalocean/go-libvirt has no per-call timeout: ConnectListAllDomains /
// DomainGetXMLDesc block on a channel read from the socket reader goroutine
// with no deadline, and the dialer timeout only covers the initial dial. So a
// libvirtd that accepts the connection but stops answering (hung, deadlocked,
// mid-restart) would otherwise wedge /metrics forever -- this scrape and every
// one after it, since c.mu stays held. Abandoning the call after a few seconds
// turns that permanent block into an ordinary per-scrape error, well under a
// typical 10-15s Prometheus scrape interval. It is a var, not a const, only so
// tests can shorten it.
var libvirtRPCTimeout = 3 * time.Second

// rpcClient is the subset of *libvirt.Libvirt this package calls, factored
// out so Cache's diff/evict logic can be tested without a live libvirtd.
type rpcClient interface {
	ConnectListAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error)
	DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error)
	Disconnect() error
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

	// Run the RPC-bearing work in a goroutine so a hung libvirtd can't hold
	// c.mu forever. On timeout we abandon that goroutine: it only touches the
	// rpc client and the captured prev map, both of which we stop referencing
	// from c below, so there's no shared-state race with the next scrape.
	type outcome struct {
		list []Domain
		next map[string]Domain
		err  error
	}
	rpc := c.rpc
	prev := c.domains
	done := make(chan outcome, 1)
	go func() {
		list, next, err := c.collectDomains(rpc, prev)
		done <- outcome{list, next, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			c.dropRPC(rpc)
			return nil, r.err
		}
		c.domains = r.next
		return r.list, nil
	case <-time.After(libvirtRPCTimeout):
		c.dropRPC(rpc)
		c.domains = make(map[string]Domain) // abandoned goroutine keeps the old one
		return nil, fmt.Errorf("libvirtsrc: libvirtd did not respond within %s; abandoning RPC and reconnecting next scrape", libvirtRPCTimeout)
	}
}

// collectDomains does the libvirt RPC work for one scrape: list the active
// domains, reuse cached entries, and build entries for newly-seen UUIDs. It
// returns the domains to report plus the fresh cache map to install (which
// naturally drops any UUID no longer active).
func (c *Cache) collectDomains(rpc rpcClient, prev map[string]Domain) ([]Domain, map[string]Domain, error) {
	active, _, err := rpc.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		return nil, nil, fmt.Errorf("libvirtsrc: list active domains: %w", err)
	}

	next := make(map[string]Domain, len(active))
	result := make([]Domain, 0, len(active))
	for _, ld := range active {
		uuid := formatUUID(ld.UUID)

		d, cached := prev[uuid]
		if !cached {
			built, err := c.buildDomain(rpc, ld, uuid)
			if err != nil {
				// Skip this VM for this scrape; a transient failure (e.g.
				// the pidfile not written yet) shouldn't block every other
				// domain.
				continue
			}
			d = built
		}
		next[uuid] = d
		result = append(result, d)
	}
	return result, next, nil
}

// dropRPC discards the current client so the next Domains() call reconnects.
// Disconnect runs in the background: against a wedged libvirtd it can itself
// block (it issues a CONNECT_CLOSE RPC), and we must not let that stall the
// scrape. Closing the client still releases its socket and reader goroutine,
// which otherwise leak across libvirtd restarts (final-review Minor #7).
func (c *Cache) dropRPC(old rpcClient) {
	if old != nil {
		go func() { _ = old.Disconnect() }()
	}
	c.rpc = nil
}

func (c *Cache) buildDomain(rpc rpcClient, ld libvirt.Domain, uuid string) (Domain, error) {
	xmlDesc, err := rpc.DomainGetXMLDesc(ld, 0)
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

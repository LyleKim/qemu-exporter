package libvirtsrc

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"
)

type fakeRPC struct {
	active       []libvirt.Domain
	xmlByName    map[string]string
	xmlErrByName map[string]error
	xmlDescCalls map[string]int
	listErr      error
	listDelay    time.Duration // if set, ConnectListAllDomains blocks this long
}

func (f *fakeRPC) ConnectListAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error) {
	if f.listDelay > 0 {
		time.Sleep(f.listDelay)
	}
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.active, uint32(len(f.active)), nil
}

func (f *fakeRPC) Disconnect() error { return nil }

func (f *fakeRPC) DomainGetXMLDesc(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
	if f.xmlDescCalls == nil {
		f.xmlDescCalls = map[string]int{}
	}
	f.xmlDescCalls[dom.Name]++
	if err, ok := f.xmlErrByName[dom.Name]; ok {
		return "", err
	}
	return f.xmlByName[dom.Name], nil
}

func writeFakeHost(t *testing.T, pid int, cgroupPath, domainName string) (hostProc, libvirtRunDir string) {
	t.Helper()
	hostProc = t.TempDir()
	libvirtRunDir = t.TempDir()

	pidDir := filepath.Join(hostProc, strconv.Itoa(pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte("0::"+cgroupPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(libvirtRunDir, "qemu"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libvirtRunDir, "qemu", domainName+".pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hostProc, libvirtRunDir
}

func TestCache_Domains_BuildsAndCaches(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/machine-qemu-1-instance-00000001.scope", "instance-00000001")

	dom := libvirt.Domain{Name: "instance-00000001", ID: 1}
	copy(dom.UUID[:], []byte{0xe1, 0xe5, 0xc1, 0xc0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x12, 0x34, 0x56, 0x78})

	fake := &fakeRPC{
		active:    []libvirt.Domain{dom},
		xmlByName: map[string]string{"instance-00000001": testDomainXML},
	}

	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d domains, want 1", len(got))
	}
	want := Domain{
		UUID:       "e1e5c1c0-1234-5678-9abc-def012345678",
		Name:       "instance-00000001",
		Flavor:     "m1.large",
		ProjectID:  "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		PID:        pid,
		CgroupPath: "/machine.slice/machine-qemu-1-instance-00000001.scope",
	}
	if got[0] != want {
		t.Errorf("Domains()[0] = %+v, want %+v", got[0], want)
	}

	// Second call must not re-fetch XML for the same domain (cache hit).
	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains (2nd call): %v", err)
	}
	if n := fake.xmlDescCalls["instance-00000001"]; n != 1 {
		t.Errorf("DomainGetXMLDesc called %d times, want 1 (cache should avoid the second call)", n)
	}
}

func TestCache_Domains_EvictsStoppedDomain(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/x.scope", "instance-00000001")
	dom := libvirt.Domain{Name: "instance-00000001"}
	fake := &fakeRPC{active: []libvirt.Domain{dom}, xmlByName: map[string]string{"instance-00000001": testDomainXML}}
	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(c.domains) != 1 {
		t.Fatalf("cache has %d entries after first call, want 1", len(c.domains))
	}

	fake.active = nil // domain stopped
	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains (after stop): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Domains() after stop = %d entries, want 0", len(got))
	}
	if len(c.domains) != 0 {
		t.Errorf("cache has %d entries after eviction, want 0", len(c.domains))
	}
}

func TestCache_Domains_SkipsDomainOnBuildFailure(t *testing.T) {
	pid := 4242
	hostProc, libvirtRunDir := writeFakeHost(t, pid, "/machine.slice/x.scope", "instance-00000001")
	good := libvirt.Domain{Name: "instance-00000001"}
	copy(good.UUID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	bad := libvirt.Domain{Name: "instance-broken"}
	copy(bad.UUID[:], []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	fake := &fakeRPC{
		active:       []libvirt.Domain{good, bad},
		xmlByName:    map[string]string{"instance-00000001": testDomainXML},
		xmlErrByName: map[string]error{"instance-broken": errors.New("boom")},
	}
	c := &Cache{rpc: fake, hostProc: hostProc, libvirtRunDir: libvirtRunDir, domains: make(map[string]Domain)}

	got, err := c.Domains()
	if err != nil {
		t.Fatalf("Domains: %v", err)
	}
	if len(got) != 1 || got[0].Name != "instance-00000001" {
		t.Errorf("Domains() = %+v, want only instance-00000001", got)
	}
}

func TestCache_Domains_RPCTimeout(t *testing.T) {
	orig := libvirtRPCTimeout
	libvirtRPCTimeout = 50 * time.Millisecond
	defer func() { libvirtRPCTimeout = orig }()

	hung := &fakeRPC{listDelay: 2 * time.Second} // never answers within the timeout
	c := &Cache{rpc: hung, hostProc: t.TempDir(), libvirtRunDir: t.TempDir(), domains: make(map[string]Domain)}

	start := time.Now()
	_, err := c.Domains()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error from a hung libvirtd, got nil")
	}
	if elapsed > time.Second {
		t.Errorf("Domains() took %s; should have abandoned the RPC near the %s timeout", elapsed, libvirtRPCTimeout)
	}
	if c.rpc != nil {
		t.Error("rpc should be nil after a timeout, to force reconnect on the next scrape")
	}
}

func TestCache_Domains_ReconnectsAfterFailure(t *testing.T) {
	hostProc, libvirtRunDir := t.TempDir(), t.TempDir()
	failing := &fakeRPC{listErr: errors.New("connection refused")}
	attempts := 0
	c := &Cache{
		connect: func() (rpcClient, error) {
			attempts++
			if attempts == 1 {
				return failing, nil
			}
			return &fakeRPC{}, nil
		},
		hostProc:      hostProc,
		libvirtRunDir: libvirtRunDir,
		domains:       make(map[string]Domain),
	}

	if _, err := c.Domains(); err == nil {
		t.Fatal("expected error on first call")
	}
	if c.rpc != nil {
		t.Error("rpc should be nil after a failed call, to force reconnect")
	}
	if _, err := c.Domains(); err != nil {
		t.Fatalf("Domains (after reconnect): %v", err)
	}
	if attempts != 2 {
		t.Errorf("connect called %d times, want 2", attempts)
	}
}

package libvirtsrc

// Domain is the identity and location information this project correlates
// cgroup/procfs metrics against. Flavor and ProjectID are empty when the
// domain carries no Nova metadata -- not every libvirt domain is
// Nova-managed.
type Domain struct {
	UUID       string
	Name       string
	Flavor     string
	ProjectID  string
	PID        int
	CgroupPath string
}

// DomainSource lists the QEMU domains this exporter should scrape. Cache is
// the production implementation; tests substitute a fake so collector
// tests don't need a live libvirtd.
type DomainSource interface {
	Domains() ([]Domain, error)
}

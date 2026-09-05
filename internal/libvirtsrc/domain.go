package libvirtsrc

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// domainXML mirrors only the subset of libvirt's domain XML this project
// reads. Go's encoding/xml matches these tags against the namespaced
// <nova:...> elements by local name alone (verified: it does not require
// the tag to repeat the nova namespace URI).
type domainXML struct {
	Metadata domainMetadataXML `xml:"metadata"`
}

type domainMetadataXML struct {
	Instance *novaInstanceXML `xml:"instance"`
}

type novaInstanceXML struct {
	Flavor novaFlavorXML `xml:"flavor"`
	Owner  novaOwnerXML  `xml:"owner"`
}

type novaFlavorXML struct {
	Name string `xml:"name,attr"`
}

type novaOwnerXML struct {
	Project novaProjectXML `xml:"project"`
}

type novaProjectXML struct {
	UUID string `xml:"uuid,attr"`
}

// parseNovaMetadata extracts the flavor name and project UUID from a
// libvirt domain XML document's <nova:instance> metadata block. Name and
// UUID aren't parsed here because ConnectListAllDomains already provides
// them more cheaply (see cache.go). It returns empty strings, not an
// error, when the domain carries no such block -- not every libvirt
// domain is Nova-managed.
func parseNovaMetadata(domainXMLBytes []byte) (flavor, projectID string, err error) {
	var dx domainXML
	if err := xml.Unmarshal(domainXMLBytes, &dx); err != nil {
		return "", "", fmt.Errorf("libvirtsrc: parse domain XML: %w", err)
	}
	inst := dx.Metadata.Instance
	if inst == nil {
		return "", "", nil
	}
	return inst.Flavor.Name, inst.Owner.Project.UUID, nil
}

// resolveQEMUPID reads libvirt's pidfile for a running qemu domain.
// libvirt's public API has no "get PID" RPC (it deliberately abstracts the
// hypervisor), so this reads the same pidfile libvirt itself writes at
// <libvirtRunDir>/qemu/<domain-name>.pid -- the standard technique used by
// other libvirt-based exporters.
func resolveQEMUPID(libvirtRunDir, domainName string) (int, error) {
	p := filepath.Join(libvirtRunDir, "qemu", domainName+".pid")
	data, err := os.ReadFile(p)
	if err != nil {
		return 0, fmt.Errorf("libvirtsrc: read pidfile %s: %w", p, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("libvirtsrc: parse pidfile %s: %w", p, err)
	}
	return pid, nil
}

package collector

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LyleKim/qemu-exporter/internal/cgroupsrc"
	"github.com/LyleKim/qemu-exporter/internal/libvirtsrc"
	"github.com/LyleKim/qemu-exporter/internal/procsrc"
)

var labelNames = []string{"node", "instance_uuid", "instance_name", "flavor", "project_id"}

var (
	cpuUsageDesc = prometheus.NewDesc(
		"openstack_vm_cpu_usage_seconds_total",
		"Cumulative CPU time consumed by the VM's QEMU process (cgroup cpu.stat usage_usec).",
		labelNames, nil,
	)
	memoryUsageDesc = prometheus.NewDesc(
		"openstack_vm_memory_usage_bytes",
		"Current memory usage of the VM's QEMU process (cgroup memory.current).",
		labelNames, nil,
	)
	cpuPressureDesc = prometheus.NewDesc(
		"openstack_vm_cpu_pressure_stall_seconds_total",
		"Cumulative CPU pressure stall time for the VM's cgroup (cpu.pressure 'some').",
		labelNames, prometheus.Labels{"type": "some"},
	)
	runqueueWaitDesc = prometheus.NewDesc(
		"openstack_vm_sched_runqueue_wait_seconds_total",
		"Cumulative time the VM's vCPU threads spent waiting on a runqueue (schedstat field 2, summed over threads).",
		labelNames, nil,
	)
	scrapeErrorsDesc = prometheus.NewDesc(
		"qemu_exporter_scrape_errors_total",
		"Cumulative count of per-domain or connection failures encountered while scraping.",
		nil, nil,
	)
	vmsDiscoveredDesc = prometheus.NewDesc(
		"qemu_exporter_vms_discovered",
		"Number of VMs successfully scraped in the most recent collection.",
		nil, nil,
	)
)

// Collector implements prometheus.Collector for QEMU VM metrics.
type Collector struct {
	source          libvirtsrc.DomainSource
	hostSysFsCgroup string
	hostProc        string
	node            string

	errorsTotal atomic.Uint64
}

// New returns a Collector. source lists the VMs to scrape; hostSysFsCgroup
// and hostProc are the (possibly container-remapped) roots of the cgroupfs
// and procfs mounts; node is the value for the "node" label.
func New(source libvirtsrc.DomainSource, hostSysFsCgroup, hostProc, node string) *Collector {
	return &Collector{
		source:          source,
		hostSysFsCgroup: hostSysFsCgroup,
		hostProc:        hostProc,
		node:            node,
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cpuUsageDesc
	ch <- memoryUsageDesc
	ch <- cpuPressureDesc
	ch <- runqueueWaitDesc
	ch <- scrapeErrorsDesc
	ch <- vmsDiscoveredDesc
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	domains, err := c.source.Domains()
	if err != nil {
		slog.Warn("qemu-exporter: list domains failed", "error", err)
		c.errorsTotal.Add(1)
		ch <- prometheus.MustNewConstMetric(scrapeErrorsDesc, prometheus.CounterValue, float64(c.errorsTotal.Load()))
		ch <- prometheus.MustNewConstMetric(vmsDiscoveredDesc, prometheus.GaugeValue, 0)
		return
	}

	discovered := 0
	for _, d := range domains {
		if err := c.collectDomain(ch, d); err != nil {
			slog.Warn("qemu-exporter: collect domain failed", "domain", d.Name, "uuid", d.UUID, "error", err)
			c.errorsTotal.Add(1)
			continue
		}
		discovered++
	}

	ch <- prometheus.MustNewConstMetric(scrapeErrorsDesc, prometheus.CounterValue, float64(c.errorsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(vmsDiscoveredDesc, prometheus.GaugeValue, float64(discovered))
}

func (c *Collector) collectDomain(ch chan<- prometheus.Metric, d libvirtsrc.Domain) error {
	cgroupDir := filepath.Join(c.hostSysFsCgroup, d.CgroupPath)

	cpuUsage, err := cgroupsrc.ReadCPUStat(cgroupDir)
	if err != nil {
		return fmt.Errorf("cpu.stat: %w", err)
	}
	memUsage, err := cgroupsrc.ReadMemoryCurrent(cgroupDir)
	if err != nil {
		return fmt.Errorf("memory.current: %w", err)
	}
	pressure, err := cgroupsrc.ReadCPUPressureSome(cgroupDir)
	if err != nil {
		return fmt.Errorf("cpu.pressure: %w", err)
	}
	waitNanos, err := procsrc.SumTaskSchedstat(c.hostProc, d.PID)
	if err != nil {
		return fmt.Errorf("schedstat: %w", err)
	}

	labels := []string{c.node, d.UUID, d.Name, d.Flavor, d.ProjectID}

	ch <- prometheus.MustNewConstMetric(cpuUsageDesc, prometheus.CounterValue, float64(cpuUsage)/1e6, labels...)
	ch <- prometheus.MustNewConstMetric(memoryUsageDesc, prometheus.GaugeValue, float64(memUsage), labels...)
	ch <- prometheus.MustNewConstMetric(cpuPressureDesc, prometheus.CounterValue, float64(pressure)/1e6, labels...)
	ch <- prometheus.MustNewConstMetric(runqueueWaitDesc, prometheus.CounterValue, float64(waitNanos)/1e9, labels...)
	return nil
}

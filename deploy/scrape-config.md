# Prometheus scrape config for qemu-exporter

qemu-exporter's `node` label must match the `node` label kube-state-metrics
puts on `kube_node_info`, so PromQL can join VM-level metrics with
node-level Kubernetes metrics via `on(node)`. The `node` label comes from
the `NODE_NAME` downward-API env var (`spec.nodeName`), which is already
exactly the node name Kubernetes itself uses -- no relabeling is needed for
that part. What *does* need relabel_configs is discovering the pods and
routing the scrape to the right port:

```yaml
scrape_configs:
  - job_name: qemu-exporter
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: qemu-exporter
        action: keep
      - source_labels: [__address__]
        regex: '(.+):\d+'
        replacement: '${1}:9179'
        target_label: __address__
```

## Example PromQL: joining VM pressure with node-level pod CPU usage

```promql
openstack_vm_cpu_pressure_stall_seconds_total
  * on(node) group_left()
  sum(rate(container_cpu_usage_seconds_total{namespace!="openstack"}[5m])) by (node)
```

This is illustrative -- the exact query used for the paper's Fig.3 will be
finalized once real contention data is captured.

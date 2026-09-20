# Prometheus + Grafana 배포 TDL (AWS 노드)

> **목표**: 배포된 `qemu-exporter`의 6개 지표를 Prometheus가 5초 주기로 긁고, Grafana 대시보드로
> Fig.1(메모리 회계 왜곡)·Fig.3(경합 은폐)를 시각화한다.
> **방식**: Helm/Operator 안 씀. 최소 매니페스트 4개(exporter Service, Prometheus, Grafana)만.
> **전제**: AWS 노드 살아있고 `qemu-exporter` DaemonSet 정상(`curl podIP:9179/metrics`에서 `qemu_exporter_vms_discovered ≥ 1`).
> `paper_data/` 는 이미 Mac에 백업됨 — 노드가 죽어도 논문 데이터는 안전.
> **셸 주의**: `session-manager-plugin` 인터랙티브가 깨진 상태 → 필요하면 `aws ssm send-command` 로 노드 명령 실행.
> heredoc 붙여넣기가 자주 깨졌으니 `cat > f <<'EOF' ... EOF` 후 `head -3 f; wc -l f` 로 매번 확인.

---

## P1 — 네임스페이스 + exporter Service

DaemonSet엔 Service가 없어서 Prometheus가 붙을 안정 주소가 없음. 하나 만든다.

```bash
kubectl create namespace monitoring

cat > /tmp/exporter-svc.yaml <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: qemu-exporter
  namespace: openstack
  labels: {app: qemu-exporter}
spec:
  selector: {app: qemu-exporter}
  ports:
    - {name: metrics, port: 9179, targetPort: 9179}
EOF
kubectl apply -f /tmp/exporter-svc.yaml
kubectl -n openstack get endpoints qemu-exporter        # ENDPOINTS에 파드IP:9179 떠야 함
```

- [ ] `monitoring` ns 생성
- [ ] `qemu-exporter` Service, endpoints 채워짐

## P2 — Prometheus (configmap + deployment + svc)

RBAC 불필요 (static target 하나만 긁음). retention 2일, emptyDir(재시작 시 소실 — 스크린샷으로 남길 것).

```bash
cat > /tmp/prometheus.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata: {name: prometheus-config, namespace: monitoring}
data:
  prometheus.yml: |
    global:
      scrape_interval: 5s
      evaluation_interval: 15s
    scrape_configs:
      - job_name: qemu-exporter
        static_configs:
          - targets: ['qemu-exporter.openstack.svc.cluster.local:9179']
      - job_name: prometheus
        static_configs:
          - targets: ['localhost:9090']
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: prometheus, namespace: monitoring}
spec:
  replicas: 1
  selector: {matchLabels: {app: prometheus}}
  template:
    metadata: {labels: {app: prometheus}}
    spec:
      containers:
        - name: prometheus
          image: prom/prometheus:v3.1.0
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.path=/prometheus
            - --storage.tsdb.retention.time=2d
            - --web.enable-lifecycle
          ports: [{containerPort: 9090}]
          volumeMounts:
            - {name: config, mountPath: /etc/prometheus}
            - {name: data, mountPath: /prometheus}
          resources:
            requests: {cpu: 50m, memory: 256Mi}
            limits: {cpu: 500m, memory: 512Mi}
      volumes:
        - {name: config, configMap: {name: prometheus-config}}
        - {name: data, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: prometheus, namespace: monitoring}
spec:
  selector: {app: prometheus}
  ports: [{name: web, port: 9090, targetPort: 9090}]
EOF
kubectl apply -f /tmp/prometheus.yaml
kubectl -n monitoring rollout status deploy/prometheus --timeout=120s
```

- [ ] prometheus 파드 `Running`

## P3 — 스크레이프 확인

```bash
PP=$(kubectl -n monitoring get pod -l app=prometheus -o jsonpath='{.items[0].status.podIP}')
# 타깃 상태
curl -s "http://$PP:9090/api/v1/targets" | grep -o '"health":"[a-z]*"' | sort | uniq -c
# 쿼리 하나
curl -s "http://$PP:9090/api/v1/query?query=qemu_exporter_vms_discovered" | grep -o '"value":\[[^]]*\]'
```

- [ ] `qemu-exporter` job health = `up`
- [ ] `qemu_exporter_vms_discovered` 쿼리에 값 나옴

## P4 — Grafana (datasource 프로비저닝 포함, NodePort)

익명 Admin 허용(프로토타입) + Prometheus 데이터소스 자동 연결. NodePort 30300으로 노출(포트포워딩 없이 SSM 터널만 필요).

```bash
cat > /tmp/grafana.yaml <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata: {name: grafana-ds, namespace: monitoring}
data:
  ds.yaml: |
    apiVersion: 1
    datasources:
      - name: Prometheus
        type: prometheus
        access: proxy
        url: http://prometheus.monitoring.svc.cluster.local:9090
        isDefault: true
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: grafana, namespace: monitoring}
spec:
  replicas: 1
  selector: {matchLabels: {app: grafana}}
  template:
    metadata: {labels: {app: grafana}}
    spec:
      containers:
        - name: grafana
          image: grafana/grafana:11.4.0
          env:
            - {name: GF_AUTH_ANONYMOUS_ENABLED, value: "true"}
            - {name: GF_AUTH_ANONYMOUS_ORG_ROLE, value: "Admin"}
            - {name: GF_SECURITY_ADMIN_PASSWORD, value: "admin"}
            - {name: GF_USERS_DEFAULT_THEME, value: "light"}
          ports: [{containerPort: 3000}]
          volumeMounts:
            - {name: ds, mountPath: /etc/grafana/provisioning/datasources}
          resources:
            requests: {cpu: 50m, memory: 128Mi}
            limits: {cpu: 300m, memory: 256Mi}
      volumes:
        - {name: ds, configMap: {name: grafana-ds}}
---
apiVersion: v1
kind: Service
metadata: {name: grafana, namespace: monitoring}
spec:
  type: NodePort
  selector: {app: grafana}
  ports: [{name: web, port: 3000, targetPort: 3000, nodePort: 30300}]
EOF
kubectl apply -f /tmp/grafana.yaml
kubectl -n monitoring rollout status deploy/grafana --timeout=120s
```

- [ ] grafana 파드 `Running`

## P5 — Mac에서 Grafana 열기 (SSM 터널)

파일 전송 때 쓴 포트포워딩과 동일. Mac에서:

```bash
IID=i-0b5727acd36092da5   # 노드 인스턴스 ID (바뀌었으면 aws ec2 describe-instances로 확인)
aws ssm start-session --target "$IID" --region ap-northeast-2 \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"portNumber":["30300"],"localPortNumber":["3000"]}'
```

브라우저: `http://localhost:3000` → 익명 Admin으로 바로 들어감 (또는 admin/admin).
데이터소스는 이미 `Prometheus`로 연결돼 있음 (Connections → Data sources에서 확인).

- [ ] Grafana 접속됨, Prometheus 데이터소스 초록

## P6 — 대시보드 패널 (UI에서 추가, PromQL만 붙여넣기)

새 대시보드 → 패널 5개. VM 라벨은 `instance_name`(예: `instance-00000004`) 또는 `flavor`로.

**패널 1 — Fig.3: 경합 은폐 (Time series)**
```promql
rate(openstack_vm_cpu_pressure_stall_seconds_total{instance_name="instance-00000004"}[1m])   # legend: cpu.pressure(some)
rate(openstack_vm_sched_runqueue_wait_seconds_total{instance_name="instance-00000004"}[1m])  # legend: runqueue_wait
rate(openstack_vm_cpu_usage_seconds_total{instance_name="instance-00000004"}[1m])            # legend: cpu_time(libvirt-equiv)
```
→ 경합 걸면 앞 둘만 치솟고 셋째는 평평.

**패널 2 — Fig.1: VM 메모리 (Stat 또는 Bar gauge)**
```promql
sum(openstack_vm_memory_usage_bytes)                          # k8s 회계 밖 총량
openstack_vm_memory_usage_bytes                                # VM별 (legend: {{instance_name}} / {{flavor}})
```

**패널 3 — 발견/에러 (Stat)**
```promql
qemu_exporter_vms_discovered
qemu_exporter_scrape_errors_total
```

**패널 4 — VM별 CPU 소비 (Time series)**
```promql
rate(openstack_vm_cpu_usage_seconds_total[1m])                 # legend: {{instance_name}}
```

**패널 5 — 누적 pressure (Time series, rate 아님)**
```promql
openstack_vm_cpu_pressure_stall_seconds_total{type="some"}     # legend: {{instance_name}}
```

- [ ] 5개 패널 저장, 대시보드 이름 `qemu-exporter`
- [ ] (선택) 대시보드 JSON export → `paper_data/grafana_dashboard.json` 로 보관

## P7 (선택) — cAdvisor 스크레이프 + `on(node)` 조인

논문 Fig.2/3장의 "VM PSI ↔ 같은 노드 k8s 워크로드" 조인을 보이려면 Prometheus가 kubelet cAdvisor도 긁어야 함. RBAC(SA + ClusterRole)와 `kubernetes_sd_configs` 필요 → 분량 늘어남.
3페이지 논문엔 "exporter가 `node` 라벨을 달아 `on(node)`로 조인 가능"을 **문장 + 쿼리 예시**로만 보여도 충분:
```promql
rate(openstack_vm_cpu_pressure_stall_seconds_total{type="some"}[1m])
  * on(node) group_left()
  (sum by(node) (rate(container_cpu_usage_seconds_total{pod=~"cpu-hog.*"}[1m])))
```
꼭 실제로 조인 그래프가 필요하면 그때 P7 확장.

---

## 정리 / 한계

- 데이터는 emptyDir → Prometheus 파드 재시작하면 소실. Fig.3 재현 실험을 Grafana 띄운 상태에서 다시 돌리고 **스크린샷**으로 그림 확보하는 게 목적. 원시 수치는 이미 `paper_data/`에 있음.
- Grafana 익명 Admin·평문 비번은 프로토타입용. 외부 노출 아님(SSM 터널만).
- 실험 재현: `paper_data/README.md` §4 절차대로 `cpu-hog` apply/delete 하면서 Grafana 패널 관찰.
- 다 끝나면: `kubectl delete ns monitoring` 후 `make down`.

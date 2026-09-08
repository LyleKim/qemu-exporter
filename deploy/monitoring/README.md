# deploy/monitoring/ — Prometheus + Grafana for the paper's live demo

이 디렉터리 = qemu-exporter 지표를 Prometheus로 긁고 Grafana로 보이는 최소 스택.
"부하 걸릴 때 kubelet/cAdvisor는 그 VM을 못 보는데 qemu-exporter만 잡아낸다" (Fig.3b)를
시계열로 뽑기 위한 것. Helm/Operator 안 씀.

## 파일

| 파일 | 내용 |
|---|---|
| `exporter-svc.yaml` | qemu-exporter DaemonSet용 ClusterIP Service (Prometheus가 붙을 안정 주소) |
| `prometheus.yaml` | SA+RBAC + Prometheus(qemu-exporter static + kubelet cAdvisor + kubelet resource, apiserver proxy 경유) |
| `grafana.yaml` | Grafana(익명 Admin) + Prometheus 데이터소스 프로비저닝 + 대시보드 `qemu-exporter` 자동 로드, NodePort 30300 |
| `cpu-hog.yaml` | 경합 유발 Deployment (busybox 코어당 1개, 기본 replicas 4) |

## 전제 (이게 먼저 있어야 함)

1. OSH 단일 노드 배포 완료 (`aws_verification_tdl.md` / `osh-node-env-handoff.md` 참조)
2. qemu-exporter DaemonSet 배포 (`kubectl -n openstack apply -f ../daemonset.yaml`).
   이미지 `docker.io/lylekim/qemu-exporter:dev`는 Docker Hub에 그대로 있음.
   `curl <podIP>:9179/metrics` 에 `qemu_exporter_vms_discovered ≥ 1`, `scrape_errors_total = 0` 확인.
3. 부하 도는 VM 1대. 아래 cloud-init user-data로 Ubuntu VM(`fig-vm`) 기동:
   ```
   #cloud-config
   password: figvm123
   chpasswd: {expire: false}
   ssh_pwauth: true
   write_files:
     - path: /usr/local/bin/figload.sh
       permissions: '0755'
       content: |
         #!/bin/sh
         for i in 1 2; do (yes > /dev/null &); done
         while :; do
           echo "GUESTSTAT $(date +%s) $(grep '^cpu ' /proc/stat)" > /dev/ttyS0
           sleep 2
         done
   runcmd:
     - [ setsid, --fork, /usr/local/bin/figload.sh ]
   ```
   `kubectl -n openstack exec osc -- openstack server create --flavor m1.medium --image ubuntu-24.04 --nic net-id=<demo-net id> --user-data /tmp/ud.yaml fig-vm`
   → `OS-EXT-SRV-ATTR:instance_name` 확인 (예 `instance-00000004`). **대시보드/쿼리의 `instance_name` 값을 이 이름으로 바꿀 것.**

## 배포 (~10분)

```bash
kubectl create namespace monitoring
kubectl apply -f deploy/monitoring/exporter-svc.yaml
kubectl apply -f deploy/monitoring/prometheus.yaml
kubectl apply -f deploy/monitoring/grafana.yaml
kubectl -n monitoring rollout status deploy/prometheus --timeout=150s
kubectl -n monitoring rollout status deploy/grafana --timeout=150s

# 3개 job 다 up 확인
PP=$(kubectl -n monitoring get pod -l app=prometheus -o jsonpath='{.items[0].status.podIP}')
curl -s "http://$PP:9090/api/v1/targets?state=active" | tr ',' '\n' | grep -E '"job"|"health"'
```

## Grafana 열기

- **SSM 포트포워딩**: `aws ssm start-session --target <IID> --region ap-northeast-2 --document-name AWS-StartPortForwardingSession --parameters '{"portNumber":["30300"],"localPortNumber":["3000"]}'` → `http://localhost:3000`
  (session-manager-plugin의 `Port` 스텝이 깨지면 노드 SSM 에이전트 재시작 `sudo snap restart amazon-ssm-agent`)
- **또는 SG 직접 개방** (노드 퍼블릭 IP 있을 때): `aws ec2 authorize-security-group-ingress --group-id <sg> --protocol tcp --port 30300 --cidr <내IP>/32` → `http://<퍼블릭IP>:30300` → **끝나면 `revoke-security-group-ingress`로 닫기**
- 익명 Admin이라 로그인 없이 진입. 좌측 Dashboards → `qemu-exporter`.

## Fig.3b 데이터 뽑기 (경합 실험 + 시계열 덤프)

```bash
cd ~/paper_data     # 또는 아무 작업 디렉터리
sed -i 's/replicas: [0-9]*/replicas: 4/' deploy/monitoring/cpu-hog.yaml

T0=$(date +%s); sleep 30
HON=$(date +%s);  kubectl apply  -f deploy/monitoring/cpu-hog.yaml
sleep 120
HOFF=$(date +%s); kubectl delete -f deploy/monitoring/cpu-hog.yaml
sleep 60
END=$(date +%s)
printf "T0=%s\nHOG_ON=%s\nHOG_OFF=%s\nEND=%s\n" $T0 $HON $HOFF $END > fig3b_markers.txt

PP=$(kubectl -n monitoring get pod -l app=prometheus -o jsonpath='{.items[0].status.podIP}')
. <(sed 's/^/export /' fig3b_markers.txt); S=$((T0-30)); E=$((END+30))
q(){ curl -s "http://$PP:9090/api/v1/query_range" --data-urlencode "query=$1" \
  --data-urlencode "start=$S" --data-urlencode "end=$E" --data-urlencode "step=5" > "$2"; }

VM='instance_name="instance-00000004"'   # ← fig-vm 이름으로
q "rate(openstack_vm_cpu_pressure_stall_seconds_total{$VM}[1m])"                              fig3b_exporter_pressure.json
q "rate(openstack_vm_sched_runqueue_wait_seconds_total{$VM}[1m])"                             fig3b_exporter_runq.json
q "rate(openstack_vm_cpu_usage_seconds_total{$VM}[1m])"                                       fig3b_exporter_cpu.json
q 'count(container_cpu_usage_seconds_total{job="kubelet-cadvisor",id=~"/machine.*"}) or on() vector(0)'                       fig3b_cadvisor_vm_series.json
q 'sum(rate(container_cpu_usage_seconds_total{job="kubelet-cadvisor",pod=~"cpu-hog.*",container!=""}[1m])) or on() vector(0)' fig3b_cadvisor_hogpods.json
q 'rate(node_cpu_usage_seconds_total[1m])'                                                                                   fig3b_kubelet_node_cpu.json
q 'rate(node_cpu_usage_seconds_total[1m]) - sum(rate(pod_cpu_usage_seconds_total[1m]))'                                      fig3b_kubelet_node_minus_pods.json
```

구간별 평균 확인:
```bash
for f in fig3b_exporter_pressure fig3b_exporter_runq fig3b_cadvisor_vm_series fig3b_cadvisor_hogpods fig3b_kubelet_node_cpu fig3b_kubelet_node_minus_pods; do
  python3 - "$f.json" "$HOG_ON" "$HOG_OFF" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); hon,hoff=int(sys.argv[2]),int(sys.argv[3])
r=d["data"]["result"]
if not r: print(f"{sys.argv[1]:38s} (no series)"); raise SystemExit
v=r[0]["values"]
a=lambda lo,hi:(lambda xs:sum(xs)/len(xs) if xs else float('nan'))([float(x) for t,x in v if lo<=t<hi])
print(f"{sys.argv[1]:38s} base={a(0,hon-5):10.4f} load={a(hon+10,hoff-5):10.4f} after={a(hoff+10,9e18):10.4f}")
PY
done
```

**기대**: exporter_pressure/runq → load가 base의 수배~수십배 / cadvisor_vm_series → 항상 0 /
cadvisor_hogpods → base 0, load 큰값 / kubelet_node_cpu → load에서 상승 / kubelet_node_minus_pods → 거의 불변.

+ Grafana 대시보드 스크린샷 → `paper_data/fig3b_grafana.png`

## 정리

```bash
kubectl delete namespace monitoring
# SG 열었으면 revoke
```

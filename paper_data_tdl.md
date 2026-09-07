# 논문 데이터 추출 TDL — Prometheus 없이 raw 수집

> exporter(G2 통과)를 도구로, 논문 그림·표에 필요한 원시 데이터를 노드에서 직접 뽑는다.
> Prometheus/Grafana 연동은 이 다음. 여기서는 `curl /metrics` + 호스트 cgroup 파일 + `virsh domstats`를
> 스크립트로 샘플링해 CSV로 저장한다.
> 대상: `basic_plan.md`의 주장 "QEMU가 K8s 자원 회계 밖 → 경합 은폐 → 무변경 수집기로 드러냄".

## 산출물 (이 TDL이 만들어야 하는 것)

| 산출물 | 내용 | Phase |
|---|---|---|
| **Fig.1** | 자원 회계 왜곡 — machine cgroup(QEMU) / kubepods.slice / 노드 실사용 메모리 3열 | H |
| **Fig.3** ★ | 경합 은폐 시계열 — guest 내부 CPU / libvirt cpu_time rate / cpu.pressure rate / runqueue_wait rate, 4분간 (baseline→경합→해제) | I |
| **Table 1** | 가시성 커버리지 매트릭스 (libvirt / cAdvisor / 본 수집기 × 자원·경합·Nova신원·K8s조인) | J |
| 정확도 문장 | exporter `cpu_usage` vs `virsh cpu.time` 상대오차 1회 | J |
| 오버헤드 문장 | exporter 자체 CPU·RSS, fio in-guest throughput ±스크레이프 1회 | J |
| 환경 명세 (4.1절) | 커널·cgroup·PSI·OSH 버전·노드 스펙 — `aws_verification_tdl.md` 진행요약에 이미 있음, 재확인만 | K |

---

## Phase G — 실험 준비

### G-1. 실험 VM (Fig.1은 메모리 커야 대비가 보임)
- [ ] `openstack image list` / `openstack flavor list` 확인
- [ ] Ubuntu 이미지 + **4GiB 이상 flavor**로 VM 1대 기동 (현재 m1.tiny=512MB는 Fig.1 대비가 약함).
      Ubuntu면 guest 안에 `mpstat`/`stress-ng` 있어 Fig.3 guest 샘플링도 쉬움
- [ ] 새 VM의 도메인명·PID·uuid·cgroup 경로 기록 (`sudo pgrep -a qemu-system`, `cat /proc/<pid>/cgroup`)
- [ ] exporter `/metrics`에 새 VM도 라벨 붙어 나오는지 확인 (`vms_discovered` 증가)

### G-2. guest 내부 접속 경로 (Fig.3의 "게스트 내부 CPU"용)
- [ ] `virsh console <dom>` 또는 floating IP + SSH 중 되는 것 확보
- [ ] guest 안에 샘플러 걸기 (백그라운드):
  ```
  # guest에서
  while :; do echo "$(date +%s.%N) $(grep '^cpu ' /proc/stat)"; sleep 2; done > /tmp/guest_stat.log &
  ```
  (Ubuntu면 `mpstat 2 > /tmp/guest_mpstat.log &` 도 병행)
- [ ] ⚠️ TCG는 paravirt steal 회계가 없어 guest가 "느리지만 바쁨"으로만 보임 → 5장 한계에 명시.
      Fig.3의 논지는 "guest·libvirt 관점에선 경합이 안 보인다"이므로 idle%/load 추이만으로 충분

### G-3. CPU 경합 유발 파드
- [ ] 매니페스트 작성 `/tmp/cpu-hog.yaml` (busybox, 코어당 1 프로세스, replicas로 강도 조절):
  ```yaml
  apiVersion: apps/v1
  kind: Deployment
  metadata: {name: cpu-hog, namespace: default}
  spec:
    replicas: 4                     # 노드 8 vCPU → 4부터, 단계적으로 6·8로
    selector: {matchLabels: {app: cpu-hog}}
    template:
      metadata: {labels: {app: cpu-hog}}
      spec:
        containers:
        - name: hog
          image: busybox:1.36
          command: ["sh","-c","while :; do :; done"]
          resources: {requests: {cpu: "900m"}, limits: {cpu: "1"}}
  ```
- [ ] ⚠️ 컨트롤 플레인 동일 노드 → replicas 4로 시작, OSH 파드 Evict/NotReady 나면 즉시 축소.
      `kubectl get pod -A | grep -v Running` 로 감시

### G-4. 샘플링 스크립트 (노드에서 실행)
- [ ] `/tmp/sample.sh`:
  ```bash
  #!/bin/bash
  POD_IP=$(kubectl -n openstack get pod -l app=qemu-exporter -o jsonpath='{.items[0].status.podIP}')
  DOM=${1:?domain name}
  CG=${2:?host cgroup path e.g. /sys/fs/cgroup/machine/qemu-2-instanceXXXX.libvirt-qemu}
  LV=$(kubectl -n openstack get pod -l application=libvirt -o jsonpath='{.items[0].metadata.name}')
  echo "ts,cpu_usage_s,cpu_pressure_some_s,runq_wait_s,mem_bytes,cg_cpu_usage_usec,kubepods_mem,node_memavail_kb"
  while true; do
    ts=$(date +%s.%N)
    m=$(curl -s "http://$POD_IP:9179/metrics")
    cu=$(awk '/^openstack_vm_cpu_usage_seconds_total/{print $2}' <<<"$m")
    cp=$(awk '/^openstack_vm_cpu_pressure_stall_seconds_total/{print $2}' <<<"$m")
    rq=$(awk '/^openstack_vm_sched_runqueue_wait_seconds_total/{print $2}' <<<"$m")
    mem=$(awk '/^openstack_vm_memory_usage_bytes/{print $2}' <<<"$m")
    cgu=$(sudo awk '/usage_usec/{print $2}' "$CG/cpu.stat")
    kp=$(sudo cat /sys/fs/cgroup/kubepods.slice/memory.current 2>/dev/null)
    na=$(awk '/MemAvailable/{print $2}' /proc/meminfo)
    echo "$ts,$cu,$cp,$rq,$mem,$cgu,$kp,$na"
    sleep 2
  done
  ```
  (`virsh domstats`는 루프에 안 넣음 — 2s마다 `kubectl exec`는 느리고 노드 부하. cpu.time은
  Phase E에서 `$CG/cpu.stat`와 0.3% 이내 일치 검증됨 → 루프는 cgroup 직독, virsh는 스냅샷만)
- [ ] 스모크: `bash /tmp/sample.sh <dom> <cg> | head -5` → 8개 컬럼 값 다 채워지는지

---

## Phase H — Fig.1 데이터 (자원 회계 왜곡, 스냅샷)

VM 부팅·안정화 후 (경합 없이):

- [ ] 3열 동시 수집 (한 시점):
  ```bash
  D=<host cgroup path>
  echo "qemu_machine_cgroup  $(sudo cat $D/memory.current)"
  echo "kubepods_slice       $(sudo cat /sys/fs/cgroup/kubepods.slice/memory.current)"
  echo "node_memtotal        $(awk '/MemTotal/{print $2*1024}' /proc/meminfo)"
  echo "node_memavailable    $(awk '/MemAvailable/{print $2*1024}' /proc/meminfo)"
  echo "node_used            $(( $(awk '/MemTotal/{print $2}' /proc/meminfo) - $(awk '/MemAvailable/{print $2}' /proc/meminfo) ))KB"
  curl -s http://$POD_IP:9179/metrics | grep '^openstack_vm_memory_usage_bytes'
  ```
- [ ] kubelet 회계 관점: `kubectl describe node ip-10-0-1-73 | grep -A8 "Allocated resources"`
- [ ] **cAdvisor가 machine cgroup 안 봄** 확인:
  ```bash
  TOKEN=$(kubectl -n openstack create token default 2>/dev/null || cat /var/run/secrets/kubernetes.io/serviceaccount/token)
  curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:10250/metrics/cadvisor | grep -c 'id="/machine'   # 0 이어야 함
  curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:10250/metrics/cadvisor | grep -c 'id="/kubepods'   # >0
  ```
- [ ] 저장: `fig1_snapshot.txt`. 필요하면 VM 2~3대로 반복해 몇 줄 확보

**해석 포인트**: `qemu_machine_cgroup + kubepods_slice ≈ node_used` 인데 `kubepods_slice`만으론 QEMU 몫이 통째로 빠짐 → 우리 exporter가 그 몫을 Nova 신원과 함께 채움.

---

## Phase I — Fig.3 데이터 (경합 은폐 시계열) ★핵심

시나리오: **t=0~60s baseline → t=60 hog 투입 → t=180 hog 제거 → t=240 종료** (총 4분, 2s 간격 = 120 샘플)

- [ ] 터미널 A (노드): `bash /tmp/sample.sh <dom> <cg> > fig3_run1.csv`
- [ ] 터미널 B (노드): 스냅샷 로그 — t=0/50/120/170/230 에 `virsh domstats` 5회
  ```bash
  for t in 0 50 120 170 230; do
    sleep_to=$t   # 대략, 수동으로 타이밍 맞춰도 됨
    echo "=== t~$t $(date +%s) ===" >> fig3_virsh.log
    kubectl -n openstack exec $LV -c libvirt -- virsh domstats <dom> --cpu-total >> fig3_virsh.log
  done
  ```
- [ ] guest(터미널 C 또는 백그라운드): G-2의 `/tmp/guest_stat.log` 계속 기록 중인지 확인
- [ ] 실행:
  - t=0: A·B·C 시작
  - t=60: `kubectl apply -f /tmp/cpu-hog.yaml` (정확한 벽시계 시각 메모)
  - t=180: `kubectl delete -f /tmp/cpu-hog.yaml` (시각 메모)
  - t=240: A 중단(Ctrl-C)
- [ ] 1차 확인 (CSV를 rate로):
  - `cpu_pressure_some_s` 델타/2s → t=60~180 구간 **급등**, 그 외 바닥
  - `runq_wait_s` 델타/2s → 마찬가지 급등
  - `cg_cpu_usage_usec` 델타 (=libvirt cpu_time 대응) → 경합 중 오히려 **비슷하거나 감소** (덜 돎), 급등 신호 없음
  - guest idle% → 크게 안 변함 ("guest는 모름")
- [ ] 급등이 약하면: hog replicas 4→6→8 올려서 `fig3_run2.csv`, `run3.csv` (basic_plan Day 9~10). OSH 안정성 감시하며
- [ ] 가장 깨끗한 run을 Fig.3 최종본으로 지정

**해석 포인트**: 같은 구간에서 guest·libvirt(cpu_time)는 평온한데 exporter의 cpu.pressure·runqueue_wait만 치솟음 = 경합이 기존 도구엔 안 보이고 우리 수집기엔 보임.

---

## Phase J — Table 1 + 정확도 + 오버헤드

### J-1. Table 1 커버리지 매트릭스 근거
각 셀 O/X의 근거를 한 줄씩:
- [ ] libvirt: `virsh domstats --cpu-total` 에 경합 지표 없음(cpu.time만) → 자원 O / 경합 X / Nova신원 O(도메인) / K8s조인 X
- [ ] cAdvisor: Phase H에서 `id="/machine` 0건 → 자원 X(QEMU 누락) / 경합 X / Nova신원 X / K8s조인 O(k8s만)
- [ ] 본 수집기: 6지표 노출 + node 라벨 → 자원 O / 경합 O(pressure·runqueue) / Nova신원 O / K8s조인 O(node 라벨)
- [ ] `coverage_matrix.txt` 로 정리

### J-2. 정확도 (1회)
- [ ] 같은 60s 윈도우에서: exporter `cpu_usage_seconds_total` 델타 vs `virsh domstats cpu.time` 델타/1e9
- [ ] 상대오차 = |Δexporter − Δvirsh| / Δvirsh. (Phase E 스냅샷에선 141.8 vs 141.36 → ~0.3%)
- [ ] `accuracy.txt` — "상대오차 < X%" 한 문장

### J-3. 오버헤드 (1회)
- [ ] exporter 자체 자원: 호스트에서 exporter 컨테이너 cgroup 찾아
  ```bash
  EXP_CG=$(sudo cat /proc/$(pgrep -f '/qemu-exporter'|head -1)/cgroup | sed 's/^0:://')
  sudo cat /sys/fs/cgroup$EXP_CG/cpu.stat /sys/fs/cgroup$EXP_CG/memory.current
  ```
  5초 간격 스크레이프 60s 걸며 usage_usec 델타 → 평균 mCPU, memory.current → RSS
- [ ] fio 영향: guest에서 `fio --name=r --rw=randread --size=512M --runtime=30 --time_based` 를
      (a) 스크레이프 중단 상태 (b) 5s 스크레이프 상태 각각 1회 → BW 차이
- [ ] `overhead.txt` — "exporter는 노드 CPU의 X%, RSS Y MiB, fio 영향 Z% 미만" 한 문장

---

## Phase K — 정리·백업

- [ ] 모든 CSV·txt·log를 `paper_data/` 한 디렉터리에 모으고 노드 밖으로 복사 (S3 또는 `kubectl cp` 대체 수단)
- [ ] 각 실험의 벽시계 타임스탬프·hog replicas·VM flavor를 `README` 한 장에 기록
- [ ] 환경 명세(4.1절) `uname -r` / `stat -fc %T /sys/fs/cgroup` / `helm list -n openstack` 재확인해 첨부
- [ ] `make down` 전에 raw 데이터 다 빠져나왔는지 확인 (재실험 = $0.8/h 다시)

---

## 주의사항

1. **단일 노드 = 컨트롤+데이터 동일**. hog replicas 낮게 시작, `kubectl get pod -A | grep -vE 'Running|Completed'` 상시 감시. OSH 흔들리면 즉시 `kubectl delete -f cpu-hog.yaml`
2. **TCG steal 회계 없음** → guest 관점 데이터는 "idle%/load가 크게 안 변한다" 수준으로만 해석. 5장 한계 명시
3. **`virsh`는 루프에 넣지 말 것** (kubectl exec 지연). cgroup 직독으로 대체, virsh는 스냅샷 검증용
4. **한 세션에 몰아서**. Fig.3 재실행 여러 번 예상되니 baseline→hog→해제 4분 사이클을 스크립트화해두면 반복이 쉬움
5. exporter 이미지에 셸 없음 → 모든 확인은 `curl podIP` + 호스트 `/proc/<pid>/root/...`

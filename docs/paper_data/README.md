# paper_data/ — qemu-exporter 논문 실측 데이터

> **이 디렉터리는 무엇인가**: OpenStack-Helm QEMU 가시성 논문(3페이지 프로토타입)의 Fig.1 / Fig.3 / Fig.3b /
> Table 1 / 정확도·오버헤드 문장에 쓸 **원시 측정 데이터** + Grafana 스크린샷 2장.
> Fig.1/Fig.3/정확도/오버헤드/Table1은 Prometheus 없이 `curl podIP:9179/metrics` + 호스트 cgroup 파일 + `virsh domstats`
> 스크립트 샘플링으로 모았고, **Fig.3b만** Prometheus + Grafana + kubelet(cAdvisor/resource) 스크레이프를 배포해 수집했다.
> **수집일**: 2026-09-08. **수집 환경은 이후 `make down`으로 파기됨** — 재현하려면 아래 "환경"·"재현 절차", 그리고 `deploy/monitoring/README.md`.
>
> **논문 집필 세션은 레포 루트 `paper_writing_handoff.md`를 먼저 볼 것** (논문 목적·진행·실험별 서술 방향·함정·장별 구조). 이 파일은 그 안의 "실험별 상세"에 대응하는 원시 데이터 문서.
> 계획서: `paper_data_tdl.md`. 배포·검증 이력: `aws_verification_tdl.md`.

---

## 0. 파일 목록

| 파일 | 실험 | 한 줄 |
|---|---|---|
| `fig1_snapshot.txt` | Fig.1 | 메모리 회계 3열(machine 2.54GB / kubepods 7.74GiB / node 8.88GiB) + cAdvisor `/machine` 0줄 |
| `fig3_run1.csv`, `fig3_run1_guest.log`, `fig3_markers.txt`, `fig3_run1_summary.txt` | Fig.3 본편 | cpu-hog×4, pressure 12.5×·runqueue 10×, guest/libvirt 무반응 |
| `fig3_run2.csv`, `fig3_run2_guest.log`, `fig3_run2_markers.txt` | Fig.3 dose-response | cpu-hog×8, load 323, pressure 122× (본 그림 아님) |
| `fig3b_exporter_{pressure,runq,cpu}.json` | Fig.3b | Prometheus 시계열 — 수집기 관점 (pressure 19×) |
| `fig3b_cadvisor_{vm_series,hogpods}.json` | Fig.3b | cAdvisor — VM 시계열 0, hog 파드 CPU는 봄 |
| `fig3b_kubelet_{node_cpu,node_minus_pods}.json` | Fig.3b | kubelet-resource — node CPU 2배, 피해자 신호 없음 |
| `fig3b_markers.txt`, `fig3b_mem_snapshot.txt`, `fig3b_summary.txt` | Fig.3b | 마커 / 메모리 스냅샷(미귀속 3.12GiB의 79%가 VM) / 요약 |
| `fig3b_grafana.png`, `fig3_grafana.png` | Fig.3b / Fig.3 | Grafana 스크린샷 (KEY 패널 이중축 / contention 패널) |
| `accuracy.txt` | 정확도 | vs `virsh cpu.time` 상대오차 0.001% |
| `overhead.txt` | 오버헤드 | 1.1 mCPU (0.11% of 1 core), RSS 8–10 MiB |
| `coverage.txt` | Table 1 | libvirt/cAdvisor/수집기 × 자원·경합·Nova·조인 근거 |
| `env_spec.txt` | 4.1절 | 커널·cgroup·PSI·OSH·`-accel` 명세 |
| `README.txt` | — | 수집 당시 짧은 메모 (이 파일이 확장판) |

---

## 1. 환경 (수집 시점)

| 항목 | 값 |
|---|---|
| 노드 | 단일 EC2 `m5.2xlarge` (8 vCPU / 32 GiB), hostname `ip-10-0-1-129` |
| OS / 커널 | Ubuntu 24.04.4 / `7.0.0-1012-aws`, cgroup v2 (`cgroup2fs`), PSI on, `kernel.sched_schedstats=1` |
| K8s / CRI | `v1.34.11` (kubeadm) / `containerd://2.2.1` / Calico |
| OpenStack-Helm | `2026.1.0` (keystone·glance·nova·neutron·placement·libvirt·mariadb·rabbitmq·memcached·openvswitch), ns `openstack` |
| libvirt | 파드 `libvirt-libvirt-default-pnsdg`, **cgroupfs 드라이버** (systemd-machined 미설치) → 도메인 cgroup = `/machine/qemu-<N>-<name>.libvirt-qemu`, 그 밑에 threaded `emulator/`·`vcpuN/`·`iothreadN/` |
| 가상화 | **KVM 없음, `-accel tcg`** (소프트웨어 에뮬레이션). EC2 자체가 VM이라 중첩 가상화 |
| project_id | `b03a4cf30a014ad1b7882b4fad2fbccf` (admin) |

### exporter 배포
- DaemonSet, ns `openstack`, 이미지 `docker.io/lylekim/qemu-exporter:dev` (`imagePullPolicy: Always`)
- `hostPID: true`, `privileged: false`, RO hostPath 마운트: `/proc`→`/host/proc`, `/sys/fs/cgroup`→`/host/sys/fs/cgroup`, `/var/run/libvirt`→`/var/run/libvirt`
- `/metrics` on `:9179`. 이미지가 `FROM scratch`라 셸 없음 → `kubectl exec` 불가, podIP로 curl.
- 노출 지표 6개: `openstack_vm_cpu_usage_seconds_total`(counter), `openstack_vm_memory_usage_bytes`(gauge),
  `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}`(counter), `openstack_vm_sched_runqueue_wait_seconds_total`(counter),
  `qemu_exporter_scrape_errors_total`, `qemu_exporter_vms_discovered`.
  공통 라벨: `node`, `instance_uuid`, `instance_name`, `flavor`, `project_id`.

### 실험 VM
| VM | 도메인 | uuid | flavor | 역할 |
|---|---|---|---|---|
| **fig-vm** | `instance-00000004` | `b00eecaa-8eb1-42c4-9ba7-3b643305db5f` | m1.medium (4 GiB / 2 vCPU), ubuntu-24.04 | 측정 대상 |
| test-vm | `instance-00000001` | `48704df8-9e53-4100-ade8-95a2949a57f9` | m1.tiny (512 MiB), cirros | idle 보조 (2대 발견 확인용) |

- fig-vm은 `demo-net`(10.10.10.0/24)에만 연결, **key/router/FIP 없음 → SSH 불가, 콘솔 경로만.**
- fig-vm cloud-init user-data(`#cloud-config` 첫 줄 필수)로 주입한 것:
  1. **상시 CPU 부하**: `yes > /dev/null` 2개 → 2 vCPU 풀 점유 (경합 실험에서 VM이 실제로 CPU를 다투도록)
  2. **guest 샘플러**: 2초마다 `/proc/stat`의 `cpu ` 라인을 `/dev/ttyS0`에 출력 → libvirt가 `console.log`에 기록.
     회수: `/var/lib/nova/instances/<uuid>/console.log`에서 `grep GUESTSTAT`.
- `openstack` CLI는 클러스터 안 `osc` 파드(openstack-client 이미지 + admin 시크릿)에서 `kubectl -n openstack exec osc -- openstack ...`로 실행.

---

## 2. 파일별 — 무엇을 해서 나왔나

### `fig1_snapshot.txt` — Fig.1 (자원 회계 왜곡, 한 시점 스냅샷)
VM 2대가 뜬 상태(부하 없이)에서 동시에 읽음:
- `machine` cgroup 메모리: `/sys/fs/cgroup/machine/memory.current` (+ VM별 `qemu-<N>-...libvirt-qemu/memory.current`)
- k8s 회계: `/sys/fs/cgroup/kubepods.slice/memory.current`
- 노드 실제: `/proc/meminfo`의 `MemTotal`/`MemAvailable`
- exporter가 Nova 신원으로 귀속한 값: `openstack_vm_memory_usage_bytes`
- cAdvisor가 `/machine`을 보는지: `kubectl get --raw "/api/v1/nodes/<node>/proxy/metrics/cadvisor"` 를 `id="/machine`·`id="/kubepods`·`instance-0000`으로 grep -c
- kubelet 회계 관점: `kubectl describe node <node>` 의 "Allocated resources"

**핵심 수치**:
| 항목 | 바이트 | ≈ |
|---|---|---|
| `machine` 전체 (VM 메모리, k8s 회계 밖) | 2,538,438,656 | 2.54 GB |
| ├ fig-vm (`instance-00000004`) | 1,877,954,560 | 1.88 GB |
| └ test-vm (`instance-00000001`) | 660,439,040 | 0.66 GB |
| `kubepods.slice` (k8s가 아는 값) | 8,309,219,328 | 7.74 GiB |
| 노드 실사용 (`MemTotal-MemAvailable`) | 9,531,031,552 | 8.88 GiB |
| kubelet "Allocated" memory | — | **272 MiB** |
| cAdvisor `id="/machine"` 라인 수 | — | **0** |
| cAdvisor `id="/kubepods"` 라인 수 | — | 11,553 |

→ 노드 실사용의 약 27%(2.54 GB)가 QEMU VM인데 스케줄러(272 MiB)·cAdvisor(`/machine` 0줄)·kubelet 어디에도 안 나온다. exporter만 `instance_uuid`/`flavor`/`project_id`와 함께 노출.
(주: fig-vm이 idle이라 4 GiB 중 1.88 GiB만 사용. 게스트 메모리를 채운 스냅샷은 안 찍음 — 더 큰 숫자를 원하면 재현 시 guest에서 메모리 부하 추가.)

### `fig3_run1.csv` + `fig3_run1_guest.log` + `fig3_markers.txt` + `fig3_run1_summary.txt` — Fig.3 (경합 은폐) **본편**
시나리오: baseline 60s → `cpu-hog` Deployment **replicas=4** (busybox `while :; do :; done`, 1개 = 1 코어) 투입 → 120s 후 제거 → 60s 관찰. 노드 load ≈ 7.
- `fig3_run1.csv`: `/tmp/sample.sh`가 2초마다 남긴 8컬럼.
  헤더 = `ts,cpu_usage_s,cpu_pressure_some_s,runq_wait_s,mem_bytes,cg_usage_usec,cg_press_some_total_us,node_load1`
  - `cpu_usage_s`/`cpu_pressure_some_s`/`runq_wait_s`/`mem_bytes` = exporter의 fig-vm 시계열 (누적값)
  - `cg_usage_usec` = `/sys/fs/cgroup/machine/qemu-4-instance-00000004.libvirt-qemu/cpu.stat`의 `usage_usec` (= libvirt `cpu.time`에 대응, 별도 검증 rel err 0.001%)
  - `cg_press_some_total_us` = 같은 cgroup `cpu.pressure`의 `some ... total=`
  - `node_load1` = `/proc/loadavg` 1분치
- `fig3_markers.txt`: `HOG_ON <epoch>` `HOG_OFF <epoch>` (run1: T0=1788847176, HOG_ON=1788847243, HOG_OFF=1788847382)
- `fig3_run1_guest.log`: fig-vm console.log에서 뽑은 `GUESTSTAT <epoch> cpu  <user> <nice> <system> <idle> <iowait> <irq> <softirq> <steal> <guest> <guest_nice>` 라인. VM **내부** 관점.
- `fig3_run1_summary.txt`: 아래 구간별 평균을 손으로 정리해 둔 것.

**핵심 수치 (구간별, delta per 2s)**:
| window | dP_some/2s | dRunq/2s | dCPUusec/2s | guest busy% | guest dsteal |
|---|---|---|---|---|---|
| baseline | 0.0117 | 0.0454 | 4,150,730 | ~100 | 0 |
| **contention** | **0.1467** (12.5×) | **0.4631** (10×) | **4,024,207** (−3%) | ~100 | 0 |
| recovery | 0.0067 | 0.0209 | 4,146,955 | ~100 | 0 |

→ 경합 구간에 exporter의 cpu.pressure는 12.5배, runqueue_wait는 10배 뛰는데, VM CPU 소비량은 −3%(libvirt 관점 거의 무변화)이고 guest는 busy% 100·steal 0으로 완전 무반응. recovery에서 pressure·runqueue 모두 baseline 복귀 → 인과 명확.

### `fig3_run2.csv` + `fig3_run2_guest.log` + `fig3_run2_markers.txt` — Fig.3 dose-response (보조, 그래프엔 안 씀)
run1과 동일 절차, `cpu-hog` **replicas=8**. 노드 load가 **323**까지 치솟은 병리적 과부하.
- markers: T0=1788847929, HOG_ON=1788847993, HOG_OFF=1788848146

**핵심 수치**:
| window | dP_some/2s | dRunq/2s | dCPUusec/2s |
|---|---|---|---|
| baseline | 0.0060 | 0.0176 | 4,147,718 |
| contention | **0.7307** (122×) | **1.5833** (90×) | **3,335,159** (−20%) |
| recovery | 0.0063 | 0.0182 | 4,148,764 |

→ 경쟁 압력을 키우면 pressure ~120×, VM 소비량 −20%까지. **load 323은 비현실적 과부하라 본 그림엔 부적합**, "더 심한 경합에서 신호가 비례해 커진다"는 한 문장 근거로만 사용.

### `accuracy.txt` — 정확도 (논문 1문장)
fig-vm이 상시 부하(2 vCPU 풀) 도는 상태에서 126초 윈도우:
- exporter `openstack_vm_cpu_usage_seconds_total` Δ = 257.1628 s
- `virsh domstats instance-00000004 --cpu-total` 의 `cpu.time` Δ / 1e9 = 257.1595 s
- 절대차 **0.0033 s**, 상대오차 **0.001%**
→ cgroup `cpu.stat` 기반 측정이 하이퍼바이저 자체 회계와 사실상 일치.

### `overhead.txt` — 오버헤드 (논문 1문장)
5초 간격으로 `/metrics`를 긁는 부하를 걸고 exporter 컨테이너 cgroup(`/sys/fs/cgroup/kubepods.slice/.../cri-containerd-*.scope`)을 73.6초 관찰:
- CPU **1.1 mCPU** (한 코어의 0.112%)
- RSS **7.8 MiB** (current), **10.0 MiB** (peak)
→ 노드 CPU의 ~0.014%. 읽기 전용·libvirt RO 소켓만 사용. **fio(디스크 영향)는 미측정** — "읽기 전용·5s 주기라 무시 가능"으로 갈음.

### `coverage.txt` — Table 1 (가시성 커버리지 매트릭스 근거)
- `virsh domstats instance-00000004` 전체 필드 덤프 + 경합 관련 필드 grep
- cAdvisor `id="/machine"` / `instance-0000` / `id="/kubepods"` 라인 수
- exporter `/metrics` 의 `openstack_vm*` + `qemu_exporter*` 전체

**표**:
| | 자원 사용 | 경합 | Nova 신원 | K8s 조인 |
|---|---|---|---|---|
| libvirt (`virsh domstats`) | ✓ `cpu.time`·balloon | **△** `vcpu.N.delay`만 (CLI 전용, per-vcpu, PSI 아님, 흔히 보는 `vcpu.wait`는 0) | ✓ 도메인 XML `<nova:instance>` | ✗ |
| cAdvisor / kubelet | ✗ (`/machine` 0줄, `instance-*` 0건) | ✗ | ✗ | ✓ (native) |
| **qemu-exporter** | ✓ | ✓ (PSI `cpu.pressure` + 스레드 합산 `runqueue_wait`, Prometheus) | ✓ (uuid·name·flavor·project 라벨) | ✓ (`node` 라벨) |

### `env_spec.txt` — 논문 4.1절 환경 명세
`uname -r`, `stat -fc %T /sys/fs/cgroup`, `ls /proc/pressure/`, `sysctl kernel.sched_schedstats`, `kubectl get nodes -o wide`, `helm list -n openstack`, libvirt 파드, cgroup 드라이버, `-accel` 종류.

### `README.txt` — 수집 당시 노드에서 만든 짧은 메모 (이 `README.md`가 확장판)

### `fig3b_*` — Fig.3b: Prometheus 파이프라인 시계열 (경합 중 kubelet/cAdvisor blind, qemu-exporter만 관측)
2026-09-08 저녁, 노드 재활용해 Prometheus + Grafana + kubelet(cAdvisor + resource) 스크레이프까지 배포한 뒤 수집.
매니페스트는 레포 `deploy/monitoring/`. 시나리오는 Fig.3 run1과 동일(`cpu-hog` replicas 4, baseline 30s → 부하 120s → 해제 60s).
Prometheus `query_range`(step 5s)로 뽑은 원본 JSON.

- `fig3b_markers.txt` — T0 / HOG_ON / HOG_OFF / END epoch
- `fig3b_summary.txt` — 아래 구간별 수치 + mem snapshot 정리
- `fig3b_mem_snapshot.txt` — 순간 쿼리 4개 (kubelet node mem vs pod mem vs exporter VM mem)
- `fig3b_exporter_pressure.json` — `rate(openstack_vm_cpu_pressure_stall_seconds_total{instance_name="instance-00000004"}[1m])`
- `fig3b_exporter_runq.json` — `rate(openstack_vm_sched_runqueue_wait_seconds_total{...}[1m])`
- `fig3b_exporter_cpu.json` — `rate(openstack_vm_cpu_usage_seconds_total{...}[1m])`
- `fig3b_cadvisor_vm_series.json` — `count(container_cpu_usage_seconds_total{job="kubelet-cadvisor",id=~"/machine.*"}) or on() vector(0)`
- `fig3b_cadvisor_hogpods.json` — `sum(rate(container_cpu_usage_seconds_total{job="kubelet-cadvisor",pod=~"cpu-hog.*",container!=""}[1m])) or on() vector(0)`
- `fig3b_kubelet_node_cpu.json` — `rate(node_cpu_usage_seconds_total[1m])` (kubelet-resource)
- `fig3b_kubelet_node_minus_pods.json` — `sum(rate(node_cpu_usage_seconds_total[1m])) - sum(rate(pod_cpu_usage_seconds_total[1m]))`

**핵심 수치 (base / load / after, hog=4, 8 vCPU 노드)**:
| 시계열 | base | load | after | |
|---|---|---|---|---|
| exporter cpu.pressure rate | 0.0035 | 0.0683 (peak ~0.10) | 0.0429 | ≈19× — VM이 경합에 시달림 |
| exporter runqueue_wait rate | 0.0120 | 0.2065 (peak ~0.31) | 0.1333 | ≈17× |
| exporter cpu (cores) | 2.041 | 1.96 (min 1.906) | 1.98 | −4~6% — 거의 평평 (libvirt 관점엔 신호 없음) |
| **cAdvisor: VM에 대한 시계열 수** | **0** | **0** | **0** | 전 구간 0 — kubelet/cAdvisor엔 그 VM이 없음 |
| cAdvisor: hog 파드 CPU | 0 | ~3.98 | 0 | 가해자는 봄 |
| kubelet: node CPU rate | 3.05 | 6.26 (peak ~7.3) | 4.44 | 노드가 바빠 보이지만 "누가 힘든지"는 모름 |
| kubelet: node − pods CPU | ~2.4 | ~2.4–3.0 (noisy) | ~2.7 | 피해자 신호 없음 |

**메모리 사각지대 스냅샷** (`fig3b_mem_snapshot.txt`):
kubelet `node_memory_working_set` 8.76 GiB, `sum(pod_memory_working_set)` 5.64 GiB → 파드로 설명 안 되는 3.12 GiB 중
**79%(2.47 GiB)가 VM** (`sum(openstack_vm_memory_usage_bytes)`), 이걸 인스턴스별로 이름 붙이는 건 qemu-exporter뿐.

주: `fig3b_kubelet_node_cpu.json`의 값이 5초 간격인데 두 번씩 반복되는 건 kubelet-resource 엔드포인트가
~10초 주기로 갱신되기 때문 (스크레이프는 5초). `after` 구간 pressure/runq가 base보다 높은 건 `[1m]` rate 창이
해제 직후에도 부하 구간 꼬리를 포함하기 때문.

### `fig3b_grafana.png` / `fig3_grafana.png` — Grafana 스크린샷 (라이브 데모)
Prometheus/Grafana 배포 상태에서 `cpu-hog` replicas 4를 다시 돌리며 라이브로 캡처.
- **`fig3b_grafana.png`** — 패널 "KEY: cAdvisor/kubelet blind, qemu-exporter sees it", **이중 축**.
  - 왼쪽 축(0–8, 코어/개수): 🟠 `kubelet: node CPU` 3→7.5, 🟡 `cAdvisor: hog pods CPU` 0→4, 🔵 `cAdvisor: series about the VM` **내내 0**
  - 오른쪽 축(0–0.15, 초/초): 🟢 `qemu-exporter: VM cpu.pressure` 0.003→0.10~0.15
  - 경합 구간(약 19:13–19:17)에 🟠🟡🟢 급등, 🔵 0 고정, hog 제거 후 셋 다 복귀. 🟢 가운데 dip은 실제 값(스케줄러 재분배).
  - **그림 캡션에 "우측 축 = cpu.pressure rate(초/초)" 명시 필요.**
- **`fig3_grafana.png`** — 패널 "Fig.3 - contention": pressure(some) rate / runqueue_wait rate / cpu_time(libvirt-equiv) rate 3줄. 축이 0~0.35라 단일 축으로 스파이크가 잘 보임.
- 논문 Fig.3(b)로 `fig3b_grafana.png` 한 장 + Fig.3로 `fig3_grafana.png` 또는 `fig3_run1.csv` 플롯.

---

## 3. 분석에 쓴 명령 (CSV → 구간별 평균 재도출용)

```bash
# 구간별 평균 (마커에서 HOG_ON/HOG_OFF epoch를 읽어 baseline / contention / recovery로 나눔)
M=fig3_run2_markers.txt; CSV=fig3_run2.csv
ON=$(awk '/HOG_ON/{print $2}' $M); OFF=$(awk '/HOG_OFF/{print $2}' $M)
awk -F, -v on=$ON -v off=$OFF 'NR>1{dp=$3-p3;dr=$4-p4;du=$6-p6;t=$1+0
  if(p1){ if(t>on-40&&t<on-5){bp+=dp;br+=dr;bu+=du;bn++}
    else if(t>on+10&&t<off-5){cp+=dp;cr+=dr;cu+=du;cn++}
    else if(t>off+10&&t<off+55){rp+=dp;rr+=dr;ru+=du;rn++} }
  p1=1;p3=$3;p4=$4;p6=$6}
  END{printf "baseline   %.4f %.4f %.0f\ncontention %.4f %.4f %.0f\nrecovery   %.4f %.4f %.0f\n",
    bp/bn,br/bn,bu/bn, cp/cn,cr/cn,cu/cn, rp/rn,rr/rn,ru/rn}' $CSV

# guest 관점 (busy% 와 steal 증분)
awk '/GUESTSTAT/{t=$2;u=$4;ni=$5;sy=$6;idl=$7;io=$8;st=$11
  b=u+ni+sy; tt=b+idl+io
  if(pt) printf "%d busy%%=%.1f dsteal=%d\n",t,100*(b-pb)/(tt-pt),st-ps
  pt=tt;pb=b;ps=st}' fig3_run1_guest.log
```

`ts` 컬럼은 epoch(소수 초). 지표 4개(`cpu_usage_s`,`cpu_pressure_some_s`,`runq_wait_s`,`cg_usage_usec`,`cg_press_some_total_us`)는 **누적값**이라 그림용으로는 인접 차분(rate)해서 쓴다. `mem_bytes`·`node_load1`은 순간값.

---

## 4. 재현 절차 요약 (환경 없어졌을 때)

1. OSH 단일 노드 재구축 → §1 "환경" 전제 재확인 (`aws_verification_tdl.md` Phase A/B). libvirt는 cgroupfs 드라이버여야 함.
2. `qemu-exporter` DaemonSet 재적용 (`deploy/daemonset.yaml`). `curl podIP:9179/metrics`로 `vms_discovered≥1`, `scrape_errors_total=0` 확인.
3. `osc` 파드로 4 GiB Ubuntu VM(`fig-vm`) 기동 + cloud-init user-data(부하 2×`yes` + guest 샘플러). 식별자(도메인·uuid·PID·cgroup 경로) 다시 확보.
4. Fig.1: §2 `fig1_snapshot.txt` 항목의 읽기들을 한 시점에.
5. Fig.3: `sample.sh`(§2 헤더 참조) 백그라운드 실행 → 60s 후 `cpu-hog`(replicas 4) apply → 120s 후 delete → 60s 후 종료. 마커 epoch 기록. console.log에서 `grep GUESTSTAT`.
6. 정확도·오버헤드·Table 1: §2 각 항목.
7. 산출물 노드 밖으로 복사 후 `make down`.

전체 상세: 레포 루트 `paper_data_tdl.md`.

---

## 5. 한계 / 주의 (논문 §5에 반영)

- **TCG(KVM 없음)**: 게스트에 paravirt steal 회계가 없어 `/proc/stat`의 `steal`이 경합 중에도 0. "게스트가 못 본다"의 근거이자 환경 의존성 — 5장에 명시.
- **run2(load 323)**: 단일 노드 과부하로 병리적. 본 그림은 run1(load ~7), run2는 dose-response 문장에만.
- **Fig.1은 fig-vm idle 상태** 스냅샷 (1.88/4 GiB). 메모리를 채우면 "안 보이는 양"이 더 커짐.
- **`runq_wait`(exporter)는 QEMU 전체 스레드(emulator+vcpu+iothread) 합산** → libvirt `vcpu.N.delay`(vcpu 스레드만)의 합보다 큼. 측정 범위가 다른 것이지 오류 아님.
- **fio 디스크 영향 미측정.**
- 정확도·오버헤드 측정은 run2 직후, fig-vm이 상시 2코어 부하 도는 상태에서 수행.
- **Fig.3b에서 "kubelet은 아무것도 못 본다"는 부정확** — 경합 중 `node_cpu_usage_seconds_total`은 오른다(3→6.26). 정확히는 "**피해자를 특정하는 신호가 없다**"(그 VM에 대한 시계열이 0). 서술 시 이 구분 유지.
- **`fig3b_grafana.png`는 이중 축** (cpu.pressure 초/초 vs CPU 코어, 스케일 4배+ 차이) — 캡션 필수.
- **libvirt에 `vcpu.N.delay`(스케줄러 run-delay)가 존재** → "경합 은폐"는 원시 데이터 부재가 아니라 관측 파이프라인(표준 exporter·telemetry 미탑재, PSI 아님, K8s 조인 불가)의 문제. Table 1은 △.

# 논문 집필 인수인계 — qemu-exporter / OpenStack-Helm QEMU 가시성

> **이 문서를 읽는 대상**: 이 프로젝트의 논문(3페이지, 2단 조판)을 **집필**할 새 세션.
> 구현·배포·실측은 전부 끝났고, 남은 건 2~5장 글쓰기 + 그림/표 렌더링뿐이다.
> 데이터 상세는 `paper_data/README.md`, 계획 원본은 `basic_plan.md`, 수집기 규칙은 `CLAUDE.md`.
> AWS 환경은 데이터 수집 후 `make down`으로 파기됨 — 재현이 필요하면 `deploy/monitoring/README.md`·`osh-node-env-handoff.md`.

---

## 1. 논문의 목적

### 한 문장 주장
**"OpenStack-Helm이 만든 QEMU VM은 쿠버네티스 자원 회계 밖에 있어 자원 경합이 은폐되며, OSH 차트·libvirt·Nova 설정을 하나도 바꾸지 않는 수집기로 이를 드러낼 수 있다."**

### 배경 (왜 문제인가)
- OpenStack-Helm은 OpenStack 컨트롤·데이터 플레인을 쿠버네티스 위에 배포한다. `nova-compute`가 `libvirt` 파드의 libvirtd에게 VM 기동을 시키고, libvirtd가 QEMU 프로세스를 띄운다.
- 그 QEMU 프로세스는 `kubepods.slice`(kubelet/cAdvisor가 회계하는 cgroup)가 **아니라** 호스트 루트의 `machine`(또는 `machine.slice`) cgroup에 놓인다. libvirt가 자기 cgroup 드라이버로 거기에 직접 만든다.
- 결과: **kubelet·cAdvisor·kube-state-metrics 어디에도 이 VM의 CPU/메모리/경합 지표가 없다.** VM이 CPU 경쟁에 굶어도 표준 K8s 모니터링은 조용하다.
- libvirt 자체(`virsh domstats`)는 *소비량*(cpu.time)은 주지만 경합 신호는 사실상 안 준다(뒤 Table 1의 △ 참조). 게다가 Prometheus/K8s 라벨과 조인이 안 된다.

### 기여 (contribution, 서론 3줄에 대응)
1. OSH 환경에서 QEMU가 K8s 회계 밖이라는 것을 **실측**으로 보이고(Fig.1), 그로 인해 CPU 경합이 은폐됨을 보인다(Fig.3/3b).
2. 호스트 cgroupfs/procfs + libvirt **읽기 전용** 소켓만으로 4개 지표를 읽어 Nova 신원(`instance_uuid`/`instance_name`/`flavor`/`project_id`)과 상관시키고 `node` 라벨을 달아 Prometheus로 노출하는 **무변경 DaemonSet 수집기**를 설계·구현한다(Fig.2, 3장).
3. libvirt 자체 회계 대비 정확도(상대오차 0.001%)와 오버헤드(노드 CPU 0.11%, RSS 10 MiB)를 측정하고, 기존 도구 대비 커버리지 매트릭스(Table 1)를 제시한다.

### 범위 (CLAUDE.md 고정)
- 3페이지 학부 논문 프로토타입. **4개 지표만**: `openstack_vm_cpu_usage_seconds_total`(Counter, cgroup `cpu.stat` usage_usec), `openstack_vm_memory_usage_bytes`(Gauge, cgroup `memory.current`), `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}`(Counter, cgroup `cpu.pressure` some total), `openstack_vm_sched_runqueue_wait_seconds_total`(Counter, `/proc/<pid>/task/*/schedstat` 2번째 필드 합).
- 수집기 자체 지표 2개: `qemu_exporter_scrape_errors_total`, `qemu_exporter_vms_discovered`.
- eBPF 금지, cgo 금지(순수 Go, `digitalocean/go-libvirt`), 읽기 전용, OSH/libvirt/Nova 무변경.

---

## 2. 전체 플랜 대비 진행 상황

`basic_plan.md`의 14일 TDL 기준. **글쓰기를 뺀 모든 것이 완료됨.**

| 항목 | 상태 | 근거 |
|---|---|---|
| 수집기 Go 코드 (3계층: 식별→수집→노출) | ✅ 완료·리뷰 통과·GitHub `LyleKim/qemu-exporter` main | 세션 이력 |
| AWS 단일 노드 OSH 배포 (m5.2xlarge, K8s 1.34, OSH 2026.1.0, TCG) | ✅ (3회 재구축, 최종 노드 `ip-10-0-1-129`, 이후 파기) | `aws_verification_tdl.md` |
| 전제 검증 G1 — QEMU가 kubepods.slice 밖인가 | ✅ 통과 — `/proc/<pid>/cgroup` = `0::/machine/qemu-N-instance-XXXX.libvirt-qemu/emulator` | `aws_verification_tdl.md` |
| 수집기 배포 + 값 검증 G2 | ✅ 통과 — 6지표 노출, `virsh domstats`와 0.001% 이내 일치 | `paper_data/accuracy.txt` |
| **Fig.1** 데이터 (자원 회계 왜곡, 스냅샷) | ✅ | `paper_data/fig1_snapshot.txt` |
| **Fig.3** 데이터 (경합 은폐, 시계열, 핵심) | ✅ run1(본편) + run2(dose-response) | `paper_data/fig3_run1*`, `fig3_run2*` |
| **Fig.3b** 데이터 (Prometheus 파이프라인, cAdvisor/kubelet blind) | ✅ | `paper_data/fig3b_*` + 스크린샷 |
| **Table 1** 커버리지 근거 | ✅ | `paper_data/coverage.txt` |
| 정확도 / 오버헤드 문장 | ✅ | `paper_data/accuracy.txt`, `overhead.txt` |
| Prometheus + Grafana 라이브 데모 + 스크린샷 | ✅ | `paper_data/fig3b_grafana.png`, `fig3_grafana.png` |
| 환경 명세 (4.1절) | ✅ | `paper_data/env_spec.txt` |
| **논문 2~5장 집필** | ❌ **← 이게 남은 일** | — |
| **Fig.2 아키텍처 다이어그램** | ❌ (데이터 불필요, 3계층 파이프라인 그림) | — |
| Fig.1/Fig.3/Fig.3b 최종 렌더링, Table 1 조판 | ❌ (원시 데이터는 다 있음) | — |
| 참고문헌 6~8편 | ❌ | — |

### 코드에 미커밋 상태로 남은 것 (구현 담당이 커밋 예정, 논문 3장 재료)
배포 중 발견해 고친 **환경 통합 이슈 3건** — 로컬 개발이 못 잡은, 실제 OSH 환경에서만 드러난 것들:
1. **libvirt cgroupfs 드라이버의 threaded 하위 cgroup.** 도메인 cgroup(`/machine/qemu-N-<name>.libvirt-qemu`) 밑에 `emulator/`·`vcpuN/`·`iothreadN/`가 threaded 타입으로 생기고, QEMU 대표 스레드는 `emulator/`에 놓인다. `/proc/<pid>/cgroup`은 그 리프를 가리키는데, 리프의 `cpu.stat`은 vcpu 사용량이 빠져 있고 `memory.current`는 아예 없다. → `ResolveCgroupPath`가 `cgroup.type`을 읽어 `threaded`면 `filepath.Dir()`로 도메인 cgroup까지 올라가도록 수정(`internal/libvirtsrc/cgrouppath.go`, 시그니처에 `hostSysFsCgroup` 추가).
2. **수집기 파드의 자체 cgroup 네임스페이스.** `/host/proc/<qemu>/cgroup`이 네임스페이스 밖 cgroup을 `0::/../../../../machine/...` 처럼 `/../` 프리픽스로 보고한다(`cgroup_namespaces(7)`). `filepath.Join("/host/sys/fs/cgroup", ...)`가 그 프리픽스에 먹혀 경로가 어긋난다. → `filepath.Clean(cg.Path)`로 정규화.
3. **startup config echo 로그** (`main.go`) — 디버깅에 결정적이었음.
→ 3장 "설계" 또는 5장 "한계/교훈"에 "무변경 수집기라도 대상 환경의 cgroup 배치·네임스페이스에 대한 이해가 필요하며, 이는 환경 독립적 발견이다" 한 문단으로.

---

## 3. 실험별 상세 — 무엇을, 어떤 데이터, 목적

모든 원시 파일은 `paper_data/` (Mac). 파일별 스키마·명령은 `paper_data/README.md`에 완비.
공통 환경: 단일 EC2 `m5.2xlarge`(8 vCPU/32 GiB), Ubuntu 24.04.4, 커널 `7.0.0-1012-aws`, cgroup v2, PSI on, `kernel.sched_schedstats=1`, K8s 1.34.11, containerd 2.2.1, OSH 2026.1.0, **KVM 없음(`-accel tcg`)**, 중첩 가상화(EC2 자체가 VM).
실험 VM: `fig-vm` = `instance-00000004`(uuid `b00eecaa-8eb1-42c4-9ba7-3b643305db5f`, m1.medium 4 GiB/2 vCPU, Ubuntu, `yes` 2개로 2 vCPU 상시 부하), `test-vm` = `instance-00000001`(cirros m1.tiny, idle). project `b03a4cf30a014ad1b7882b4fad2fbccf`.

### 3.1 Fig.1 — 자원 회계 왜곡 (스냅샷)
**목적**: QEMU VM의 메모리가 K8s의 어떤 회계 뷰에도 나타나지 않음을 한 시점 숫자로 보인다.
**방법**: VM 2대가 뜬 상태(부하 없이) 동시 측정 — `machine` cgroup `memory.current`, `kubepods.slice/memory.current`, `/proc/meminfo`, `kubectl describe node` Allocated, kubelet cAdvisor를 `id="/machine"`·`id="/kubepods"`로 grep 카운트.
**데이터**: `paper_data/fig1_snapshot.txt`
**핵심 수치**:
| 항목 | 값 |
|---|---|
| `machine` 전체 (VM 메모리, K8s 회계 밖) | 2,538,438,656 B ≈ **2.54 GB** (fig-vm 1.88 + test-vm 0.66) |
| `kubepods.slice` (K8s가 아는 값) | 8,309,219,328 B ≈ 7.74 GiB |
| 노드 실사용 (`MemTotal−MemAvailable`) | 9,531,031,552 B ≈ 8.88 GiB |
| kubelet "Allocated" memory | **272 MiB** |
| cAdvisor `id="/machine"` 라인 수 | **0** |
| cAdvisor `id="/kubepods"` 라인 수 | 11,553 |

**Prometheus 파이프라인 버전** (`paper_data/fig3b_mem_snapshot.txt`): kubelet `node_memory_working_set_bytes` 9,405,759,488(8.76 GiB) − `sum(pod_memory_working_set_bytes)` 6,056,726,528(5.64 GiB) = 3,349,032,960(3.12 GiB) 미귀속. 그중 `sum(openstack_vm_memory_usage_bytes)` = 2,650,984,448(2.47 GiB) = **79%가 VM**.
**논문 서술**: 노드 실사용의 약 27%가 QEMU VM인데 스케줄러(272 MiB)·cAdvisor(`/machine` 0줄)·kubelet 어디에도 없다. qemu-exporter만 그 몫을 `instance_uuid`/`flavor`/`project_id`와 함께 노출한다.
**정직하게**: fig-vm이 idle이라 4 GiB 중 1.88 GiB만 사용 — 게스트 메모리를 채우면 "안 보이는 양"이 더 커진다(안 함).

### 3.2 Fig.3 — 경합 은폐 (시계열, 핵심)
**목적**: 실제 CPU 경합이 걸릴 때 게스트 관점·libvirt 소비량 관점에는 신호가 없고, qemu-exporter의 pressure·runqueue 지표만 급등함을 시간축으로 보인다.
**방법**: 4분 시나리오 — baseline 60s → `cpu-hog` Deployment(busybox `while :; do :; done`, 코어당 1개) 투입 → 120s → 삭제 → 60s. `sample.sh`로 2초마다 CSV(exporter 4지표 + 호스트 `$D/cpu.stat` usage_usec[=libvirt cpu_time 대응] + `$D/cpu.pressure` + loadavg). guest는 `/proc/stat`의 `cpu` 라인을 시리얼 콘솔로 2초마다(→ `console.log` → `GUESTSTAT` 라인).
- **run1**: `cpu-hog` replicas 4, 노드 load ~7 → **본 그림**
- **run2**: replicas 8, 노드 load **323**(병리적 과부하) → dose-response 한 문장만
**데이터**: `paper_data/fig3_run1.csv`(8컬럼), `fig3_run1_guest.log`, `fig3_markers.txt`(run1 마커), `fig3_run1_summary.txt`; run2 동일(`fig3_run2*`).
**run1 핵심 수치** (구간 평균, per-2s delta):
| window | cpu.pressure(some)/2s | runqueue_wait/2s | VM cpu 소비 µs/2s | guest busy% / steal |
|---|---|---|---|---|
| baseline | 0.0117 | 0.0454 | 4,150,730 | ~100 / 0 |
| **contention** | **0.1467 (12.5×)** | **0.4631 (10×)** | **4,024,207 (−3%)** | ~100 / 0 |
| recovery | 0.0067 | 0.0209 | 4,146,955 | ~100 / 0 |
**run2**: contention에서 pressure 122×, runqueue 90×, VM cpu −20%. (load 323 = 비현실적, 스케일링 근거로만)
**논문 서술**: 경합은 *소비 감소*(−3%, libvirt cpu_time로는 안 보임)가 아니라 *대기*(pressure 12.5×, runqueue 10×)로 나타난다. 누적 시간 지표는 이를 못 잡는다. 게스트는 busy% ~100·steal 0(TCG는 paravirt steal 회계 없음)으로 완전 무반응. recovery에서 pressure·runqueue 모두 baseline 복귀 → 인과 명확.

### 3.3 Fig.3b — Prometheus 파이프라인 + cAdvisor/kubelet blind
**목적**: (a) 같은 경합을 Prometheus 시계열로 재현하고, (b) 경합 중 cAdvisor/kubelet에는 그 VM에 대한 시계열이 **0개**임을 직접 보인다 — "가해자는 보되 피해자는 못 본다".
**방법**: Prometheus(수집기 static + kubelet cAdvisor + kubelet resource, apiserver proxy 경유) + Grafana 배포. `cpu-hog` replicas 4 재실행. `query_range`(step 5s)로 7개 시계열 + 메모리 스냅샷 덤프.
**데이터**: `paper_data/fig3b_exporter_{pressure,runq,cpu}.json`, `fig3b_cadvisor_{vm_series,hogpods}.json`, `fig3b_kubelet_{node_cpu,node_minus_pods}.json`, `fig3b_markers.txt`, `fig3b_mem_snapshot.txt`, `fig3b_summary.txt`
**핵심 수치** (base / load / after):
| 시계열 (PromQL) | base | load | after | 의미 |
|---|---|---|---|---|
| `rate(openstack_vm_cpu_pressure_stall_seconds_total{type="some"}[1m])` | 0.0035 | **0.0683** (peak ~0.10) | 0.0429 | ≈**19×** — 수집기는 VM 경합을 봄 |
| `rate(openstack_vm_sched_runqueue_wait_seconds_total[1m])` | 0.0120 | **0.2065** (peak ~0.31) | 0.1333 | ≈**17×** |
| `rate(openstack_vm_cpu_usage_seconds_total[1m])` (코어) | 2.041 | 1.96 (min 1.906) | 1.98 | −4~6%, 사실상 평평 |
| `count(container_cpu_usage_seconds_total{job="kubelet-cadvisor",id=~"/machine.*"}) or on() vector(0)` | **0** | **0** | **0** | cAdvisor/kubelet엔 그 VM 시계열이 **아예 없음** |
| `sum(rate(container_cpu_usage_seconds_total{...pod=~"cpu-hog.*"...}[1m]))` (코어) | 0 | **~3.98** | 0 | cAdvisor는 **가해자**를 봄 |
| `rate(node_cpu_usage_seconds_total[1m])` (코어) | 3.05 | **6.26** (peak ~7.3) | 4.44 | 노드가 바빠 보임 (누가 힘든지는 모름) |
| `sum(rate(node_cpu[1m])) - sum(rate(pod_cpu[1m]))` (코어) | ~2.4 | ~2.4–3.0 (noisy) | ~2.7 | 피해자 신호 없음 |
**논문 서술**: 경합 중 kubelet(node CPU ↑)·cAdvisor(hog 파드 CPU ↑)는 "CPU가 소비되고 있다"와 "가해자가 누구인가"는 본다. 그러나 피해자인 QEMU VM에 대해서는 어떤 지표도 없다(cAdvisor 시계열 0). QEMU가 `machine` cgroup에 있어 kubelet/cAdvisor의 관측 대상(`kubepods`)에서 빠지기 때문. 오직 qemu-exporter만 그 VM의 정체를 정량화하며, Nova 신원 + `node` 라벨을 달아 Prometheus에서 K8s 워크로드와 `on(node)` 조인이 가능하다.

**스크린샷** (`paper_data/`):
- **`fig3b_grafana.png`** — Grafana 패널 "KEY: cAdvisor/kubelet blind, qemu-exporter sees it", **이중 축** 버전. 왼쪽 축(0–8 코어): 🟠 kubelet node CPU(3→7.5), 🟡 cAdvisor hog 파드(0→4), 🔵 cAdvisor의 VM 시계열 수(내내 0). 오른쪽 축(0–0.15 초/초): 🟢 qemu-exporter VM cpu.pressure(0.003→0.10~0.15). 경합 구간(약 19:13–19:17)에 🟠🟡🟢 급등, 🔵는 0 고정, hog 제거 후 셋 다 복귀. 캡션에 "우측 축 = cpu.pressure rate" 명시 필요.
- **`fig3_grafana.png`** — 패널 "Fig.3 - contention": 🟢 pressure(some) rate, 🔵 runqueue_wait rate, 🟠 cpu_time(libvirt-equiv) rate 3줄. pressure·runqueue만 솟고 cpu_time 평평.
- 🟢의 가운데 dip(0.10→0.05→0.10)은 노이즈가 아니라 실제 값(스케줄러가 잠깐 VM에 CPU를 더 준 구간), `fig3b_exporter_pressure.json`과 일치.

### 3.4 Table 1 — 가시성 커버리지 매트릭스
**목적**: libvirt / cAdvisor·kubelet / 본 수집기가 [자원 사용 · 경합 · Nova 신원 · K8s 조인]을 각각 제공하는지 정성 비교.
**데이터**: `paper_data/coverage.txt` (`virsh domstats` 전체 필드 덤프, cAdvisor grep 카운트, 수집기 `/metrics`).
| | 자원 사용 | 경합 | Nova 신원 | K8s 조인 |
|---|---|---|---|---|
| libvirt (`virsh domstats`) | ✓ `cpu.time`·balloon | **△** — `vcpu.N.delay`(스케줄러 run-delay)는 있으나 `virsh` CLI 전용, per-vcpu, PSI 아님, 흔히 보는 `vcpu.wait`는 0 | ✓ 도메인 XML `<nova:instance>` | ✗ |
| cAdvisor / kubelet | ✗ (`id="/machine"` 0줄, `instance-*` 0건) | ✗ | ✗ | ✓ (native) |
| **qemu-exporter** | ✓ | ✓ (PSI `cpu.pressure` + 스레드 합산 `runqueue_wait`, Prometheus) | ✓ (uuid·name·flavor·project 라벨) | ✓ (`node` 라벨) |
**주의(정직)**: libvirt에 `vcpu.N.delay`가 있으므로 "경합이 은폐된다"는 *원시 데이터의 부재*가 아니라 *관측 파이프라인*(표준 exporter·telemetry 어디에도 안 실림, PSI 아님, K8s 조인 불가)의 문제로 서술해야 정확하다. Table 1의 △가 그 뜻.

### 3.5 정확도 (4장, 한 문장)
**목적**: cgroup 기반 CPU 측정이 하이퍼바이저 자체 회계와 일치함을 검증.
**데이터**: `paper_data/accuracy.txt` — 126초 윈도우에서 수집기 `openstack_vm_cpu_usage_seconds_total` Δ = 257.1628 s vs `virsh domstats cpu.time` Δ = 257.1595 s → 절대차 0.0033 s, **상대오차 0.001%**.

### 3.6 오버헤드 (4장, 한 문장)
**목적**: 수집기가 저비용임을 보인다.
**데이터**: `paper_data/overhead.txt` — 5초 주기 스크레이프에서 수집기 컨테이너 cgroup 기준 **CPU 1.1 mCPU(한 코어의 0.11%), RSS 7.8 MiB(peak 10 MiB)**. 노드 CPU의 ~0.014%. 읽기 전용·libvirt RO 소켓만 사용. **fio(디스크 영향)는 미측정** — "읽기 전용·RO 소켓·5초 주기라 무시 가능"으로 갈음.

### 3.7 환경 명세 (4.1절)
**데이터**: `paper_data/env_spec.txt` — `uname -r`, cgroup 타입, `/proc/pressure/`, `sched_schedstats`, `kubectl get nodes -o wide`, `helm list -n openstack`, cgroup 드라이버(cgroupfs, systemd-machined 미설치), `-accel tcg`.

---

## 4. 논문 구조 (basic_plan.md 기준) + 데이터 배치

2단 조판, **본문 3페이지 이내**, 그림·표 4개 이하.

| 장 | 분량 | 내용 | 쓸 데이터 |
|---|---|---|---|
| **1 서론** | 0.45p | 위 "한 문장 주장" + 기여 3줄. 관련 연구 압축 문단(cAdvisor raw cgroup / libvirt-exporter·Ceilometer / KubeVirt 각 1문장, "경합 미제공 또는 K8s 미조인"으로 마무리) | — |
| **2 배경** | 0.85p | (2.1) libvirt는 소비량만, 경합 신호 사실상 없음 — 배포 방식 무관. (2.2) cAdvisor가 QEMU를 누락 — OSH 고유(machine cgroup). (2.3) KubeVirt는 문헌만(1문장 인용, 배포 안 함) | `coverage.txt`(virsh 필드), Fig.1 |
| **3 설계** | 0.65p | 3계층 파이프라인: 식별(libvirt RO 소켓 `qemu+unix` + pidfile + `/proc/<pid>/cgroup`) → 수집(4개 소스 파일 파싱) → 노출(`prometheus.Collector`, 라벨링). `log/slog`, `CGO_ENABLED=0`. **Fig.2 아키텍처 다이어그램(그려야 함)**. + 2장의 환경 통합 이슈(emulator threaded cgroup climb, cgroupns `/../`) 한 문단 | 코드, `deploy/` |
| **4 평가** | 0.65p | Fig.1(회계 왜곡), Fig.3(+3b, 경합 은폐), Table 1(커버리지), 정확도 1문장, 오버헤드 1문장. 4.1 환경 명세 | 3.1~3.7 전부 |
| **5 한계** | 0.15p | 요약 2문장 + 한계 2문장(전제의 환경 의존성 — 단일 노드/중첩 가상화, 단일 노드 규모) + 향후 과제 1문장(eBPF·GPU는 언급 안 함) | — |

**그림·표 (4개)**:
- **Fig.1** — 메모리 3열 막대 (machine=2.54GB / kubepods=7.74GiB / node used=8.88GiB) + "cAdvisor `/machine` 시계열 0" 주석. `fig1_snapshot.txt`에서 렌더.
- **Fig.2** — 아키텍처 다이어그램. **데이터 불필요, 새로 그려야 함.** 식별→수집→라벨링·노출 3단계 + hostPath RO 마운트 + libvirt RO 소켓.
- **Fig.3** — 경합 시계열. `fig3_run1.csv`에서 3~4줄 플롯(pressure rate, runqueue rate, cpu_time rate; 선택적으로 guest busy%). 경합 구간 음영. **또는** Grafana 스크린샷 `fig3_grafana.png` / `fig3b_grafana.png` 사용. Fig.3b를 Fig.3(b)로 합치거나 별도.
- **Table 1** — 커버리지 매트릭스 (3.4의 표). `coverage.txt`.

**참고문헌 6~8편**: OSH 문서, libvirt cgroups 문서, PSI 커널 문서(`Documentation/accounting/psi.rst`), schedstat 문서, KubeVirt, Ceilometer, cAdvisor, (선택) prometheus/procfs.

---

## 5. 집필 시 주의 — 정직하게 밝힐 것 / 흔한 함정

1. **run2(load 323)는 병리적 과부하** — 본 그림은 run1(load ~7). run2는 "경쟁을 키우면 신호가 비례해 커진다(pressure 122×)" 한 문장 근거로만.
2. **Fig.1은 idle VM 스냅샷** (fig-vm 1.88/4 GiB) — 더 큰 수치 가능하지만 안 함.
3. **TCG(KVM 없음)는 paravirt steal 회계가 없음** — 게스트 `/proc/stat`의 `steal`이 경합 중에도 0. 이건 5장 한계이면서 동시에 "게스트가 못 본다"의 근거. 양쪽으로 서술.
4. **`runqueue_wait`(수집기)는 QEMU 전체 스레드(emulator+vcpu+iothread) 합산** — libvirt `vcpu.N.delay`(vcpu 스레드만)의 합보다 크다. 측정 범위가 다른 것이지 오류 아님.
5. **libvirt에 `vcpu.N.delay`가 존재** — "경합 은폐"는 원시 데이터 부재가 아니라 관측 파이프라인의 문제로. Table 1은 △.
6. **Fig.3/3b의 pressure 곡선 가운데 dip**은 실제 값(스케줄러 재분배). 노이즈로 설명하지 말 것.
7. **"kubelet은 아무것도 못 본다"는 부정확** — node CPU는 오른다. 정확히는 "**피해자를 특정하는 신호가 없다**". Fig.3b의 🟠(node CPU ↑)가 그 증거.
8. **Grafana KEY 패널은 이중 축** — cpu.pressure(초/초, 0–0.15)와 CPU(코어, 0–8) 스케일이 4배+ 차이. 캡션에 우측 축 명시.
9. **단일 노드 + 컨트롤·데이터 플레인 공존 + 중첩 가상화** — 5장 한계에 반드시.
10. **수집기 이미지가 `FROM scratch`** — 셸 없음. "디버깅은 로그로만" 정도는 3장에 한 줄 가능.

---

## 6. 파일 인덱스

### `paper_data/` (Mac: `~/Desktop/openstack_helm_observility/paper_data/`)
| 파일 | 내용 |
|---|---|
| `README.md` | **데이터 상세 문서 (파일별 스키마·명령·수치·재현 절차·한계).** 먼저 읽을 것 |
| `README.txt` | 수집 당시 노드에서 만든 짧은 메모 |
| `fig1_snapshot.txt` | Fig.1 — 메모리 회계 3열 + cAdvisor grep |
| `fig3_run1.csv` / `fig3_run1_guest.log` / `fig3_markers.txt` / `fig3_run1_summary.txt` | Fig.3 본편 (replicas 4) |
| `fig3_run2.csv` / `fig3_run2_guest.log` / `fig3_run2_markers.txt` | Fig.3 dose-response (replicas 8, load 323) |
| `fig3b_exporter_{pressure,runq,cpu}.json` | Fig.3b — 수집기 시계열 (Prometheus query_range) |
| `fig3b_cadvisor_{vm_series,hogpods}.json` | Fig.3b — cAdvisor 시계열 (VM 시계열=0, hog 파드 CPU) |
| `fig3b_kubelet_{node_cpu,node_minus_pods}.json` | Fig.3b — kubelet resource 시계열 |
| `fig3b_markers.txt` / `fig3b_mem_snapshot.txt` / `fig3b_summary.txt` | Fig.3b 마커·메모리 스냅샷·요약 |
| `fig3b_grafana.png` / `fig3_grafana.png` | Grafana 스크린샷 (KEY 패널 이중축 / contention 패널) |
| `accuracy.txt` | 정확도 0.001% |
| `overhead.txt` | 오버헤드 1.1 mCPU / 10 MiB |
| `coverage.txt` | Table 1 근거 |
| `env_spec.txt` | 4.1절 환경 명세 |

### 레포 루트
| 파일 | 내용 |
|---|---|
| `CLAUDE.md` | 수집기 목적·범위·절대 규칙·4지표 정의 |
| `basic_plan.md` | 원래 14일 TDL + 논문 구조(장별 분량, 게이트) |
| `paper_data_tdl.md` | 데이터 추출 상세 계획 (완료됨) |
| `aws_verification_tdl.md` | AWS 배포·검증 이력 (G1/G2 통과) |
| `osh-node-env-handoff.md` | OSH 환경 재구축용 인수인계 (환경 전제 8개) |
| `prometheus_grafana_tdl.md` | Prometheus/Grafana 배포 계획 |
| `deploy/monitoring/` | Prometheus/Grafana 매니페스트 4개 + README (Fig.3b 재현용) |
| `deploy/{Dockerfile,daemonset.yaml,scrape-config.md}` | 수집기 배포 |
| `internal/{libvirtsrc,cgroupsrc,procsrc,collector}/` | 수집기 코드 |
| `cmd/qemu-exporter/main.go` | 진입점 (3계층 조립, :9179 /metrics) |
| `HANDOFF.md` / `.superpowers/sdd/2026-09-05-qemu-exporter/` | 구현 단계 SDD 이력 |

---

## 7. 남은 작업 체크리스트 (집필 세션)

- [ ] 1장 서론 (0.45p) — 주장 + 기여 3줄 + 관련연구 압축 문단
- [ ] 2장 배경 (0.85p) — libvirt 경합 부재 / cAdvisor QEMU 누락 / KubeVirt 인용
- [ ] 3장 설계 (0.65p) — 3계층 파이프라인 서술 + **Fig.2 다이어그램 작성** + 환경 통합 이슈 문단
- [ ] 4장 평가 (0.65p) — Fig.1 / Fig.3(+3b) / Table 1 / 정확도·오버헤드 문장 / 4.1 환경
- [ ] 5장 한계 (0.15p) — 단일 노드·중첩 가상화·TCG steal 부재
- [ ] Fig.1 렌더 (막대 3열, `fig1_snapshot.txt`)
- [ ] Fig.2 렌더 (아키텍처, 새로 그림)
- [ ] Fig.3 렌더 (`fig3_run1.csv` 플롯 또는 `fig3_grafana.png`/`fig3b_grafana.png`)
- [ ] Table 1 조판 (`coverage.txt` → 3.4의 표)
- [ ] 초록
- [ ] 참고문헌 6~8편
- [ ] 2단 조판 → 3페이지 이내 확인, 그림·표 4개 이하 확인
- [ ] 최종 점검: "libvirt는 게스트 값만 준다" 같은 부정확 문장 없나 / `nr_throttled`에 논지 안 걸었나 / 메트릭 타입(Counter/Gauge) 정확 / 전제의 환경 의존성이 5장에 있나

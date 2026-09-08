# qemu-exporter

OpenStack-Helm 환경에서 kubelet·cAdvisor가 보지 못하는 **QEMU VM의 자원·경합 지표**를
Prometheus로 노출하는 읽기 전용 DaemonSet 수집기. OSH 차트·libvirt·Nova 설정을 하나도 바꾸지 않는다.

> 3페이지 학부 논문의 프로토타입. `github.com/LyleKim/qemu-exporter`, Go 1.25, `CGO_ENABLED=0`.

---

## 문제의식

OpenStack-Helm은 OpenStack을 쿠버네티스 위에 올린다. `nova-compute`의 요청으로 `libvirt` 파드의
libvirtd가 QEMU 프로세스를 띄우는데, 이 프로세스는 쿠버네티스가 자원을 회계하는 `kubepods.slice`
cgroup이 **아니라** 호스트 루트의 `machine` cgroup에 놓인다 — libvirt가 자기 cgroup 드라이버로
거기에 직접 만들기 때문이다.

그 결과:

- **자원 회계에서 통째로 누락.** kubelet·cAdvisor·kube-state-metrics 어디에도 이 VM의 CPU·메모리
  지표가 없다. 실측에서 노드 사용 메모리의 약 27%가 어떤 쿠버네티스 뷰에도 안 잡혔다.
- **경합이 은폐된다.** VM이 CPU 경쟁에 굶어도 표준 모니터링은 조용하다. 경합은 *소비량 감소*가 아니라
  *대기*로 나타나는데, libvirt가 주는 `cpu.time`(누적 소비량)으로는 안 보이고, cAdvisor에는 그 VM에
  대한 시계열 자체가 존재하지 않는다.
- **쿠버네티스와 조인이 안 된다.** libvirt의 per-vCPU 지표(`virsh domstats`)는 CLI 전용이고 Prometheus
  라벨이 없어 "어느 노드의 어느 쿠버네티스 워크로드와 경쟁 중인가"를 알 수 없다.

## 해결방안

호스트의 cgroupfs/procfs를 읽고 libvirt **읽기 전용** 소켓으로 Nova 메타데이터를 조회해 상관시킨 뒤
Prometheus 형식으로 노출하는 DaemonSet. **3계층 파이프라인**:

1. **식별** (`internal/libvirtsrc`) — RO 소켓으로 활성 도메인 목록 → 도메인 XML에서 flavor/project →
   pidfile에서 PID → `/proc/<pid>/cgroup`을 읽어 실제 cgroup 경로 확정 (systemd scope 이름 파싱에 의존 안 함)
2. **수집** (`internal/cgroupsrc`, `internal/procsrc`) — cgroup `cpu.stat`·`memory.current`·`cpu.pressure`,
   `/proc/<pid>/task/*/schedstat` 파싱
3. **노출** (`internal/collector`) — `prometheus.Collector`로 조립, Nova 신원 + `node` 라벨 부착

노출 지표:

| 지표 | 타입 | 출처 |
|---|---|---|
| `openstack_vm_cpu_usage_seconds_total` | Counter | cgroup `cpu.stat` usage_usec |
| `openstack_vm_memory_usage_bytes` | Gauge | cgroup `memory.current` |
| `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}` | Counter | cgroup `cpu.pressure` some total (PSI) |
| `openstack_vm_sched_runqueue_wait_seconds_total` | Counter | `/proc/<pid>/task/*/schedstat` 필드 2 합 |
| `qemu_exporter_scrape_errors_total` / `qemu_exporter_vms_discovered` | Counter / Gauge | 수집기 자체 |

공통 라벨: `node`, `instance_uuid`, `instance_name`, `flavor`, `project_id`

원칙: eBPF 금지 · cgo 금지(정적 빌드, `digitalocean/go-libvirt`) · 읽기 전용(`libvirt-sock-ro`만) ·
DaemonSet `hostPID: true`, `privileged: false`, 호스트 경로 RO 마운트.

## 이 프로젝트가 해결하는 문제

- **자원 회계 왜곡** — QEMU VM의 메모리·CPU를 Nova 인스턴스별로 노출해, 쿠버네티스가 못 보는 노드 자원 몫을 채운다.
- **경합 은폐** — `cpu.pressure`(PSI)와 runqueue 대기시간으로 VM의 CPU 경합을 정량화. 실측에서 경합 시
  pressure가 **12–19배** 급등하는 동안 libvirt `cpu.time`은 −3%, cAdvisor의 그 VM 시계열은 **0**.
- **쿠버네티스와의 조인** — `node` 라벨로 Prometheus에서 같은 노드의 쿠버네티스 워크로드와 `on(node)` 조인 가능.

실측 데이터·그래프는 `paper_data/`, 논문 초안 재료는 `paper_writing_handoff.md`.

## 빠른 시작

```bash
CGO_ENABLED=0 GOOS=linux go build ./cmd/qemu-exporter
docker buildx build --platform linux/amd64 -f deploy/Dockerfile -t <repo>/qemu-exporter:dev --push .
kubectl -n openstack apply -f deploy/daemonset.yaml

POD_IP=$(kubectl -n openstack get pod -l app=qemu-exporter -o jsonpath='{.items[0].status.podIP}')
curl -s http://$POD_IP:9179/metrics | grep '^openstack_vm'
```

환경변수: `HOST_PROC`(기본 `/host/proc`), `HOST_SYS_FS_CGROUP`(`/host/sys/fs/cgroup`),
`LIBVIRT_SOCK`(`/var/run/libvirt/libvirt-sock-ro`), `NODE_NAME`(downward API).

## 디렉터리 구조

```
cmd/qemu-exporter/main.go      진입점 — 설정 읽고 3계층 조립, :9179 /metrics, graceful shutdown
internal/
  libvirtsrc/                  식별 계층 — 어느 VM이 누구고 어디(PID/cgroup)에 있나
    source.go                    Domain 구조체 + DomainSource 인터페이스
    client.go                    libvirt RO 소켓 연결 (qemu+unix, dial timeout)
    domain.go                    도메인 XML → flavor/project, pidfile → PID
    cgrouppath.go                /proc/<pid>/cgroup → 실제 cgroup 경로 (threaded 리프면 도메인 cgroup까지 상승, cgroupns /../ 정규화)
    cache.go                     DomainSource 구현체 — 매 스크레이프 활성 도메인 diff, 새 VM만 비싼 조회, RPC timeout
  cgroupsrc/                   cgroup 파일 파서 3종 (libvirt 모름) + testdata/
  procsrc/schedstat.go         /proc/<pid>/task/*/schedstat 대기시간 합산
  collector/collector.go       prometheus.Collector — VM마다 파서 호출, 단위 변환, 라벨 부착, VM 1대 실패는 스킵+카운트
deploy/
  Dockerfile                   멀티스테이지 (golang:1.25-alpine → scratch)
  daemonset.yaml               ns openstack, hostPID, RO hostPath 마운트 3개
  scrape-config.md             Prometheus relabel + PromQL 예시
  monitoring/                  논문 실험용 Prometheus + Grafana 스택 (매니페스트 4개 + README)
docs/superpowers/{specs,plans}/  구현 SDD 설계·계획
paper_data/                    논문 실측 데이터 + Grafana 스크린샷 (paper_data/README.md에 상세)
```

문서 (루트):

| 파일 | 역할 |
|---|---|
| `CLAUDE.md` | 수집기 목적·범위·절대 규칙·4지표 정의 (구현 계약) |
| `basic_plan.md` | 논문 14일 실행 계획 + 장별 구조 |
| `paper_writing_handoff.md` | **논문 집필 세션 진입점** — 목적·진행·실험별 서술 방향·함정·파일 인덱스 |
| `aws_verification_tdl.md` | AWS 단일 노드 배포·검증 이력 (전제 검증 G1, 값 검증 G2) |
| `paper_data_tdl.md` | 데이터 추출 절차 (완료) |
| `osh-node-env-handoff.md` | OSH 환경 재구축용 — 환경 전제 8개, 실험 요구사항 |
| `prometheus_grafana_tdl.md` | Prometheus/Grafana 배포 계획 |
| `HANDOFF.md` | 구현 단계 인수인계 메모 |

## 현재 상태

수집기 구현·리뷰 완료 (main 반영). AWS 단일 노드 OSH 2026.1.0에 배포·검증 완료 —
`virsh cpu.time` 대비 정확도 **0.001%**, 오버헤드 **1.1 mCPU / RSS 10 MiB**.
논문 실측(Fig.1 자원 회계 왜곡 / Fig.3·3b 경합 은폐 / Table 1 커버리지) 완료, 환경은 `make down`.
**남은 작업: 논문 집필** — `paper_writing_handoff.md` 참조.

## 대상 환경 / 한계

단일 노드 검증(컨트롤+데이터 플레인 공존), 중첩 가상화(EC2 내부), KVM 없는 QEMU/TCG.
cgroup v2 (unified) 및 커널 PSI(4.20+) 가정. TCG는 paravirt steal 회계가 없어 게스트 내부에서는
경합이 드러나지 않는다 — 이는 한계이자 "게스트가 못 본다"의 근거.

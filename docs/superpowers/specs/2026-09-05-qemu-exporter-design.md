# qemu-exporter 설계

## 배경

OpenStack-Helm에서 libvirtd가 생성한 QEMU 프로세스는 `kubepods.slice`가 아닌
`machine.slice`혹은 `machine`에 배치되어 kubelet/cAdvisor 자원 회계에서 누락된다. 이 프로젝트는
호스트 cgroupfs/procfs에서 QEMU의 자원·경합 지표를 읽고, libvirt 소켓으로 Nova
메타데이터를 조회해 상관시킨 뒤 Prometheus 형식으로 노출하는 exporter를 만든다.

3페이지 학부 논문의 프로토타입이다. 범위 확장보다 4개 지표의 정확한 동작이 우선이다.
전체 컨텍스트·절대 규칙·구현 범위 표는 `CLAUDE.md`에 있으며, 이 spec은 그 규칙을
어기지 않는 선에서 아키텍처를 확정한다.

## 구현 범위

`CLAUDE.md`의 표와 동일 (여기 재정의하지 않음):
- `openstack_vm_cpu_usage_seconds_total` (Counter) — cgroup `cpu.stat`의 `usage_usec`
- `openstack_vm_memory_usage_bytes` (Gauge) — cgroup `memory.current`
- `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}` (Counter) — cgroup `cpu.pressure`의 `some total`
- `openstack_vm_sched_runqueue_wait_seconds_total` (Counter) — `/proc/<pid>/task/*/schedstat` 2번째 필드 합계
- `qemu_exporter_scrape_errors_total` (Counter), `qemu_exporter_vms_discovered` (Gauge)
- 공통 라벨: `node`, `instance_uuid`, `instance_name`, `flavor`, `project_id`

## 대상 환경

`infra-cloud-kr/openstack-on-kubernetes-terraform` — AWS EC2 m5.2xlarge 단일 노드,
Ubuntu 24.04, Kubernetes 1.34 + Calico, OpenStack-Helm 2026.1.0, KVM 없이 QEMU/TCG,
SSM 전용 접근. 상세는 `CLAUDE.md`의 "대상 환경" 절 참조.

**미확인 전제** (Day 1에 실측 확인 예정, 이 spec은 확인됐다고 가정하고 설계):
cgroup v2 unified hierarchy, 커널 PSI 지원(`/proc/pressure/cpu`), `kernel.sched_schedstats=1`.
전제가 깨지면 Day 2 게이트에서 주제 자체가 선회하므로 이 spec도 재작성 대상이 된다.

## 기술 스택

Go 1.22+ / `prometheus/client_golang` / `prometheus/procfs` / `digitalocean/go-libvirt`
(순수 Go RPC, cgo 아님).

모듈 경로: `github.com/LyleKim/qemu-exporter` (저장소는 구현 착수 시 생성).
로깅: 표준 라이브러리 `log/slog`.

## 아키텍처

3계층 파이프라인. Prometheus가 `/metrics`를 긁을 때마다 한 번 전체가 실행된다.

```
libvirtsrc (식별)  →  cgroupsrc + procsrc (수집)  →  collector (노출)
```

계층 간 결합은 순수 데이터(`Domain` struct)와 인터페이스로만 이루어진다. `cgroupsrc`,
`procsrc`는 libvirt를 전혀 모른다. `collector`는 `libvirtsrc.Cache`의 구체 타입이
아니라 `DomainSource` 인터페이스에 의존한다 (아래 "로컬 검증 전략" 참고).

## 파일 구조

```
cmd/qemu-exporter/main.go     — flag 파싱, 계층 조립, graceful shutdown, :9179
internal/libvirtsrc/
  client.go      — go-libvirt 연결. `qemu+unix:///system?socket=<LIBVIRT_SOCK>` URI로
                   `libvirt.ConnectToURI` 호출 (기본 /var/run/libvirt/libvirt-sock-ro)
  domain.go      — Domain{UUID,Name,Flavor,ProjectID,PID,CgroupPath} + <nova:instance> XML 파싱
                   + PID 확보(`<LIBVIRT_SOCK 디렉터리>/qemu/<domain-name>.pid` 파일을 읽음 —
                   libvirt 공개 API엔 PID 반환 RPC가 없어 실무 표준인 pidfile 방식을 씀)
  cgrouppath.go  — PID → /proc/<pid>/cgroup 읽어서 실제 cgroup 상대경로 확정
  cache.go       — uuid→Domain 캐시. 매 스크레이프마다 가벼운 `ConnectListAllDomains`로
                   현재 활성 uuid 집합을 확인해 신규는 `GetXMLDesc`로 채우고, 더는
                   활성이 아닌 uuid는 evict (아래 "캐시 무효화" 참고)
  source.go      — DomainSource 인터페이스 정의 + Cache가 이를 구현
  testdata/domain.xml
internal/cgroupsrc/
  cpustat.go     — cpu.stat 파서 (usage_usec만)
  memcurrent.go  — memory.current 파서
  psi.go         — cpu.pressure 파서 (some 행의 total만), 파일 부재 시 sentinel error
  testdata/{cpu.stat,memory.current,cpu.pressure}
internal/procsrc/
  schedstat.go   — /proc/<pid>/task/*/schedstat 2번째 필드 합계
  testdata/schedstat
internal/collector/
  collector.go   — prometheus.Collector 구현 (Describe/Collect), DomainSource + 파서 함수들을 조립
  collector_test.go — 로컬 통합 테스트 (아래 참고)
deploy/
  Dockerfile         — 멀티스테이지, CGO_ENABLED=0, 최종 scratch/distroless
  daemonset.yaml      — hostPID:true, privileged:false, 3개 읽기 전용 마운트
  scrape-config.md    — relabel_configs 예시 + PromQL 예시
```

## 핵심 인터페이스

```go
// internal/libvirtsrc
type Domain struct {
    UUID, Name, Flavor, ProjectID string
    PID        int
    CgroupPath string // /proc/<pid>/cgroup 에서 확정한, sys/fs/cgroup 기준 상대경로
}

type DomainSource interface {
    Domains() ([]Domain, error)
}

// internal/cgroupsrc
func ParseCPUStat(r io.Reader) (usageUsec uint64, err error)
func ParseMemoryCurrent(r io.Reader) (bytes uint64, err error)
func ParsePSISome(r io.Reader) (totalUsec uint64, err error) // 파일 없으면 ErrPSIUnsupported

// internal/procsrc
func ParseSchedstatRunqueueWait(r io.Reader) (waitNanos uint64, err error)
func SumTaskSchedstat(hostProc string, pid int) (waitNanos uint64, err error) // /proc/<pid>/task/*/ 순회
```

## 데이터 흐름 (스크레이프 1회)

1. `collector.Collect()` 호출
2. `DomainSource.Domains()` 호출 — 내부적으로 가벼운 `ConnectListAllDomains(활성만)`로
   현재 실행 중인 uuid 집합 확인
3. 캐시에 없는 신규 uuid만 `GetXMLDesc` + pidfile 읽기로 `Domain` 채워서 캐시에 적재
   (비싼 RPC·파일 I/O는 캐시 미스 때만), 캐시에는 있는데 더는 활성이 아닌 uuid는 evict
4. 각 `Domain`의 `CgroupPath`·`PID`로 4개 파서 순차 호출
5. 파서 하나 실패 → 해당 VM만 스킵 + `slog.Warn` + `qemu_exporter_scrape_errors_total` 증가, 나머지 계속
6. 성공한 값을 `prometheus.MustNewConstMetric`으로 emit + exporter 자체 메트릭 2개 emit

## 캐시 무효화

별도의 이벤트 구독이나 타이머를 두지 않는다. 매 스크레이프의 3번 단계(활성 uuid
집합과 캐시를 대조)가 곧 무효화 메커니즘이다 — 신규 VM은 등장한 첫 스크레이프에서
캐시에 적재되고, 사라진 VM은 사라진 첫 스크레이프에서 evict된다. "비싼 RPC는 VM
생명주기 이벤트당 1번만" 목표를 lifecycle 이벤트 구독 없이 동일하게 달성한다.
(go-libvirt의 이벤트 구독 API는 신뢰성 있게 검증하지 못해 채택하지 않음 — 스크레이프
주기가 5~15초로 짧아 "최대 1주기 지연"은 실질적 차이가 없다.)

## 에러 처리

- VM 1대의 수집 실패가 전체 스크레이프를 실패시키지 않는다 (`CLAUDE.md` 절대 규칙)
- libvirt 연결 자체가 끊기면: 이번 스크레이프는 exporter 자체 메트릭만 emit, 다음
  스크레이프에서 재연결 시도. 재연결 로직은 `client.go`에 둔다
- PSI 파일 부재(커널/cgroup 미지원)는 `ErrPSIUnsupported`로 다른 실패와 구분해서 로그
- 로그 spam 방지 장치는 넣지 않는다. 실패 빈도는 `scrape_errors_total`로 추적되고,
  실제로 로그가 시끄러워지면 그때 추가한다 (YAGNI)

## 로컬 검증 전략 (AWS 비용 절감)

개발·검증은 3단계로 나누고, AWS는 마지막 단계에서만 켠다.

**1단계 — 파서 단위 테스트.** 각 파서를 합성 testdata(커널 문서 형식 기준, Day 1
이후 실제 AWS 호스트 샘플로 교체)로 검증. `go test ./...`, 인프라 불필요.

**2단계 — 로컬 통합 테스트.** `collector`가 `libvirtsrc.Cache`가 아니라
`DomainSource` 인터페이스에 의존하므로, `collector_test.go`에서 `[]Domain{...}`을
하드코딩한 fake `DomainSource`를 만든다. `t.TempDir()`로 가짜 `/proc`, cgroup 트리를
구성(1단계와 같은 testdata 내용을 파일로 기록)하고, fake source + 그 임시 디렉터리를
collector에 물린 뒤 `httptest`로 실제 `/metrics` 응답을 GET해서 텍스트를 검증한다.
libvirt·Docker·AWS 전부 불필요.

**3단계 — 컨테이너 빌드 확인.** `docker build`로 이미지를 만들고, 2단계와 동일한
가짜 디렉터리를 볼륨 마운트해 `docker run`한 뒤 `curl localhost:9179/metrics`로
확인. Dockerfile·정적 빌드·바이너리 동작까지 로컬에서 끝낸다.

**AWS에서만 확인 가능한 것** (Day 1/4/6에 짧게):
실제 go-libvirt 소켓 연결(`client.go`, 코드량이 작아 리스크 낮음), 실제 DaemonSet
배포 + hostPID + 실제 QEMU PID/cgroup 경로, Fig.1/Fig.3 실측 데이터.

## 테스트 전략 요약

프레임워크 없이 표준 `testing` 패키지만 사용. 파서 단위 테스트 + collector 통합
테스트(2단계) 두 층으로 충분하며, 그 이상의 테스트 인프라(mock 라이브러리, 별도
fixture 패키지)는 만들지 않는다.

## 배포

- `deploy/Dockerfile`: 멀티스테이지, `CGO_ENABLED=0 GOOS=linux go build`, 최종
  스테이지는 scratch 또는 distroless
- `deploy/daemonset.yaml`: `hostPID: true`, `privileged` 미사용, 볼륨 3개 전부
  `readOnly: true` (`/sys/fs/cgroup`, `/proc`, `/var/run/libvirt`), env로
  `NODE_NAME`(downward API), `HOST_PROC`, `HOST_SYS_FS_CGROUP`, `LIBVIRT_SOCK` 주입
- `deploy/scrape-config.md`: `node` 라벨이 `kube_node_info`와 `on(node)`로 조인되는
  relabel_configs + 검증용 PromQL 예시 1개

## 스코프 밖

`io.stat`, `memory.stat` 세부, `nr_throttled`, full PSI(avg10/avg60/avg300),
per-tid 라벨, 웹 UI, 설정 파일 포맷, Grafana 대시보드 JSON, Nova REST API/Keystone
호출. `CLAUDE.md`와 동일하며 이 spec에서 확장하지 않는다.

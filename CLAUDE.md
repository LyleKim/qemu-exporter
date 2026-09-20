# qemu-exporter

## 프로젝트 목표
- **목표1 (완료)** — qemu-exporter: machine.slice에 숨겨진 QEMU 자원·경합 지표를 읽어
  Prometheus로 노출. 3페이지 학부 논문 프로토타입, KIPS ASK 제출 완료.
- **목표2 (확장 트랙, 진행 중)** — live-migration-webhook: 목표1이 노출하는 경합 지표(PSI)가
  임계치를 넘으면 Alertmanager→webhook으로 Nova live migration을 선제적으로 트리거.
  상세 규칙은 `## live-migration-webhook (확장 트랙)` 섹션 참조.

**이 문서의 다른 모든 섹션은 별도 명시가 없는 한 목표1(qemu-exporter 본체)에 한정된다.**
목표2에서만 달라지는 규칙은 각 섹션 안에 "(목표2 예외)"로 표시했다.

## 목적
OpenStack-Helm에서 libvirtd가 생성한 QEMU 프로세스는 `kubepods.slice`가 아닌
`machine.slice`에 배치되어 kubelet/cAdvisor 자원 회계에서 누락된다.
이 수집기는 호스트 cgroupfs/procfs에서 QEMU의 자원·경합 지표를 읽고,
libvirt 소켓으로 Nova 메타데이터를 조회해 상관시킨 뒤 Prometheus 형식으로 노출한다.

3페이지 학부 논문의 프로토타입이다. **범위 확장보다 4개 지표의 정확한 동작이 우선이다.**

## 구현 범위 — 이 4개만 만든다
| 메트릭 | 타입 | 출처 |
|---|---|---|
| openstack_vm_cpu_usage_seconds_total | Counter | cgroup cpu.stat 의 usage_usec |
| openstack_vm_memory_usage_bytes | Gauge | cgroup memory.current |
| openstack_vm_cpu_pressure_stall_seconds_total{type="some"} | Counter | cgroup cpu.pressure 의 total |
| openstack_vm_sched_runqueue_wait_seconds_total | Counter | /proc/<pid>/schedstat 2번째 필드 |

Exporter 자체 지표: qemu_exporter_scrape_errors_total (Counter),
qemu_exporter_vms_discovered (Gauge)

공통 라벨: node, instance_uuid, instance_name, flavor, project_id

**io.stat, memory.stat 세부, nr_throttled, full PSI, per-tid 라벨은 구현하지 않는다.**
요청받지 않은 지표를 추가하지 말 것.

## 절대 규칙
- **eBPF 금지.** 필요한 지표는 전부 cgroupfs/procfs 파일로 얻는다.
- **cgo 금지.** CGO_ENABLED=0 정적 빌드가 되어야 한다.
  → libvirt 접근은 github.com/digitalocean/go-libvirt (순수 Go RPC) 사용.
  → libvirt.org/go/libvirt (공식 cgo 바인딩) 사용 금지.
- **읽기 전용.** 어떤 파일도 쓰지 않고, libvirt는 libvirt-sock-ro 만 사용한다.
  (목표2 예외) webhook 리시버는 Nova live-migration 트리거가 본업이므로 이 규칙 대상이
  아니다. qemu-exporter 본체는 목표2 도입 이후에도 read-only를 그대로 유지한다.
- **cgroup 경로를 scope 이름 파싱으로 유추하지 않는다.**
  반드시 /proc/<pid>/cgroup 을 읽어 실제 경로를 확정한다.
  (systemd \x2d 이스케이프 규칙에 의존하지 않는 것이 논문의 설계 논거다)
- **openstack-helm 차트, libvirt 설정, Nova 설정을 변경하지 않는다.**
  (목표2 예외) live-migration을 켜려면 이 설정들을 바꿀 수밖에 없다 — 목표2 트랙에서는
  허용한다. 단, **qemu-exporter 코드는 이 설정 변경과 무관하게 기존 동작을 그대로
  유지해야 한다** (설정이 바뀌어도 exporter 쪽 수정 없이 동작해야 함).
- Nova REST API / Keystone 호출 금지. 메타데이터는 libvirt domain XML 에서만 얻는다.
  (목표2 예외) 이 규칙은 qemu-exporter의 메타데이터 수집 경로에만 적용된다. webhook
  리시버는 live-migration 트리거를 위해 Nova REST API / Keystone 호출을 허용한다.
  단 호출 범위는 **live-migration API로 한정** — 다른 Nova API 호출은 금지.

## 대상 환경

### exporter 검증 환경 (목표1, 완료)
실험 대상은 `infra-cloud-kr/openstack-on-kubernetes-terraform` 레포로 AWS 위에 구축했다.
- EC2 **m5.2xlarge** (8 vCPU/32GB), Ubuntu 24.04(Noble), 100GB gp3 — **단일 노드**
- Kubernetes **1.34** + Calico (kubeadm, `make up/ready`로 유저데이터 자동 설치)
- OpenStack-Helm **2026.1.0** — Keystone/Glance/Nova/Neutron/Placement, 전부 이 한 노드에 배포
- **KVM 없이 QEMU/TCG**(소프트웨어 에뮬레이션). 관측 지표는 실행 방식과 무관하지만,
  TCG는 고부하를 쉽게 만들어 Fig.3(경합 재현)에는 오히려 유리
- **중첩 가상화**: EC2 인스턴스 자체가 VM이고 그 안에 K8s+libvirt+QEMU가 또 있다.
  "파드 안에서 본 cgroup 경로 vs 호스트에서 본 cgroup 경로"가 다를 수 있으므로
  Day 1에 반드시 양쪽을 대조 확인한다 (5장 한계에도 명시)
- **컨트롤 플레인 + 데이터 플레인이 동일 노드** — Fig.3 경합 강도는 단계적으로 올릴 것,
  OSH 자체를 불안정하게 만들지 않도록 주의
- 접근은 **SSM Session Manager만** (SSH 없음)
- 비용 약 **$0.8/시간** — 실험은 세션 단위로 몰아서 하고 끝나면 `make down`
- cgroup v2 (unified hierarchy) 가정, 커널 PSI 지원(4.20+) 가정 — 둘 다 Day 1에 실측 확인
  (`stat -fc %T /sys/fs/cgroup`, `ls /proc/pressure/`)
- Kubernetes DaemonSet, hostPID: true, privileged: false
- 호스트 경로 read-only 마운트: /host/sys/fs/cgroup, /host/proc, /var/run/libvirt

### live-migration-webhook 실험 환경 (목표2)
위 단일 노드 구성을 기반으로 노드를 하나 더 붙인 **2노드** 구성. 같은 terraform 레포를
포크해서 확장한다 (원본은 단일 노드 전용, 노드 수 변수 없음).
- **node-a**: 기존 단일 노드 그대로 — 컨트롤 플레인 + 데이터 플레인 + qemu-exporter
- **node-b**: 신규 EC2 m5.2xlarge, **컴퓨트 전용** (nova-compute + libvirt Pod만)
- 공유 스토리지 없음 → Nova live-migration은 `--block-migrate`로 수행 (로컬 qcow2 디스크째 복사)
- 보안그룹에 node-a↔node-b 마이그레이션 포트(TCP 49152–49215) 상호 허용 추가
- 비용 약 **$1.6/시간** (m5.2xlarge × 2) — 역시 세션 단위로 몰아서 하고 양쪽 다 `make down`
- 상세 절차는 `live_migration_tdl.md` 참조

## 기술 스택
Go 1.22+ / prometheus/client_golang / prometheus/procfs / digitalocean/go-libvirt
모듈 경로: `github.com/LyleKim/qemu-exporter` (저장소는 구현 착수 시점에 생성)
로깅: `log/slog` (표준 라이브러리, 의존성 추가 없음)

## 코드 규칙
- 호스트 경로 프리픽스는 환경변수로 주입: HOST_PROC, HOST_SYS_FS_CGROUP, LIBVIRT_SOCK
- 파서는 파일 경로가 아니라 io.Reader 를 받는다 (테스트 가능성)
- 파서마다 실제 파일 샘플 testdata 기반 단위 테스트를 둔다
- 누적값은 Counter, 순간값은 Gauge. 타입을 틀리면 rate() 결과가 왜곡되어
  논문 그래프가 그대로 잘못된다
- VM 한 대의 수집 실패가 전체 스크레이프를 실패시키면 안 된다.
  실패는 로그 + qemu_exporter_scrape_errors_total 증가로 처리하고 계속 진행한다
- 마이크로초/나노초 원본은 모두 초 단위로 변환해 노출한다

### webhook 리시버 코드 규칙 (목표2)
- qemu-exporter 바이너리에 섞지 않는다 — 별도 cmd/바이너리로 분리
- Nova API 호출은 live-migration 트리거 용도로만 사용 (다른 오퍼레이션 금지)
- 같은 VM에 이미 진행 중인 마이그레이션이 있으면 중복 트리거하지 않는다
- 실패는 로그로 남기고 프로세스는 계속 동작 (exporter의 부분 실패 허용 원칙과 동일)

## live-migration-webhook (확장 트랙) — 목표2

**목적**: 목표1(qemu-exporter)이 노출하는 CPU 경합 지표(PSI)가 임계치를 넘으면,
쿠버네티스 스케줄러나 cAdvisor를 수정하지 않고 외부 파이프라인(Alertmanager→webhook)만으로
Nova live migration을 선제적으로 트리거해 OOM/자원고갈 전에 VM을 회피시킨다.

**범위 — 이것만 만든다**
- Alertmanager 룰 1개: `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}`의
  rate가 임계치를 일정 시간 초과하면 alert
- webhook 리시버 바이너리 1개: alert의 `instance_uuid` 라벨을 받아 Nova
  live-migration API 호출

**허용되는 Nova API 호출 범위**: live-migration 트리거뿐. 그 외 Nova API(서버 생성/삭제,
flavor 변경 등)는 호출하지 않는다.

**비용**: 2노드 구성이라 시간당 약 $1.6 (목표1의 $0.8/h 대비 2배). 세션 단위로 몰아서
실험하고 끝나면 양쪽 노드 모두 `make down`.

**하지 말 것 (목표2)**
- 자동 롤백, 재시도 정책, 알림 채널 다변화 등 요청받지 않은 자동화 기능
- 웹 UI, 대시보드 JSON (목표1과 동일한 제약)

상세 구현 절차(terraform 확장, Nova/libvirt 설정, 실험 시나리오)는
`live_migration_tdl.md` 참조.

## 하지 말 것 (목표1)
- 요청하지 않은 지표·기능·추상화 계층 추가
- 웹 UI, 설정 파일 포맷, Grafana 대시보드 JSON
- 리팩터링 제안 (동작이 우선이다)

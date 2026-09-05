# qemu-exporter

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
- **cgroup 경로를 scope 이름 파싱으로 유추하지 않는다.**
  반드시 /proc/<pid>/cgroup 을 읽어 실제 경로를 확정한다.
  (systemd \x2d 이스케이프 규칙에 의존하지 않는 것이 논문의 설계 논거다)
- **openstack-helm 차트, libvirt 설정, Nova 설정을 변경하지 않는다.**
- Nova REST API / Keystone 호출 금지. 메타데이터는 libvirt domain XML 에서만 얻는다.

## 대상 환경
실험 대상은 `infra-cloud-kr/openstack-on-kubernetes-terraform` 레포로 AWS 위에 구축한다.
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

## 하지 말 것
- 요청하지 않은 지표·기능·추상화 계층 추가
- 웹 UI, 설정 파일 포맷, Grafana 대시보드 JSON
- 리팩터링 제안 (동작이 우선이다)

# 논문 전체 실행 TDL — OpenStack-Helm QEMU 가시성 (2주 완주판)

> 대상 레포: infra-cloud-kr/openstack-on-kubernetes-terraform (OSH 2026.1.0, 단일 노드
> m5.2xlarge, KVM 없이 QEMU/TCG). 하루 4시간 × 14일 = 56시간 예산.
> 주장 하나: **"QEMU가 K8s 자원 회계 밖에 있어 경합이 은폐되며, 무변경 수집기로
> 이를 드러낼 수 있다."** 이 주장을 돕지 않는 작업은 전부 삭제 대상.
>
> **게이트 3개**: Day 2(전제 성립?) · Day 6(4지표 노출?) · Day 10(Fig.3 재현?).
> 각 게이트는 그날 안에 판정하고 다음 날 계획을 즉시 조정한다.

---

## 1주차 — 실측 + 수집기 구현

### Day 1 (4h) — 환경 기동 + 전제 검증 ★최우선

**환경 기동 (1.5h)**
- [ ] `make init` → `make up` → `make ready` → `make osh-deploy` → `make osh-vm`
- [ ] SSM으로 노드 접속, CirrOS 부팅 확인
- [ ] 환경 명세 수집(4.1절 재료): `uname -r`, `stat -fc %T /sys/fs/cgroup`,
      `kubectl version`, `helm list -n openstack`, `virsh version`, `ls /proc/pressure/`

**전제 검증 (2h) — 이 환경에서 QEMU가 kubepods.slice 밖인가**
- [ ] `pgrep -a qemu-system-x86_64` 로 QEMU PID 확보
- [ ] ★ 양쪽 비교:
      `cat /proc/<PID>/cgroup` (호스트) vs
      `kubectl exec -n openstack <libvirt-pod> -- cat /proc/<PID>/cgroup` (파드 안)
- [ ] 배치 메커니즘: `ps -o ppid=,cmd= -p <PID>`, `cat /proc/<libvirtd_PID>/cgroup`,
      `readlink /proc/<PID>/ns/cgroup` vs `readlink /proc/1/ns/cgroup`
- [ ] machine1 원인: `systemctl status systemd-machined`,
      `kubectl exec ... -- ls -l /run/dbus/system_bus_socket`
- [ ] 모든 출력 텍스트 저장

**PSI/schedstat 활성화 (0.5h)**
- [ ] `cat <QEMU_CG>/cpu.pressure` 값 갱신 확인
- [ ] `sysctl kernel.sched_schedstats` 확인, 0이면 `sysctl -w kernel.sched_schedstats=1`

### ▶ Day 2 게이트 (아래 Day 2 종료 시 판정)

### Day 2 (4h) — Fig.1 데이터 + 게이트 판정

- [ ] 메모리 큰 VM 기동 (Ubuntu 이미지 8~16GiB flavor)
- [ ] 세 관점 동시 수집:
      `<QEMU_CG>/memory.current` / `kubepods.slice/memory.current` / `free -b`
- [ ] kubelet 회계: `kubectl describe node <node> | grep -A5 "Allocated resources"`
- [ ] "QEMU 메모리가 kubepods.slice 회계에 누락"을 수치로 대조
- [ ] **Fig.1 초안 렌더링** (machine.slice / kubepods.slice / 노드 실사용 3열)
- [ ] **게이트 판정**: QEMU가 kubepods.slice 밖 확인 → 진행 /
      안에 있음 → 주제를 "libvirt cgroup 배치 환경 의존성 분석"으로 선회

### Day 3 (4h) — 베이스라인 + 2장 집필

- [ ] cAdvisor raw cgroup whitelist 한계 확인 (whitelist해도 Nova 신원 못 붙음 — 한 문단)
- [ ] `virsh domstats <domain> --cpu-total` → libvirt는 소비량만, 경합 부재 재확인
- [ ] KubeVirt는 문헌 확인만 (1문장 인용, 배포 안 함)
- [ ] **2.1~2.3절 초안 작성 (0.85p 목표)** — 2.3절은 준비해둔 초안 활용,
      "libvirt 경합 부재(배포 무관) + cAdvisor QEMU 누락(OSH 고유)" 두 축으로

### Day 4 (4h) — 수집기: 식별 계층

- [ ] libvirt 소켓(`libvirt-sock-ro`) 연결 → 도메인 목록 조회 (Go)
- [ ] 도메인 XML `<nova:instance>` 파싱 → instance_uuid/name/flavor/project_id
- [ ] 도메인 → PID → `/proc/<pid>/cgroup` 역방향 매핑 (명명 규칙 비의존)
- [ ] lifecycle 이벤트 기반 UUID 캐시 골격

### Day 5 (4h) — 수집기: 수집 계층

- [ ] 4개 파서 구현: `cpu.stat`(usage_usec) / `memory.current` /
      `cpu.pressure`(PSI some) / `/proc/<pid>/schedstat`(runqueue wait)
- [ ] 각 파서 단위 테스트

### Day 6 (4h) — 노출·배포 + 게이트 판정

- [ ] `prometheus.Collector` 구현, 라벨 부착
      (`node`, `instance_uuid`, `instance_name`, `flavor`, `project_id`)
- [ ] DaemonSet 매니페스트 작성:
      hostPath로 `/sys/fs/cgroup`·`/proc` readOnly 마운트, `hostPID: true`,
      **privileged 불요** 확인 (읽기 전용)
- [ ] 배포 후 `/metrics` 에 4개 지표 노출 확인
- [ ] **게이트 판정**: 4지표 정상 노출 → 진행 /
      실패 → 지표를 2개(cpu.pressure, schedstat)로 축소하고 실험 범위도 축소

### Day 7 (4h) — Prometheus 연동 + 3장 집필

- [ ] Prometheus scrape 설정, `node` 라벨 relabel
- [ ] PromQL `on(node)` 조인 동작 확인 (VM PSI ↔ 같은 노드 k8s 워크로드)
- [ ] **3.1~3.3절 초안 작성 (0.65p 목표)** + Fig.2 아키텍처 스케치
      (식별 → 수집 → 라벨링·노출 3단계)

> **1주차 종료 체크**: 2장·3장 초안 존재, 수집기 `/metrics` 동작, Fig.1 초안 완성.

---

## 2주차 — 핵심 실험 + 집필 + 제출

### Day 8 (4h) — Fig.3 실험 설계·준비

- [ ] `sysctl kernel.sched_schedstats=1` 재확인
- [ ] 시나리오 확정: t=0 정상 → t=60 CPU 점유 파드 투입(오버커밋) → t=180 제거
- [ ] 기록 대상 4종: 게스트 내부 CPU / libvirt `cpu_time` / `cpu.pressure` / runqueue delay
- [ ] 기록 자동화 스크립트 + Grafana 대시보드 준비 (수작업 최소화 → 반복 실험 대비)
- [ ] 경합 유발 파드는 자원을 단계적으로 (컨트롤 플레인 동일 노드이므로 과부하 주의)

### Day 9 (4h) — Fig.3 실험 1차

- [ ] 시나리오 실행, 4개 시계열 동시 기록
- [ ] "게스트·libvirt 평온 / cpu.pressure·runqueue delay 급등" 구간 1차 확인
- [ ] 재현 실패 시 즉시 원인 점검 (nr_throttled는 논지에서 제외, PSI 갱신, TCG 부하량 등)

### Day 10 (4h) — Fig.3 확정 + 게이트 판정

- [ ] 파라미터 조정 후 재실험 (경합 강도)
- [ ] 가장 명확한 구간을 Fig.3 최종본으로 확정
- [ ] Table 1 (가시성 커버리지 O/X 매트릭스: libvirt / cAdvisor / 본 수집기) 확정
- [ ] **게이트 판정**: 핵심 구간 재현 성공 → 진행 / 실패 → Day 11 전량 재실험 투입

### Day 11 (4h) — 정확도·오버헤드 + Fig.2 마감 + 4장 집필

- [ ] libvirt `cpu_time` 대비 상대오차 1회 측정 (한 문장 분량, 표 안 만듦)
- [ ] 5초 주기 exporter CPU·RSS, fio 처리량 영향 1회 측정 (한 문장 분량)
- [ ] Fig.2 아키텍처 다이어그램 최종본
- [ ] **4.1~4.4절 초안 작성 (0.65p 목표)**

### Day 12 (4h) — 서론 + 결론

- [ ] **1장 서론 (0.45p)**: 기여 3줄이 2·3·4장에서 증명됐는지 대조하며 작성
- [ ] 관련 연구 압축 문단 (cAdvisor raw cgroup / libvirt-exporter·Ceilometer / KubeVirt
      각 1문장, "경합 미제공 또는 K8s 미조인"으로 마무리)
- [ ] **5장 (0.15p)**: 요약 2문장 + 한계 2문장(전제의 환경 의존성 — 단일 노드/중첩
      가상화, 단일 노드 규모) + 향후 과제 1문장(eBPF, GPU는 언급 안 함)

### Day 13 (4h) — 통합 + 분량 조정

- [ ] 초록 작성
- [ ] 참고문헌 6~8편 (OSH 문서, libvirt cgroups 문서, PSI 커널 문서, KubeVirt,
      Ceilometer, cAdvisor 등)
- [ ] 2단 조판 렌더링 → **본문 3페이지 이내 확인**
- [ ] 그림·표 4개(Fig.1~3 + Table 1) 이하 확인
- [ ] 초과분 삭제 (2.3절 전통배포 대비 문단 → 필요시 서론/2.2로 흡수)

### Day 14 (4h) — 최종 검토 + 제출

- [ ] "libvirt는 게스트 값만 준다"는 잘못된 문장 잔존 여부 확인
- [ ] `nr_throttled`에 논지를 걸지 않았는지 확인
- [ ] 서론 기여 3줄 ↔ 본문 대응 최종 확인
- [ ] 메트릭 타입(Counter/Gauge) 정확성 확인
- [ ] 전제의 환경 의존성이 5장 한계에 있는지 확인
- [ ] 최종본 제출

---

## 전체 게이트·선회 요약

| 게이트 | 시점 | 성공 조건 | 실패 시 |
|---|---|---|---|
| G1 | Day 2 | QEMU가 kubepods.slice 밖 | 주제를 cgroup 배치 환경 의존성 분석으로 선회 |
| G2 | Day 6 | 4지표 `/metrics` 노출 | 지표 2개로 축소, 실험 범위 축소 |
| G3 | Day 10 | Fig.3 경합 은폐 구간 재현 | Day 11 전량 재실험, 정확도·오버헤드는 1회만 |

## 이 레포 특유 주의사항 (재확인)

1. **KVM 없음(TCG)은 문제 없음** — 관측 지표는 QEMU 실행 방식과 무관, TCG 고부하는
   Fig.3에 유리.
2. **단일 노드 = EC2 VM 안, 중첩 가상화** — Day 1 "파드 안 vs 호스트" cgroup 비교를
   특히 꼼꼼히. 5장 한계에 명시.
3. **컨트롤+데이터 동일 노드** — Fig.3 경합 강도를 단계적으로. OSH 자체 불안정 주의.
4. **비용 ~$0.8/h** — 세션 단위 `make down`. Fig.3 반복은 한 세션에 몰아서.

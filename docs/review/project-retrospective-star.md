# 프로젝트 회고 (STAR)

`qemu-exporter`(목표1)와 그 확장인 `live-migration-webhook`(목표2), 두 트랙을 진행하며 겪은
문제와 해결 과정을 STAR(Situation-Task-Action-Result) 형식으로 정리한다. 각 트랙은 전체
개요 STAR 1개 + 겪었던 주요 문제별 STAR로 구성한다.

---

## 1부. qemu-exporter (목표1)

### 개요

**Situation** — OpenStack-Helm(OSH) 환경에서는 Nova가 생성한 QEMU 프로세스가 Kubernetes Pod
cgroup(`kubepods.slice`)이 아니라 호스트 루트의 `machine.slice`에 배치된다. 그 결과 kubelet/
cAdvisor 기반의 표준 K8s 모니터링은 VM의 자원 사용량을 전혀 인식하지 못하고(실측: 호스트 메모리
사용량의 상당 부분이 회계 누락), libvirt API는 자원량은 보여줘도 실시간 경합(contention)은
보여주지 못한다(`vcpu.N.delay`는 누적 카운터일 뿐 커널 PSI 표준을 따르지 않음).

**Task** — OpenStack/Nova/Libvirt/Kubernetes 어느 쪽도 건드리지 않고, 호스트 cgroupfs/procfs를
읽기 전용으로 파싱해 QEMU 프로세스의 자원·경합 지표 4종(CPU 사용량, 메모리, CPU PSI, 런큐
대기시간)을 Prometheus 형식으로 노출하는 exporter를 만든다. 3페이지 학부 논문(KIPS ASK)의
프로토타입이라 범위를 4개 지표로 엄격히 제한하고, eBPF·cgo 금지(순수 Go 정적 바이너리) 제약을
지킨다.

**Action** — Go로 DaemonSet exporter를 구현(`internal/cgroupsrc`, `internal/procsrc`,
`internal/libvirtsrc`, `internal/collector`). libvirt 접근은 cgo 바인딩 대신 순수 Go RPC
라이브러리(`digitalocean/go-libvirt`)로 read-only 소켓(`libvirt-sock-ro`)만 사용. cgroup 경로는
systemd 이스케이프 규칙으로 유추하지 않고 항상 `/proc/<pid>/cgroup`을 읽어 확정. AWS EC2
`m5.2xlarge` 단일 노드에 OSH 2026.1.0을 배포해 실측 검증(Fig.1 자원회계 왜곡, Fig.3 경합
은폐/재현)까지 진행.

**Result** — 6개 지표(4개 VM 지표 + exporter 자체 지표 2개) 전부 노출 확인, cgroup 경합 부하
시나리오에서 CPU PSI 12.5배·런큐 대기 10배 급등을 정확히 포착(반면 guest 체감 CPU·cAdvisor는
변화 없음 — 문제가 "은폐"된다는 논문 핵심 주장의 실측 근거). exporter 자체 오버헤드는 CPU
0.11%·메모리 0.03% 수준. `virsh domstats`와의 CPU 값 상대오차 0.001%. 논문은 KIPS ASK에 한글·
영문 2종 제출 완료(`docs/Paper/`).

### 겪었던 문제와 해결

#### 문제 1 — threaded cgroup 하위구조 때문에 엉뚱한 cgroup을 읽음

**Situation** — libvirt의 cgroupfs 드라이버는 도메인 cgroup 아래에 `emulator/`, `vcpu0/`,
`vcpu1/`... 같은 threaded 하위 cgroup을 추가로 만든다. QEMU 메인 PID의 `/proc/<pid>/cgroup`이
가리키는 경로는 도메인 전체가 아니라 그중 `emulator/` 리프였다.

**Task** — 실제로 필요한 것은 VM(도메인) 단위의 자원·경합 지표이지 emulator 스레드 하나만의
지표가 아니다. 잘못된 리프에서 읽으면 CPU/메모리 수치가 VM 전체 사용량과 어긋난다.

**Action** — `cgrouppath.go`에서 `cgroup.type` 파일이 `threaded`면 `filepath.Dir`로 상위(도메인
cgroup)까지 올라가는 climb 로직을 추가했다. `ResolveCgroupPath`의 시그니처에 `hostSysFsCgroup`을
추가해 `cache.go`/`main.go`까지 전파했다.

**Result** — 도메인 cgroup에서 정확히 읽도록 수정, `cgrouppath_test.go`에 threaded-climb 테스트
케이스 추가. AWS 검증 단계에서 CPU/메모리 값이 `virsh domstats`와 정확히 일치함을 확인.

#### 문제 2 — cgroup 네임스페이스 프리픽스로 인한 전체 지표 ENOENT

**Situation** — exporter 파드 자신도 별도 cgroup 네임스페이스 안에 있어서, 호스트 QEMU
프로세스의 `/host/proc/<pid>/cgroup` 내용이 `0::/../../../../machine/...`처럼 파드 네임스페이스
바깥 경로를 가리킬 때 `/../` 프리픽스가 붙은 형태로 나온다. 이걸 그대로 `filepath.Join`에
넘기면 `/host/sys/fs/cgroup` 프리픽스가 `..`에 의해 먹혀버려 모든 지표 읽기가 ENOENT로
실패했다.

**Task** — 어떤 cgroup 네임스페이스 상황에서도 실제 호스트 절대경로로 정확히 해석되어야 한다.

**Action** — `cgrouppath.go`에서 `filepath.Clean(cg.Path)`을 적용해 `/../` 프리픽스를 정규화한
뒤 join하도록 수정. 원인 규명에는 `main.go`에 추가한 startup config echo 로그
(`host_proc`/`host_sys_fs_cgroup` 등)가 결정적이었다.

**Result** — AWS 노드에서 전 지표 정상 노출 확인(`scrape_errors_total=0`), 회귀 테스트로
cgroupns 프리픽스 케이스를 `cgrouppath_test.go`에 추가.

#### 문제 3 — cgo 없이 libvirt 메타데이터를 조회해야 하는 제약

**Situation** — CLAUDE.md 절대 규칙상 cgo 금지(정적 바이너리 요구)라, 공식 libvirt Go 바인딩
(`libvirt.org/go/libvirt`, cgo 기반)을 쓸 수 없었다. 동시에 Nova REST/Keystone 호출도 금지라
인스턴스 메타데이터(flavor, project_id 등)를 얻을 다른 경로가 필요했다.

**Task** — 순수 Go로 libvirt read-only 소켓에 RPC 연결해 도메인 XML을 파싱, `<nova:instance>`
확장 요소에서 메타데이터를 뽑아내야 한다.

**Action** — `digitalocean/go-libvirt`(순수 Go RPC 클라이언트)로 `qemu+unix:///system?socket=...`
URI 연결을 구현(`client.go`), 도메인 XML의 `<nova:instance>` 네임스페이스를 파싱해 flavor/
project UUID를 추출(`domain.go`).

**Result** — `CGO_ENABLED=0 GOOS=linux go build`로 정적 빌드 확인, AWS 검증에서 라벨 5종
(`node`/`instance_uuid`/`instance_name`/`flavor`/`project_id`) 전부 정상 부착 확인.

---

## 2부. live-migration-webhook (목표2)

### 개요

**Situation** — qemu-exporter는 경합(PSI)을 관측만 할 뿐 대응하지 않는다. 관측된 경합이
임계치를 넘었을 때 VM을 선제적으로 다른 노드로 피신시킬 수 있다면 "관측 → 대응"까지
이어지는 완결된 스토리가 된다. 다만 기존 실험 환경은 단일 노드라 live migration 자체가
불가능했다.

**Task** — 기존 단일 노드 terraform 구성을 2노드로 확장하고(공유 스토리지 없음 →
`--block-migrate`), qemu-exporter가 노출하는 `openstack_vm_cpu_pressure_stall_seconds_total`이
임계치를 넘으면 Alertmanager → webhook → Nova live-migration API로 이어지는 파이프라인을
구축해 "선제적 회피"를 실측으로 입증한다. qemu-exporter 본체의 read-only 원칙은 유지하되,
webhook 리시버는 별도 바이너리로 분리해 Nova live-migration 트리거만 허용한다.

**Action** — terraform을 포크해 2노드(node-a: 컨트롤플레인+qemu-exporter, node-b: 컴퓨트
전용) 구성. L1(2노드 기동)→L2(Nova multi-compute)→L3(수동 마이그레이션 검증)→L4(Alertmanager
룰)→L5(webhook 리시버)→L6(전체 시나리오 실측)의 6단계로 나눠 각 단계를 fresh 서브에이전트가
전담하는 오케스트레이션 방식으로 진행(세션이 길어지며 생기는 컨텍스트 오염 방지 목적).

**Result** — 6단계 전부 성공적으로 완료. cpu-hog로 유발한 실제 경합 → Alertmanager 발화 →
webhook 수신 → Nova `os-migrateLive` 트리거 → 마이그레이션 완료까지 end-to-end 자동화 검증.
임계치 초과부터 alert 발화까지 지연은 튜닝 후 35.7~75.7초(측정 기준에 따라 다름, 아래 문제 6
참조), alert부터 마이그레이션 완료까지는 약 37초. OSH 컨트롤플레인 CPU는 마이그레이션 후
1분 내 baseline 대역으로 복귀 — "선제적 회피"가 실제로 다른 워크로드의 경합을 해소한다는
근거를 확보했다.

### 겪었던 문제와 해결

#### 문제 1 — 같은 서브넷 2노드 사이 Pod 통신 완전 두절 (Calico × AWS SourceDestCheck)

**Situation** — node-b를 K8s에 join하고 라벨링했지만 node-a↔node-b 간 Pod IP 통신이 100%
실패(ping 손실, CoreDNS 조회 타임아웃)했다. neutron-ovs-agent가 `hostname --fqdn`의 DNS 실패로
CrashLoop, libvirt/nova-compute는 그 의존성 대기로 Init에 멈췄다.

**Task** — 원인을 정확히 규명해야 이후 단계가 전부 막히지 않는다.

**Action** — 조사 결과, Calico IPPool의 `vxlanMode: CrossSubnet` 설정 때문에 같은 서브넷
(10.0.1.0/24)인 두 노드 사이는 VXLAN 캡슐화 없이 언더레이(ENI) 직접 라우팅을 시도했고, AWS의
`SourceDestCheck: True`가 자기 ENI IP가 아닌 출발/도착지를 가진 패킷을 드롭하고 있었다.
`kubectl patch installation.operator.tigera.io default`로 IPPool의 `encapsulation`을
`VXLAN`(Always)으로 바꿔 같은 서브넷이라도 항상 캡슐화하도록 수정.

**Result** — pod-to-pod 통신 정상화, CrashLoop 상태였던 neutron-ovs-agent 파드를 삭제해
재생성시키자 libvirt/nova-compute도 자동으로 Init을 통과. 이 패치가 재현 절차(`live_migration_
tdl.md` "패치 a")로 문서화되어 이후 세션에서도 동일하게 적용됨.

#### 문제 2 — Calico VXLAN과 Neutron VXLAN이 같은 UDP 포트를 두고 충돌

**Situation** — 문제 1의 해결책(Calico VXLAN Always)을 적용한 뒤에도 neutron-ovs-agent가
영구적으로 Not Ready였다. 처음엔 "CPU 경합으로 인한 프로브 타임아웃"을 의심했지만, 프로브
스크립트 실행시간이 14ms에 불과해 그 가설은 기각됐다.

**Task** — readiness probe가 실제로 실패하는 근본 원인을 찾아야 한다.

**Action** — 근본원인은 Calico VXLAN encapsulation(기본 `vxlanPort: 4789`)과 Neutron OVS의
테넌트 네트워크 VXLAN 터널(역시 기본 4789)이 호스트 루트 netns에서 같은 UDP 포트를 두고
충돌하는 것이었다. Neutron 터널 인터페이스가 "Address already in use"로 영구 실패 → readiness
probe의 `ovs-vsctl show` 체크에 걸림. `kubectl patch felixconfiguration default`로 Calico
쪽 vxlanPort를 4790으로 옮겨 충돌을 회피.

**Result** — 양쪽 노드 대칭적으로 동일 증상이었음을 확인해 노드 개별 문제가 아님을 검증,
patch 적용 후 neutron-ovs-agent 정상화. 이 역시 재현 절차("패치 b")로 고정됨.

#### 문제 3 — libvirt 기본 `listen_addr`가 루프백이라 마이그레이션 자체가 거부됨

**Situation** — 네트워크 문제를 다 해결한 뒤 첫 수동 live migration을 시도했으나 "unable to
connect to server at '10.0.1.84:16509': Connection refused"로 실패했다. SG/Calico 포트는
이미 열려 있었다(L1/L2에서 확인).

**Task** — libvirt RPC 자체가 거부되는 원인을 찾아야 한다.

**Action** — 원인은 OSH `libvirt` 차트 values의 `listen_addr`가 기본값 `127.0.0.1`이라 외부
IP로 노출되지 않는 것이었다(`listen_tcp=1`이었지만 루프백에만 바인딩). `helm upgrade`로
`conf.dynamic_options.libvirt.listen_address=0.0.0.0`을 설정.

**Result**: 첫 시도부터 마이그레이션 성공(트리거~완료 약 37~47초, 세션마다 소폭 차이).
이 세 patch(a/b/c)를 한 번에 순서대로 적용하는 것이 이후 세션 재현 절차의 핵심이 됨 —
발견 순서대로 하나씩 고치느라 첫 세션에 3라운드가 걸렸던 것을, 이후 세션들은 순서를 알고
있었기 때문에 한 번에 통과.

#### 문제 4 — 2노드에서만 드러나는 Prometheus 스크레이프 버그

**Situation** — 단일 노드 검증 때는 문제없던 `prometheus.yaml`의 qemu-exporter scrape_config가
ClusterIP Service를 정적 타겟으로 사용하고 있었는데, 2노드에서는 DaemonSet이 노드당 1개씩
총 2개 파드가 뜨고, kube-proxy의 connection stickiness 때문에 Prometheus가 매번 같은 파드
하나만 계속 스크레이프했다 — 하필 VM이 없는 노드의 파드였다.

**Task** — 두 노드 모두의 exporter 파드가 개별적으로 스크레이프되도록 해야 경합 지표를
놓치지 않는다.

**Action** — scrape_config를 `kubernetes_sd_configs(role: pod)`로 전환해 DaemonSet의 각 파드를
직접 타겟팅하도록 수정.

**Result** — `/api/v1/targets`에서 두 파드 IP 모두 `up` 확인. 단일 노드 설계에서는 절대 드러나지
않았을 버그로, 다중 노드 확장 시 "정적 타겟 + ClusterIP" 패턴이 위험하다는 일반적 교훈을 얻음.

#### 문제 5 — 임계치·평가주기 튜닝: 원안 threshold는 실측으로 도달 불가능

**Situation** — TDL 원안의 Alertmanager 임계치(`rate(...) > 0.5`)는 목표1 Fig.3 실측 피크
(0.1467)보다도 훨씬 높은 값이었다. cpu-hog replicas를 8까지 올려도 rate 피크가 ~0.012에
불과해, 이 부하 패턴으로는 원안 threshold에 영원히 도달할 수 없었다.

**Task** — 게이트를 통과시킬 현실적인 threshold가 필요하되, 그 값이 임시방편임을 명확히
남겨야 한다(실사용 threshold는 별도 재측정 필요).

**Action** — threshold를 0.005로 낮추고 `alert-rules.yaml`에 ponytail 주석으로 "임시값" 표시.
이후 L6 전체 시나리오 1차 실행에서 "임계치초과→alert 100.7초"라는 예상보다 훨씬 긴 지연을
발견했고, 원인이 Prometheus 룰의 `evaluation_interval`이 명시돼 있지 않아 기본값 60초로
동작하며 `for: 30s` 판정의 병목이 된 것임을 규명. `evaluation_interval: 15s`로 튜닝 후
재실행.

**Result** — 메커니즘 자체는 의도대로 고쳐졌음을 확인(`for:30s`가 정확히 15s×2 평가
사이클로 동작). 다만 재실행에서 부하 곡선이 threshold 근처에서 우연히 한 번 떨어졌다
재상승하는 flicker가 발생해 "첫 초과 시점" 기준(75.7초)과 "실제 발화로 이어진 두 번째
crossing" 기준(35.7초) 두 숫자가 나왔다. 둘 다 지우지 않고 각각의 의미와 논문 집필 시
선택 기준을 `docs/paper_data/live_migration/README.md`와 `live_migration_tdl.md`에 함께
기록해, 판단을 논문 집필 시점으로 미뤘다.

#### 문제 6 — 하니스 제약: 공유 인프라 patch는 서브에이전트가 실행할 수 없음

**Situation** — 서브에이전트 오케스트레이션 방식으로 진행하던 중, L2의 patch a/b/c
(`kubectl patch`, `helm upgrade`)를 서브에이전트 지시문에 그대로 넣었더니 Agent 호출 자체가
하니스의 "Protected-Scope IaC Apply" 분류로 거부됐다. 비슷하게, 평문 비밀번호
(`OS_PASSWORD=password`)를 `systemd-run` 커맨드에 직접 타이핑하면 "Credential Leakage"로
Bash 호출 자체가 거부되는 것도 발견했다.

**Task** — 자동화(서브에이전트 위임)와 하니스의 안전장치를 동시에 만족시키는 작업 분할
방식을 찾아야 한다.

**Action** — 서브에이전트 프롬프트를 "patch 적용 이전 범위(join/라벨링/통신 테스트)까지만"
으로 쪼개고, 공유 리소스를 실제로 변경하는 patch 3개는 사용자가 SSM에서 직접 실행하도록
분리. 비밀번호는 `kubectl get secret ... -o jsonpath='{.data.OS_PASSWORD}' | base64 -d`로
런타임에 읽어와 셸 변수로 주입하는 방식으로 우회(값을 평문으로 타이핑하지 않음).

**Result** — 이후 세션들에서 이 패턴(서브에이전트=읽기/준비 작업, 사용자=공유 리소스 변경
직접 실행, 비밀값=런타임 조회)이 그대로 재현·검증되어 막힘 없이 진행. "새로 만들고 돌리는"
류는 서브에이전트로 안 막히고 "이미 있는 공유 설정을 patch/upgrade"하는 류만 막힌다는
일반화된 규칙을 얻음.

#### 문제 7 — webhook 리시버의 보안 결함 (백그라운드 보안 리뷰로 발견)

**Situation** — L5에서 구현한 `cmd/live-migration-webhook/main.go`는 인증 없이 `/webhook`
POST를 그대로 처리하고, Alertmanager alert의 `instance_uuid` 라벨 값을 검증 없이 Nova API
URL 경로에 그대로 이어붙이고 있었다. 자동화된 백그라운드 보안 리뷰가 이 두 가지를 각각
"인증되지 않은 권한 있는 동작"(HIGH), "경로 주입 가능성"(MEDIUM)으로 지적했다.

**Task** — 실험용 프로토타입이라도, 이 포트에 닿을 수 있는 누구든 임의 VM을 마이그레이션시킬
수 있는 결함과 라벨 값을 검증 없이 URL에 꽂는 결함은 고치는 비용이 작으므로 반영한다.

**Action** — `WEBHOOK_SECRET` 환경변수를 필수화하고 `Authorization: Bearer <secret>` 헤더를
`crypto/subtle.ConstantTimeCompare`로 검증(불일치 시 401). `instance_uuid`를 UUID 정규식으로
검증한 뒤에만 Nova API 호출에 사용하도록 수정. `deploy/monitoring/alertmanager.yaml`의
`webhook_configs`에 `http_config.authorization.credentials`를 추가해 Alertmanager가 같은
토큰을 보내도록 반영. 새 검증 로직에 대한 단위 테스트(`TestConstantTimeBearerMatch`,
`TestInstanceUUIDRe`) 추가.

**Result** — `go build`/`go vet`/`go test ./...` 전부 통과. `live_migration_tdl.md`의 L5 재개
절차에도 `WEBHOOK_SECRET` 필수화 및 UUID 검증 변경사항을 반영해, 다음 재현 세션이 인증
실패로 막히지 않도록 문서화.

---

## 요약

| 구분 | 핵심 성과 | 가장 오래 걸린 문제 |
| --- | --- | --- |
| 목표1 (qemu-exporter) | eBPF/cgo 없이 read-only로 4개 지표 노출, 경합 은폐를 실측으로 입증(PSI 12.5배 급등) | cgroup 네임스페이스 프리픽스로 인한 전체 지표 ENOENT (원인이 파드 자신의 네임스페이스에 있어 처음엔 비직관적) |
| 목표2 (live-migration-webhook) | PSI 임계치 초과 → Alertmanager → webhook → Nova live migration 자동 트리거 end-to-end 실증 | 네트워크 3중 충돌(Calico cross-subnet → VXLAN 포트 충돌 → libvirt listen_addr) — 각각 독립된 근본원인이라 순차 발견에 3라운드 소요 |

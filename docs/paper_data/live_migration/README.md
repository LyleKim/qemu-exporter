# live-migration-webhook (목표2) 실험 데이터

`live_migration_tdl.md`(레포 루트) L1~L6의 raw 실행 로그/결과를 단계별로 보관한다.
목표1(qemu-exporter)의 `paper_data/`와 형식은 같되 별개 서브디렉터리로 분리했다 —
목표2는 아직 별도 확장판/후속 컨퍼런스용이라 목표1 데이터와 섞지 않는다.

| 파일 | 내용 |
|---|---|
| `l1_terraform_2node.txt` | 2노드 terraform plan/apply, cgroup v2/PSI 실측, node-a OSH 재배포 (2026-09-16, 이후 make down으로 삭제됨) |
| `l1_reprovision_20260917.txt` | L3 삽질 후 인스턴스 삭제된 환경을 처음부터 재기동(2026-09-17). terraform plan 11 add/0 change/0 destroy로 코드 불변 확인 → make up → 양쪽 cgroup v2/PSI 재확인 → node-a OSH 재배포, test-vm ACTIVE(10.10.10.241) 확인. 신규 인스턴스: node-a=i-07c9a9e048b7d6e14/54.180.248.121, node-b=i-027146e2336a9f23f/43.201.16.119 |
| `l1_reprovision_20260919.txt` | 2026-09-17 세션도 L3 직전 사용자 판단으로 make down, 이번 세션(2026-09-19)은 처음부터(L1) 재기동. terraform state에 잔여물 없음 확인 후 plan 11 add/0 change/0 destroy(직전 중단 시도 없이 처음부터) → make up → 양쪽 cgroup v2/PSI 확인 → node-a make osh-deploy(exit=0) → make osh-vm, test-vm ACTIVE(10.10.10.225) 확인. 신규 인스턴스: node-a=i-0ee904b7547b9ee9f/3.39.253.218(private 10.0.1.4), node-b(compute)=i-0548b42c537e6fc56/13.124.156.226(private 10.0.1.207). node-b는 아직 K8s 미join(L2 담당) |
| `l1_reprovision_20260920.txt` | (신규 인스턴스, 2026-09-20) L1 재기동 — 서브에이전트 실행. 부트스트랩 약 2분18초(예상 5-8분보다 빠름), 양쪽 cgroup2fs+PSI 확인, osh-deploy(~20분, 58파드 정상) → osh-vm, test-vm ACTIVE(id=da91d986-..., host=node-a, 10.10.10.16) 재확인. 신규 인스턴스: node-a=i-0533a052301376202/15.165.77.116(private 10.0.1.129), node-b=i-0d466ea20e5b18c00/43.203.209.162(private 10.0.1.174) |
| `l2_nova_multi_compute.txt` | (구 인스턴스, 2026-09-16) node-b K8s join, Calico cross-subnet 문제 원인·해결, Nova cell 등록, 하이퍼바이저 2개 확인 |
| `l2_activation_20260917.txt` | (신규 인스턴스, 2026-09-17) L2 재실행 — node-b K8s join/Ready/라벨링 완료, SG는 이미 적용됨. pod-to-pod 통신 테스트에서 Calico cross-subnet 문제 재현(ping 100% loss, DNS timeout) — 지난 세션과 동일 원인, 미해결 상태로 **중단**. nova-compute/hypervisor 2개 up 확인은 이 통신 문제로 미도달, Calico patch(사용자 직접 실행) 후 재개 필요 |
| `l2_activation_20260919.txt` | (신규 인스턴스, 2026-09-19) L2 완료. 전반부: node-b K8s join/라벨링, pod-to-pod 통신 실패 재현(지난 세션과 동일 원인, read-only 확인). 후반부: 사용자가 node-a SSM에서 patch a/b/c 순서대로 적용 → c(libvirt) rollout이 "1/2"에서 멈춤 → CrashLoopBackOff 파드(patch 적용 이전 타이밍에 뜬 것) 삭제로 해소 → rollout 완료 → `discover_hosts`/`hypervisor list`로 두 호스트 모두 `up` 확인. "rollout 멈춤은 타이밍 이슈"라는 재개 절차 가설이 재검증됨 |
| `l2_multicompute_20260920.txt` | (신규 인스턴스, 2026-09-20) L2 — 서브에이전트가 node-b join·라벨링, cross-subnet 문제 재현까지 진행 후 patch a/b/c는 사용자가 SSM에서 직접 적용(하니스 제약 재확인). 이번엔 rollout이 "1/2"에서 멈추지 않고 바로 완료(CrashLoopBackOff 재현 안 됨) — 양쪽 노드 hypervisor up 확인(별도 서브에이전트로 검증) |
| `l3_manual_migration.txt` | 1차 시도(2026-09-16, 구 인스턴스) — **실패**: listen_addr=127.0.0.1. 이어서 neutron-ovs-agent Not Ready 조사(Calico/Neutron VXLAN 4789 포트 충돌 규명), 수정 미승인으로 재시도 미실행, test-vm은 `--auto-approve`로 ACTIVE 복구. **재시도(2026-09-19, 새 인스턴스, 파일 하단) — 성공**: L2 완료(patch a/b/c 반영) 직후 첫 시도부터 `openstack server migrate --live-migration --block-migration`으로 node-a→node-b 마이그레이션 완료(트리거~완료 약 47초, migration 레코드 기준 running→completed 약12초), status=ACTIVE 유지, ERROR 상태 없이 성공. listen_addr/vxlanPort 두 근본원인이 patch로 실제 해소됐음을 실측 검증. **L3 게이트 통과 — L4 진행 가능** |
| `l3_manual_migration_20260920.txt` | (신규 인스턴스, 2026-09-20) L3 — 서브에이전트 실행, **첫 시도부터 성공**. 트리거(16:14:40Z)~완료(16:15:17Z) 약 37초, migration 레코드 created→updated 약 36초. node-a→node-b, ACTIVE 유지 |
| `l4_alertmanager.txt` | qemu-exporter+Prometheus+Alertmanager를 2노드 클러스터에 신규 배포(목표1 단일노드 스택엔 Alertmanager가 없어서 `deploy/monitoring/alertmanager.yaml`/`alert-rules.yaml` 신규 작성). 배포 중 발견한 버그: Prometheus가 ClusterIP Service를 정적 타겟으로 잡으면 DaemonSet 2파드 중 kube-proxy가 고정한 한쪽만 스크레이프됨 → `kubernetes_sd_configs`(role: pod)로 수정. cpu-hog(8 replicas, node-b 타겟)로 부하 유발 후 PSI(some) rate가 TDL 원안 threshold(0.5)엔 전혀 못 미침을 확인(Fig.3 실측 피크 0.1467과 같은 자릿수) — 사용자 확인 후 L4 게이트 통과용으로 threshold를 0.005로 임시 하향, `VMCPUPressureHigh` alert가 Alertmanager API에 `state=active`로 정확한 instance_uuid/node와 함께 발화함을 확인 |
| `l4_alertmanager_20260920.txt` | (신규 인스턴스, 2026-09-20) L4 — 서브에이전트 실행, 기존 매니페스트 그대로 apply(신규 작성 불필요). webhook URL을 새 node-a IP(10.0.1.129)로 갱신. qemu-exporter/Prometheus/Alertmanager 전부 정상, `/api/v1/targets` qemu-exporter 2/2·cadvisor 2/2·resource 2/2 up — 2노드 스크레이프 fix 재검증 |
| `l5_webhook.txt` | `cmd/live-migration-webhook`(신규, qemu-exporter와 분리된 Go 바이너리, stdlib만 사용) 구현·배포. 컨테이너 레지스트리 없이 node-a에 Go 1.22를 설치해 직접 빌드, systemd transient unit으로 구동, Nova/Keystone은 ClusterIP로 직접 접근. synthetic payload로 1차 검증(Keystone 인증→Nova 현재 호스트 조회→os-migrateLive 호출) 후, Alertmanager를 실제로 이 리시버에 연결하고 cpu-hog로 유발한 진짜 alert가 Alertmanager→webhook→Nova로 이어져 자동으로 live-migration을 트리거함을 확인(migration id=4, node-b→node-a, completed). 부하 시작~마이그레이션 완료 총 약 119초 |
| `l5_webhook_20260920.txt` | (신규 인스턴스, 2026-09-20) L5 — 서브에이전트 실행, 기존 main.go 그대로 재빌드·구동. **주의**: TDL에 적혀있던 시크릿명 `keystone-admin`이 실제로는 `keystone-keystone-admin`임을 이 세션에 발견·정정(TDL 반영 완료). cpu-hog replicas=4→rate 미달, 8→발화. e2e 성공(alert→webhook→migration, node-b→node-a, 약 31초) |
| `l6_scenario_20260920.txt` | 6단계 end-to-end 시나리오 1회 실행(threshold 0.005 그대로, cpu-hog replicas=4로 발화). 3개 지표: 임계치초과→alert 100.7s(Prometheus rule evaluation_interval 기본값 60s가 병목이었음을 이 세션에 처음 확인), alert→migration완료 38.3s, node-a OSH 컨트롤플레인 CPU baseline~1.06→부하중 피크3.36→정리후1.22(하강 추세, 완전 회복까지는 추가 대기 필요). OSH 안정성 이슈 없음, test-vm 최종 위치 node-b(ip-10-0-1-174) |
| `l6_scenario_v2_20260920.txt` | L6 재측정 — `deploy/monitoring/prometheus.yaml`에 `evaluation_interval: 15s` 추가(1차 실행에서 발견한 60s 기본값 병목의 수정), test-vm을 node-a로 되돌린 뒤 동일 시나리오 재실행. **"임계치초과→alert" 지표에 숫자가 두 개 있음 — 아래 참조**. alert→migration완료 37.3s(1차 38.3s와 사실상 동일, 이 구간은 evaluation_interval과 무관하므로 예상대로). node-a OSH CPU baseline~1.29→피크3.12→1분 내 1.11~1.14 회복(1차보다 빠른 회복). OSH 안정성 이슈 없음(26회 폴링 전부 클린), test-vm 최종 위치 node-b(ip-10-0-1-174) |

### L6 "임계치초과→alert" 지표 — 두 숫자에 대한 설명 (논문 작성 시 택1 필요)

evaluation_interval 튜닝(60s→15s)이 메커니즘 자체는 의도대로 고쳤다(`for:30s` 판정이
정확히 2×15s 평가 사이클로 동작함을 `/api/v1/rules`로 확인). 하지만 2차 실행에서 PSI
rate가 threshold(0.005) 바로 근처에서 한 번 떨어졌다가 다시 올라가는 흐름(flicker)이
우연히 발생해서, "지연"을 어느 시점부터 잴지에 따라 숫자가 갈린다:

- **75.7초** — 1차와 동일한 방법론(Prometheus range query상 **첫 번째로** threshold를
  넘은 시점, 17:11:25Z, 을 기준). 이 첫 초과는 곧바로 다시 threshold 밑으로 떨어지면서
  `for:30s` pending이 리셋됐다 — 즉 이 시점 자체는 실제 발화로 이어지지 않았다.
  1차(100.7s) 대비 24.8% 개선. **방법론 일관성을 우선한다면 이 숫자를 쓴다** — "언제부터
  경합이 시작됐는지" 기준으로는 이게 더 정직한 답.
- **35.7초** — 실제로 발화까지 이어진 **두 번째** crossing 시점(17:12:05Z) 기준. 이
  crossing부터는 리셋 없이 정확히 30초 후(17:12:40.729Z) 발화했다 — evaluation_interval
  수정이 의도한 메커니즘이 이상적 조건에서 어떻게 동작하는지 보여주는 숫자.
  1차 대비 64.5% 개선. **"튜닝이 실제로 얼마나 효과가 있었는지"를 보이고 싶다면** 이
  숫자가 더 적합하지만, "PSI가 threshold를 넘나든 첫 순간"을 기준으로 삼지 않았다는
  점을 논문에 명시해야 함.

두 숫자 다 `l6_scenario_v2_20260920.txt`에 원본 Prometheus range query 응답과 함께
남아있다 — 어느 쪽을 쓸지는 이 실험의 "지연"을 정의하는 방식(첫 신호 vs 실제 반응으로
이어진 신호)에 대한 논문 저술 시점의 판단이 필요하다.

각 파일은 raw 명령 출력을 시간순으로 담는다(해석·요약은 `live_migration_tdl.md`의 체크박스에).

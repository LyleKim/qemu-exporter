# AWS 검증 TDL — qemu-exporter 프리플라이트

> 목적: 이미 빌드·머지된 exporter(`f61d65a`)가 실제 OSH 노드에서 도는지, 그리고
> 논문 전제(QEMU가 kubepods.slice 밖)가 이 환경에서 성립하는지 확인한다.
> `basic_plan.md`의 Day 4–6(수집기 구현)은 완료 처리. 이 문서는 Day 1–2 + 배포 검증에 해당.
> 환경: m5.2xlarge 단일 노드, K8s 1.34 + Calico, OSH 2026.1.0, QEMU/TCG, SSM 전용.
> 게이트 2개: **G1**(전제 성립?) · **G2**(4지표 정상 노출?). 각 게이트에서 판정 후 진행/선회.

## 진행 요약 (2026-09-06)

- **Phase A 완료**, **Phase B / G1 통과** — QEMU가 kubepods.slice 밖, 커널 전제 전부 충족.
- 파드 관점 == 호스트 관점 cgroup 경로 **완전 동일**, qemu는 호스트 루트 cgroup ns(`4026531835`, PID 1과 동일) → 중첩 가상화 경로 어긋남 없음.
- 배치 메커니즘 확정: libvirtd(파드 안)가 cgroupfs 드라이버로 호스트 `/machine/`에 직접 cgroup 생성·이동. qemu 부모 = 파드 containerd-shim.
- **`/emulator` 리프 이슈 → 코드 수정 완료** (cgrouppath.go climb, 아래 "알려진 리스크"). gofmt/vet/test/빌드 green.
- **배포 후 발견: cgroupns `/../` 프리픽스** (2026-09-07). exporter가 자체 cgroup 네임스페이스에 있어
  `/host/proc/<QEMU>/cgroup`가 `0::/../../../../machine/...`로 나옴 → `filepath.Join`이 `/host/sys/fs/cgroup`
  프리픽스를 먹음 → 전 지표 ENOENT. `filepath.Clean(cg.Path)`로 수정, 테스트 추가.
- 다음: 재빌드·재배포 → G2 재판정.

**확정된 환경값**

| 항목 | 값 |
|---|---|
| 노드 이름 (`node` 라벨) | `ip-10-0-1-48` |
| 커널 | `7.0.0-1012-aws` (PSI OK) |
| CRI | `containerd://2.2.1` → 빌드는 `nerdctl -n k8s.io` |
| libvirt 파드 | `libvirt-libvirt-default-qdg87` (ns `openstack`, 컨테이너 `libvirt`) |
| 테스트 VM | `instance-00000001`, uuid `86581d7b-b212-4156-a6a3-27460c999598`, PID `72472`, `-accel tcg`, 512 MiB |
| QEMU cgroup | `0::/machine/qemu-1-instance-00000001.libvirt-qemu/emulator` (cgroupfs 드라이버) |
| QEMU 부모 PID | `33979` |

---

## Phase A — 환경 기동 + 명세 수집

- [x] `make init` → `make up` → `make ready` → `make osh-deploy` → `make osh-vm`
- [x] SSM으로 노드 접속, CirrOS/인스턴스 부팅 확인
- [x] 노드 명세 저장 (논문 4.1절 재료):
  - [x] `uname -r` → `7.0.0-1012-aws`
  - [x] `kubectl version` → `v1.34.11`, `helm list -n openstack` → OSH 2026.1.0 전 차트 deployed
  - [x] `kubectl get nodes -o wide` → `ip-10-0-1-48`, containerd 2.2.1, Ubuntu 24.04.4
  - [x] `virsh` 는 호스트에 없음 → 파드에서: `kubectl -n openstack exec libvirt-libvirt-default-qdg87 -c libvirt -- virsh ...`
- [ ] 비용: 세션 끝나면 `make down`

## Phase B — G1 게이트: 전제 + 커널 전제조건

**커널 전제조건 (exporter 코드가 의존)** — 전부 충족

- [x] `stat -fc %T /sys/fs/cgroup` → `cgroup2fs` ✓
- [x] `ls /proc/pressure/` → `cpu io memory` ✓ (PSI 지원)
- [x] `sysctl kernel.sched_schedstats` → `1` ✓ (이미 설정됨, 조치 불필요)

**전제 검증 — QEMU가 kubepods.slice 밖인가** — 통과

- [x] `pgrep -a qemu-system` → PID `72472`, `instance-00000001`, `-accel tcg`, 512 MiB
- [x] `cat /proc/72472/cgroup` (호스트) → `0::/machine/qemu-1-instance-00000001.libvirt-qemu/emulator`
      → `/machine/...` = kubepods.slice 밖 ✓. `/machine.slice`가 아닌 `/machine` = libvirt **cgroupfs 드라이버**.
- [x] 중첩 가상화 대조 — 파드(`libvirt-libvirt-default-qdg87`) vs 호스트 `cat /proc/72472/cgroup`:
      **경로 완전 동일** → cgroup 네임스페이스 리맵 없음. exporter `/host/proc`+`/host/sys/fs/cgroup` join 성립. D-2 거의 해소.
- [x] `systemctl status systemd-machined` → "could not be found" → machined 미설치 = cgroupfs 드라이버 확정 (§4.1 재료)
- [x] `ps -o ppid= -p 72472` → 부모 PID `33979`
- [x] 배치 메커니즘 보강 증거 (§4.1·5장용):
  - `ps -o pid=,cmd= -p 33979` → `containerd-shim-runc-v2 -namespace k8s.io` (libvirt 파드 shim)
  - `cat /proc/33979/cgroup` → `0::/system.slice/containerd.service` (shim은 pod cgroup 밖, qemu는 shim이 subreaper로 재양육)
  - `readlink /proc/72472/ns/cgroup` == `readlink /proc/1/ns/cgroup` == `cgroup:[4026531835]` → qemu는 호스트 root cgroup ns, 격리 안 됨
- [ ] 모든 출력 텍스트 저장 (논문 4.1절 문단으로 정리 — 진행 요약 참조)

**B-x: 도메인 cgroup 트리 — `/emulator` 리프 이슈 (2026-09-06 확인 완료, "알려진 리스크" 참조)**

- [x] `$D/cgroup.type` = `domain threaded`, `$D/emulator/cgroup.type` = `threaded`, 자식 = `emulator`/`vcpu0`/`iothread1`
- [x] `$D/emulator/memory.current` → **ENOENT** (threaded cgroup엔 memory 컨트롤러 없음). `$D/memory.current` = `665223168` (634 MiB)
- [x] `$D/emulator/cpu.stat` usage_usec = `25489665` vs `$D/cpu.stat` = `58997385` → 리프는 전체의 **43%** (vcpu0+iothread1 누락)
- [x] 결정: **exporter 수정 필요** — 리프 cgroup이 아니라 도메인 cgroup에서 읽어야 함

### ▶ G1 판정: **통과 (2026-09-06)** — QEMU가 kubepods.slice 밖 확인. Phase C로.
- (참고) 실패였다면: 주제를 "libvirt cgroup 배치의 환경 의존성 분석"으로 선회.

## 알려진 리스크 — `/emulator` 리프 cgroup (2026-09-06 **확인 완료 → 수정 완료**)

`/proc/72472/cgroup` = `.../qemu-1-instance-00000001.libvirt-qemu/emulator`. libvirt cgroupfs
드라이버는 도메인 cgroup(`cgroup.type=domain threaded`) 밑에 `emulator/`·`vcpu0/`·`iothread1/`
**threaded 하위 cgroup**을 만든다. exporter는 `/proc/<pid>/cgroup`이 준 경로 그대로
`cpu.stat`·`memory.current`·`cpu.pressure`를 읽으므로 지금은 **`/emulator` 리프**를 읽는다.

실측 (B-x):
| 지표 | 지금 (리프) | 맞는 값 (도메인) | 영향 |
|---|---|---|---|
| `cpu.stat` usage_usec | 25,489,665 | 58,997,385 | CPU 사용량 **43%만** 잡힘 |
| `memory.current` | 파일 없음 | 665,223,168 | memory 지표 **ENOENT 실패**, `scrape_errors_total`++ |
| `cpu.pressure` | emulator 스레드만 | 전체 subtree | Fig.3 핵심 지표 축소 |
| `schedstat` | — | — | `/proc/<pid>/task/*` 순회라 **영향 없음** |

**수정 (완료 2026-09-06)**: `ResolveCgroupPath(hostProc, hostSysFsCgroup, pid)` — `/proc/<pid>/cgroup`
경로를 얻은 뒤 `climbToDomainCgroup`가 `<hostSysFsCgroup>/<경로>/cgroup.type`을 읽어 값이 `threaded`면
`filepath.Dir()`로 한 단계씩 상승, `domain` / `domain threaded` / 읽기 실패에서 멈춤. → kernel 파일만
읽음(scope 이름 파싱 아님, CLAUDE.md 규칙 준수), cgroupfs·systemd 두 드라이버 모두 대응, cgroup v1/마운트
없음이면 경로 그대로(degrade).
- 변경: `internal/libvirtsrc/cgrouppath.go` (시그니처 + `climbToDomainCgroup`),
  `internal/libvirtsrc/cache.go` (`hostSysFsCgroup` 필드 + `NewCache` 파라미터),
  `cmd/qemu-exporter/main.go` (`NewCache` 호출), `cgrouppath_test.go` (climb 테스트 2개 추가)
- gofmt / `go vet` / `go test ./...` (4 pkg) / `CGO_ENABLED=0 GOOS=linux go build` 전부 green.
- **미커밋** — 커밋은 사용자가 직접.

## Phase C — 컨테이너 빌드 + 배포

- [x] 로컬 Mac에서 빌드+push: `docker buildx build --platform linux/amd64 -f deploy/Dockerfile -t lylekim/qemu-exporter:dev --push .` → 성공
- [x] `deploy/daemonset.yaml`: `image: docker.io/lylekim/qemu-exporter:dev`, `imagePullPolicy: Always`
- [ ] 노드(SSM)에 `daemonset.yaml` 만 가져감 (scp / 붙여넣기 / git clone)
- [ ] `kubectl apply -f daemonset.yaml`
- [ ] `kubectl -n openstack rollout status ds/qemu-exporter --timeout=60s`
- [ ] `kubectl -n openstack get pod -l app=qemu-exporter -o wide` → `Running`, restart 0, `ImagePullBackOff` 아님
- [ ] `kubectl -n openstack logs -l app=qemu-exporter` → `qemu-exporter: listening address=:9179`, panic/fatal 없음

> ⚠️ 이미지가 `FROM scratch` — 셸·wget·curl 없음. **`kubectl exec` 불가.** `/metrics`는 podIP/port-forward, 파일 확인은 호스트에서, 디버깅은 로그로.

## Phase D+E — G2 게이트: 지표로 D-1~3 동시 판정

```
POD_IP=$(kubectl -n openstack get pod -l app=qemu-exporter -o jsonpath='{.items[0].status.podIP}')
curl -s http://$POD_IP:9179/metrics | grep -E '^(openstack_vm|qemu_exporter)'
```

- [ ] `qemu_exporter_vms_discovered` == `1` → **D-3 통과** (libvirt-sock-ro 연결)
- [ ] `qemu_exporter_scrape_errors_total` == `0` → **D-1 통과** (pidfile `/var/run/libvirt/qemu/instance-00000001.pid` 읽힘) + **D-2 통과** (cgroup join + climb 동작)
- [ ] 6개 패밀리 전부 존재:
  - [ ] `openstack_vm_cpu_usage_seconds_total` (Counter)
  - [ ] `openstack_vm_memory_usage_bytes` (Gauge)
  - [ ] `openstack_vm_cpu_pressure_stall_seconds_total{type="some"}` (Counter)
  - [ ] `openstack_vm_sched_runqueue_wait_seconds_total` (Counter)
  - [ ] `qemu_exporter_scrape_errors_total`, `qemu_exporter_vms_discovered`
- [ ] 라벨: `node="ip-10-0-1-48"`, `instance_uuid="86581d7b-b212-4156-a6a3-27460c999598"`, `instance_name="instance-00000001"`, `flavor`·`project_id` 채워짐
      (비어 있으면 도메인 XML `<nova:instance>` 없는 것 — `virsh dumpxml instance-00000001` 확인)
- [ ] `go_*` / `process_*` / `promhttp_*` 안 나옴 (커스텀 레지스트리)

**값 대조 (호스트, 같은 시점)**
```
D=/sys/fs/cgroup/machine/qemu-1-instance-00000001.libvirt-qemu
sudo cat $D/memory.current ; sudo head -1 $D/cpu.stat
kubectl -n openstack exec libvirt-libvirt-default-qdg87 -c libvirt -- virsh domstats instance-00000001 --cpu-total
```
- [ ] memory 지표값 ≈ `$D/memory.current` (값이 **존재** = climb이 도메인 cgroup 잡음, ENOENT 아님)
- [ ] **climb 검증**: cpu 지표값이 `$D/cpu.stat` usage_usec/1e6 (전에 본 ~59s대) 와 부합. `$D/emulator/cpu.stat`(~25s)면 climb 실패
- [ ] cpu 지표 ≈ `virsh domstats ... cpu.time` 과 대략 부합
- [ ] 30초 간격 2회 curl → Counter 3종 단조 증가

**실패 시** (scratch라 exec 불가 → 로그가 유일 단서)
- `kubectl -n openstack logs -l app=qemu-exporter` 의 `slog` 에러가 pidfile / cgroup / libvirt 중 어디서 깨졌는지 표시
- 호스트에서 대조: `sudo ls -l /var/run/libvirt/qemu/ /var/run/libvirt/libvirt-sock-ro`, `sudo cat /proc/<pid>/cgroup`

### ▶ G2 판정: **통과 (2026-09-07)**
- 6지표 전부 노출, 라벨 5종(flavor=m1.tiny, project_id 포함), `scrape_errors_total=0`, `vms_discovered=1`
- CPU 141.8s ≈ domain `cpu.stat` 141.0s ≈ `virsh domstats cpu.time` 141.36s (emulator 85.6s 아님 → climb 검증됨)
- memory 660,324,352 == domain `memory.current` 정확히 일치
- Counter 단조 증가 확인, `go_*`/`process_*`/`promhttp_*` 오염 없음
- 배포 중 발견·수정: (1) `/emulator` threaded 리프 → 도메인 cgroup climb, (2) cgroupns `/../` 프리픽스 → `filepath.Clean`, (3) startup config echo 로그 추가. **3건 미커밋.**
- 다음: `basic_plan.md` Day 7 (Prometheus 연동) → Fig.1 → Fig.3.

## Phase F — 마무리

- [ ] Prometheus scrape 대상에 추가 (`deploy/scrape-config.md` 참고), `node` 라벨 relabel
- [ ] PromQL `on(node)` 조인 동작 확인 (VM PSI ↔ 같은 노드 k8s 워크로드)
- [ ] D-1~D-3 / G1 대조 결과를 논문 4.1·5장 재료로 텍스트 저장
- [ ] 첫 실행에서 고친 것(Dockerfile, cgroup 경로 로직, 마운트 등) 커밋 — **커밋은 직접**
- [ ] `HANDOFF.md`의 "AWS 검증" 항목 체크 반영

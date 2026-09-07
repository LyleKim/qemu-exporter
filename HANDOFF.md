# qemu-exporter — 작업 인수인계 (2026-09-06)

> 이 파일은 세션 재개용 메모입니다. 커밋 여부는 직접 결정하세요.

## 현재 상태

**Go 코드 구현 완료.** 13개 태스크 전부 완료 → 태스크별 리뷰 통과 → 전체 코드 최종 리뷰 →
수정 1회 → 재리뷰 통과. `gofmt`·`go vet`·`go test ./...`·정적 빌드 전부 green.

- **`main` == `worktree-qemu-exporter` == `origin/main` == `f61d65a`** (18 commits, root `7fd3852`).
  세 ref 모두 동일 → 코드가 이미 로컬·GitHub `main` 양쪽에 반영됨. 별도 merge 불필요.
- 2026-09-06: `git filter-branch`로 전체 히스토리에서 `Co-Authored-By: Claude` /
  `Claude-Session:` 트레일러 제거 후 `git push --force origin main` 완료. `git log --all`
  트레일러 0건, GitHub도 클린 확인. (내용은 바이트 단위 동일, 메시지·SHA만 변경.)
- worktree(`.claude/worktrees/qemu-exporter/`)는 아직 존재 — 필요 없으면 정리 가능.
- 이 `HANDOFF.md`는 아직 미커밋. 커밋 여부는 직접 결정.
- 상세 이력·모든 판단(Ruling)·보류(PARKED) 항목: `.superpowers/sdd/2026-09-05-qemu-exporter/progress.md`
  (원장의 SHA들은 rewrite 이전 값 — 히스토리 참고용)
- **커밋 규칙: 앞으로 커밋은 사용자가 직접. Claude 트레일러 금지.**

## 디렉터리 구조

```
cmd/qemu-exporter/main.go   진입점: 설정 읽고 3계층 조립, :9179에서 /metrics 서빙, graceful shutdown
internal/
  cgroupsrc/     cgroup 파일 파서 3종 (libvirt 모름) — collector만 호출
    cpustat.go       cpu.stat 의 usage_usec (µs)
    memcurrent.go    memory.current (bytes)
    psi.go           cpu.pressure 의 some/total (µs). 파일 없으면 ErrPSIUnsupported
  procsrc/
    schedstat.go     /proc/<pid>/task/*/schedstat 대기시간 합산 (procfs 재사용) — collector만 호출
  libvirtsrc/    식별 계층 — "이 VM이 누구고 어디 있나"
    source.go        Domain 구조체 + DomainSource 인터페이스 (collector가 의존하는 계약)
    client.go        libvirt RO 소켓 연결 (qemu+unix URI, 3s dial timeout)
    domain.go        도메인 XML 에서 flavor/project UUID 파싱 + pidfile 에서 PID
    cgrouppath.go    /proc/<pid>/cgroup 읽어 실제 cgroup 경로 확정 (procfs 재사용)
    cache.go         DomainSource 구현체. 매 스크레이프 활성 VM diff, 새 VM만 비싼 조회, 3s RPC timeout
  collector/
    collector.go     prometheus.Collector. 6개 지표. VM마다 4개 파서 호출 → µs/ns→s 변환 → emit
deploy/
  Dockerfile       golang:1.25-alpine 빌드 → scratch
  daemonset.yaml   hostPID:true, privileged 없음, 호스트 3경로 readOnly 마운트
  scrape-config.md Prometheus relabel + PromQL 예시
CLAUDE.md / docs/superpowers/{specs,plans}/2026-09-05-*   규칙·설계·구현계획
basic_plan.md   AWS 14일 실측 TDL (별개 단계, 미착수)
```

데이터 흐름: `Prometheus → collector.Collect() → libvirtsrc.Cache.Domains() → VM별 cgroupsrc/procsrc 파서 → 지표 emit`

## 새 세션에서 재개하는 법

1. 이 worktree로 들어간다 (EnterWorktree `path` 또는 cd)
2. `.superpowers/sdd/2026-09-05-qemu-exporter/progress.md` 읽는다 (전체 이력)
3. 아래 "남은 일" 중 할 것을 고른다

## 남은 일

### 1. 브랜치 마무리 (지금 결정)
- A) `worktree-qemu-exporter` → 로컬 `main` merge, 그다음 `gh repo create LyleKim/qemu-exporter` + push
- B) 그대로 두고 나중에 직접 merge
- C) 지금 GitHub 저장소 + PR

### 2. AWS 호스트에서만 검증 가능한 것 (코드가 의존하지만 로컬 테스트 불가)
- [ ] libvirt가 qemu pidfile을 `<LIBVIRT_SOCK 디렉터리>/qemu/<도메인>.pid` 에 쓰는지, 그 디렉터리가 마운트된 `/var/run/libvirt` 아래인지
- [ ] `/host/proc` 로 본 `/proc/<pid>/cgroup` 경로가 `/host/sys/fs/cgroup` 에 join되는 호스트 절대경로인지
- [ ] `kernel.sched_schedstats=1` (아니면 schedstat 이 조용히 all-zero)
- [ ] `docker build` + 컨테이너 스모크 테스트 (로컬 Docker 없어 Task 12에서 deferred)
- [ ] 실제 libvirtd 연결 (`client.go` 의 `ConnectToURI` 경로), 멈춘 소켓에서 dial timeout 동작

### 3. 논문 실측 (완전히 별개, 미착수)
`basic_plan.md` 의 14일 TDL: 환경 기동(Day 1) → Fig.1 → Fig.3 경합 실험 → 집필.
이 exporter 는 그 실험의 도구.

## 보류(PARKED) / 알려진 미세 이슈 — 블로킹 아님

- `collector.go` 의 cpu.pressure read 근처 inline 주석 1줄이 부정확: 최종 리뷰 수정에서
  emit-as-you-go 로 바뀌어, 비-PSI cpu.pressure 에러(파일 손상/EACCES)는 이제 그 VM 의
  나머지 3개 지표를 emit하면서 `scrape_errors_total` 도 증가시킴 (예전엔 VM 전체 스킵).
  동작 자체는 spec 부합. 주석만 고치면 됨.
- `psiUnsupportedOnce` 는 프로세스 전역 `sync.Once` — 테스트 실행 시 첫 PSI 테스트만 로그 관측
  (현재 아무 테스트도 그 로그를 검증 안 하므로 무해).

## 설계 결정 (확정, 재논의 불필요)

- go-libvirt 연결: `qemu+unix:///system?socket=<LIBVIRT_SOCK>` URI + `ConnectToURI`, RO 소켓
- cgroup 경로/schedstat: 커스텀 파서 대신 `prometheus/procfs` 의 `Proc.Cgroups()` /
  `AllThreads()+Schedstat()` 재사용
- 캐시 무효화: lifecycle 이벤트/타이머 없이 매 스크레이프 활성 도메인 diff 만
- 모듈 경로: `github.com/LyleKim/qemu-exporter` (GitHub 저장소는 merge 후 생성 예정)
- 지표: CLAUDE.md 표의 4개 + exporter 자체 2개. 그 이상 추가 금지
- 로깅 `log/slog`, 테스트 `testing` + `testutil.CollectAndCompare`

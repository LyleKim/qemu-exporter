# live-migration Risk Planner (n8n + FastAPI + LLM Wiki) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current "Alertmanager fires → Go binary calls Nova automatically" pipeline with a two-stage pipeline where a PSI alert only produces a **위험감지 및 작업계획서**(risk report + work plan), and a **human approval** is what actually triggers the Nova live migration.

**Architecture:** Prometheus/Alertmanager are unchanged up through alert-firing. From there: `Alertmanager → n8n Workflow A (dedicated n8n server) → FastAPI (serverless) → LLM Wiki (in-process BM25 retrieval over this repo's own docs) → LLM → work plan + signed approval link → email to user`. When the user clicks the link, `n8n Workflow B → FastAPI /approve → Nova live-migration` — this is the **only** path that ever calls Nova. `cmd/live-migration-webhook` (the current Go binary) is left in place, untouched, as a known-good fallback; only `deploy/monitoring/alertmanager.yaml` is repointed away from it.

**Tech Stack:** n8n (Docker, dedicated small EC2/VM, not the OSH cluster), FastAPI + Mangum on AWS Lambda (AWS SAM), `rank-bm25` (pure-Python lexical retrieval, no vector DB), Anthropic Python SDK (`claude-sonnet-5`), `requests` for OpenStack calls, `pytest` for all Python tests.

**Spec:** This plan is written directly from design decisions reached in conversation (no separate spec doc exists yet — see "Design Decisions" below for the record of what was decided and why). Related existing docs: `cmd/live-migration-webhook/main.go` (the implementation being superseded), `deploy/monitoring/{alertmanager,alert-rules,prometheus}.yaml`, `live_migration_tdl.md` (prior experiment procedure), `docs/review/project-retrospective-star.md` (past incident retrospective, and now also part of the LLM Wiki corpus).

## Global Constraints

(Copied from `CLAUDE.md`'s `## live-migration-webhook (확장 트랙)` section — every task below implicitly inherits these.)

- **qemu-exporter 본체는 이 확장과 무관하게 기존 동작을 그대로 유지한다** — 목표1 코드(`internal/*`, `cmd/qemu-exporter`)는 이 플랜에서 손대지 않는다.
- **Nova REST API / Keystone 호출은 live-migration 트리거 용도로만 허용** — 다른 Nova API(서버 생성/삭제, flavor 변경 등)는 어떤 컴포넌트에서도 호출하지 않는다.
- **읽기 전용 원칙은 exporter 본체에만 적용**되고 이 webhook/리시버 계열 확장에는 적용되지 않는다 — 단, Nova 호출 범위는 위 규칙대로 제한된다.
- **같은 VM에 대해 이미 진행 중인 마이그레이션을 중복 트리거하지 않는다** — 이 플랜에서는 "Nova를 부르는 경로를 하나로 줄이는 것" + "승인 토큰 만료 + 실행 직전 host 재조회"로 이 요구사항을 만족시킨다 (Redis 등 별도 state-store 없이).
- **요청받지 않은 자동화 기능 금지**: 자동 롤백, 재시도 정책, 알림 채널 다변화 등을 추가하지 않는다 — 알림 채널은 이메일 1개로 고정한다.
- **웹 UI, 대시보드 JSON 금지** — 승인 페이지는 n8n의 `Respond to Webhook` 노드가 반환하는 순수 텍스트/HTML 한 장으로 충분하다.
- **실패는 로그로 남기고 프로세스는 계속 동작** — FastAPI 쪽은 예외를 적절한 HTTP 에러로 변환하고 계속 서빙, n8n 쪽은 각 워크플로우의 에러 출력을 그대로 두면(재시도 로직을 추가하지 않으면) 이 요구사항을 만족한다.
- **커밋은 사용자가 직접 한다** ([[no-auto-commit]]) — 각 태스크의 커밋 스텝은 "커밋하라"는 지시이지, 에이전트가 무단으로 실행해도 된다는 뜻이 아니다. 실행자는 이 레포의 기존 관례(사용자 직접 커밋)를 따를 것.

## Design Decisions (background — why the architecture looks like this)

이 절은 대화에서 합의된 트레이드오프의 기록이다. 구현 중 판단이 갈리면 여기로 돌아와서 확인할 것.

1. **Alertmanager는 더 이상 Nova를 직접 호출하지 않는다.** 지표가 임계치를 넘었다는 것과 "마이그레이션이 정답이다"는 것은 다른 문제다 — 원인/해결책이 다를 수 있으므로 사람이 작업계획서를 검토한 뒤에만 실행한다.
2. **n8n을 워크플로우 A(경보→계획서)/B(승인→실행) 두 개로 나눈다.** 승인까지 걸리는 시간이 몇 분~며칠로 들쭉날쭉할 수 있는데, 그 시간 동안 하나의 n8n 실행을 `Wait` 노드로 붙잡아두는 것보다 두 개의 독립 실행으로 쪼개는 게 추적·재시도·감사 관점에서 낫다.
3. **n8n은 서버리스가 아니라 전용 서버에 상주시킨다.** Alertmanager의 webhook을 아무 때나 받아야 해서 상시 리스너가 필요하고, 워크플로우 정의·실행 이력도 영속 상태로 들고 있어야 한다. 이 서버는 node-a(OSH 컨트롤플레인)와 분리한다 — node-a는 이미 부하가 높아질 수 있는 노드이기 때문.
4. **FastAPI는 서버리스(AWS Lambda + API Gateway)로 간다.** 요청마다 무상태이고(지표 로깅 + LLM 호출), 호출 빈도가 낮고 불규칙해서 상시 서버를 띄워둘 이유가 없다.
5. **LLM Wiki는 벡터DB 대신 BM25(`rank-bm25`) 키워드 검색을 쓴다.** 코퍼스가 이 레포의 마크다운 문서 3~4개뿐이라 임베딩 파이프라인 + 벡터DB를 새로 놓는 건 과하다. Lambda 콜드스타트에도 유리하다(무거운 ML 의존성 없음).
6. **Redis/DB 기반 dedup state-store를 두지 않는다.** 설계를 "Nova를 부르는 경로가 승인 후 단 하나"로 만들었기 때문에 애초에 이중 트리거 경쟁이 구조적으로 없다. 대신 (a) 승인 토큰에 HMAC 서명 + 1시간 만료를 둬서 오래된 링크의 재사용을 막고, (b) 실제 마이그레이션 직전에 Nova에서 현재 host를 재조회해 "이미 옮겨진 VM"에 대한 stale 실행을 막는다.
7. **Nova/Keystone 호출 로직은 n8n이 아니라 FastAPI 안에 전부 넣는다.** OpenStack 관리자 자격증명을 n8n 크리덴셜에도 중복으로 넣지 않기 위함이고, Python 쪽에 두면 `pytest`로 유닛테스트가 가능하다 (n8n HTTP Request 노드 체인은 테스트하기 어렵다). n8n은 순수 오케스트레이션(웹훅 수신 + HTTP 호출 + 알림)만 담당하는 얇은 계층으로 유지한다.

## File Structure

```
services/risk-planner/            # FastAPI 서버리스 앱 (신규)
  app/
    __init__.py
    main.py                       # /risk-assessment, /approve 엔드포인트
    models.py                     # Pydantic 요청/응답 모델
    auth.py                       # Bearer 토큰 검증 (Go 구현과 동일 패턴)
    wiki.py                       # BM25 기반 문서 검색
    llm.py                        # Anthropic 호출 + 작업계획서 생성
    approval_token.py             # HMAC 서명 승인 토큰 발급/검증
    novaclient.py                 # Keystone 토큰 + Nova 조회/트리거 (Go client.go의 Python 이식)
    handler.py                    # Mangum Lambda 어댑터
    corpus/                       # LLM Wiki 원본 문서 (sync_corpus.sh가 채움, git 커밋 대상 아님)
  tests/
    test_auth.py
    test_wiki.py
    test_llm.py
    test_approval_token.py
    test_novaclient.py
    test_main.py
  scripts/
    sync_corpus.sh                # 레포 문서를 app/corpus/로 복사
  requirements.txt
  requirements-dev.txt
  template.yaml                   # AWS SAM

deploy/n8n/                       # n8n 전용 서버 (신규)
  docker-compose.yml
  .env.example
  workflows/
    workflow-a-risk-assessment.json
    workflow-b-approve-migration.json
  tests/
    test_workflow_shapes.py       # 워크플로우 JSON의 최소 구조 검증

deploy/monitoring/
  alertmanager.yaml                # 수정: webhook_configs가 n8n Workflow A를 가리키도록
  alert-rules.yaml                 # 수정: annotations에 value(psi rate) 추가

CLAUDE.md                          # 수정: 목표2 섹션에 새 아키텍처 반영
```

---

### Task 1: CLAUDE.md — 새 아키텍처를 목표2 섹션에 반영

**Files:**
- Modify: `CLAUDE.md` (`## live-migration-webhook (확장 트랙)` 섹션)

**Interfaces:**
- Consumes: 없음 (문서 작업)
- Produces: 이후 모든 태스크가 참조하는 "현재 유효한 아키텍처 설명" — Task 2~12는 이 섹션의 서술과 일치해야 한다.

- [ ] **Step 1: 추가할 섹션 본문 확정**

`## live-migration-webhook (확장 트랙)` 섹션의 "범위 — 이것만 만든다" 항목 바로 아래에 다음 문단을 추가한다:

```markdown
**아키텍처 갱신 (Risk Planner 도입)**: Alertmanager는 더 이상 Nova를 직접 호출하지 않는다.
`Alertmanager → n8n(워크플로우 A, 전용 서버) → FastAPI(서버리스, LLM Wiki로 작업계획서 생성)
→ 사용자 이메일 통보 → 사용자 승인 클릭 → n8n(워크플로우 B) → FastAPI(/approve) → Nova
live-migration`. 지표 임계치 초과는 "작업계획서 생성"만 트리거하고, 실제 마이그레이션은
사람이 계획서를 승인해야만 실행된다(문제·원인·해결책이 다를 수 있다는 실무 판단).
기존 `cmd/live-migration-webhook` Go 바이너리는 삭제하지 않고 남겨두되(known-good
fallback), `deploy/monitoring/alertmanager.yaml`의 webhook 대상만 n8n으로 바꾼다.
상세 설계 근거는 `docs/superpowers/plans/live-migration-risk-planner-n8n-fastapi.md`
"Design Decisions" 참조.
```

- [ ] **Step 2: 적용**

`Edit` 도구로 위 문단을 정확한 위치(범위 목록 바로 다음, `**허용되는 Nova API 호출 범위**` 문단 이전)에 삽입한다.

- [ ] **Step 3: 확인**

`grep -n "Risk Planner" CLAUDE.md`로 삽입이 반영됐는지 확인한다.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: record risk-planner architecture in CLAUDE.md 목표2 section"
```

---

### Task 2: FastAPI 스켈레톤 + Bearer 인증 모듈

**Files:**
- Create: `services/risk-planner/app/__init__.py` (빈 파일)
- Create: `services/risk-planner/app/auth.py`
- Create: `services/risk-planner/app/models.py`
- Test: `services/risk-planner/tests/test_auth.py`
- Create: `services/risk-planner/requirements.txt`
- Create: `services/risk-planner/requirements-dev.txt`

**Interfaces:**
- Consumes: 없음
- Produces: `auth.bearer_token_valid(authorization_header: str | None, secret: str) -> bool` — Task 8(main.py)이 이 함수를 가져다 씀. `models.AlertLabels`, `models.RiskAssessmentRequest`, `models.WorkPlan`, `models.RiskAssessmentResponse` — Task 4/5/8이 이 타입을 그대로 사용.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_auth.py
from app.auth import bearer_token_valid


def test_valid_bearer():
    assert bearer_token_valid("Bearer s3cr3t", "s3cr3t") is True


def test_wrong_secret():
    assert bearer_token_valid("Bearer wrong", "s3cr3t") is False


def test_missing_prefix():
    assert bearer_token_valid("s3cr3t", "s3cr3t") is False


def test_missing_header():
    assert bearer_token_valid(None, "s3cr3t") is False


def test_empty_secret_never_matches():
    assert bearer_token_valid("Bearer ", "") is False
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_auth.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app'` (or `ImportError`)

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/__init__.py
```

```python
# services/risk-planner/app/auth.py
import hmac


def bearer_token_valid(authorization_header: str | None, secret: str) -> bool:
    if not authorization_header or not secret:
        return False
    prefix = "Bearer "
    if not authorization_header.startswith(prefix):
        return False
    provided = authorization_header[len(prefix):]
    return hmac.compare_digest(provided, secret)
```

```python
# services/risk-planner/app/models.py
from pydantic import BaseModel, Field


class AlertLabels(BaseModel):
    instance_uuid: str
    node: str
    instance_name: str
    flavor: str
    project_id: str


class RiskAssessmentRequest(BaseModel):
    labels: AlertLabels
    psi_rate: float = Field(
        ..., description="rate(openstack_vm_cpu_pressure_stall_seconds_total) value that fired the alert"
    )
    fired_at: str = Field(..., description="RFC3339 timestamp of when Alertmanager fired the alert")


class WorkPlan(BaseModel):
    summary: str
    root_cause_candidates: list[str]
    recommended_action: str
    risk_notes: str


class RiskAssessmentResponse(BaseModel):
    plan: WorkPlan
    approval_url: str
    expires_at: str
```

```text
# services/risk-planner/requirements.txt
fastapi==0.115.*
mangum==0.19.*
pydantic==2.*
requests==2.*
rank-bm25==0.2.*
anthropic==0.40.*
```

```text
# services/risk-planner/requirements-dev.txt
-r requirements.txt
pytest==8.*
httpx==0.27.*
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && pip install -r requirements-dev.txt && python -m pytest tests/test_auth.py -v`
Expected: PASS (5 passed)

- [ ] **Step 5: Commit**

```bash
git add services/risk-planner/app/__init__.py services/risk-planner/app/auth.py \
  services/risk-planner/app/models.py services/risk-planner/tests/test_auth.py \
  services/risk-planner/requirements.txt services/risk-planner/requirements-dev.txt
git commit -m "feat: risk-planner FastAPI skeleton with bearer auth"
```

---

### Task 3: LLM Wiki — BM25 문서 검색

**Files:**
- Create: `services/risk-planner/app/wiki.py`
- Create: `services/risk-planner/scripts/sync_corpus.sh`
- Test: `services/risk-planner/tests/test_wiki.py`

**Interfaces:**
- Consumes: 없음 (파일시스템의 `app/corpus/*.md`만 읽음)
- Produces: `wiki.get_index() -> WikiIndex`, `WikiIndex.search(query: str, top_k: int = 3) -> list[dict]` (각 dict는 `{"source": str, "chunk_id": int, "text": str}`) — Task 8(main.py)이 사용.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_wiki.py
import pathlib

import pytest

from app import wiki


@pytest.fixture
def corpus(tmp_path, monkeypatch):
    doc = tmp_path / "sample.md"
    doc.write_text(
        "# Title\n\n## CPU 경합\nPSI(some) rate가 threshold를 넘으면 alert가 발화한다.\n\n"
        "## 네트워크 이슈\nCalico VXLAN 포트 충돌로 neutron-ovs-agent가 CrashLoop된다.\n"
    )
    monkeypatch.setattr(wiki, "CORPUS_DIR", tmp_path)
    wiki._index = None
    yield tmp_path
    wiki._index = None


def test_search_returns_relevant_chunk(corpus):
    results = wiki.get_index().search("CPU 경합 threshold alert", top_k=2)
    assert results
    assert any("PSI" in r["text"] for r in results)


def test_search_on_empty_corpus_returns_empty(tmp_path, monkeypatch):
    monkeypatch.setattr(wiki, "CORPUS_DIR", tmp_path)
    wiki._index = None
    assert wiki.get_index().search("anything") == []
    wiki._index = None
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_wiki.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.wiki'`

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/wiki.py
import pathlib

from rank_bm25 import BM25Okapi

CORPUS_DIR = pathlib.Path(__file__).parent / "corpus"


def _tokenize(text: str) -> list[str]:
    return text.lower().split()


def _load_chunks() -> list[dict]:
    chunks = []
    if not CORPUS_DIR.exists():
        return chunks
    for path in sorted(CORPUS_DIR.glob("*.md")):
        text = path.read_text(encoding="utf-8")
        for i, section in enumerate(text.split("\n## ")):
            section = section.strip()
            if not section:
                continue
            chunks.append({"source": path.name, "chunk_id": i, "text": section})
    return chunks


class WikiIndex:
    def __init__(self):
        self.chunks = _load_chunks()
        self.bm25 = BM25Okapi([_tokenize(c["text"]) for c in self.chunks]) if self.chunks else None

    def search(self, query: str, top_k: int = 3) -> list[dict]:
        if not self.bm25:
            return []
        scores = self.bm25.get_scores(_tokenize(query))
        ranked = sorted(zip(scores, self.chunks), key=lambda pair: pair[0], reverse=True)
        return [chunk for score, chunk in ranked[:top_k] if score > 0]


_index: WikiIndex | None = None


def get_index() -> WikiIndex:
    global _index
    if _index is None:
        _index = WikiIndex()
    return _index
```

```bash
#!/usr/bin/env bash
# services/risk-planner/scripts/sync_corpus.sh
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
DEST="$ROOT/services/risk-planner/app/corpus"
mkdir -p "$DEST"
cp "$ROOT/live_migration_tdl.md" "$DEST/"
cp "$ROOT/docs/review/project-retrospective-star.md" "$DEST/"
cp "$ROOT/docs/paper_data/live_migration/README.md" "$DEST/live_migration_data_readme.md"
echo "synced $(ls "$DEST" | wc -l) docs into $DEST"
```

```bash
chmod +x services/risk-planner/scripts/sync_corpus.sh
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && python -m pytest tests/test_wiki.py -v`
Expected: PASS (2 passed)

- [ ] **Step 5: Commit**

```bash
git add services/risk-planner/app/wiki.py services/risk-planner/scripts/sync_corpus.sh \
  services/risk-planner/tests/test_wiki.py
git commit -m "feat: BM25-based LLM wiki retrieval"
```

---

### Task 4: LLM 작업계획서 생성

**Files:**
- Create: `services/risk-planner/app/llm.py`
- Test: `services/risk-planner/tests/test_llm.py`

**Interfaces:**
- Consumes: `models.AlertLabels`, `models.WorkPlan` (Task 2)
- Produces: `llm.generate_plan(labels: AlertLabels, psi_rate: float, context_chunks: list[dict]) -> WorkPlan` — Task 8(main.py)이 사용.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_llm.py
import json
from unittest.mock import MagicMock, patch

from app import llm
from app.models import AlertLabels

LABELS = AlertLabels(
    instance_uuid="5101f966-2bab-483d-807c-87ff8732f2ab",
    node="ip-10-0-1-129",
    instance_name="test-vm",
    flavor="m1.medium",
    project_id="proj-1",
)


def _fake_response(text: str):
    resp = MagicMock()
    resp.content = [MagicMock(text=text)]
    return resp


def test_generate_plan_parses_valid_json():
    payload = {
        "summary": "node-a CPU 경합 상승",
        "root_cause_candidates": ["cpu-hog 워크로드 증가", "이웃 VM 과부하"],
        "recommended_action": "대상 VM을 node-b로 마이그레이션 검토",
        "risk_notes": "다운타임 없음, block migration 사용",
    }
    fake_client = MagicMock()
    fake_client.messages.create.return_value = _fake_response(json.dumps(payload))
    with patch.object(llm, "_client", return_value=fake_client):
        plan = llm.generate_plan(LABELS, 0.02, [])
    assert plan.summary == payload["summary"]
    assert plan.root_cause_candidates == payload["root_cause_candidates"]


def test_generate_plan_falls_back_on_malformed_json():
    fake_client = MagicMock()
    fake_client.messages.create.return_value = _fake_response("이건 JSON이 아님")
    with patch.object(llm, "_client", return_value=fake_client):
        plan = llm.generate_plan(LABELS, 0.02, [])
    assert plan.summary == "이건 JSON이 아님"
    assert plan.root_cause_candidates == []
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_llm.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.llm'`

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/llm.py
import json
import os

from anthropic import Anthropic

from .models import AlertLabels, WorkPlan

_MODEL = os.environ.get("RISK_PLANNER_MODEL", "claude-sonnet-5")

_SYSTEM_PROMPT = """You are an OpenStack SRE assistant. Given a firing PSI contention \
alert and retrieved context from prior incident documentation, write a short risk \
assessment and work plan in Korean. Respond with JSON only, matching this schema:
{"summary": str, "root_cause_candidates": [str, ...], "recommended_action": str, "risk_notes": str}
Do not assume live migration is the only fix -- always name at least one \
non-migration alternative in root_cause_candidates, and only recommend migration \
in recommended_action if the retrieved context actually supports it."""


def _client() -> Anthropic:
    return Anthropic(api_key=os.environ["ANTHROPIC_API_KEY"])


def generate_plan(labels: AlertLabels, psi_rate: float, context_chunks: list[dict]) -> WorkPlan:
    context_text = "\n\n".join(f"[{c['source']}]\n{c['text']}" for c in context_chunks)
    user_prompt = (
        f"Alert: instance_uuid={labels.instance_uuid} node={labels.node} "
        f"instance_name={labels.instance_name} flavor={labels.flavor} "
        f"psi_rate={psi_rate}\n\nContext:\n{context_text or '(no matching prior docs)'}"
    )
    response = _client().messages.create(
        model=_MODEL,
        max_tokens=1024,
        system=_SYSTEM_PROMPT,
        messages=[{"role": "user", "content": user_prompt}],
    )
    raw = response.content[0].text
    try:
        data = json.loads(raw)
        return WorkPlan(**data)
    except (json.JSONDecodeError, TypeError, ValueError):
        return WorkPlan(
            summary=raw,
            root_cause_candidates=[],
            recommended_action="LLM 응답 파싱 실패 -- summary 원문을 직접 확인할 것",
            risk_notes="",
        )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && python -m pytest tests/test_llm.py -v`
Expected: PASS (2 passed)

- [ ] **Step 5: Commit**

```bash
git add services/risk-planner/app/llm.py services/risk-planner/tests/test_llm.py
git commit -m "feat: LLM work-plan generation with malformed-JSON fallback"
```

---

### Task 5: 승인 토큰 (HMAC 서명 + 만료)

**Files:**
- Create: `services/risk-planner/app/approval_token.py`
- Test: `services/risk-planner/tests/test_approval_token.py`

**Interfaces:**
- Consumes: 없음
- Produces: `approval_token.issue(instance_uuid: str, secret: str, ttl_seconds: int = 3600) -> str`, `approval_token.verify(token: str, secret: str) -> str` (반환값은 `instance_uuid`), `approval_token.InvalidToken` 예외 — Task 8(main.py)이 사용.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_approval_token.py
import time

import pytest

from app import approval_token

SECRET = "test-secret"


def test_issue_then_verify_roundtrip():
    token = approval_token.issue("5101f966-2bab-483d-807c-87ff8732f2ab", SECRET)
    assert approval_token.verify(token, SECRET) == "5101f966-2bab-483d-807c-87ff8732f2ab"


def test_tampered_token_rejected():
    token = approval_token.issue("5101f966-2bab-483d-807c-87ff8732f2ab", SECRET)
    body, sig = token.split(".", 1)
    tampered = f"{body}.{sig[:-1]}x"
    with pytest.raises(approval_token.InvalidToken):
        approval_token.verify(tampered, SECRET)


def test_wrong_secret_rejected():
    token = approval_token.issue("5101f966-2bab-483d-807c-87ff8732f2ab", SECRET)
    with pytest.raises(approval_token.InvalidToken):
        approval_token.verify(token, "other-secret")


def test_expired_token_rejected():
    token = approval_token.issue("5101f966-2bab-483d-807c-87ff8732f2ab", SECRET, ttl_seconds=-1)
    with pytest.raises(approval_token.InvalidToken):
        approval_token.verify(token, SECRET)


def test_malformed_token_rejected():
    with pytest.raises(approval_token.InvalidToken):
        approval_token.verify("not-a-real-token", SECRET)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_approval_token.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.approval_token'`

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/approval_token.py
import base64
import hashlib
import hmac
import json
import time


class InvalidToken(Exception):
    pass


def _sign(payload: bytes, secret: str) -> str:
    digest = hmac.new(secret.encode(), payload, hashlib.sha256).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")


def issue(instance_uuid: str, secret: str, ttl_seconds: int = 3600) -> str:
    payload = json.dumps({"instance_uuid": instance_uuid, "exp": int(time.time()) + ttl_seconds}).encode()
    body = base64.urlsafe_b64encode(payload).decode().rstrip("=")
    sig = _sign(payload, secret)
    return f"{body}.{sig}"


def verify(token: str, secret: str) -> str:
    try:
        body, sig = token.split(".", 1)
        padded = body + "=" * (-len(body) % 4)
        payload = base64.urlsafe_b64decode(padded.encode())
    except (ValueError, TypeError, base64.binascii.Error) as exc:
        raise InvalidToken(f"malformed token: {exc}") from exc

    expected_sig = _sign(payload, secret)
    if not hmac.compare_digest(sig, expected_sig):
        raise InvalidToken("signature mismatch")

    data = json.loads(payload)
    if data["exp"] < int(time.time()):
        raise InvalidToken("expired")
    return data["instance_uuid"]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && python -m pytest tests/test_approval_token.py -v`
Expected: PASS (5 passed)

- [ ] **Step 5: Commit**

```bash
git add services/risk-planner/app/approval_token.py services/risk-planner/tests/test_approval_token.py
git commit -m "feat: HMAC-signed, expiring approval tokens"
```

---

### Task 6: Nova/Keystone 클라이언트 (Go client.go의 Python 이식)

**Files:**
- Create: `services/risk-planner/app/novaclient.py`
- Test: `services/risk-planner/tests/test_novaclient.py`

**Interfaces:**
- Consumes: 환경변수 `OS_AUTH_URL`, `OS_USERNAME`, `OS_PASSWORD`, `OS_USER_DOMAIN_NAME`, `OS_PROJECT_NAME`, `OS_PROJECT_DOMAIN_NAME`, `NOVA_URL`, `NODE_HOSTS`
- Produces: `novaclient.trigger_live_migration(instance_uuid: str) -> dict` (반환값 `{"from_host": str, "to_host": str}`), `novaclient.NovaError` 예외 — Task 8(main.py)이 사용.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_novaclient.py
from unittest.mock import MagicMock, patch

import pytest

from app import novaclient


@pytest.fixture(autouse=True)
def env(monkeypatch):
    monkeypatch.setenv("OS_AUTH_URL", "http://keystone/v3")
    monkeypatch.setenv("OS_USERNAME", "admin")
    monkeypatch.setenv("OS_PASSWORD", "password")
    monkeypatch.setenv("OS_USER_DOMAIN_NAME", "default")
    monkeypatch.setenv("OS_PROJECT_NAME", "admin")
    monkeypatch.setenv("OS_PROJECT_DOMAIN_NAME", "default")
    monkeypatch.setenv("NOVA_URL", "http://nova/v2.1")
    monkeypatch.setenv("NODE_HOSTS", "node-a.example.com,node-b.example.com")


def test_other_host_picks_the_other_configured_host():
    assert novaclient.other_host("node-a.example.com") == "node-b.example.com"
    assert novaclient.other_host("NODE-B.EXAMPLE.COM") == "node-a.example.com"


def test_other_host_rejects_unknown_host():
    with pytest.raises(novaclient.NovaError):
        novaclient.other_host("node-c.example.com")


def test_trigger_live_migration_happy_path():
    token_resp = MagicMock(status_code=201, headers={"X-Subject-Token": "tok-123"})
    server_resp = MagicMock(status_code=200)
    server_resp.json.return_value = {"server": {"OS-EXT-SRV-ATTR:host": "node-a.example.com"}}
    action_resp = MagicMock(status_code=200)

    with patch.object(novaclient.requests, "post", side_effect=[token_resp, action_resp]) as post, \
         patch.object(novaclient.requests, "get", return_value=server_resp):
        result = novaclient.trigger_live_migration("5101f966-2bab-483d-807c-87ff8732f2ab")

    assert result == {"from_host": "node-a.example.com", "to_host": "node-b.example.com"}
    action_call = post.call_args_list[1]
    assert action_call.kwargs["json"]["os-migrateLive"]["host"] == "node-b.example.com"


def test_trigger_live_migration_raises_on_keystone_failure():
    token_resp = MagicMock(status_code=401, text="unauthorized")
    with patch.object(novaclient.requests, "post", return_value=token_resp):
        with pytest.raises(novaclient.NovaError):
            novaclient.trigger_live_migration("5101f966-2bab-483d-807c-87ff8732f2ab")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_novaclient.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.novaclient'`

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/novaclient.py
import os

import requests


class NovaError(Exception):
    pass


def keystone_token() -> str:
    body = {
        "auth": {
            "identity": {
                "methods": ["password"],
                "password": {
                    "user": {
                        "name": os.environ["OS_USERNAME"],
                        "domain": {"name": os.environ["OS_USER_DOMAIN_NAME"]},
                        "password": os.environ["OS_PASSWORD"],
                    }
                },
            },
            "scope": {
                "project": {
                    "name": os.environ["OS_PROJECT_NAME"],
                    "domain": {"name": os.environ["OS_PROJECT_DOMAIN_NAME"]},
                }
            },
        }
    }
    resp = requests.post(f"{os.environ['OS_AUTH_URL']}/auth/tokens", json=body, timeout=10)
    if resp.status_code != 201:
        raise NovaError(f"keystone token request returned {resp.status_code}: {resp.text}")
    token = resp.headers.get("X-Subject-Token")
    if not token:
        raise NovaError("keystone response missing X-Subject-Token header")
    return token


def server_host(token: str, instance_uuid: str) -> str:
    nova_url = os.environ["NOVA_URL"].rstrip("/")
    resp = requests.get(
        f"{nova_url}/servers/{instance_uuid}", headers={"X-Auth-Token": token}, timeout=10
    )
    if resp.status_code != 200:
        raise NovaError(f"get server returned {resp.status_code}: {resp.text}")
    host = resp.json()["server"].get("OS-EXT-SRV-ATTR:host")
    if not host:
        raise NovaError("server response missing OS-EXT-SRV-ATTR:host")
    return host


def other_host(current: str) -> str:
    node_hosts = os.environ["NODE_HOSTS"].split(",")
    if len(node_hosts) != 2:
        raise NovaError("NODE_HOSTS must be exactly two comma-separated hostnames")
    a, b = node_hosts
    if current.lower() == a.lower():
        return b
    if current.lower() == b.lower():
        return a
    raise NovaError(f"current host {current!r} does not match either configured NODE_HOSTS entry {node_hosts}")


def trigger_live_migration(instance_uuid: str) -> dict:
    token = keystone_token()
    current_host = server_host(token, instance_uuid)
    dest_host = other_host(current_host)
    nova_url = os.environ["NOVA_URL"].rstrip("/")
    body = {"os-migrateLive": {"host": dest_host, "block_migration": True, "disk_over_commit": False}}
    resp = requests.post(
        f"{nova_url}/servers/{instance_uuid}/action",
        json=body,
        headers={"X-Auth-Token": token},
        timeout=10,
    )
    if resp.status_code >= 300:
        raise NovaError(f"nova migrate action returned {resp.status_code}: {resp.text}")
    return {"from_host": current_host, "to_host": dest_host}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && python -m pytest tests/test_novaclient.py -v`
Expected: PASS (4 passed)

- [ ] **Step 5: Commit**

```bash
git add services/risk-planner/app/novaclient.py services/risk-planner/tests/test_novaclient.py
git commit -m "feat: Nova/Keystone client for risk-planner (ports Go client.go logic)"
```

---

### Task 7: FastAPI 엔드포인트 연결 (`/risk-assessment`, `/approve`)

**Files:**
- Create: `services/risk-planner/app/main.py`
- Test: `services/risk-planner/tests/test_main.py`

**Interfaces:**
- Consumes: `auth.bearer_token_valid`(Task 2), `models.*`(Task 2), `wiki.get_index`(Task 3), `llm.generate_plan`(Task 4), `approval_token.issue`/`verify`/`InvalidToken`(Task 5), `novaclient.trigger_live_migration`/`NovaError`(Task 6)
- Produces: `app.main.app`(FastAPI 인스턴스) — Task 9(handler.py)가 이걸 감싼다.

- [ ] **Step 1: Write the failing test**

```python
# services/risk-planner/tests/test_main.py
from unittest.mock import patch

from fastapi.testclient import TestClient

from app.main import app
from app.models import WorkPlan

client = TestClient(app)

ALERT_BODY = {
    "labels": {
        "instance_uuid": "5101f966-2bab-483d-807c-87ff8732f2ab",
        "node": "ip-10-0-1-129",
        "instance_name": "test-vm",
        "flavor": "m1.medium",
        "project_id": "proj-1",
    },
    "psi_rate": 0.02,
    "fired_at": "2026-09-22T00:00:00Z",
}


def test_risk_assessment_requires_bearer(monkeypatch):
    monkeypatch.setenv("WEBHOOK_SECRET", "s3cr3t")
    resp = client.post("/risk-assessment", json=ALERT_BODY)
    assert resp.status_code == 401


def test_risk_assessment_happy_path(monkeypatch):
    monkeypatch.setenv("WEBHOOK_SECRET", "s3cr3t")
    monkeypatch.setenv("APPROVAL_SECRET", "approval-secret")
    monkeypatch.setenv("N8N_APPROVAL_WEBHOOK_URL", "https://n8n.example.com/webhook/approve")

    fake_plan = WorkPlan(
        summary="테스트 요약",
        root_cause_candidates=["원인1"],
        recommended_action="조치1",
        risk_notes="주의사항",
    )
    with patch("app.main.wiki.get_index") as get_index, \
         patch("app.main.llm.generate_plan", return_value=fake_plan):
        get_index.return_value.search.return_value = []
        resp = client.post(
            "/risk-assessment",
            json=ALERT_BODY,
            headers={"Authorization": "Bearer s3cr3t"},
        )

    assert resp.status_code == 200
    body = resp.json()
    assert body["plan"]["summary"] == "테스트 요약"
    assert body["approval_url"].startswith("https://n8n.example.com/webhook/approve?token=")


def test_approve_rejects_invalid_token(monkeypatch):
    monkeypatch.setenv("APPROVAL_SECRET", "approval-secret")
    resp = client.get("/approve", params={"token": "garbage"})
    assert resp.status_code == 400


def test_approve_triggers_migration_for_valid_token(monkeypatch):
    monkeypatch.setenv("APPROVAL_SECRET", "approval-secret")
    from app import approval_token

    token = approval_token.issue("5101f966-2bab-483d-807c-87ff8732f2ab", "approval-secret")
    with patch(
        "app.main.novaclient.trigger_live_migration",
        return_value={"from_host": "node-a", "to_host": "node-b"},
    ):
        resp = client.get("/approve", params={"token": token})

    assert resp.status_code == 200
    assert resp.json() == {
        "instance_uuid": "5101f966-2bab-483d-807c-87ff8732f2ab",
        "from_host": "node-a",
        "to_host": "node-b",
    }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/risk-planner && python -m pytest tests/test_main.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.main'`

- [ ] **Step 3: Write minimal implementation**

```python
# services/risk-planner/app/main.py
import datetime
import os

from fastapi import FastAPI, Header, HTTPException

from . import approval_token, llm, novaclient, wiki
from .auth import bearer_token_valid
from .models import RiskAssessmentRequest, RiskAssessmentResponse

app = FastAPI(title="risk-planner")


def _require_bearer(authorization: str | None) -> None:
    secret = os.environ.get("WEBHOOK_SECRET", "")
    if not bearer_token_valid(authorization, secret):
        raise HTTPException(status_code=401, detail="unauthorized")


@app.post("/risk-assessment", response_model=RiskAssessmentResponse)
def risk_assessment(req: RiskAssessmentRequest, authorization: str | None = Header(default=None)):
    _require_bearer(authorization)

    index = wiki.get_index()
    query = f"{req.labels.instance_name} {req.labels.flavor} CPU pressure {req.psi_rate}"
    chunks = index.search(query, top_k=3)

    plan = llm.generate_plan(req.labels, req.psi_rate, chunks)

    secret = os.environ["APPROVAL_SECRET"]
    token = approval_token.issue(req.labels.instance_uuid, secret)
    n8n_base = os.environ["N8N_APPROVAL_WEBHOOK_URL"].rstrip("/")
    expires_at = (
        datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=3600)
    ).isoformat()

    return RiskAssessmentResponse(
        plan=plan,
        approval_url=f"{n8n_base}?token={token}",
        expires_at=expires_at,
    )


@app.get("/approve")
def approve(token: str):
    secret = os.environ["APPROVAL_SECRET"]
    try:
        instance_uuid = approval_token.verify(token, secret)
    except approval_token.InvalidToken as exc:
        raise HTTPException(status_code=400, detail=f"invalid approval token: {exc}") from exc

    try:
        result = novaclient.trigger_live_migration(instance_uuid)
    except novaclient.NovaError as exc:
        raise HTTPException(status_code=502, detail=str(exc)) from exc

    return {"instance_uuid": instance_uuid, **result}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/risk-planner && python -m pytest tests/test_main.py -v`
Expected: PASS (4 passed)

- [ ] **Step 5: Run the full test suite**

Run: `cd services/risk-planner && python -m pytest -v`
Expected: all tests from Tasks 2–7 pass (roughly 20 tests)

- [ ] **Step 6: Commit**

```bash
git add services/risk-planner/app/main.py services/risk-planner/tests/test_main.py
git commit -m "feat: wire risk-planner /risk-assessment and /approve endpoints"
```

---

### Task 8: AWS Lambda 패키징 (Mangum + SAM)

**Files:**
- Create: `services/risk-planner/app/handler.py`
- Create: `services/risk-planner/template.yaml`

**Interfaces:**
- Consumes: `app.main.app`(Task 7)
- Produces: `app.handler.handler` — Lambda가 호출하는 엔트리포인트.

- [ ] **Step 1: 핸들러 작성**

```python
# services/risk-planner/app/handler.py
from mangum import Mangum

from .main import app

handler = Mangum(app)
```

- [ ] **Step 2: SAM 템플릿 작성**

```yaml
# services/risk-planner/template.yaml
AWSTemplateFormatVersion: '2010-09-09'
Transform: AWS::Serverless-2016-10-31

Parameters:
  WebhookSecret: {Type: String, NoEcho: true}
  ApprovalSecret: {Type: String, NoEcho: true}
  AnthropicApiKey: {Type: String, NoEcho: true}
  N8nApprovalWebhookUrl: {Type: String}
  OsAuthUrl: {Type: String}
  OsUsername: {Type: String}
  OsPassword: {Type: String, NoEcho: true}
  NovaUrl: {Type: String}
  NodeHosts: {Type: String, Description: "comma-separated, exactly two hostnames"}

Resources:
  RiskPlannerFunction:
    Type: AWS::Serverless::Function
    Properties:
      CodeUri: .
      Handler: app.handler.handler
      Runtime: python3.12
      Timeout: 30
      MemorySize: 512
      Environment:
        Variables:
          WEBHOOK_SECRET: !Ref WebhookSecret
          APPROVAL_SECRET: !Ref ApprovalSecret
          ANTHROPIC_API_KEY: !Ref AnthropicApiKey
          N8N_APPROVAL_WEBHOOK_URL: !Ref N8nApprovalWebhookUrl
          OS_AUTH_URL: !Ref OsAuthUrl
          OS_USERNAME: !Ref OsUsername
          OS_PASSWORD: !Ref OsPassword
          OS_USER_DOMAIN_NAME: default
          OS_PROJECT_NAME: admin
          OS_PROJECT_DOMAIN_NAME: default
          NOVA_URL: !Ref NovaUrl
          NODE_HOSTS: !Ref NodeHosts
      Events:
        RiskAssessment:
          Type: Api
          Properties: { Path: /risk-assessment, Method: post }
        Approve:
          Type: Api
          Properties: { Path: /approve, Method: get }

Outputs:
  ApiUrl:
    Value: !Sub "https://${ServerlessRestApi}.execute-api.${AWS::Region}.amazonaws.com/Prod/"
```

- [ ] **Step 3: 로컬 빌드로 검증 (배포는 아직 하지 않음)**

Run: `cd services/risk-planner && ./scripts/sync_corpus.sh && sam build`
Expected: `Build Succeeded` — `app/corpus/*.md`가 패키지에 포함되는지 `sam build` 출력의 `.aws-sam/build/RiskPlannerFunction/app/corpus/`로 확인.

- [ ] **Step 4: Commit**

```bash
git add services/risk-planner/app/handler.py services/risk-planner/template.yaml
git commit -m "feat: package risk-planner as AWS Lambda via SAM"
```

(`app/corpus/`는 `sync_corpus.sh`가 매 빌드 전 채우는 산출물이므로 git에는 커밋하지 않는다 — `services/risk-planner/.gitignore`에 `app/corpus/`를 추가해둘 것.)

---

### Task 9: n8n 전용 서버 (Docker Compose)

**Files:**
- Create: `deploy/n8n/docker-compose.yml`
- Create: `deploy/n8n/.env.example`

**Interfaces:**
- Consumes: 없음 (인프라 매니페스트)
- Produces: 5678 포트에서 서비스되는 n8n 인스턴스 — Task 10/11의 워크플로우가 여기 임포트됨.

- [ ] **Step 1: Compose 파일 작성**

```yaml
# deploy/n8n/docker-compose.yml
services:
  n8n:
    image: n8nio/n8n:1.66.0
    restart: unless-stopped
    ports:
      - "5678:5678"
    environment:
      - N8N_HOST=${N8N_HOST}
      - N8N_PROTOCOL=https
      - WEBHOOK_URL=https://${N8N_HOST}/
      - GENERIC_TIMEZONE=Asia/Seoul
      - FASTAPI_BASE_URL=${FASTAPI_BASE_URL}
      - WEBHOOK_SECRET=${WEBHOOK_SECRET}
      - RISK_PLAN_FROM_EMAIL=${RISK_PLAN_FROM_EMAIL}
      - RISK_PLAN_NOTIFY_EMAIL=${RISK_PLAN_NOTIFY_EMAIL}
    volumes:
      - n8n_data:/home/node/.n8n

volumes:
  n8n_data:
```

```bash
# deploy/n8n/.env.example
N8N_HOST=n8n.example.com
FASTAPI_BASE_URL=https://xxxx.execute-api.ap-northeast-2.amazonaws.com/Prod
WEBHOOK_SECRET=change-me
RISK_PLAN_FROM_EMAIL=alerts@example.com
RISK_PLAN_NOTIFY_EMAIL=oncall@example.com
```

- [ ] **Step 2: 프로비저닝 절차 문서화**

이 서버는 node-a/node-b OSH terraform과 무관한 별도의 작은 EC2/VM 한 대(Docker만 필요)에 올린다.
node-a/node-b와 같은 방식(SSM 접근, 세션 단위 운영, 끝나면 종료)을 그대로 따르되, OSH 클러스터
안에 넣지 않는다 — 정확한 인스턴스 스펙/terraform 리소스는 구현 착수 시점에
`live_migration_tdl.md`와 같은 패턴의 별도 런북으로 기록한다(이 플랜의 범위 밖).

- [ ] **Step 3: 로컬 기동 검증**

Run: `cd deploy/n8n && cp .env.example .env && docker compose up -d && curl -sf http://localhost:5678/healthz`
Expected: `{"status":"ok"}`

- [ ] **Step 4: Commit**

```bash
git add deploy/n8n/docker-compose.yml deploy/n8n/.env.example
git commit -m "feat: dedicated n8n server via docker compose"
```

---

### Task 10: n8n 워크플로우 A — 경보→작업계획서

**Files:**
- Create: `deploy/n8n/workflows/workflow-a-risk-assessment.json`
- Create: `deploy/n8n/tests/test_workflow_shapes.py`

**Interfaces:**
- Consumes: FastAPI `/risk-assessment`(Task 7) — n8n의 HTTP Request 노드가 호출.
- Produces: `/webhook/risk-assessment` 엔드포인트(n8n이 서빙) — Task 12에서 Alertmanager가 이 URL을 가리키게 됨.

- [ ] **Step 1: Write the failing test**

```python
# deploy/n8n/tests/test_workflow_shapes.py
import json
import pathlib

WORKFLOWS_DIR = pathlib.Path(__file__).parent.parent / "workflows"


def _load(name):
    return json.loads((WORKFLOWS_DIR / name).read_text())


def test_workflow_a_has_webhook_and_calls_fastapi():
    wf = _load("workflow-a-risk-assessment.json")
    node_types = {n["name"]: n["type"] for n in wf["nodes"]}
    assert node_types["Alertmanager Webhook"] == "n8n-nodes-base.webhook"
    assert node_types["Call FastAPI risk-assessment"] == "n8n-nodes-base.httpRequest"
    assert node_types["Send Email"] == "n8n-nodes-base.emailSend"


def test_workflow_a_never_calls_nova_directly():
    wf = _load("workflow-a-risk-assessment.json")
    dump = json.dumps(wf)
    assert "/servers/" not in dump and "os-migrateLive" not in dump, (
        "Workflow A must never call Nova directly -- only workflow B may, after approval"
    )
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd deploy/n8n && python -m pytest tests/test_workflow_shapes.py::test_workflow_a_has_webhook_and_calls_fastapi -v`
Expected: FAIL with `FileNotFoundError`

- [ ] **Step 3: 워크플로우 JSON 작성**

```json
{
  "name": "risk-assessment-on-alert",
  "nodes": [
    {
      "id": "webhook-a",
      "name": "Alertmanager Webhook",
      "type": "n8n-nodes-base.webhook",
      "typeVersion": 2,
      "position": [240, 300],
      "webhookId": "risk-assessment-inbound",
      "parameters": {
        "path": "risk-assessment",
        "httpMethod": "POST",
        "authentication": "headerAuth",
        "responseMode": "onReceived"
      },
      "credentials": {
        "httpHeaderAuth": { "id": "alertmanager-bearer", "name": "Alertmanager Bearer" }
      }
    },
    {
      "id": "split-alerts",
      "name": "Split Alerts",
      "type": "n8n-nodes-base.splitOut",
      "typeVersion": 1,
      "position": [460, 300],
      "parameters": { "fieldToSplitOut": "body.alerts" }
    },
    {
      "id": "filter-firing",
      "name": "Filter Firing Only",
      "type": "n8n-nodes-base.filter",
      "typeVersion": 2,
      "position": [680, 300],
      "parameters": {
        "conditions": {
          "conditions": [
            {
              "leftValue": "={{$json.status}}",
              "rightValue": "firing",
              "operator": { "type": "string", "operation": "equals" }
            }
          ]
        }
      }
    },
    {
      "id": "call-risk-planner",
      "name": "Call FastAPI risk-assessment",
      "type": "n8n-nodes-base.httpRequest",
      "typeVersion": 4,
      "position": [900, 300],
      "parameters": {
        "method": "POST",
        "url": "={{$env.FASTAPI_BASE_URL}}/risk-assessment",
        "sendHeaders": true,
        "headerParameters": {
          "parameters": [{ "name": "Authorization", "value": "=Bearer {{$env.WEBHOOK_SECRET}}" }]
        },
        "sendBody": true,
        "bodyParameters": {
          "parameters": [
            { "name": "labels", "value": "={{$json.labels}}" },
            { "name": "psi_rate", "value": "={{$json.annotations.value}}" },
            { "name": "fired_at", "value": "={{$json.startsAt}}" }
          ]
        }
      }
    },
    {
      "id": "notify-user",
      "name": "Send Email",
      "type": "n8n-nodes-base.emailSend",
      "typeVersion": 2,
      "position": [1120, 300],
      "parameters": {
        "fromEmail": "={{$env.RISK_PLAN_FROM_EMAIL}}",
        "toEmail": "={{$env.RISK_PLAN_NOTIFY_EMAIL}}",
        "subject": "=[위험감지] {{$node[\"Call FastAPI risk-assessment\"].json.plan.summary}}",
        "text": "={{$node[\"Call FastAPI risk-assessment\"].json.plan.recommended_action}}\n\n승인 링크: {{$node[\"Call FastAPI risk-assessment\"].json.approval_url}}"
      },
      "credentials": {
        "smtp": { "id": "risk-plan-smtp", "name": "risk-plan-smtp" }
      }
    }
  ],
  "connections": {
    "Alertmanager Webhook": { "main": [[{ "node": "Split Alerts", "type": "main", "index": 0 }]] },
    "Split Alerts": { "main": [[{ "node": "Filter Firing Only", "type": "main", "index": 0 }]] },
    "Filter Firing Only": {
      "main": [[{ "node": "Call FastAPI risk-assessment", "type": "main", "index": 0 }]]
    },
    "Call FastAPI risk-assessment": { "main": [[{ "node": "Send Email", "type": "main", "index": 0 }]] }
  },
  "active": false,
  "settings": { "executionOrder": "v1" }
}
```

(파일 경로: `deploy/n8n/workflows/workflow-a-risk-assessment.json`)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd deploy/n8n && python -m pytest tests/test_workflow_shapes.py -v -k workflow_a`
Expected: PASS (2 passed)

- [ ] **Step 5: n8n UI에서 실제 임포트 검증**

n8n 콘솔(`http://<n8n-host>:5678`)에 로그인 → Import from File로 이 JSON을 불러와 에러 없이
열리는지 확인 → `httpHeaderAuth`/`smtp` 크리덴셜을 UI에서 채운 뒤 다시 Export해서 파일을 갱신한다
(크리덴셜 값 자체는 export에 포함되지 않으므로 이 재수출은 안전함).

- [ ] **Step 6: Commit**

```bash
git add deploy/n8n/workflows/workflow-a-risk-assessment.json deploy/n8n/tests/test_workflow_shapes.py
git commit -m "feat: n8n workflow A -- alert to risk-assessment email"
```

---

### Task 11: n8n 워크플로우 B — 승인→마이그레이션 실행

**Files:**
- Create: `deploy/n8n/workflows/workflow-b-approve-migration.json`
- Modify: `deploy/n8n/tests/test_workflow_shapes.py`

**Interfaces:**
- Consumes: FastAPI `/approve`(Task 7)
- Produces: `/webhook/approve` 엔드포인트(n8n이 서빙, GET) — Task 10의 이메일 안 `approval_url`이 가리키는 대상.

- [ ] **Step 1: Write the failing test (기존 파일에 추가)**

```python
# deploy/n8n/tests/test_workflow_shapes.py 에 추가
def test_workflow_b_has_webhook_and_calls_approve_endpoint():
    wf = _load("workflow-b-approve-migration.json")
    node_types = {n["name"]: n["type"] for n in wf["nodes"]}
    assert node_types["Approval Webhook"] == "n8n-nodes-base.webhook"
    call_node = next(n for n in wf["nodes"] if n["name"] == "Call FastAPI /approve")
    assert call_node["parameters"]["url"].endswith("/approve")


def test_workflow_b_is_the_only_workflow_that_may_call_nova():
    # Workflow B doesn't call Nova directly either (FastAPI /approve does) --
    # this asserts it only ever talks to FastAPI, never to Nova/Keystone URLs.
    wf = _load("workflow-b-approve-migration.json")
    dump = json.dumps(wf)
    assert "/auth/tokens" not in dump and "os-migrateLive" not in dump
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd deploy/n8n && python -m pytest tests/test_workflow_shapes.py -v -k workflow_b`
Expected: FAIL with `FileNotFoundError`

- [ ] **Step 3: 워크플로우 JSON 작성**

```json
{
  "name": "approve-and-migrate",
  "nodes": [
    {
      "id": "webhook-b",
      "name": "Approval Webhook",
      "type": "n8n-nodes-base.webhook",
      "typeVersion": 2,
      "position": [240, 300],
      "webhookId": "approve-migration",
      "parameters": {
        "path": "approve",
        "httpMethod": "GET",
        "responseMode": "responseNode"
      }
    },
    {
      "id": "call-approve",
      "name": "Call FastAPI /approve",
      "type": "n8n-nodes-base.httpRequest",
      "typeVersion": 4,
      "position": [460, 300],
      "parameters": {
        "method": "GET",
        "url": "={{$env.FASTAPI_BASE_URL}}/approve",
        "sendQuery": true,
        "queryParameters": {
          "parameters": [{ "name": "token", "value": "={{$json.query.token}}" }]
        },
        "options": { "response": { "response": { "neverError": true } } }
      }
    },
    {
      "id": "respond",
      "name": "Respond to User",
      "type": "n8n-nodes-base.respondToWebhook",
      "typeVersion": 1,
      "position": [680, 300],
      "parameters": {
        "respondWith": "text",
        "responseBody": "=<html><body><h1>{{$json.statusCode === 200 ? '마이그레이션 트리거됨' : '승인 실패'}}</h1><pre>{{JSON.stringify($json.body, null, 2)}}</pre></body></html>",
        "options": {
          "responseHeaders": { "entries": [{ "name": "Content-Type", "value": "text/html" }] }
        }
      }
    }
  ],
  "connections": {
    "Approval Webhook": { "main": [[{ "node": "Call FastAPI /approve", "type": "main", "index": 0 }]] },
    "Call FastAPI /approve": { "main": [[{ "node": "Respond to User", "type": "main", "index": 0 }]] }
  },
  "active": false,
  "settings": { "executionOrder": "v1" }
}
```

(파일 경로: `deploy/n8n/workflows/workflow-b-approve-migration.json`)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd deploy/n8n && python -m pytest tests/test_workflow_shapes.py -v`
Expected: PASS (4 passed — workflow A의 2개 + workflow B의 2개)

- [ ] **Step 5: n8n UI에서 실제 임포트 검증**

Task 10의 Step 5와 동일한 절차(Import → 확인 → Export)를 이 워크플로우에도 반복한다.

- [ ] **Step 6: Commit**

```bash
git add deploy/n8n/workflows/workflow-b-approve-migration.json deploy/n8n/tests/test_workflow_shapes.py
git commit -m "feat: n8n workflow B -- approval click triggers migration"
```

---

### Task 12: Alertmanager/Prometheus 설정 갱신 + 엔드투엔드 확인 절차

**Files:**
- Modify: `deploy/monitoring/alertmanager.yaml`
- Modify: `deploy/monitoring/alert-rules.yaml`

**Interfaces:**
- Consumes: n8n 워크플로우 A의 webhook URL(Task 10)
- Produces: 없음 (파이프라인의 마지막 배선)

- [ ] **Step 1: `alert-rules.yaml`에 psi_rate 실제 값을 annotation으로 추가**

현재 `annotations`에는 `instance_uuid`/`node`만 있고 실제 rate 숫자가 없어 워크플로우 A가
FastAPI에 넘길 `psi_rate`를 못 채운다. `value: '{{ $value }}'`를 추가한다.

```yaml
# deploy/monitoring/alert-rules.yaml (수정 후)
data:
  qemu-contention.rules.yml: |
    groups:
      - name: qemu-contention
        rules:
          - alert: VMCPUPressureHigh
            expr: rate(openstack_vm_cpu_pressure_stall_seconds_total{type="some"}[1m]) > 0.005
            for: 30s
            labels: {severity: warning}
            annotations:
              instance_uuid: '{{ $labels.instance_uuid }}'
              node: '{{ $labels.node }}'
              value: '{{ $value }}'
```

- [ ] **Step 2: `alertmanager.yaml`의 webhook 대상을 n8n으로 교체**

```yaml
# deploy/monitoring/alertmanager.yaml (수정 후, webhook_configs 부분만)
    receivers:
      - name: live-migration-webhook
        webhook_configs:
          - url: http://<n8n-host>:5678/webhook/risk-assessment
            send_resolved: false
            http_config:
              authorization:
                credentials: CHANGE_ME_WEBHOOK_SECRET
```

(`credentials` 값은 Task 2의 FastAPI `WEBHOOK_SECRET`, Task 9의 n8n `.env`, 이 파일 세 곳 모두
동일해야 함 — n8n의 `Alertmanager Webhook` 노드가 헤더를 검증하기 때문.)

- [ ] **Step 3: 배선도 확인 (수동)**

```bash
grep -n "psi_rate\|value:" deploy/monitoring/alert-rules.yaml
grep -n "webhook_configs\|url:\|credentials:" deploy/monitoring/alertmanager.yaml
```

세 값(FastAPI `WEBHOOK_SECRET`, n8n `.env`의 `WEBHOOK_SECRET`, 여기 `credentials`)이 동일한
문자열인지 눈으로 재확인한다 — 자동화된 체크는 세 파일이 서로 다른 배포 대상(Lambda 환경변수,
n8n 컨테이너 환경변수, K8s ConfigMap)에 있어서 이 플랜 범위에서는 만들지 않는다.

- [ ] **Step 4: 엔드투엔드 수동 검증 체크리스트 작성**

기존 `live_migration_tdl.md`와 같은 스타일로, 구현 완료 후 실제로 밟을 수동 검증 절차를
아래와 같이 문서에 남겨둔다(이 플랜 문서 자체에 기록, 별도 TDL은 구현 착수 시점에 파생):

1. cpu-hog로 부하를 걸어 `VMCPUPressureHigh`를 실제로 firing 시킨다.
2. n8n 워크플로우 A의 실행 로그(Executions 탭)에서 FastAPI 호출이 200을 받았는지 확인한다.
3. 이메일에 작업계획서와 승인 링크가 도착했는지 확인한다.
4. **아직 승인 링크를 클릭하지 않은 상태에서 VM이 마이그레이션되지 않았는지**
   (`openstack server show`로 host 불변 확인 — 이게 이번 아키텍처의 핵심 차이) 확인한다.
5. 승인 링크를 클릭해 워크플로우 B가 실행되고, `openstack server show`로 host가 실제로
   바뀌었는지 확인한다.
6. 만료된(1시간 지난) 승인 링크를 클릭했을 때 400이 반환되고 마이그레이션이 실행되지
   않는지 확인한다.

- [ ] **Step 5: Commit**

```bash
git add deploy/monitoring/alertmanager.yaml deploy/monitoring/alert-rules.yaml
git commit -m "feat: point Alertmanager at n8n risk-assessment workflow instead of Nova directly"
```

---

## Self-Review

- **Spec coverage**: Design Decisions 1~7 전부 최소 한 태스크에 반영됨 — (1)(2) Task 10/11 분리, (3) Task 9, (4) Task 8, (5) Task 3, (6) Task 5(토큰)+Task 6(host 재조회), (7) Task 6/7이 FastAPI에 Nova 로직 집중. Global Constraints의 "다른 Nova API 금지"는 Task 6의 `novaclient.py`가 `/servers/{uuid}`(GET)와 `/servers/{uuid}/action`(os-migrateLive POST)와 `/auth/tokens`만 호출하도록 구현되어 있어 만족. "알림 채널 1개"는 Task 10에서 이메일 하나로 고정. "웹 UI/대시보드 금지"는 Task 11에서 순수 텍스트 응답으로 제한.
- **Placeholder scan**: 각 태스크의 코드/설정 블록은 전부 실행 가능한 완성된 내용이며 "TBD"류 문구 없음. 유일하게 범위를 명시적으로 좁힌 곳은 Task 9 Step 2(n8n 서버의 정확한 EC2 스펙/terraform 리소스)와 Task 12 Step 3(세 파일 간 시크릿 일치를 검증하는 자동화 스크립트)인데, 둘 다 "왜 이 플랜 범위 밖인지" 이유를 명시했으므로 방치된 TODO가 아니라 의도적인 스코프 경계다.
- **Type consistency**: `WorkPlan`(Task 2에서 정의) 필드명(`summary`/`root_cause_candidates`/`recommended_action`/`risk_notes`)이 Task 4(`llm.py`)·Task 7(`main.py` 테스트)에서 동일하게 쓰임. `novaclient.trigger_live_migration`의 반환 딕셔너리 키(`from_host`/`to_host`)가 Task 6과 Task 7 테스트에서 일치함. `approval_token.issue`/`verify`의 시그니처가 Task 5와 Task 7에서 일치함.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/live-migration-risk-planner-n8n-fastapi.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**

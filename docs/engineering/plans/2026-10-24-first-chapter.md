# Novel Studio first chapter Implementation Plan

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

> 历史原型计划：完整范围和执行顺序已由 v1.4 的 DELIVERY_ROADMAP、REQUIREMENTS_TRACE 与 platform-foundation 计划覆盖。这里的一轮修订、单章与技术栈不得沿用为当前产品上限。

**Goal:** Deliver a working local writing studio from brief to reviewed, committed chapter with editable skills, export and backup restore.

**Architecture:** FastAPI serves a built React/TypeScript client. SQLite owns authoritative revisions and a serial persisted worker calls isolated role contexts; immutable blobs store prose. Paid calls are disabled until the human enables a bounded budget.

**Tech Stack:** Python, FastAPI, SQLite FTS5/jieba, httpx Responses API, React, TypeScript, Vite.

**Spec:** [产品设计](../../FINAL_PLAN.md)、[需求追踪](../../REQUIREMENTS_TRACE.md)。

## Global Constraints
- Single user, Windows, loopback only, no cloud deployment or paid calls during implementation.
- Preset Director, Architect, Planner, Writer, Critic, Bookkeeper, QA. Default model gpt-6.1-sol.
- One automatic repair maximum; no speculative retry after ambiguous model outcome.
- Facts, beliefs, statements, intentions and hypotheses remain distinct. Confirmation requires verified evidence and current revisions.
- Immutable text, atomic ledger commit, backups restore in isolation, all public APIs require local session authorization.

## Review Focus
- A stale edited candidate must not overwrite a newer project or canonical chapter.
- A model timeout or application restart must not repeat an unknown charged request.
- A dishonest character's dialogue must not become objective canon.
- Backup files with traversal, missing blobs, bad hashes or revoked content must not become visible.
- A foreign website or missing token must not trigger generation, read private prose or change budgets.

### Task 1: Persistence and chapter authority
**Files:** studio/models.py, studio/store.py, studio/checks.py, tests/test_store.py.
**Interfaces:** Store(root), create_project, project, save_lore(expected_revision), create_candidate, commit_candidate(expected_revision), context, export.
- [ ] Write tests for reopen persistence, stable IDs, fact/belief separation, evidence anchors, stale commits and searchable canon.
- [ ] Run pytest tests/test_store.py; expect RED before code.
- [ ] Implement immutable blobs, SQLite transactions/CAS, retrieval and deterministic checks.
- [ ] Run same tests; expect PASS; commit.

### Task 2: Real role pipeline and paid boundary
**Files:** studio/skills.py, presets/*/SKILL.md, studio/provider.py, studio/worker.py, tests/test_worker.py.
**Interfaces:** SkillRegistry snapshot/fork, Provider.generate(role,input,schema), Worker.enqueue/run/cancel/recover; Store task persistence.
- [ ] Test full chapter and independent ledger extraction with fixture transport, one repair limit, pinned skills, budget gate and unknown outcome accounting.
- [ ] Run pytest tests/test_worker.py; expect RED.
- [ ] Implement strict typed outputs, seven substantive skills, versioned project overrides, durable serial stages and conservative reservations.
- [ ] Run store and worker tests; expect PASS; commit.

### Task 3: Local API and restore
**Files:** studio/api.py, studio/backup.py, run.py, tests/test_api.py, tests/test_backup.py.
**Interfaces:** create_app(data_root, provider=None), authenticated JSON API and file endpoints. Backup archive uses consistent DB snapshot, manifest hashes and referenced blobs only.
- [ ] Test origin/token enforcement, malformed imports, isolated restore, tampered archives and deletion journal behavior.
- [ ] Run tests; expect RED.
- [ ] Implement endpoints, session bootstrap, manual draft review, import, export, budget controls, backup/restore and launcher.
- [ ] Run full pytest; expect PASS; commit.

### Task 4: Usable writing UI and delivery
**Files:** frontend/src/*, frontend/package.json, Start.ps1, README.md, tests/browser.*.
**Interfaces:** Three-column writing workspace consumes authenticated API, edits lore/prose/skills, shows plan/issues/ledger/versions/cost and restores uploaded backups.
- [ ] Build accessible Chinese UI with empty-project onboarding and real backend behavior.
- [ ] Type-check/build and browser-test creation, edit, export and restore with paid calls off.
- [ ] Fresh whole-branch review; fix important findings with regression tests.
- [ ] Document exact passing checks and paid-model quality validation still pending. Deliver local launch instructions, no paid calls.

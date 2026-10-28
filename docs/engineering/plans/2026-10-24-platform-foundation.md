# 全平台公共底座 Implementation Plan

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

**Goal:** 交付真实可运行、可从 Vue 操作、支撑三条完整产线的公共底座，不以首章原型限制平台范围。

**Architecture:** Go/Echo 承担模块化业务，MySQL 保存权威与发布状态，Temporal Go SDK 是唯一持久调度。内容不可变，RAG 使用结构约束、MySQL 全文和 BGE-M3 精确向量，能力、依赖与恢复统一管理。

**Tech Stack:** Vue 3/TypeScript、Echo v5、MySQL、Temporal Go SDK、本机/S3 BlobStore、受控本地嵌入和隔离工具执行器。

**Spec:** [完整分期方案](../../DELIVERY_ROADMAP.md)、[v1.1 基线](../../reference/V1_1_BASELINE.md)、[需求追踪表](../../REQUIREMENTS_TRACE.md)。本计划为第一期；第二期 N01—N12 是后续完整小说交付，不是可选增强。

新增模型/入口契约见 [五家模型与小说创建](../../MODELS_AND_NOVEL_ENTRY.md)。第一期必须完成五家实际适配、持久 Key 后台和用途绑定；第二期五步创建与角色管理见 N01/N02/N04/N07。

新增协作契约见 [内容候选、采用与阶段门](../../ASSISTED_CREATION_AND_PLANNING.md)，以下任务为原 F 工作包的必需增量，不另起缩水产线。

## v1.4.2 必需增量

- [ ] F03：实现 CreationDraft/CandidateRevision/AdoptionRecord，采用检查版本和依赖；对照界面、候选修改、刷新恢复与重复采用实际验证。
- [ ] F04：Temporal 持久等待用户采用，重启可恢复；信号不作为权限证明，旧任务不能越过新版门禁。
- [ ] F05/F08：TXT/MD/DOCX/PDF/PNG/JPEG/WebP 摄取、预览、页/单元覆盖、OCR/视觉缺能力、权限与超限错误；源文件不执行，私有来源不自动公开。
- [ ] F06：预置资料理解、字段完善、反馈落实、规划和图片需求/生成/编辑契约；图像能力与文本能力分开声明，专业真实验收属第二期。
- [ ] F07：稳定章节/场景 ID，采用新版后精确标记下游需复核，无关节点不变。
- [ ] 测试包含 TestDraftDoesNotAdopt、TestAdoptionRejectsStaleRevision、TestApprovalWaitSurvivesRestart、TestUploadCoverageFailure、TestPlanChangeInvalidatesDependents；Vue 验证修改意见、差异和明确采用按钮。
- [ ] 第二期 N01—N04/N06/N07 接通规划门与反馈循环，N02/N09 接通至少一个真实图片生成/编辑适配及人物/场景图/封面，N10/N12 验证质量，不以本期通用接口冒充小说已完成。

## v1.4.3 角色协作公共机制增量

契约见 [角色批量与修订](../../CHARACTER_ASSISTANCE.md)。

- [ ] F02/F03：批量角色项级 ID、关系闭合和采用事务；模板字段来源/锁定，删除语义与历史保留。
- [ ] F04/F06：分块任务与部分失败恢复，模板/标签/Skill 版本冻结；官方分类与趋势快照分离，异常来源不能发布伪热门。
- [ ] F07/F03：引用/语义影响分组与覆盖状态，ScenePatch 的基准hash/锚点/依赖组，RepairReleaseManifest 一致切换。
- [ ] 覆盖 TestBatchRetryDoesNotDuplicateCharacters、TestAdoptionRejectsDanglingRelationship、TestCharacterDeletionKeepsHistory、TestStaleScenePatchRejected、TestRepairReleaseIsAtomic；第二期验证真实人物生成、章节修复与阅读质量，不能以公共测试替代。

## Global Constraints

- 3 个独立 IP、1 个生产批次；外部媒体作业上限 2，未来试玩会话上限 5。现在不伪造媒体或游戏能力。
- 前端 Vue，业务后端 Go/Echo，业务数据库 MySQL。模型推理与专业工具可为受控独立进程，不能取得业务写权限。
- 真实收费默认关闭；本地测试仅替换模型传输，数据库/调度/向量/文件必须实际执行。付费阻断不阻止无依赖的开发。
- 所有领域写入检查作用域、当前权限、撤销、预期版本和执行代号。模型只产生候选。
- 临时生成、已就绪、已发布分开；跨正史与章节发布使用一个业务库事务，不能分成互相独立提交。
- Echo 框架对象不进入领域或 Worker。Temporal Workflow 不作非确定性 IO；收费请求禁默认重试。
- 旧仓库 `studio/`、`frontend/` 和小说数据保留到迁移验收。严禁在正式库运行集成测试或任意重置。
- 每任务包含可操作 UI 或通过真实服务被调用的功能；仅目录、表结构、空方法不算完成。

## Review Focus

- 旧任务在权限撤销或基础版本更新后返回，必须拒绝提交：F02/F03/F04。
- 数据库已提交但客户端丢回执，不能再次扣费、重复应用变更或重新请求模型：F03/F04。
- 同名人物、分支例外、不同可知时间和未就绪向量不能混入正文上下文：F02/F05。
- 社区包通过重定向、符号链接、脚本或恶意方法文本访问宿主与密钥必须失败：F06/F08。
- 恢复旧备份时当前撤销不丢失，未保存正文和并发新发布不能被覆盖：F03/F08，编辑器后续 N04。

## 文件与公共接口

以下路径是实施目标，不表示目录或功能已经存在。边界遵循既有 Go 目标目录；每个功能拥有明确文件，而不是继续向旧 Python Store 加逻辑。

| 路径 | 职责 |
|---|---|
| `backend/cmd/studio/main.go`、`cmd/worker/main.go` | HTTP 与 Temporal Worker 生命周期和依赖装配 |
| `backend/internal/domain/` | 作用域、版本、事实/分支、能力、清单及错误类型 |
| `backend/internal/service/` | 应用用例、授权、事务和发布关卡 |
| `backend/internal/storage/`、`backend/migrations/` | MySQL 仓储、单位事务、版本迁移、BlobStore |
| `backend/internal/httpapi/` | Echo 认证/请求校验/错误与端点 |
| `backend/internal/worker/`、`provider/` | Temporal Workflow/Activity、模型网关和对账 |
| `backend/internal/retrieval/`、`skills/`、`changes/`、`backup/` | 检索、能力、影响、恢复实现 |
| `tools/embedding/`、`tools/sandbox/` | 固定依赖的本地向量推理、受限评测/构建 |
| `deploy/local/`、`deploy/locks/`、`scripts/` | 本机服务、精确版本/镜像清单、统一启停/预检 |
| `web/src/features/{workspace,ip,knowledge,skills,tasks,changes,operations}/` | 第一期真实工作台页面，页面分步随所属任务交付 |
| `backend/tests/integration/`、`web/tests/`、`evals/platform/` | 真服务集成、浏览器与反例；不装作专业成品 |

公共类型在 F01 的 `domain/contracts.go` 定义：`Scope{WorkspaceID,ProjectID,ActorID string}`；`VersionGuard{ExpectedHead string,Generation int64,PolicyRevision string}`；`Operation{ID string,Scope Scope,Guard VersionGuard}`；`Ref{ID,Revision,Hash string}`。所有 ID、revision、hash 不可混用。业务错误至少区分 Forbidden、Conflict、Revoked、NotReady、OutcomeUnknown、BudgetDenied、ValidationFailed。

F02 定义 `ChangeSet`（类型化断言/事件/规则增改删及证据）、`EffectiveSnapshot`（版本、有效值、来源、例外与冲突）；F03 定义 `Manifest`（内容、IP/作品头、能力、依赖、索引水位与 hash）、`PreparedRelease`、`Release`；F04 定义 `TaskSpec`（输入引用、Recipe/SkillSet/模型/工具版本、预算、修订上限）、`TaskRef`、`CallRequest`/`CallResult`；F05 定义 `ContextRequest`（Scope、snapshot、purpose、故事/可知时间、视角、token 上限）和 `ContextPackage`；F06 定义 `SkillPackage`/`Qualification`；F07 定义 `ImpactPlan`；F08 定义 `BackupManifest`/`RestoreResult`。实现时为这些字段形成版本化 JSON Schema 和 Go struct，不使用自由 map 代替领域契约。

## F01 环境、身份与工作区

**Files:** 新建 `backend/go.mod`、`internal/domain/contracts.go`、`internal/service/workspace.go`、`internal/httpapi/auth.go`、`internal/storage/mysql.go`、`migrations/0001_workspace.sql`、`deploy/local/compose.yaml`、`scripts/check-environment.ps1`、`web/src/features/workspace/WorkspacePage.vue`。测试 `backend/tests/integration/workspace_test.go`、`web/tests/workspace.spec.ts`。

**Interfaces:** `CreateProject(ctx context.Context, scope Scope, name string, operationID string) (Ref,error)`；`Authorize(ctx context.Context, scope Scope, action string, resource Ref) error`；`RunMigrations(ctx context.Context, db *sql.DB) error`。后续任务只能通过鉴权后的 Scope 工作；不信任客户端传入的 ActorID。

- [ ] 写 `TestProjectIsolation`：A 项目凭据不能读取 B；`TestReadOnlyCannotWrite`；`TestCreateProjectIdempotent`：同操作只建一次；`TestNewerSchemaRefusesWrite`。先运行确认失败。
- [ ] 核查本机和 WSL2/容器条件、可用磁盘与内存，不复用未知数据库账号。锁定受支持 Go、Echo、MySQL、Temporal 和前端依赖；执行 Temporal/MySQL 8.4 建库、迁移、历史、重启与可见性兼容验证，记录实际版本。缺环境显示失败，不静默跳过。
- [ ] 实现身份、四角色、项目、版本迁移与统一启动；建立三个隔离 IP。Vue 可登录、建项目和切项目，默认所有者可兼任角色。
- [ ] 在 `backend` 执行 `go test ./tests/integration -run 'Test(ProjectIsolation|ReadOnlyCannotWrite|CreateProjectIdempotent|NewerSchemaRefusesWrite)' -count=1`；前端运行 workspace 浏览器场景。应全部通过且访问真实测试 MySQL/Temporal。
- [ ] 保存环境锁定与验证记录，提交 `feat: add workspace and durable runtime foundation`。

## F02 IP、时间、锁定与分支

**Files:** `domain/ip.go`、`domain/branch.go`、`service/ip.go`、`storage/ip.go`、`migrations/0002_ip_versions.sql`、`httpapi/ip.go`、`web/src/features/ip/IPPage.vue`；测试 `domain/branch_test.go`、`tests/integration/ip_test.go`、`web/tests/ip.spec.ts`。

**Interfaces:** `ResolveSnapshot(ctx context.Context, scope Scope, branchID, baseRevision string) (EffectiveSnapshot,error)`；`ProposeChange(ctx context.Context, op Operation, change ChangeSet) (Ref,error)`；`MergeBranch(ctx context.Context, op Operation, oldBase,newBase,local Ref) (ChangeSet,error)`。消耗 F01 授权和版本类型，供 F03/F05/F07 使用。

- [ ] 写并运行失败测试：`TestLieIsNotFact`、`TestSameNameKeepsIdentity`、`TestFlashbackKnowledge`、`TestLockedCanonChange`、`TestBranchExceptionSurvivesMerge`、`TestCompositeDoesNotMergeBeliefs`。断言谎言仅为陈述、原 ID 不变、未来秘密不可用、冲突标记明确。
- [ ] 实现全部实体类型、三种时间、命题类别、结构化规则及 UNKNOWN/冲突状态。实现显式删除/禁用、单父分支、三方合并、锁定和提升提案。
- [ ] Vue 实际创建人物/能力/关系、查看时间与来源、比较分支、处理冲突；以两个不同原创小样验证，不写死人名。
- [ ] 执行 `go test ./internal/domain ./tests/integration -run 'Test(Lie|SameName|Flashback|Locked|Branch|Composite)' -count=1` 和 IP 浏览器场景；核对未经发布的候选不成为权威。
- [ ] 提交 `feat: add canonical IP and branch resolution`，记录需求与反例证据。

## F03 产物与联合发布

**Files:** `domain/release.go`、`storage/blob.go`、`storage/blob_local.go`、`storage/blob_s3.go`、`service/release.go`、`migrations/0003_releases.sql`、`httpapi/releases.go`；测试 `tests/integration/release_test.go`、`storage/blob_contract_test.go`、`web/tests/release.spec.ts`。

**Interfaces:** `BlobStore.Put(ctx context.Context, projectID string, input io.Reader) (Ref,error)`；`PrepareRelease(ctx context.Context, op Operation, manifest Manifest, change ChangeSet) (PreparedRelease,error)`；`MarkReady(ctx context.Context, op Operation, preparedID string, evidence []Ref) error`；`PublishRelease(ctx context.Context, op Operation, preparedID string) (Release,error)`。发布使用同一 MySQL 事务，消费 F02 规则。

- [ ] 写并运行失败测试：`TestCandidateInvisibleUntilPublished`、`TestConcurrentPublishOneWinner`、`TestCommitReplyLostIsIdempotent`、`TestBlobStoredButSQLFailed`、`TestPublishRevokedScope`。断言旧发布头保留、重复 operation 只返回同一发布、孤立对象不变正文。
- [ ] 实现本机和 S3 BlobStore 同一契约、不可变引用、来源锚点；实现 PREPARED/READY/PUBLISHED，按固定快照验证证据与索引水位。
- [ ] 实现正文/台账/获准正史/清单/outbox 同事务，读取当前权限和 generation；页面提供候选比较、状态和旧版读取，不通过改 UI 状态假发布。
- [ ] 执行 `go test ./internal/storage ./tests/integration -run 'Test(Blob|Candidate|ConcurrentPublish|CommitReply|PublishRevoked)' -count=1`，真实 S3 兼容测试端点验证 put/get/hash/删除权限；云提供商未验证另列。
- [ ] 提交 `feat: add immutable artifacts and atomic publication`。

## F04 Temporal、模型网关与费用

**Files:** `domain/task.go`、`worker/workflows.go`、`worker/activities.go`、`service/tasks.go`、`provider/gateway.go`、`provider/{openai,anthropic,deepseek,qwen,gemini}.go`、`provider/reconcile.go`、`credentials/vault.go`、`service/model_settings.go`、`storage/budget.go`、`migrations/0004_tasks_budget.sql`、`web/src/features/tasks/TaskPage.vue`、`web/src/features/settings/models/ModelSettings.vue`；测试 `worker/workflows_test.go`、`tests/integration/task_recovery_test.go`。

**Interfaces:** `StartTask(ctx context.Context, op Operation, spec TaskSpec) (TaskRef,error)`；`CancelTask(ctx context.Context, scope Scope, taskID string) error`；`ModelGateway.Call(ctx context.Context, request CallRequest) (CallResult,error)`；`ReconcileCall(ctx context.Context, scope Scope, operationID string) (CallResult,error)`。TaskSpec 引用 F03 manifest；所有外部动作都经过网关和预算。

- [ ] 写失败测试：`TestPaidDisabledMakesZeroRequests`、`TestConcurrentReservationsCannotOverspend`、`TestLostReplyDoesNotResend`、`TestRestartResumesCompletedStage`、`TestLateResultAfterCancelRejected`、`TestWorkflowHistoryRollsOver`、五家逐一的 `TestProviderContract`、`TestSaveKeyDoesNotGenerate`、`TestCredentialsSurviveRestartWithoutLeak`、`TestModelSwitchKeepsTaskSnapshot`。夹具计数断言发送次数，未知占用不释放；验证各家拒绝、截断、流式用量及不同参数，不能只测 OpenAI。
- [ ] 以真 Temporal 实现父/子流程、Activity、版本冻结、执行代号和对账；对收费 Activity 默认最多一次尝试，重入查询 operation 状态；安全本地操作有限重试。Workflow 无直接 IO。
- [ ] 网关实现五家真实适配、能力目录与严格输出校验，处理拒绝/截断、价格/配置版本、整数微元预算。实现后台持久凭据的保存/替换/停用与脱敏状态；`SaveConnection(ctx context.Context, op Operation, input ConnectionInput) (ConnectionMetadata,error)` 只返回引用，`BindModel(ctx context.Context, op Operation, binding ModelBinding) error` 按用途绑定且不改活动任务。ConnectionInput 含 provider/region/workspace/endpoint_profile/secret，secret 只进入凭据库；ConnectionMetadata 不含 secret；ModelBinding 含 work_id/use/connection_id/model_profile_revision。Vue 提供进度、暂停/取消/恢复、估算、已花/在途/未知和明确恢复动作。
- [ ] 执行 `go test ./internal/worker ./tests/integration -run 'Test(Paid|ConcurrentReservations|LostReply|Restart|LateResult|WorkflowHistory|ProviderContract|SaveKey|CredentialsSurvive|ModelSwitch)' -count=1`；实际重启 Worker/Temporal，保留已有结果且不重复模型请求。
- [ ] 提交 `feat: add durable orchestration and budget gateway`。

## F05 知识摄取、完整 RAG 与上下文

**Files:** `domain/context.go`、`retrieval/ingest.go`、`retrieval/hybrid.go`、`retrieval/assemble.go`、`retrieval/embedding.go`、`migrations/0005_knowledge.sql`、`tools/embedding/worker.py`、`tools/embedding/requirements.lock`、`web/src/features/knowledge/KnowledgePage.vue`；测试 `retrieval/hybrid_test.go`、`tests/integration/context_test.go`、`evals/platform/retrieval.jsonl`。

**Interfaces:** `Ingest(ctx context.Context, op Operation, source Ref) (Ref,error)`；`BuildContext(ctx context.Context, request ContextRequest) (ContextPackage,error)`；`Embed(ctx context.Context, texts []string, modelRevision string) ([][]float32,error)`。消费 F02 快照、F03 对象、F01 当前权限；F04 调度索引。

- [ ] 写失败测试：`TestSemanticOnlyEvidenceRetrieved`、`TestRequiredConstraintNeverDropped`、`TestUnknownSecretExcluded`、`TestStaleIndexCannotMix`、`TestRevokedCachedContextDenied`、`TestUnicodeAnchorRoundTrip`、`TestImportCoverageIncludesFailedUnits`。
- [ ] 实现四空间、完整摄取覆盖、分片和证据锚点；锁定 BGE-M3 revision，真实生成 1024 维向量，拒绝维度/NaN/hash 不符。测 CPU/内存与批量耗时，禁止假向量通过。
- [ ] MySQL 存结构元数据、ngram 索引与向量 BLOB；在全部授权有效成员上作分页精确向量检索，RRF 融合，必要事实独立装配。缓存含权限/用途/时间/快照和检索版本，每次访问重新检查撤销。
- [ ] Vue 展示覆盖/失败单元、知识来源、水位、上下文证据与遗漏；执行 `go test ./internal/retrieval ./tests/integration -run 'Test(Semantic|RequiredConstraint|UnknownSecret|StaleIndex|RevokedCached|UnicodeAnchor|ImportCoverage)' -count=1` 和真实小样检索评测。
- [ ] 提交 `feat: add scoped hybrid retrieval and context packages`。

## F06 能力中心、网上研究与人工编辑

**Files:** `domain/skill.go`、`skills/registry.go`、`skills/fetch.go`、`skills/qualify.go`、`skills/merge.go`、`skills/revoke.go`、`migrations/0006_capabilities.sql`、`presets/shared/`、`evals/skills/`、`web/src/features/skills/SkillPage.vue`；测试 `skills/registry_test.go`、`tests/integration/skill_lifecycle_test.go`、`web/tests/skills.spec.ts`。

**Interfaces:** `ImportCandidate(ctx context.Context, op Operation, pkg SkillPackage) (Ref,error)`；`Evaluate(ctx context.Context, op Operation, candidate Ref, baseline Ref, suite Ref) (Qualification,error)`；`PublishQualified(ctx context.Context, op Operation, qualification Qualification) (Ref,error)`；`RevokeCapability(ctx context.Context, op Operation, root Ref) error`。使用 F04 调度/预算和 F08 隔离执行契约；F08 未就绪时只允许静态分析，不执行第三方代码。

- [ ] 写失败测试：`TestRedirectToPrivateIPRejected`、`TestArchiveSymlinkEscapeRejected`、`TestPromptCannotGrantToolPermission`、`TestAuthorCannotAlterHeldOutAnswers`、`TestUpstreamMergePreservesLocalChanges`、`TestRevokedAncestorBlocksDerivedPackage`。
- [ ] 实现原始包/平台适配/项目派生的血缘、来源许可、文件/hash、依赖和任务资格；Agent/Skill/Tool/Recipe 分离，预设清单缺工具或证据不可发布。
- [ ] 实现 Scout/Analyst/Author/Evaluator 方法和研究流程，真实抓取登记来源；每日发现/每周评测、两轮缺口上限和无付费路径；评测集与门禁独立管理。真实模型收费评测待授权，不能伪装通过。
- [ ] Vue 提供表单/Markdown/工具/例子/附件编辑、差异、三方合并、评测结果、发布/回退/撤销。运行 `go test ./internal/skills ./tests/integration -run 'Test(Redirect|ArchiveSymlink|PromptCannot|AuthorCannot|UpstreamMerge|RevokedAncestor)' -count=1`，运行 skills 浏览器场景；补齐与 F08 的真实隔离联调。
- [ ] 提交 `feat: add versioned capability research and editing`，专业角色的成品资格在 N10/M08/G09 验收。

## F07 依赖、变更与局部重建

**Files:** `domain/change.go`、`changes/graph.go`、`changes/plan.go`、`changes/rebuild.go`、`storage/outbox.go`、`migrations/0007_dependencies.sql`、`web/src/features/changes/ChangePage.vue`；测试 `changes/graph_test.go`、`tests/integration/change_test.go`。

**Interfaces:** `PlanChange(ctx context.Context, scope Scope, base Ref, change ChangeSet) (ImpactPlan,error)`；`ApplyPlan(ctx context.Context, op Operation, planID string) (TaskRef,error)`。使用 F02 差异、F03 发布、F04 调度、F05 证据；计划含必须重建/复核/可用/裁决和费用。

- [ ] 写失败测试：`TestUnrelatedArtifactHashUnchanged`、`TestFrozenNodeStillPropagatesReview`、`TestOutboxRedeliveryAppliesOnce`、`TestOldPlanCannotOverwriteNewHead`、`TestCompensatingRollbackPreservesLaterWork`。
- [ ] 实现六类实际依赖边、闭包、语义补查候选与范围证据；影响计划持久化，应用前重新校验版本/权限/预算。冻结不删边，失败保留旧发布。
- [ ] Vue 展示具体前后差异、受影响作品、预计费用和局部动作；接入公共文件/设定/RAG 真实重建，不能用改状态假装已重做专业产物。
- [ ] 执行 `go test ./internal/changes ./tests/integration -run 'Test(UnrelatedArtifact|FrozenNode|OutboxRedelivery|OldPlan|CompensatingRollback)' -count=1` 与变更页面场景，比较闭包外 hash。
- [ ] 提交 `feat: add dependency driven changes and safe rebuilds`。

## F08 运维、隔离、备份与首期总验收

**Files:** `backup/export.go`、`backup/restore.go`、`backup/retention.go`、`service/revocation.go`、`httpapi/diagnostics.go`、`tools/sandbox/runner`、`deploy/local/backup-config`、`web/src/features/operations/OperationsPage.vue`、`scripts/check-platform.ps1`；测试 `tests/integration/recovery_test.go`、`web/tests/platform-foundation.spec.ts`、`evals/platform/isolation/`。

**Interfaces:** `CreateBackup(ctx context.Context, scope Scope) (BackupManifest,error)`；`RestoreIsolated(ctx context.Context, scope Scope, backup Ref) (RestoreResult,error)`；`RunIsolated(ctx context.Context, request ToolRunRequest) (Ref,error)`。ToolRunRequest 固定输入副本/镜像hash/工具能力/资源时限，输出由发布服务接收，不能直接改发布头。

- [ ] 写失败测试：`TestRestoreReplaysCurrentRevocations`、`TestDeletedProjectNotSearchable`、`TestReferencedBlobNotCollected`、`TestSandboxCannotReadCredentials`、`TestSandboxNoNetworkByDefault`、`TestTimedRetentionWindows`、`TestBackupConcurrentPublicationConsistent`。
- [ ] 实现独立受保护撤销记录、每日基线/≤15分钟日志保护、对象清单和自动轮换；隔离恢复重放当前删除/权限/能力撤销、重建索引后接入。保留策略使用可控时钟验证，真实恢复计时验证 RTO≤8小时；独立介质未配置时明确保障范围，不虚称抗整盘损坏。
- [ ] 实现一次性无凭据工具环境、资源限制和受控依赖下载；接回 F06 的真实脚本评测。实现脱敏诊断、任务/预算/索引/备份故障页面，统一启动不依赖开发工具会话。
- [ ] 从干净隔离数据运行 `scripts/check-platform.ps1`：Go test/vet/gofmt、Vue vue-tsc/lint/build、真实 MySQL/Temporal/向量/对象/隔离测试和浏览器整体场景。任一必需依赖不可用为未通过，不静默跳过。三个 IP、分支、版本、检索、Skill、变更、重启/恢复全部操作成功才退出第一期。
- [ ] 导出 `foundation-evidence.json`（源码提交、依赖锁、场景/命令/退出码、产物hash、失败边界），提交 `feat: complete platform operations and foundation acceptance`。接着以 N01—N12 编写并执行第二期细化任务，不重新询问是否保留完整小说范围。

## 顺序与自审

执行顺序 F01 → F02 → F03 → F04 → F05 → F06 → F07 → F08。F06 的脚本评测依赖 F08 隔离能力，故先实现静态生命周期，F08 接通后必须返回补验；不能提前把 F06 标为全完成。可将 F08 隔离子能力提前到 F06 之前实施，但不改变验收要求。

跨任务提交只带引用与 operation ID，禁止串传框架 Context 或直接暴露连接；所有消费方沿用本计划定义的类型。第一期以机制与真实公共操作验收，小说/漫剧/游戏专业成品分别属于后续期。当前全部复选框未勾选，这份计划本身不是实现证明。

# F02 BranchDelta 与独立语义审查 Implementation Plan

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

**Goal:** 逐项解释 IP 变更，执行独立且可恢复的语义审查，并给 F03 派生稿提供针对当前内容的真实重新审查及发布出口。

**Architecture:** Snapshot 继续是事实存储，BranchDelta 是程序生成、带逐项理由和来源的不可变差异。SemanticReviews 固定目标、输入、独立 reviewer 与 Generation 子任务，通过既有 Temporal、预算、资格机制运行；模型报告经程序校验后仍须人工选择和采用。旧报告、旧来源和旧发布不被改写。

**Tech Stack:** Go、MySQL InnoDB、现有 Temporal runtime、Vue 3 / TypeScript、现有不可变对象库。

**Spec:** `docs/F02_SEMANTIC_REVIEW_CONTRACT.md`；上位约束为 FINAL_PLAN、V1_1_BASELINE 与 AGENTS。

## 2026-10-25 当前执行证据（尚待最终独立复审）

- BranchDelta、严格报告、资格保留集、预置schema/example、Temporal流程纯测试已运行通过；service/httpapi/worker及相关tests vet通过。
- F02 browser runner15149：人工理由保存/刷新/无模型采用，派生内容42步测试资格→独立审查→人选报告→采用/发布，2/2 PASS（1.9m）。后续新增回流/合并草稿和影响入口尚待新构建浏览器复验。
- 实际随机MySQL 30319 RED复现：来源头推进仍可准备、暂停dispatch误BLOCKED、当前UNKNOWN可绕过人工采用、跨作者选择未完整重试。66024同5组用例GREEN51.263s：含草稿CAS、merge/promotion来源失效、暂停保留预留、取消晚到、UNKNOWN不重发、漏覆盖、负报告/新内容/新PASS出口、资格撤销、双作者交错和撤权、merge/promotion/IP+资料联合发布及幂等。
- 既有Go integration中无模型合并/提升改为检查待审阻断；原成功合并基线推进、选择性提升、IP+资料原子提交已由`TestSemanticReviewMergePromotionAndJointPublicationHaveActualExit`实际测试覆盖。HTTP integration整体尚未在该历史节点新改动后运行，不能以compile代替。
- 新`TestSemanticReviewActualWorkerRestartAndOutboxReplayDoNotResend`已编译，实际Temporal重启/重投验证等待共享设施窗口；未宣称已通过。
- 所有模型执行均为tests目录内传输替身，无实际供应商请求。有限语义保留集和fixtures通过不证明真实模型专业审查、全书质量或F07远端覆盖。

## Global Constraints

- 纯人工保存、采用、明确手动发布不新增付费门槛；真实显示未审，不声称 AI 质量合格。
- 已追踪 AI 派生、跨分支合并/提升、设定与资料联合发布须完成所需独立审查；硬冲突与 UNKNOWN 不能用满意覆盖。
- 模型只产出候选，不更新正式状态；无预算仍可保存和准备待审请求，实际调用沿既有显式 Start。
- schema31 仅新增三表；集成层先接备份/恢复和跨 schema 日志兼容，再提升运行版本。不得修改28–30。
- 恢复的报告全部 restored_disabled=true，历史可读、永久不可重新授权；只能新建审查。
- 不实现第二 provider 通道或调度器；不把测试替身或机械评测误称真实语义质量验收。
- F07 远端漏边、发布传播、补丁执行和补偿仍须完整后续实施；本任务明确显示尚未覆盖，不自动清除其阻断。

## Review Focus

- 保存失败/迟到响应不能清空逐项理由或覆盖刚编辑的草稿；任务1、4浏览器覆盖。
- 同内容不同目标、不同基础头或不完整覆盖的报告不能复用；任务2、3反例覆盖。
- 无 Key、价格失效、预算关闭时仍能保存和准备，恢复入口必须真实；任务2、4覆盖。
- 跨作者审查、撤权、暂停/取消与晚到输出不得死锁或授予资格；任务3并发覆盖。
- 旧人工候选/旧备份保持可读，恢复的 PASS 不成为新发布权威；任务1、5覆盖。

## Task 1: 逐项差异、草稿和人工路径

**Files:** 新增 `backend/internal/domain/branch_delta.go`、`backend/internal/service/ip_delta.go`；修改 `service/ip.go`、`ip_draft.go`、`ip_merge.go`、`ip_promotion.go`；新增 `backend/tests/semanticreview/delta_test.go`、`backend/tests/integration/ip_delta_test.go`；新增 `web/src/features/ip/BranchDeltaPanel.vue`，修改 `types.ts`、`IPPage.vue`、`PromotionPanel.vue`。

**Interfaces:** `BuildBranchDelta(base, next Snapshot, reasons map[string]string) (BranchDelta,error)` 以稳定对象ID+字段路径为key；`ApplyBranchDelta(base Snapshot, delta BranchDelta) (EffectiveSnapshot,error)` 验证前值摘要并输出来源、覆盖和冲突。服务附加受信来源及固定分支基线，不信任客户端来源种类。

- [ ] 写 RED：增加/修改/禁用/删除逐项理由、空理由拒绝、排序稳定、错误前值拒绝、无差异不假造变更；int64 时间不经 float64。
- [ ] 运行 `go test ./tests/semanticreview -run BranchDelta` 确认缺实现失败。
- [ ] 实现类型、严格纯函数和候选事务内 delta 写入；旧候选只读推导显示“旧版本未记录理由”，不得伪造历史理由。
- [ ] 草稿兼容 schema1，新增理由字段并保持CAS；合并/提升每项来源保留具体父版本，不能把总理由代替每项理由。
- [ ] 运行纯测试 GREEN；schema合同就绪后随机库测试普通手写无模型可保存/采用/发布、merge与promotion门禁及幂等。

## Task 2: 固定审查请求和真实独立执行

**Files:** `backend/migrations/0031_semantic_reviews.sql`；新增 `domain/semantic_review.go`、`service/semantic_reviews.go`、`semantic_review_prepare.go`、`semantic_review_targets.go`、`worker/semantic_review.go`、`httpapi/semantic_reviews.go`；协调后最小修改 `service/generation.go`、`generation_control.go`、`worker/runtime.go`、`httpapi/server.go`；新增 `qualification/semantic_review.go` 和共享审查预置。

**Interfaces:** `SemanticReviews{Generations *Generations}`；`Prepare(ctx,actor,project,SemanticReviewInput) (SemanticReview,error)`、`Start(ctx,actor,project,id) (SemanticReview,error)`、`Read(ctx,actor,project,id) (SemanticReview,error)`、`Advance(ctx,id) (string,error)`。输入选择目标ID、预期hash、binding、价格和token档位，不接收pass/report。冻结类型见契约。

- [ ] 写 RED：准备不调用模型/不占预算；无配置返回保存的BLOCKED与恢复原因；两个不同操作不可共用一个child；原作者Generation不得作reviewer。
- [ ] 实现冻结全部必要目标正文、IP快照、逐项delta、硬检查与范围说明的输入blob；使用独立critic请求，不传写作会话历史。
- [ ] 新共享审查 Skill/Agent 使用严格输入输出schema；新增有限保留集的语义契约suite与任务kind，保留真实未评测状态，UI可安装并进入既有qualification流程。
- [ ] 复用Generation Prepare/Start与父Temporal workflow，实际发送前、结果复用前重查目标/撤权/资格；UNKNOWN不重发。
- [ ] 用tests内传输与Temporal环境验证GREEN、重启、outbox重投、固定一次dispatch；不给生产目录加测试输出。

## Task 3: 报告、人工选择和发布门禁

**Files:** 新增 `service/semantic_review_reports.go`、`semantic_review_acceptance.go`、`semantic_review_control.go`；修改 `service/ip.go`、`release.go`、`release_evidence.go`；新增 `tests/qualification/semantic_review_test.go`、`semantic_review_concurrency_test.go`。

**Interfaces:** `SemanticReviewReport.Validate(input SemanticReviewDocument) error`；`Select(ctx,actor,project,id,expectedRevision int64) (SemanticReviewAcceptance,error)`；`requireSemanticReviewTx(ctx,tx,project,targetKind,targetID,targetHash string) error`；`Control(ctx,actor,project,id,action string) (SemanticReview,error)`。

- [ ] 写 RED：错误目标/hash、漏覆盖、假引用、PASS含阻断问题、未知类目、source generation复用全部拒绝；未知语义保留UNKNOWN。
- [ ] 保存报告blob并程序校验，receipt采用target行锁+revision CAS，不能修改已发布目标选择；模型输出不直接调用Select或Adopt。
- [ ] 将mandatory gate接所有采用/发布入口，人工普通路径保持未审可用，F03 derived的旧来源保留并另附当前新审查选择。
- [ ] 检查run/child/当前qualification/actor权限/目标hash，restored_disabled永久阻断；所有跨作者锁沿F03安全重试策略，禁止project后阻塞锁actor。
- [ ] 真实随机MySQL验证取消晚到、UNKNOWN/结果恢复、资格撤销、第三作者锁竞争、采用后变更、重复选择、重复发布只有一条outbox。

## Task 4: 用户可操作的审查出口

**Files:** 新增 `web/src/features/ip/SemanticReviewPanel.vue`、`semanticReviews.ts`；修改 `IPPage.vue`、`BundlePanel.vue`、`TaskPanel.vue`；新增 `web/tests/semantic-reviews.spec.ts`。

- [ ] 写浏览器RED：人工不配置模型仍可采用；derived保留草稿并显示“旧资格对应旧内容，需要复核当前修改”；无预算能准备且给出模型/预算页真实入口。
- [ ] 实现目标固定、估算、显式运行、暂停/取消/恢复、报告查看与人工选择；按钮对应真实API，轮询不覆盖本地编辑或跨分支迟到响应。
- [ ] 展示每项变更原因/影响与覆盖范围；既有确定性ReviewWorkflow仍明确标注硬检查，不冒充独立语义审查。
- [ ] 浏览器GREEN：准备→启动测试审查→看报告→选择→采用/发布；刷新/分支切换、改稿后旧审查失效、失败恢复保留文字。
- [ ] 运行Vue类型、定向lint/格式与构建，检查实际文案和保存确认。

## Task 5: 恢复兼容及独立复审

**Files:** 集成层维护 `backup/*`、`storage/mysql.go`、`storage/migration_history.go` 的schema切换；本任务提供精确SQL/对象ref契约并新增独立专项tests，复用日志集成测试。

- [ ] 集成层接入30→31空表补齐和runs恢复禁用规则后，验证schema30 archive与history仍合法，31完整图可恢复。
- [ ] 验证原PASS报告历史可读、选择/采用/发布拒绝，重新新建审查可正常走流程；旧人工内容不被审查恢复规则误封锁。
- [ ] 跑相关Go纯测试/vet、随机MySQL、Temporal恢复和前端浏览器，记录明确RED→GREEN及未运行真实模型边界。
- [ ] 请求独立代码复审并修复实际finding；按F03与F02范围分别集成变更。不以此宣称整个F01–F08完成。

## 实施结果与当前交接

2026-10-25：任务1–4已落实到源码、随机MySQL/实际Temporal和浏览器路径；schema31备份与30升级由集成流程验证。上述原始TDD清单保留为计划，逐项实际证据以 `docs/F02_SEMANTIC_REVIEW_CONTRACT.md` 的“该历史节点验证边界”为准，避免把计划命令当运行记录。独立初审P1（负面等价可被新候选身份洗掉）与P2（FAILED再次Start进入无人执行队列）已真实RED→GREEN；回流checkbox事件顺序问题亦经浏览器RED→GREEN。

历史验证记录：12项不同浏览器用例、15项定向HTTP、完整边界/并发/联合发布专项、实际Temporal重启/重投及F03回归已通过；源文件格式化后冻结，待独立复核与完整schema31集成验证。未运行真实模型、未验证专业语义能力，未把此项当整个foundation或小说模块完成。

# F02 变更与独立语义审查契约

维护日期：2026-10-28。F02变更与独立审查已有实际实现；本文规定行为契约，不构成真实模型语义质量验收。功能与待验边界见[实施状态](IMPLEMENTATION_STATUS.md)。F02必须保留F03来源血缘，不得把旧资格授予新内容。

## 保存、采用和发布政策

[产品方案](FINAL_PLAN.md)规定无Key手写、作者采用及确认/质量/正史发布分层，[需求基线](reference/V1_1_BASELINE.md)规定自动一致发布审查门，两者同时保留。人工可以保存尚未完成的草稿；正式候选仍须满足结构/硬规则。普通纯人工IP设定和普通人工/导入资料允许明确人工采用/手动发布，并显示未进行独立语义审查，不新增付费门槛。

内置普通规划、首章试写和候选修改无需先执行600次资格评测；正式独立审稿与发布仍须有效的精确用途资格及当前目标审查。安装预设或成功试写不会自动取得这些资格。

已追踪AI派生稿、跨分支合并/提升、IP与资料联合发布须对当前确切内容独立审查。新内容不能继承旧内容报告；hard conflict/UNKNOWN 不能通过用户满意强行覆盖。无Key/资格/预算可保存候选及待审请求，必须返回具体阻断原因和配置/预算/重新准备入口；准备不调模型，启动沿既有费用授权。不能声称识别在全新人工资料中粘贴的所有AI内容。

纯人工内容主动执行审查后，针对同一分支、实际内容和基础头的真实完成FAIL/UNKNOWN也会阻止直接采用/发布；仅更换候选ID、生成执行ID、系统RecordedAt或无序对象/集合顺序不会消除已知问题；负面等价指纹保留真实正文、对象ID、有效时间、资源/基础头与范围，不做同义或空白泛化。正面的PASS receipt仍严格绑定原目标ID/hash/来源元组，不能跨候选借用。旧live记录缺少指纹时从经过hash与目标校验的输入blob推导，材料缺失时拒绝授权。须新内容独立判断，或后来完成的独立PASS经人选择。取消、未执行、BLOCKED和恢复禁用历史不构成新的负面结论；旧PASS不能覆盖后来完成的负面报告。

## BranchDelta

Snapshot 保持唯一事实源。候选对应不可变 BranchDelta schema1；每项包含稳定对象种类/ID、字段路径、add/replace/disable/remove、before_hash/after_hash、before/after原始JSON、reason、受信source_refs。摘要使用原始类型，不能以float64归一化时间/数字。改变理由不会偷偷修改旧候选，必须新建候选。草稿可缺理由，准备候选时变化项须完整理由。系统记录的时间戳不冒充用户语义变化。

base_revision固定分支规范基线；parent_revision固定本次变更前头。输入delta必须校验前值，应用后hard检查，不能靠模型推断不存在的实体/路径。变更来源按每项记录，合并记录选择后的理由与来源版本，冲突须明确选择。影响入口连接现有ChangePlan的实际确定性清单，区分已登记影响、疑似及尚未检查；不能由该清单推断已完成F07全项目远端漏边扫描。合并上游与回流来源头在准备、dispatch、采用及发布重新核对，不能对已过期来源发起新的审查。

现有ip_drafts.payload的schema2同时保存普通逐项理由与branch_actions草稿（合并版本/选择/理由、回流来源/选择/理由），共用原revision CAS；不新增表或对象引用。草稿中的来源只是恢复编辑上下文，不能直接授予采用权威。版本变化时保留理由供对照并要求重新检查实际内容。

## 固定审查与输出

目标只允许 ip_candidate 或 artifact_release。固定目标ID、target_hash、branch、expected_ip_head、候选delta_hash、完整必要IP快照以及每个资料的F03 ArtifactReviewTarget元组。reviewer 使用独立critic Generation、显式semantic审查taskkind和精确binding/config/token档位；不得复用来源Generation或写作者自评，也不自动继承旧模型上下文。固定材料包含作者真相与角色所知各自标签，不能把信念/陈述当事实。

`frozen` schema1顶层字段：`schema`、`input_hash`、`expected_ip_head`、`delta_hash`（无则空）、`source_generation_ids`、`reviewer_binding_id`、`price_id`、`max_output_tokens`、`scope`、`content_scope_hash`（旧live记录可缺）。content_scope_hash仅为负面等价元数据，不是blob引用。input_hash是实际审查输入blob；blob内是typed SemanticReviewDocument，包含target tuple、snapshot、delta、artifact texts、hard findings、可验证source anchors、required coverage keys。Generation.Frozen独立保存最终provider输入/配置/估算，不重复写密钥或另存第二价格权威。

输出schema1含target_hash、verdict(PASS/FAIL/UNKNOWN)、逐target覆盖、issues、summary。issue含category、severity、target key、依据anchor和解释。类别至少world_rule、identity、fact_belief_claim、temporal、knowledge_visibility、causal、outline_chapter_consistency、unsupported、coverage。程序拒绝不存在的anchor、漏覆盖、重项、错误target、PASS含blocker或UNKNOWN。作者接受报告是显式receipt，不是模型PASS自动发布。世界/角色/大纲/章节复用这些类别，审查只覆盖绑定目标的实际内容，不授予未执行的小说质量状态。

## 持久化和状态

精确DDL：`backend/migrations/0031_semantic_reviews.sql`。仅新增ip_branch_deltas、semantic_review_runs、semantic_review_acceptances三表，无旧行转换。run.operation_id对actor唯一，request_hash不匹配必须冲突；generation_id nullable且唯一，不允许两个审查共享一个执行。目标行锁序列化receipt revision CAS。不得更改已发布目标的选择。

PREPARED→QUEUED→RUNNING→COMPLETED/FAILED/UNKNOWN；FAILED/CANCELLED为终态，Start拒绝再次排队，必须新准备审查；缺条件保存BLOCKED并显示原因，恢复时固定目标不变、明确重新准备所需参数。暂停不重发已派请求；取消保留历史且晚到不得授权。UNKNOWN保留既有预算占用，不能用新的操作ID自动重发。报告读取与当前可采用状态分开，撤权/过期/目标变化不删除历史；所有采用/发布重新检查当前权限、资格、目标、receipt和restored_disabled。

暂停在实际dispatch事务中再次检查；尚未发出时返回PAUSED并保留原RESERVED，不由失败恢复误释放费用预留。跨作者选择沿现有来源授权的NOWAIT与最多8次完整事务重试，任何一次退出先rollback，不在持有项目锁时等待另一作者锁。估算金额和币种均读取绑定Generation的固定价格，展示不取当前下拉项价格。

## 备份与恢复对象合同

三表顺序为delta、runs、acceptances，FK详见DDL。ip_branch_deltas.payload是内联规范JSON，delta_hash是其摘要，**不是对象库引用**。runs.frozen.input_hash是对象库引用；runs.report_hash非空是对象库引用。acceptances.report_hash须等于对应run.report_hash，重复引用同blob。input blob内target内容hash指向已有IP/manifest/artifact对象，应核对真实对象存在，不能从客户端增加任意blob引用。Generation已有input/output图维持原规则。

30→31补三空表，不改28–30文件、checksum、资格archive/history。归档sanitize将全部run.restored_disabled置true、cancel_requested置true、paused置false；PREPARED/QUEUED/RUNNING/UNKNOWN/BLOCKED改CANCELLED，已完成的报告/verdict保留历史。acceptance保留历史但对应run禁用使其永远不能作为采用/发布权威。任何API不得重新enabled；恢复后只能新建审查。schema切换必须同步备份、来源引用、日志重放与版本登记，不能只修改DDL或全局版本号。

## 实现边界

领域对象位于`backend/internal/domain/branch_delta.go`与`semantic_review.go`；应用服务使用`backend/internal/service/ip_delta.go`和`semantic_review_*.go`，调度入口为`worker/semantic_review.go`，HTTP入口为`httpapi/semantic_reviews.go`。Vue通过独立组件调用同一服务端门禁。Generation、预算、Worker、资格suite、备份对象图及SchemaVersion必须保持一致，不能在语义审查路径复制另一套费用或资格权威。

## 已记录的工程验证与质量边界

已修复并保留回归的行为包括：同内容重建候选不能清除负面报告；FAILED/CANCELLED不得重新Start；负面等价与正面目标授权分别检查。真实隔离MySQL及Temporal验证覆盖等价内容反例、旧记录输入回推、精确目标PASS、暂停后重建Worker、outbox重投只生成一个child且一次dispatch，以及终态拒绝重启。测试fixture的同步和等待修正不放宽生产调度或版本检查。

工程记录还覆盖逐项理由与branch_actions草稿CAS、合并/回流来源头变化阻断、暂停/取消/UNKNOWN/错误覆盖/撤权、跨作者选择并发及后续撤权、合并/提升/IP与资料联合发布及outbox重试。浏览器记录包含人工理由刷新、无模型人工采用、派生资料审查→选择→采用→发布、合并/回流来源流程，共12个不同用例；回流勾选框使用change后的新值保存，避免重绘取消选中。相应领域/服务/Worker、Go静态检查、Vue类型/lint和构建有工程通过记录。

上述模型响应仅来自tests内固定传输，没有真实模型请求或专业语义质量证明。保留期投影、在线过期门禁、F07全项目语义漏边、补偿回滚及小说质量均有各自验证要求，不能用指纹与固定协议测试替代。当前支持环境为Windows本机loopback，工程记录不证明Linux、Docker或公网部署就绪。

关键回归要求同时保留：旧输入对象不可读取时拒绝授权；后来同scope的UNKNOWN使旧selected PASS失效；重新选择后来完成的exact PASS才可恢复采用。幂等重放必须发送同一原始请求，不能重算过期base后当作重放，也不能允许过期的新操作。F03 qualified release与P1三分支的工程回归不为新内容补授专业资格。

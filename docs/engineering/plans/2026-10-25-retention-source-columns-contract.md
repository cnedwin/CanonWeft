# F08 源列完整性与 owner 读取契约

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

状态：2026-10-25。先在schema31 gate期间只读核查；完成源码冻结验证后，已实现独立逐列分类/覆盖契约与EvidenceID纯helper，并再次冻结Go源码供集成验证。当前固定业务 catalog 为 **63 表、483 列**（读取 `backend/internal/backup/catalog.go` 得出）。本文件不表示SQL scanner、全部转换、API或实际清理已实现；该历史节点没有数据库操作或迁移激活。六张pending32目录表不计入这63张业务表。

## 分类原则与未实现门禁

每个真实列必须在独立固定描述符中恰好出现一次，不用 `_hash` 后缀、SQL 类型或“剩余列都是安全元数据”的默认规则来推断。下表显式列出所有列；“最小壳”只表示它可以进入严格类型校验后的结构记录，并不表示无限期保留或可直接信任原字符串。ID/hash/数字/时间/布尔值必须按字段类型验证，枚举或服务端生成 key 必须符合本版本固定词汇。未知表、列、枚举、来源、scope 或解析失败均使 destructive plan BLOCKED。

已新增独立 `internal/retention/column_catalog.go`、`column_catalog_data.go` 与覆盖测试：`ColumnPolicy{Column, Privacy, Handling, Class, Shape}`、`OwnerContract{Table,Scope,Columns,PrimaryKey}`。`OwnerSpec(table)`返回独立副本，`Column(table,column)`拒绝未知字段，`ValidateColumnCoverage(table,columns)`要求实际列恰好覆盖且无重复。Privacy区分STRUCTURAL、PRIVATE_BODY、PRIVATE_TEXT、PRIVATE_KEY、CREDENTIAL与WORKSPACE_PRIVATE；Handling区分VALIDATED_SHELL、INLINE_ENVELOPE、CLEAR_SCALAR、TYPED_FINANCIAL、TYPED_DERIVED、ANONYMIZE_BLOCKED、CREDENTIAL_LIFECYCLE、EXTERNAL_SCOPE。Shape是scanner后续必须落实的字段类型约束，当前helper不验证SQL值或授权。**分类完成与可执行handler完成分开**：Classified不得自动成为Complete。实际plan Complete要求列集合完整、所有owner scope已证明、每个due私密列均有已验证的typed materializer、所有真实blob引用闭包已登记。

下表策略缩写：

- `C`：作者保留的内容，active 时不因普通30天运行记录策略删除；被放弃候选可按候选30天，真正发布/采用的必要正文由明确内容根保留。项目删除到期后仍须清除。
- `R30`：原始运行/请求/输出/报告/度量/预览及其派生副本，30天，不可晋升为内容根。
- `D7`：故障/调试解释等7天，不能因为存入TEXT而成为永久审计记录。
- `A365`：经过固定结构验证的审计/费用最小元数据，一年；UNKNOWN/未结算保留与对账，不通过清理释放资金。
- `W`：workspace/account 共享数据。单项目清理不能改掉别的项目仍使用的用户名、模型配置、密钥引用或预算；需要单独 workspace 生命周期。明确私密，不冒充“无内容”。
- `K!`：用户自由文本参与PK/唯一键/回放身份，暂阻止清理，等待固定匿名化及显式映射证明。不是完成态。

## 逐表列分类（63表/483列）

`Scope` 使用下一节的固定连接规则。最小壳列中的内容哈希仍必须通过引用目录区分 BLOB 与指纹；不存在“保留hash就保留其对象”的隐含规则。

| Table | Scope | 最小壳列（显式） | 私密/特殊列（显式） | 待实现处理 |
|---|---|---|---|---|
| workspaces | W | id,created_at | name | W；项目删除不能抹掉workspace名称 |
| users | W | id,workspace_id,role,enabled,created_at | username,password_hash | W；username个人数据、password_hash认证材料；不纳入项目GC |
| sessions | W | user_id,expires_at | token_hash | 认证会话，按会话撤销策略；恢复仍禁止复活 |
| projects | self | id,workspace_id,revision,created_at,deleted_at | name | C；非唯一短文本可CLEAR；deleted_at不是删除权威来源 |
| project_members | direct | project_id,user_id,role | — | 权限关系壳；当前受保护删除账本仍拒访问 |
| operations | operation-result | actor_id,kind,request_hash,result_id | operation_id | K!；任意用户文本PK；kind→result固定归属，不能按actor推断project |
| audit_events | audit-resource | id,workspace_id,actor_id,action,resource_id,created_at | — | A365；action固定枚举，resource_id按动作解析归属 |
| branches | direct | id,project_id,parent_id,base_revision,head_revision,revision | name | C；UNIQUE(project_id,name)，K!；不能多行清为空 |
| ip_revisions | branch | id,branch_id,base_head,content_hash,status,created_by,created_at | payload | C；整快照含正文；保留原content_hash，不重签旧快照 |
| adoptions | candidate | candidate_id,actor_id,content_hash,adopted_at | — | 采用事件结构；正文由candidate闭包处理 |
| outbox | direct | id,project_id,kind,resource_id,created_at,processed_at | payload | TYPED_DERIVED/R30；release.published复制完整manifest，不能当纯ID JSON；清理/恢复后不能重派 |
| ip_drafts | branch | branch_id,actor_id,revision,base_head,updated_at | payload | C；用户草稿，不误用运行记录30天；删除项目到期清除 |
| ip_merge_candidates | candidate | candidate_id,parent_head,old_base | — | 固定版本关系 |
| resources | direct | id,project_id,branch_id,kind,head_revision,content_hash,revision,created_at | name | C；当前DDL无name唯一约束，可CLEAR；kind仅note/source |
| artifact_releases | direct | id,project_id,branch_id,content_hash,status,created_by,adopted_by,adopted_hash,created_at | manifest | C；含frozen_change_reason/policy等文本及逐artifact hash；数组resource_id必须唯一，具体field定位 |
| resource_drafts | resource | resource_id,actor_id,revision,base_head,updated_at | body | C；用户草稿原文 |
| ip_amendments | candidate | candidate_id,approved_by,approved_hash | reason | C/删除到期CLEAR；不可因批准事件将reason正文无限保存 |
| model_connections | W | id,workspace_id,revision | — | 共享连接标识 |
| model_connection_versions | W | connection_id,revision,created_at | config,credential_ref | W；config有name/endpoint/model/provider_workspace；credential_ref不是可重启授权 |
| model_bindings | W | workspace_id,role,connection_id,connection_revision,revision | — | role必须固定ModelRoles；跨项目共享，不能随一个project改写 |
| model_budgets | W | workspace_id,currency,limit_micros,per_call_micros,spent_micros,reserved_micros,enabled,revision | — | 费用余额保持；expiry不能释放UNKNOWN或重扣 |
| model_calls | direct | id,workspace_id,project_id,actor_id,input_hash,reserved_micros,charged_micros,state,cancel_requested,output_hash,settlement_hash,created_at,updated_at | operation_id,provider_receipt | operation_id K!（UNIQUE actor/op）；provider_receipt R30 CLEAR+原值hash；input_hash是请求指纹，不是blob |
| ip_promotions | candidate | candidate_id | payload | C；规则提升候选正文 |
| platform_tasks | direct | id,project_id,branch_id,candidate_id,input_hash,base_head,actor_id,state,paused,cancelled,revision,created_at,updated_at | reason | D7；input_hash实际引用candidate blob，原始任务读路径需到期门禁 |
| knowledge_indexes | direct | id,project_id,branch_id,ip_head,model_revision,chunks,created_at | manifest | C/派生索引；manifest.Sources含名称与policy，必要published来源之外不能晋升 |
| knowledge_chunks | knowledge-index | id,index_id,resource_id,resource_revision,source_hash,start_rune,end_rune,vector_hash | resource_name,content,vector | C/派生索引；名称CLEAR，content/vector整体过期；JSON向量替换后搜索必须跳过EXPIRED壳 |
| context_packages | direct | id,project_id,branch_id,actor_id,index_id,content_hash,created_at | payload | R30；query/命中正文/工作草稿/预览等多重副本 |
| source_imports | direct | id,project_id,branch_id,actor_id,source_hash,source_size,request_hash,created_at | filename,report,operation_id | C；report.Text和Units.Text是真实提取正文；filename CLEAR；operation_id K! |
| source_import_candidates | import | import_id,resource_id,release_id | — | import/resource/release三方必须同project |
| model_prices | W | id,workspace_id,connection_id,connection_revision,content_hash,created_at | payload | W；内含source URL及定价；费用摘要保留币种/数字但不能删共享价格记录 |
| generation_tasks | direct | id,workspace_id,project_id,branch_id,actor_id,request_hash,input_hash,state,call_id,paused,cancel_requested,created_at | operation_id,frozen,credential_ref | frozen R30；op K!；credential引用到期停用后CLEAR，绝不再执行；整frozen与nested input一次快照 |
| generation_previews | generation | generation_id,sequence,content_hash | content | R30；无created_at，承继generation原始期限，不按preview刷新续期 |
| resource_policy_drafts | resource | resource_id,actor_id,revision | payload | C；作者草稿policy可含自由文本 |
| skill_packages | direct | id,project_id,parent_id,upstream_id,bundle_hash,state,created_by,created_at | report,source | C/候选30；report含name/description/license/paths/findings；source含repository/path/URL且嵌套archive_hash需独立proof |
| skill_revocations | direct | package_id,project_id,actor_id,created_at | reason | reason正文按项目清理；撤销身份保留；独立authority副本是另一个必修gap |
| model_call_accounting | call | call_id,currency,revision | — | A365/UNKNOWN保持；不包含输出正文 |
| model_call_receipts | call | id,call_id,created_at | payload | R30+TYPED_FINANCIAL；实际四项数组[state,amount,receipt,outputHash]；第三项是私密文本，原tuplehash/id不可重签 |
| model_reconciliations | call | id,call_id,actor_id,revision,content_hash,created_at | payload | R30正文+A365数值链；bill_reference/evidence/reason/operation_id须清；保留previous/released/amount/revision/currency及原证据hash |
| change_plans | direct | id,project_id,branch_id,candidate_id,content_hash,created_by,created_at | payload | C；变更说明/待改文本及依赖 |
| change_plan_releases | plan | plan_id,release_id | — | plan/release同project；表PK只有plan_id，不按两个列拼PK |
| index_jobs | direct | id,project_id,branch_id,actor_id,index_id,release_id,candidate_id,state,total_sources,done_sources,chunks,created_at,kind | reason | D7；执行状态/计数壳 |
| working_indexes | direct | id,project_id,branch_id,actor_id,model_revision,chunks,enabled,created_at | manifest | **R30不可晋升**；PrivatePin.Name与Omission.Reason；该历史节点已修原Candidate/promotable规则 |
| working_chunks | working-index | id,index_id,source_key,ref_kind,ref_id,ref_version,part_number,source_hash,start_rune,end_rune,vector_hash | source_name,content,vector | R30不可晋升；承继原source最早期限，重建索引不能刷新预览TTL；source_hash是派生文本hash不是新blob |
| capability_versions | direct | id,project_id,kind,parent_id,content_hash,created_by,created_at | capability_key,definition | C；definition含instructions/schema/description；key自由命名须与heads统一匿名映射，K! |
| capability_heads | direct | project_id,version_id | capability_key | K!；自由文本PK；不得仅根据内容像hex就当结构ID |
| capability_revocations | direct | version_id,project_id,actor_id,created_at | reason | 同skill_revocations；永久撤销身份与reason正文分离 |
| capability_preset_steps | direct | project_id,step_key,kind,request_hash,result_id,created_by,created_at | — | step_key只有验证过的编译模板+ID可保留；未知值BLOCKED，通用PK编码仍按TEXT摘要 |
| skill_discovery_sources | direct | id,project_id,actor_id,enabled,candidate_limit,revision,created_at | repository,source_ref,source_path,keyword | C配置，项目清理CLEAR；私有工作兴趣与路径即使指公共repo也按私密处理；先停用调度 |
| skill_discovery_runs | direct | id,project_id,source_id,actor_id,source_revision,state,done_count,total_count,created_at | source_snapshot,reason,inventory | snapshot R30；reason/inventory D7；snapshot包含repository/path/keyword完整副本；保留limit等配额数值防重放绕限 |
| skill_discovery_candidates | direct | id,project_id,source_id,run_id,source_commit,package_id,previous_id,created_at | source_path | C候选；UNIQUE(source_id,source_path,source_commit)，K!；commit严格SHA40 |
| tool_trials | direct | id,project_id,actor_id,capability_id,state,lease_id,output_hash,elapsed_ms,created_at | frozen,reason | frozen R30、reason D7；lease_id固定随机ID，不是可复活执行权 |
| qualification_runs | direct | id,project_id,branch_id,actor_id,state,paused,cancelled,report_hash,qualification_id,created_at | frozen,reason | frozen R30、reason D7；suite/price/weekly摘要与显式legacy转换仍保留 |
| qualification_steps | direct+run | id,run_id,project_id,ordinal,arm,case_key,repeat_index,child_id,child_kind,state,created_at | metrics | R30；metrics.Output/Score.Detail是真实运行内容；case_key必须来自验证套件白名单 |
| capability_qualifications | direct+run | id,project_id,run_id,report_hash,enabled,expires_at,created_at | payload | R30；整Frozen可能复制suite/config与评测范围；EXPIRED不保留可用资格 |
| capability_bindings | direct | id,project_id,task_kind,qualification_id,previous_id,enabled,created_by,created_at | — | task_kind来自编译套件用途，未知值BLOCKED；expiry不能保留enabled授权 |
| capability_binding_heads | direct | project_id,task_kind,binding_id,revision | — | 同task_kind范围；PK仍采用固定TEXT编码 |
| qualification_approvals | direct+run | run_id,project_id,actor_id,created_at | payload | R30；含维护者note，不能视为纯hash审批 |
| qualification_schedules | direct | id,project_id,actor_id,enabled,revision,last_week,created_at | config,reason | C配置/删除清理，reason D7；config保留typed budget_limit_micros/币种链，关闭schedule避免失效配置执行 |
| qualification_week_grants | direct | id,project_id,week_key,schedule_id,schedule_revision,currency,cap_micros,reserved_micros,spent_micros,enabled,created_at | — | A365/UNKNOWN保持；不得伪造weekly grant或因expiry重置余额 |
| qualification_calls | direct+run+call | call_id,project_id,run_id,step_id,grant_id,reserved_micros,held_micros,charged_micros,state,created_at,origin,legacy_source | — | legacy_source必须固定Proof/EXPIRED_LEGACY schema，只有边界/旧hash/新投影hash；未知JSON拒绝 |
| ip_branch_deltas | direct+candidate | candidate_id,project_id,branch_id,base_revision,parent_revision,delta_hash,created_by,created_at | payload | C；差量事实/规则/文本与源引用，不是纯metadata |
| semantic_review_runs | direct | id,project_id,branch_id,actor_id,request_hash,target_kind,target_id,target_hash,generation_id,state,verdict,paused,cancel_requested,restored_disabled,report_hash,created_at,updated_at | operation_id,frozen,reason | op K!；frozen R30（含scope hashes）；reason D7；EXPIRED不能采用为新审查资格 |
| semantic_review_acceptances | direct | id,project_id,target_kind,target_id,target_hash,revision,review_id,report_hash,actor_id,created_at | — | hash-only接受事件；report_hash仍是受期限约束blob，不因接受而永久保留报告正文 |

## Owner scope 读取与一次原始快照

1. `direct` 必须实际row.project_id等于目标项目；所有关联branch/run/call/candidate也需相同项目。`self`取projects.id。workspace_id只能作为额外一致性校验，不能替代project归属。
2. 固定间接路径：branch→branches.project_id；candidate→ip_revisions.branch_id→branches；resource→resources.project_id；knowledge-index→knowledge_indexes.project_id；working-index→working_indexes.project_id；generation→generation_tasks.project_id；import→source_imports.project_id；call→model_calls.project_id；plan→change_plans.project_id。缺失/冲突父项不允许跨项目猜测，BLOCKED。
3. operations使用固定kind到result表映射；audit_events使用固定action到resource表映射。两者没有project_id，actor可能参与多个项目。不能把该actor全部日志认作当前project，也不能只看result_id碰巧存在于某一表。没有created_at的operation必须承继其实际owner的原始时间/可信首次观测，不用扫描时钟每次续期。
4. W表只检查共享资料的结构/安全边界，不将它们的private列归入本项目删除。跨项目秘密清除必须另有workspace级生命周期授权；restore禁用会话/凭据仍维持既有规则。
5. 使用固定ColumnPolicy构造已编译查询，不能将归档或目录提供的table/field直接拼SQL。列值只通过参数传入。owner row、关联归属和对应registry条目在相同事务锁下读出；全行原始digest、每个字段原digest、PK和revision先全部验证，再生成一次owner UPDATE与所有证据。
6. 整JSON与嵌套blob值从同一个原始快照提取。receipt第四项、skill source.archive_hash、manifest逐resourceID、generation/semantic/tool input_hash分别有固定typed extractor，不使用任意JSONPath。重复artifact.resource_id拒绝，不能用ordinal掩盖。
7. 私密JSON整体转换默认丢弃未知未来字段，避免只擦prompt而遗留messages/context。保留元数据必须来自具体typed结构；未知JSON字段不能自动被归入摘要。严格备份仍拒重复key/未声明字段与伪造proof。未知业务结构如新的财务schema应BLOCKED而非丢字段。
8. 派生副本期限受原始材料约束。working index/preview/context重复构建不能重新获得30天；最晚deadline取该副本自身政策与其原始引用deadline较早者。若无法证明来源期限，禁止将副本当新生数据/永久内容根，应BLOCKED并补provenance。

## 三种落地形态与当前限制

- INLINE_ENVELOPE：足够容量的正文列写精确ExpiredProjection，保留壳与原hash；所有阅读/搜索/执行入口须理解EXPIRED，不再把JSON envelope当向量/正文/可执行frozen。
- BLOB_REFERENCE：原hash值保留，旁证精确匹配hash scalar或已验证父投影摘要；此引用不再计入live closure，另一合法发布正文仍可保留同一对象。
- CLEAR_SCALAR：固定非key且无唯一性依赖的列写精确空串；旁证绑定原字节hash与实际当前空串，不能用于frozen绕过结构验证。现白名单仅三个列，后续须逐列扩展并验证长度/约束/业务读取行为。
- 本片不DELETE owner行，也不支持ROW_REMOVED。branches.name、operation_id、capability_key、discovery candidate.source_path等唯一/主键冲突先BLOCKED。最终必须实现确定匿名值+唯一约束安全的两阶段改名及引用映射，不能以BLOCKED冒充全部F08完成。
- credential_ref/token_hash/password_hash等即使形似hash也不能当普通内容引用。项目generation到期后credential_ref清除须与停止/取消其执行一起落实；workspace密码/会话另走认证生命周期，不能从本项目GC清除别人的共享认证。

## 财务与受保护账本的实际缺口

1. `service/budget.go` Settle 将 `[outcome,amount,receipt,outputHash]`写入model_call_receipts.payload，第三项是自由文本。settlement_hash是原元组摘要，receipt.id又包含它。当前目录只有payload.output_hash，必须补整个payload的R30 typed转换。最小保留state/amount/receipt_digest/output_hash/原tuplehash，原id/settlement_hash不改，未知/冲突回执仍可对账；不能保留receipt原文或摘要替代后重签成旧tuple。
2. `model_reconciliations.payload`含bill_reference/evidence/reason/operation_id。原content_hash验证的是整个Reconciliation。转换需保留call/id/revision、previous_amount_micros、released_micros、amount_micros、currency、expected_revision、原证据digest、confirmed固定事实；当前EvidenceSummary尚无完整reconciliation子类型，不能盲用通用空摘要。
3. `qualification_schedules.config`含输入配置和budget_limit_micros。到期清正文后仍需typed budget摘要供archive校验“最新周限额==配置限额”；资格/执行enabled必须关闭。不能清空JSON然后略过财务关系校验。
4. 独立authority.events.reason永久复制skill/capability撤销原文（service AppendTx与provision回填）。它不在63表，但会使“已清全部项目正文”不成立。永久保留kind/project/resource/actor/sequence的防复活事实，与reason正文的保留期限必须分开。未来事件可写固定reason_code+reason_hash；历史正文需专门受保护维护转换与证明，不能由普通app篡改权威身份或借本slice偷偷删除tombstone。当前记为AUTHORITY_LEGACY_PRIVATE/BLOCKED。
5. `release.published` outbox复制Manifest，不是纯ID事件；working_indexes.manifest包含private name/reason。两者都不能成为绕过raw/删除政策的永久副本。

## 恢复与时间锚点

旧30/31归档升级为空32目录时，Manifest.SnapshotAt是可信首次观测anchor，project_lifecycle.first_verified_at应使用该原始快照时刻；恢复时间、文件mtime、现在扫描时间不能延长期限。32已有登记必须原样保留。空registry不表示全新30天。旧source_created_at时区不可信时仍保存原值，但eligibility承继这个可信anchor；每个ref初登时取来源证明中最早已存在的anchor。恢复为新projectID也保留原始运行材料期限，不因此重新获得30天。扫描仍必须重新核验当前独立账本，删除/撤销/停用不因恢复而消失。

## retention_evidence.id 稳定派生建议

已实现 `EvidenceID(referenceID string,revision int64)(string,error)`，使用 `SHA256(json.Marshal([]string{"retention-evidence-v1", referenceID, strconv.FormatInt(referenceRevision,10)}))`，referenceID为lowercase64hex，revision>=1。Project已编码在referenceID，不再生成另一份owner编码。不要把expired_at或projection_hash混入ID：同一ref/revision重试只能命中同一证据ID；字段/日期/投影hash不同必须Conflict，不能生成第二张证明绕过unique(reference_id,reference_revision)。RED `c1575f`，GREEN `1fb9dc` 0.414s。

## 已验证与后续顺序

1. 已完成固定逐列ColumnPolicy/OwnerSpec及483列严格覆盖测试：对真实backup catalog AST逐表逐列比较，遗漏/额外/重复列及未知表拒绝；敏感字段不能归为metadata；workspace与operation-result范围显式分开；返回副本不允许调用方污染目录。RED `b6bad2`，GREEN `76a3fb` 0.407s。这里验证分类描述符，不宣称完成各闭集字段的实际值校验或复杂owner resolver。
2. 已修当前Rules中的working_indexes.manifest策略冲突：从Candidate/promotable改为RawRun不可晋升，真实行为RED `2080d6`。新增测试保证任何已有RawRun列不能在引用目录被晋升。作者capability_key仍属Content，不能误受30天运行TTL；反例RED `f9b0b0`后修正。最终全retention GREEN `869f20` 0.413s、vet exit0；前一联合检查 `de78ba` service owner纯测0.639s及retention/service/storage vet exit0。gofmt已用显式文件列表完成。
3. 下一步独立owner原始快照解析：非唯一短文本/CLEAR与派生JSON；receipt/reconciliation/schedule必须typed financial映射及摘要schema审查；parent+child冲突返回零partial结果。需要Artifacts[resource_id,hash,original_hash]（最多32、resource_id唯一）、ArchiveHash、ReceiptHash等封闭摘要扩展，尚未在该历史节点改共享projection。
4. 待补多项目复杂owner连接、闭集值校验、唯一键匿名化、同源期限传播、重复恢复不续期、未知账款保持等反例。Root已独立用OwnerSpec.Scope接archive owner resolver，workspace/operations/audit仍明确拒绝；本文件不把他处结果当成本slice的执行验证。随后分别接backup32 proof、公共读/执行门禁、writer lease与随机DB事务验证。没有自行激活schema或使用真实DB。

这份逐列契约完成的是当前源字段的核查与明确归类，不表示已有63表运行扫描器或全部清理handler。三项财务转换、权威reason遗留正文、唯一键匿名化、派生期限传播、读取/执行阻断和事务执行仍是上线前必修。

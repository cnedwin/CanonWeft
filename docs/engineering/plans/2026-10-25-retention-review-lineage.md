# 资格、审查和变更的来源链

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

使用已有公共框架和实际冻结输入串联评测/审查结果，不以当前配置或自述评分代替原始证据。当前先完成资格case与冻结请求；语义审查、变更计划和工具输出按同一路径继续。

- [x] 共享资格契约和固定case请求构造，保留服务端调度权限校验及原JSON。
- [x] 实际三组模型评测/绑定产生的资格来源闭包与篡改反例。
- [x] 冻结模型评测、case请求及资格绑定来源登记；结果与报告连接真实回执。
- [x] 工具输出、工具评测control/metrics和资格使用来源链及实际隔离验证。
- [x] 语义审查输入、独立生成、实际报告与采用记录来源链。
- [x] 固定生成请求中的变更计划全文和受影响资源原文来源。
- [x] 变更计划自身差异/依赖来源及Recipe步骤资格引用。
- 后续执行入口：retention-live-inventory.md Task2，写入时捕获/租约、原子过期和恢复；本计划不再追加通用来源框架。

## 资格节点已完成

- qualification共享Input/Frozen/RequestQuote/Metrics/Step/Report/WeeklyGrant，服务沿用别名。调度指针改为共享类型的Schedule字段，明确json:"-"；JSON无法注入或导出该指针，原调度数据库权限校验不变，周评测默认关闭回归通过。
- 固定case请求由qualification.EvaluationRequest统一构造，只使用公开题面/输入，不含保留答案。登记核对原候选/基线能力清单、冻结suite、逐case实际请求全文/输出档位/quote hash、步骤身份及模型配置；来源使用历史固定版本，不恢复资格或自动采用结果。
- 资格记录payload/report与原run连接；报告与实际步骤metrics连接，metrics输出文字/使用量与不可变模型回执核对。当前metrics已变化且无法证明原投影时仍未绑定，不能猜历史；工具对照和工具metrics尚未完成，明确UnboundSources。
- 实际测试首先发现既有qualification.mechanical.passed审计把资格ID误当run ID，已修正为capability_qualifications归属。登记错误现在记录阶段/表类别供内部诊断，外部服务仍统一隐去细节。
- 新真实MySQL9.7.4用例先失败再通过：18个模拟评测调用产生实际数据后，来源登记不再调用模型；篡改题面、题号、输出文字和资格ID被拒绝，原样重放不改来源版本。与当前绑定/未知费用/周调度三个回归合计16.070s通过。qualification/retention/service/models单元与相关vet通过。
- 没有正式0036启用、业务迁移/重启、清理或真实付费。继续本计划最后一项，再写入捕获/租约、过期恢复和底座出口；完整小说与Reelbench顺序不变。

## 工具结果与资格节点已完成

- tooltrial.Output成为服务与登记共用契约，JSON保持不变。isolation.RequestHash只校验和计算实际guest序列化摘要，不启动VM；区别于Frozen.InputHash的存储请求JSON摘要。输出逐项核对原环境、输入、原始字节、显示投影、结果状态和真实输出契约。成功、格式失败、无效UTF8诊断均保留正确来源，登记不授予资格或执行工具。
- 工具评测原case输入、repeat、三臂和执行子任务相互核对。NoMethod旧契约只有完整能力根引用，因此按其固定依赖重建闭包，不读取当前头、不猜新的manifest。工具metrics核对原始回执、输出、零模型费用和实际耗时；已绑定用途连接原资格payload/report和run。模型评测来源既有行为不变。
- JSON输入使用有界严格解析，重复键、不同大整数、数组换序、null/缺字段不能混淆；SQL等值数字表示允许。单元测试覆盖这些差异。
- 三种实际隔离输出及固定输入回归53.104s；既有18步三臂工具评测与一次资格使用（含撤销）新增来源登记/metrics篡改反例，与模型资格来源回归共247.119s通过。执行前独立控制组来源与根hash/case/runtime篡改测试4.034s通过，确认任务仍QUEUED且无输出，登记未启动执行。
- retention/tooltrial/service/isolation单元通过，最后JSON改动后retention再测0.825s；上述包及两组集成测试包vet通过，diff检查通过。长VM用例验证的是本地工具执行，不代表真实模型写作品质。测试脚本新增TimeoutMinutes，默认4分钟，仅实际三臂VM节点使用8分钟。
- 未改业务数据、主服务、正式迁移门禁或支付授权。语义审查/变更计划、写入租约/过期/恢复仍待完成；小说尚未进入完整实现与验收，Reelbench仍严格后置。
## 语义审查与变更输入节点已完成

- 原服务的快照硬校验、覆盖和锚点组装提取为domain.CompleteSemanticReviewSnapshot；常量Scope共享。服务继续按原顺序构造正文锚点，再构造快照锚点，JSON字段与输出不变。
- semantic_review_runs固定输入重新核对原候选或发布manifest、IP快照、可选delta、完整正文及来源生成ID。空IP head沿用snapshotTx的空entities/assertions数组。ContentScopeHash存在时核对，旧版本省略时仍验证实际文档；不以当前head或审查状态替代原始材料。
- 审查子generation绑定原review、actor/branch、critic/绑定用途/price/max tokens；原方法输入必须逐字等于review固定文档。report_hash读取实际文件，与model_calls不可变回执的Text逐字一致，并按原文档解析覆盖、引用与结论。acceptance关联同一目标及原报告。当前权限/复用/负面审查/发布门仍由原服务控制，登记不授予权利。
- ChangePlan、ChangedField、DependencyDifference、ChangeReviewTarget/Context共享到domain，service沿用别名。固定prompt中的proposed-change-data解析后核对原计划完整payload hash；受影响目标顺序、原资源release版本、完整文字、策略/依赖及名称来源逐项匹配。已过期任务历史仍登记，生产用途仍受原过期门禁控制。
- 新候选来源测试（2.32s）和既有无模型保存/手工采用（1.96s）通过；新报告发布后来源与既有独立审查发布回归33.592s；补充实际报告文件存在/文字一致性后仅受影响专项20.904s。最初两项发布测试环境未载入本地embedding配置，preparePublicationIndex拒绝发布；runner新增-Embedding参数后通过，未弱化门禁。
- 原变更过期回归增加来源登记与正文/计划描述/依赖理由篡改反例，4.319s通过；登记无额外模型调用。domain/retention/service单元0.685/0.794/1.720s，相关包及集成包vet与diff检查通过。全部模型为测试模拟，无真实供应商费用。
- 边界：上述变更工作证明消费者复制了原计划和正文，尚不能替代change_plans.payload自身before/after、Differences、Items/Via依赖来源。登记UnboundSources=0仅指已登记关系，不代表全平台来源覆盖完成。正式迁移仍不激活。

## 下一步定位

- change_plans.payload用domain.ChangePlan读取，原生成规则在service/dependencies.go、dependency_diff.go、dependency_resources.go与change_propagation.go。DependencyRef的IP对象hash来自typed valueHash；resource节点hash为struct{Hash;Policy;Dependencies}，以对应release的ArtifactRef验证；skill使用package ID=Revision和bundle_hash。已有retention/context_required_sources.go可复用校验/固定原资源及名称；不要读当前head代替历史Revision。
- Differences的IP部分由dependencyDifferences产生；资源部分是原/新text、policy、dependencies及资源Name。Items/Via来自固定依赖图；GraphHash不能拿当前图重算冒充原图。按实际可证明的版本登记，缺失历史必须显式保留未绑定。
- Recipe步骤冻结资格仍需接已有qualification来源助手。然后进入已设计写入捕获/租约、原子过期/七天回收恢复与底座出口；该历史节点未实现这些，也未完成小说。
## 2026-10-26：计划差异与Recipe资格节点完成

- 新ChangePlan.GraphSnapshot可选字段保存当时完整DependencyGraph（原字段名/GraphHash算法不变）。手动计划与发布传播两处在原事务写入，旧文档省略字段仍保持原字节契约；不读取当前图冒充旧图。DependencyEntry/Graph共享domain，service保持别名。IP差异展示函数也共享domain，避免两个版本的字段解释。
- change_plans.payload从固定候选base/new快照重算全部IP变化，资源变化从指定release及其ExpectedHead重算正文/策略/依赖；字段说明、影响项、Via来源、冻结状态、未追踪及闭包外范围逐项核对。所有资源原文都验证实际对象和长度；每个图节点按不可变Revision验证。资源/分支名称固定原值hash并复用已有历史引用，不能偷偷换成当前名称。节点和引用均有4096上限，失败不截断。
- 有GraphSnapshot的记录核对GraphHash、原资源依赖链、Frozen/Untracked以及DependencyImpact结果；只有旧GraphHash的记录仍验证可取得的版本和差异，并明确增加UnboundSources=1，不能从当前图猜补。旧档案缺完整图不是丢弃原计划的理由，也不表示允许回收所有原文。
- Recipe步骤的原用途binding连接当时qualification的payload/report；核对候选ID/hash/manifest、role、task、模型配置和max tokens。Trial步骤不能混入资格字段；历史资格来源不检查当前绑定头，从而不抹掉旧执行依据，但原服务执行门禁保留。
- 两项实际MySQL9.7.4发布传播测试7.580s通过，覆盖延迟传播仍用原文、跨分支、删除后历史、篡改差异/依赖理由/冻结标志/影响动作、旧图明确缺证、恢复原记录及幂等。修改图和计划后同时重算hash也不能绕过真实来源核验。
- 新真实资格Recipe测试8.487s通过，18次模拟评测完成后准备正式用途工作流，再切换当前binding头；登记仍保留旧资格。替换binding/task/max tokens/model被拒绝，登记无额外调用。试用Recipe输入路由/原尝试重试与实际备份回归4.284s通过。
- domain/retention/service单元0.501/0.779/1.779s通过；相关源码与集成测试包vet通过。共享Graph类型导致的3处vet未命名字面量已改为命名字段，不改变JSON。没有正式迁移、业务重启、清理或费用。

## 此来源计划结束后的执行边界

统一来源登记仍为PARTIAL，不能把上述用例或UnboundSources=0当成Inventory.Complete。下一步直接落实写入时捕获和租约：消费可变材料时必须在原事务留下证据，不能等后续扫描补丢失的历史；实际对象写入/采用/发布/备份复制与GC需共享锁和租约。现有retention_objects/retention_references.lease_until为pending契约，尚无真实租约调用，禁止据此启用清理。

沿用retention-live-inventory.md Task2—4及现有expiry/projection/identity/financial规则，不新造并行规则。完成写入与读取覆盖、原子过期、精确物理处理、回收新副本/恢复采用和兼容迁移后做一次底座出口，再实施完整小说。Reelbench仍后置。

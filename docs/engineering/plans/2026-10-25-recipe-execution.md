# Recipe 持久执行 Implementation Plan

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

**Goal:** 将已保存的 Recipe 真正执行为有独立上下文、阶段确认、累计预算和恢复记录的父子任务，完成公共编排机制后供小说专业流程使用。

**Architecture:** 复用现有能力不可变版本、Generations、预算账本、受保护撤销和 Temporal outbox；不另建调度中心。MySQL 保存冻结的执行规格和阶段投影，Temporal 只传任务 ID。每次模型调用仍走统一网关；结果只作为候选，不因流程结束取得采用、出版或能力资格。

**Tech Stack:** Go/Echo v5、database/sql/MySQL 8.4、Temporal Go SDK、Vue 3/TypeScript。

**Spec:** `docs/FINAL_PLAN.md` F04/F06、§长任务与外部费用；`AGENTS.md`；`docs/CODING_STANDARDS.md`；已有 `domain.CapabilityDefinition` 与 `RecipeStep`。

## Global Constraints

- 默认关闭付费；真实调用必须已有实际费用授权；本次验证仅使用测试目录中的协议夹具。
- 模型只产出候选。正史、费用、任务状态和取消由程序校验。
- Writer、Bookkeeper、Critic 保持独立上下文；阶段只接收显式允许的前驱输出，不自动继承全部聊天。
- 同一任务的尝试、模型更换和修订合并计费；UNKNOWN 占用不得自动释放或重发。
- 先公共底座，再完整 N01—N12；此增量不是小说完成声明。小说最多两轮自动修订及不限次数的人工修订由后续专业流程使用同一费用根任务实现。
- 不改写已执行迁移，不放松备份闭合目录、当前撤销或 Windows 身份检查。

## Review Focus

1. 父任务取消与子任务发送/回执竞争：发送前再查父任务，晚到输出不能推进下一阶段，用量仍能结算。
2. 丢失准备/启动回执：父子绑定与生成记录同事务，确定性操作 ID 恢复，不能多创建收费子任务。
3. 修改模型绑定、Key 或方法撤销：冻结配置不随当前下拉框漂移，Key 停用/替换和上游撤销仍实时阻断。
4. 阶段数据路由含重复目标、前向引用、缺字段、大整数或过大正文：显式拒绝，不静默取空或舍入；输入契约再次校验。
5. 备份恢复的执行记录：保留正文和费用来源，恢复副本停用，不恢复调用权限、不重发 UNKNOWN。

## 执行契约

`RecipeExecutionInput` 包含 recipe_version_id、branch_id、expected_ip_head、operation_id、input、currency、budget_limit_micros、steps。每阶段包含 key、price_id、max_output_tokens、context_id、基础 input 以及 bindings（target_pointer、source_step、source_pointer）。bindings 只能读取该阶段 After 声明的直接前驱；按 RFC6901 的对象/数组位置读取，目标冲突或缺失一律拒绝，不运行表达式、脚本或模型生成的指令。

指针语义已核对 [RFC 6901](https://www.rfc-editor.org/info/rfc6901/)：按字符原样匹配，不做 Unicode 归一化；依次解码 `~1` 和 `~0`，数组下标不允许前导零，`-` 不对应已存在元素。实现只接受 JSON 字符串形式，不接受 URI fragment。重复对象成员没有唯一值，必须在解码前拒绝；复用现有领域 JSON 重复成员/深度校验，再用 `UseNumber` 保留数值。目标位置必须已经存在，输入模板用 null 或明确初值占位；根目标只允许单个绑定且结果仍为对象。这些目标写入限制是平台选择，不声称 RFC 定义了写入操作。

冻结规格包含根 Recipe 及全部 Agent/Skill 的版本和 manifest hash、每阶段模型配置/价格/输出限额/资料上下文、输入 hash、执行协议版本。密钥仅沿现有 credential_ref 通道引用，不进入 Frozen 或 Temporal 历史。阶段最终输出必须通过 Agent 输出 Schema；末阶段还须通过 Recipe 输出 Schema。

执行状态使用 PREPARED → RUNNING → WAITING_CONFIRMATION / COMPLETED，另外保留 PAUSED、CANCELLED、FAILED、UNKNOWN、BLOCKED。确认携带预期阶段版本和输出 hash；不能用前一轮确认放行新输出。用户可检查并重做一个阶段，后继阶段失效但历史不抹除，所有尝试仍属于同一预算根。

能力试运行与正式资格分开：试运行需显式选择，不能伪造生产绑定；正式路径检查实际任务用途的有效资格、版本与配置绑定。Recipe 工程跑通不自授文学质量资格；核心小说 Recipe 的实际端到端质量资格仍按 N10/N12 记录。

### Task 1: 确定性阶段数据路由

**Files:** 新建 `backend/internal/domain/recipe_execution.go`、`recipe_execution_test.go`。

**Interfaces:** `ResolveRecipeInput(base json.RawMessage, bindings []RecipeValueBinding, allowed []string, outputs map[string]json.RawMessage) (json.RawMessage, error)`；输入与输出固定 UTF-8 JSON，256 KiB 输入上限，大整数保留。

- [ ] 先写测试：嵌套对象/数组、`~0/~1`、前驱权限、缺字段、重复/交叠目标、非法索引、无隐式全文继承、Unicode、大整数、输出体积上限。
- [ ] 运行 `go test ./internal/domain -run TestRecipeInput -count=1` 确认失败，再实现无 IO 路由与闭合校验。
- [ ] 同命令通过，补 `go vet ./internal/domain`；只提交已核对的本任务文件。

### Task 2: 真实父任务与阶段投影、费用和子任务边界

**Files:** 新迁移 `0034_recipe_execution.sql`；新建 `service/recipe_execution.go`、`recipe_control.go`、`recipe_children.go`；修改 `generation.go`、`generation_control.go`、`generation_stream.go`、存储迁移目录。

**Interfaces:** `RecipeExecutions{Generations *Generations}` 提供 Prepare/Read/List/Start/Control/Confirm/Advance；内部私有 `recipeReference` 绑定生成子任务，不接受 HTTP 自填父身份。

- [ ] 真实 MySQL 测试先覆盖：准备不收费、同操作幂等/冲突、跨项目拒绝、父子原子绑定、阶段确认、取消竞争和失效输入。
- [ ] `recipe_runs` 保存归属、操作指纹、冻结规格、状态/控制版本、预算根与限额；`recipe_steps` 保存阶段/尝试/子任务/确认/结果 hash。子模型调用沿 generation_tasks→model_calls 查询真实费用，不能另造一份可漂移的结算事实。
- [ ] 在现有生成准备、预算预留和发送前重验父状态与固定配置；全部子调用的已发生费用加保留占用不得超父上限，UNKNOWN 保留；重复调用不重复计入。
- [ ] 同一事务写子生成和父阶段链接。采用已有当前权限锁顺序；模型 IO 在事务外。人工重做保持预算根，冻结历史不改写。
- [ ] 使用真实 MySQL 的本地供应商协议测试证明阶段各一次、UNKNOWN 不重发、取消迟到拒收、预算累加和凭据撤销。

### Task 3: Temporal、HTTP 与 Vue 完整操作

**Files:** 新建 `worker/recipe_execution.go`、`httpapi/recipe_execution.go`、`web/src/features/skills/RecipeExecutionPanel.vue`；修改 runtime/server/能力页面；新建对应 integration 与 browser tests。

- [ ] Workflow 只处理 ID/状态；Activity 执行本地幂等推进。复用 generation.start 子流程；父任务等待期间不重发收费操作。按固定轮数 Continue-As-New。
- [ ] 路由提供准备/启动/历史/确认/暂停/继续/取消/重做。页面显示阶段、候选输出、待确认项、累计费用与未知占用；不得把 JSON 自报分数显示为资格。
- [ ] 浏览器覆盖准备→启动→确认→结果、刷新恢复、延迟回执不抢回旧步骤、暂停/取消、第二页面过期确认。后台付费关闭时解释原因并保留已填输入。
- [ ] 真 Temporal 进程重启与 outbox 重投后继续原任务，确认步骤不会被跳过。测试仅使用本地协议夹具。

### Task 4: 恢复、撤销与完整回归

**Files:** backup 目录/兼容/数据验证、retention 目录与归属分类、真实恢复与日志迁移测试、`docs/IMPLEMENTATION_STATUS.md`。

- [ ] schema24–33 旧归档显式补空表，未来表夹带拒绝；新增对象引用、账号/分支/阶段/子调用/预算根闭合校验。
- [ ] 恢复保留已完成输出、尝试与费用，全部执行停用；绝不以恢复激活旧凭据或撤销能力。
- [ ] 更新唯一表/列分类与指纹，用实际 schema34 核对；日志跨版本恢复保留明确升级缺口。
- [ ] 执行定向备份/日志/HTTP/浏览器后运行完整平台脚本，保留失败及修复证据。只有当前实际通过项记录完成；继续 F07/F08 和全部小说范围。

## 自审与实施顺序

当前 schema33 后台摄取完整回归结束后才开始代码变更，避免全量测试读到混合实现。Task 1 可单独验证；Task 2–4 必须构成完整可操作纵向结果。不能将尚未支持的资格或专业修订规则变成默认放行，不能因为前端可点就视为后端完成。

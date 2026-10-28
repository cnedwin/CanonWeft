# 记忆与改编内置能力（F09-C）

## 可安装能力

| 内置包 | 既有模型角色 | 确认式 Recipe | 用途 |
|---|---|---|---|
| `memory-consolidation` | `bookkeeper` | `shared-memory-consolidation-recipe` | 从给定章节、规划及旧记忆形成有来源的事实/信念/关系/状态、分层摘要、伏笔/高光/主题候选 |
| `adaptation-fidelity-review` | `continuity_editor` | `shared-adaptation-fidelity-review-recipe` | 核对主题、主线、动机、因果和高光铺垫/回收的来源与目标证据 |

包在 `shared/` 下由 `Names()` 自动进入现有 Skill 目录。许可沿用项目内部原创方法许可；安装保留 CANDIDATE，不自动激活或授予质量资格。两套输入不同，分别作为单步骤工作流，不能把记忆输出直接当作改编审查输入。

## 调用

1. 仅装方法包：在现有认证会话中 `POST /api/projects/{project}/skills/presets/memory-consolidation`（或 `adaptation-fidelity-review`），请求体 `{"operation_id":"唯一操作号"}`。
2. 装完整固定依赖：`POST /api/projects/{project}/capabilities/presets/memory` 或 `/presets/adaptation`，请求体 `{}`。返回 `root_id` 和 skill/agent/recipe 三个版本引用；同项目重复安装复用 v1 安装收据，不覆盖用户另存的新版本。此步骤不调用模型。
3. 查看 `GET /api/projects/{project}/capabilities/{root_id}/manifest`。Recipe 根的 `trial_ready=false` 是既有规则：该字段只表示 Agent 可直接参加能力试验，不表示 Recipe 依赖不完整。其子 Agent 的 manifest 在依赖完整且未撤销时为 `trial_ready=true`；Recipe 准备流程逐一检查子 Agent。两者均 `qualified=false`，仅有 `QUALIFICATION_REQUIRED` 提示不代表正式生产合格。
4. 使用现有 `POST /api/projects/{project}/recipe-executions` 准备流程；`recipe_version_id=root_id`，`input` 与唯一 `steps[0].input` 使用相应包的 `input.schema.json`，`steps[0].key` 分别为 `memory-consolidation` / `adaptation-fidelity-review`。本次没有运行该流程。准备和启动仍需要当前分支/IP head、授权上下文、模型绑定、价格卡、预算及现有资格/试验门。`trial=true` 不代表免费或绕过费用授权。不要直接提交带示例项目/版本/资源 ID 的 examples.json。
5. 运行只产生等待确认的候选；调用方以固定输入/输出调用 `presets.ValidateMemoryProposal(name,input,output)`，再由服务核对真实资源归属与不可变版本、独立审查和明确采用。能力执行的确认不等于记忆或正史发布。

## 验证边界

所有对象拒绝额外字段，状态固定为 CANDIDATE。来源引用包含资源 ID、revision、SHA-256、Unicode code point 半开区间及原文 quote。程序校验器检查完整文本 hash、每段锚点、分支/视角/时间与保密继承、候选 ID、要求覆盖、改编两侧证据、铺垫/回收配对及 PASS/FAIL 前提。独立反例覆盖伪造来源、修改正文、错误 UTF-16 锚点、提前知情、公开泄露、跨分支、局部缺失外推、仅来源冒充目标、漏项与重复候选。

该校验器不会证明自然语言推论或画面解读正确；调用方传入文本的真实归属也不能靠自报 hash 证明。Recipe推进器已对两个内置v1 Agent key，在普通schema检查后加载冻结的resolved input对象，调用跨输入校验；失败进入无效结果路径，不能进入待确认成功状态。相关纯测试验证正确示例和跨项目结果拒绝，尚未执行真实模型。自动提炼调度、真实来源授权快照获取、输出到MemoryInput的转换及作者采用桥接仍待实现；模型和Recipe阶段确认均不能自动晋升正史。

新增方法未执行真实模型、未产生付费、未完成文学/改编质量评测；示例和通过的契约反例只证明受测结构与程序规则。

# 作者开书与写作台改造实施计划

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

**Goal:** 交付八套完整预设、一句话开书、一页方案确认，并衔接阶段工作台、章节记忆与原地精修。
**Architecture:** 复用现有建书草稿/候选采用、小说规划门、上下文、润稿及版本接口；静态预设生成可编辑建议，真实 AI 完善沿用现有模型及预算流程。
**Tech Stack:** Go/Echo v5、Vue 3/TypeScript、MySQL，沿用已锁定运行时。
**Spec:** [产品设计](../../FINAL_PLAN.md)、[需求追踪](../../REQUIREMENTS_TRACE.md)。

## 全局约束与决定

- 在现有 feat/first-chapter 及未提交成果上增量开发；不重置、清理或复制丢失成果的新工作树，不自动提交推送。
- 保留世界/故事→全书分卷粗纲→当前细纲→正文的服务端确认与版本门；采用建书方案不代替正式规划发布。
- 展示/手改静态预设不调用模型；明确标记预设建议，不冒充对一句话的真实语义分析。AI 定制、图片与真实模型质量仍需原费用授权。
- 延续一套数据与任务系统，不新增业务数据库、微服务或公开社区；正文、记忆、权限、预算与恢复保护不降低。
- 作者手改不得被旧候选、轮询或迟到结果覆盖；旧五步草稿可以恢复，完整字段与导入/已有IP保留在高级设置中。
- 产品方案允许将“五步必填”的交互调整为核心信息完整、渐进呈现，更新该处规范。

## 接口与所有权

后端预设由 novel.SetupPresets() 返回八套 SetupPreset；novel.PreviewSetupPreset(id, idea) 返回完整但未采用的 SetupPresetPreview。HTTP 为 GET /novel-setups/presets 与 POST /novel-setups/preset-preview（preset_id、idea）。预览返回 preset_id、preset_revision、setup、reader_promise、skill_names、notes。版本 2026-10-27.1；Setup 增加可选 preset_id/preset_revision，旧数据兼容。

预设 ID 固定为 urban-career、fantasy-growth、mystery-rules、historical-strategy、modern-romance、era-career、ancient-intrigue、heroine-fantasy。目录项含 id、revision、name、audience、description、example_idea、reader_promise、skill_names；可额外提供题材指导及来源日期。所有默认内容为本项目独立编写。

后端预设实施只修改 novel 预设/设置、novel_entry HTTP、相应服务完善提示和预设说明；开书前端实施拥有 NovelSetupWizard、NovelSetupAssistant、types 与新增开书组件/专项测试；写作台实施拥有 NovelWorkspace、NovelChapterEditor、其新增局部组件及专项测试；主代理负责上下文衔接、技能、计划/续接与最终集成。避免跨区并发改文件。

## Task 1 完整开书

- [x] 八套内容通过 Setup.Validate(true)，目录和免费预览已有非法输入、引用隔离及无模型副作用的定向反例。
- [x] 新入口展示预设、想法与示例；恢复草稿；一页编辑故事、人物和篇幅；高级项保留导入、已有 IP、模型选择。默认一次建议，按需换方向。
- [x] 复用现有 AI 完善和附件；成组采用继续检查 revision，修改后旧确认失效。补齐保存等待期间手改与迟到采用保护。
- [x] 定向浏览器及 HTTP 测试覆盖免费预览、正式建书确认门、草稿恢复及手改保护；已安装页面读回八套预设。测试使用隔离数据与模型替身，未在正式工作室创建验收小说。

## Task 2 日常作者工作台与原地精修

- [x] 工作台按当前阶段、常用动作与按需展开工具组织；保留原有小说能力入口。
- [x] 基于现有润稿接口支持明确选区或整章的润色、精简、扩写、按要求改与比较采用；选区外正文、手改、取消和过期候选有保护。
- [x] 创作助手通过明确任务按钮连接既有能力，写作及精修要求明确章节；本次未实现任意自然语言全局调度，也不伪造模型结果。

## Task 3 本章材料与记忆衔接

- [x] 按需从固定设置与四份完整规划派生本章写作卡，整理目标、人物、读者期待、场景、近期计划与约束；既有检索继续负责已发生事实和伏笔，保留来源、视角、时点与预算约束。
- [x] 复用既有记忆、章节记账与批量依赖；写作卡读取不生成、不记账、不写入正史，规划提示与事实分开。
- [x] Writer 保留四份完整规划并加入派生卡和聚焦检索；Critic 沿用既有审稿，Bookkeeper 不接收未来规划或派生卡。独立来源校验重算派生卡，拒绝伪造或跨书来源；复用已有 Skill 和 Agent。

## 集中验收

只运行本次 novel、service 和 HTTP 受影响的定向检查、Vue 类型/lint/构建及该实现浏览器流程。沿用既有环境、数据库迁移、备份与供应商协议证据，变化未涉及的部分不重跑。模型替身只在 tests，真实质量单列待验。完成后报告实际可见成果与剩余条件。

## 执行记录

- 2026-10-27：现有分支 feat/first-chapter，存量大量未提交成果保留。真实模型验证独立于工程实现。
- 2026-10-28：该实现产品改造已实施并安装至本机 18765；schema38 不变。开书定向 9 例与新增保存竞态 1 例、工作台 8 例、选区辅助函数 4 例通过；预设/设置/材料卡/来源定向 Go 检查、Vue 类型、受影响文件 lint、构建通过。真实隔离 MySQL + 已鉴权 HTTP 的 TestNovelChapterWritingCardHTTP 通过（17.193s）。两处收尾缺陷均先有失败反例再修复，已集中复核一次。

- 默认测试入口中的旧开书套件已最小适配新入口与高级设置折叠，原确认重试、自动保存恢复、人工模式解绑3例通过（7.5s），只改测试，不影响已安装产物。

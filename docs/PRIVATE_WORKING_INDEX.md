# F05 私人工作区语义索引

文档核对：2026-10-28。本文定义专项契约与验证边界；当前工程实现、安装和资格状态见 [实施状态](IMPLEMENTATION_STATUS.md)。

本文定义 F05 私人工作区索引；专项工程验证不代表长篇专业质量、全部 RAG 或整个公共底座资格。

## 使用和权限

知识面板保留现有“明确纳入项目工作资料”和完整输入选择器。额外勾选“在我的草稿与可访问候选中进行语义检索”，再点击“构建 / 更新我的工作区索引”。后台任务保存进度，可关闭页面后回来查看、取消或重新准备。

`include_private_working=true` 还必须同时设置 `include_working=true`，仅允许 author / critic / research。writer 和 npc 拒绝此开关。默认请求、已发布索引指纹、显式 `working_refs` 完整输入行为均保持兼容。语义命中在独立的 `private_hits` 中，不进入 facts、hidden_constraints 或正式原文 evidence。容量不足先移除可选语义片段；必要设定、明确选择的完整输入不被截断，仍不足则拒绝创建上下文。

语料包括当前登录用户的 resource_draft / ip_draft，以及沿原有 workingInputs 项目权限可读取的 PREPARED / READY 设定和资料候选。项目 owner 也不能读取其他用户的私人索引任务、状态或锚点。共享候选的原有访问规则不变。

## 固定版本与持久化

`index_jobs.kind` 为 published 或 working，省略或显式 published 保留旧操作指纹。working 作业走相同 outbox / Temporal worker / cancellation 管线。来源计数按可检索文字单元计数。

`working_indexes` 与 `working_chunks` 是独立 MySQL 表。manifest 包含 project、branch、actor、当前分支 head、本地 BGE 模型 revision、切块版本、每个 typed WorkingRef、状态、基础版本、part 编号、名称、格式、全文 hash，以及明确的遗漏原因。索引 ID 是 manifest 指纹。草稿基础版本失效、候选依赖失效的单位会被列为遗漏，而不是混用旧基础。

构建前在权限事务中固定完整语料；模型工作不占用该事务。每批最多 8 段、每段 512 Unicode 字符、重叠 32 字符，调用既有回环 BGE-M3 服务，校验实际 1024 维标准化向量后持久化。没有伪向量，也没有把已发布索引改名冒充私人索引。结束时重新检查成员权限、作业状态与 manifest，仅完整一致的全部片段在一个事务中可见。中断重试重新构建，不暴露半成品；每批保存进度并检查取消。

检索在 query embedding 前后检查当前 manifest；对全部片段按 ID 每页 128 条扫描，分别保留完整的语义与全文关键词候选列表并排序，执行 RRF 融合后才应用请求的结果数量限制。融合前不截断各模态，以免丢失单项排名较低、合并后应进入前列的候选及其第二个得分贡献。命中会验证 source pin、正文 Unicode 范围、来源 hash、chunk ID、向量 hash 和维度。显式完整输入不参与相似度淘汰。

缓存上下文的 ReadContext 再检查成员权限、当前私人 manifest 与启用状态，因此草稿编辑、候选状态变化、发布、增加或删除语料都会使旧私人包失效。现有 Generation 读取该上下文时继承这一检查。原文锚点接口同样先验证上下文，再重新读取 typed ref/part 并验证 Unicode 切片。

## 接口

- `POST .../knowledge/jobs`：`{operation_id, kind:"working"}`；不接受 release_id / candidate_id 与 working 混用。
- `GET .../knowledge/private-index`：当前用户的当前 manifest、ready、chunks 和遗漏项；索引缺失会显示未就绪。
- `POST .../knowledge/search`：额外开关 `include_private_working`，返回 `private_index_id` 和 `private_hits`。
- `GET .../knowledge/contexts/:context/private-sources/:chunk`：经当前权限和水位校验的完整 part。

## 迁移和恢复

0027 引入私人索引，并允许在 DDL 已提交、迁移日记未提交后重跑。schema 27 使用固定表列目录；24 / 25 / 26 仅通过可信数据转换升级。当前备份使用对应受支持结构的编译目录，拒绝历史档案注入新表、新列或不匹配的迁移哈希。私人片段的 source_hash 指向 SQL 中的版本文本，不被错误当作 blob 引用。

备份恢复保留私人文字、锚点和向量字节，但禁用所有私人索引、取消全部 working 索引任务；恢复继续禁用用户并排除 membership 等运行授权；历史格式的 outbox 排除规则保留，较新结构按其固定清单保留不可投递事件历史，不恢复待投递权限。恢复后的身份不能直接复用旧索引，须明确重新授权并发起新构建。

## 本地验证与边界

兼容与迁移回归必须覆盖：接口接受 kind=working、界面可选择私人检索，以及 DDL 已提交而迁移日记未提交时的安全重入；不能直接重跑无保护的 ADD COLUMN。

Go 集成覆盖真实本地 BGE 的零关键词语义命中、完整显式草稿不丢失、Unicode 锚点、同名跨用户/项目隔离、成员撤销、候选 PREPARED→READY 水位、发布后过期草稿遗漏、中断恢复和取消。另以 140 个真实向量片段验证第二页命中，以实际 BGE 请求期间修改草稿验证提交前水位检查。预算单测覆盖必需全文不被私人片段挤掉。备份覆盖 24/25/26 实际恢复、升级注入拒绝、27 私人索引关闭和任务取消。

2026-10-25 本地检查：Go 全套与 vet 通过；新增分页与推理期间修改测试另行通过；Vue 全量类型、lint、格式检查和构建通过；24 项浏览器测试通过，含私人任务刷新恢复、语义命中、原文核对和过期拒绝。截图核验显示真实示例语义分数 0.673，关键词分数在服务端测试中为 0。真实 MySQL 备份恢复全套通过，新增私人行的十对象 / 25 行样本恢复后保留向量并关闭索引；历史 24 / 25 / 26 恢复另行复验通过。

这些是本机合成数据验证，不是生产数据或付费模型验收。当前实现采用内存中的精确全池扫描；很大语料的索引构建重启需重新计算，容量和长篇质量基准仍需后续专门验收。

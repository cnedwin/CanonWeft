# Vue Go MySQL 技术栈决定与迁移方案

维护日期：2026-10-28。正式技术栈采用MySQL、Vue和Go/Echo；React、Python/FastAPI、SQLite原型保留为历史迁移对照。

当前状态：`backend/`与`web/`已提供实际Go/Vue/MySQL业务实现，包括八个内置预设、快速入口、小说工作台、材料卡与精修。真实模型文学质量和长篇资格仍待验，详见[实施状态](IMPLEMENTATION_STATUS.md)。旧数据是否已完成迁移必须逐实例核对；历史33项Python测试和2条浏览器流程不能作为新栈或存量数据迁移证明。

本文只说明旧原型数据与实现迁移，不再作为产品总计划。完整范围以 [五期方案](DELIVERY_ROADMAP.md) 为准；不能在首章等价迁移后停止建设公共底座和完整小说。

## 1 正式选型

| 部分 | 决定 | 原因 |
|---|---|---|
| 前端 | Vue 3、TypeScript、Composition API、SFC、Vite | 保留类型检查和可复现构建 |
| 状态 | Pinia 管理共享项目/任务状态；编辑中的正文留在页面状态 | 避免后台刷新覆盖未保存稿件 |
| 后端 | Go模块化单体，Echo v5.4.2 | 集中路由、校验和错误处理，保留net/http生态 |
| 数据访问 | database/sql + go-sql-driver/mysql，显式 SQL 与事务 | 正史和费用提交规则便于审查；不靠 ORM 自动迁移 |
| 数据库 | MySQL9.7.4 LTS Community、InnoDB、utf8mb4 | 新安装使用锁定社区版；既有8.4数据单独受控迁移 |
| 中文检索 | 实体/别名 + MySQL FULLTEXT ngram + BGE-M3 dense/Go 精确向量 + 结构化账本 | 保留完整语义能力，旧分词/FTS5 替换；嵌入工具隔离运行 |
| 调度 | Temporal Go SDK 与 Activity Worker | 父子任务、跨天恢复、未知费用对账，保持完整专业流程 |
| 正文文件 | 本地不可变文件、SHA-256、MySQL 引用 | 不因换数据库重新生成正文或改变证据 |
| 部署 | 原生Windows下Go可执行文件提供Vue构建产物；连接本机MySQL | 同源loopback工作台；MySQL仍是独立运行服务 |

Vue 官方推荐的工程路径支持 Vite、SFC 与 TypeScript；Go 的 `database/sql` 提供连接池和事务接口。这里的具体组合是本项目的选择，不宣称是框架唯一用法。[Vue 工程指南](https://vuejs.org/guide/quick-start.html)、[Vue TypeScript 指南](https://vuejs.org/guide/typescript/overview.html)、[Go 连接管理](https://go.dev/doc/database/manage-connections)、[Go MySQL 驱动](https://github.com/go-sql-driver/mysql)。

版本策略以[社区免费兼容基线](COMMUNITY_STACK_BASELINE.md)为准：MySQL9.7.4 LTS Community、Python3.12.15、Go1.27.3、Vue3.5.44。精确锁见deploy/locks、go.mod/go.sum和前端lockfile；Python版本依据现有embedding/OCR组件兼容证据选择，Node等配套保持已验证组合。

框架选择与编码边界见[Echo v5选型](FRAMEWORK_DECISION.md)；v5.4.2已锁入实际服务依赖。锁定版本、构建通过和实际业务验收是不同层次的证据。

模型适配不再仅移植旧 OpenAIProvider；必须按 [五家模型与小说入口](MODELS_AND_NOVEL_ENTRY.md) 完成实际协议、Key 后台和新建小说/角色体验。

## 2 保留哪些成果

保留首章用户流程、三个界面区域、24 个 API 操作的语义、领域输出契约、七个 Skill 方法、稳定实体 ID、证据、费用策略、任务状态和原测试场景。

React 组件重写为 Vue SFC；Python 服务与校验器重写为 Go；SQL 改为 MySQL 语法和隔离机制。原 pytest 用例是移植清单和对照依据，不能仅改扩展名变成 Go 测试。Playwright 尽量保留按可见行为定位的场景，重新对新界面运行。

所有新增能力以新栈为主，旧栈只做数据保护、导出和必要修复。迁移是第一、二期的一部分，不能替代全平台公共机制、能力中心和完整小说；专业漫剧/游戏分别在第三、四期接入。

## 3 目标目录

```text
backend/
  cmd/studio/             启动、配置、生命周期
  internal/httpapi/       路由、中间件、DTO、错误映射
  internal/domain/        实体、版本、证据、账本和纯校验
  internal/service/       项目、章节、上下文和提交用例
  internal/storage/      MySQL 仓储、事务、不可变文件
  internal/worker/        固定章流程、恢复和取消
  internal/provider/      模型协议、响应与用量
  internal/skills/        内置方法和项目派生版本
  internal/backup/        导出、导入、撤销与恢复
  migrations/            有序版本 SQL 与校验和
  go.mod / go.sum
web/
  src/components/         复用交互组件
  src/features/           项目、编辑、审查、设定、费用
  src/composables/        轮询、会话、未保存保护等逻辑
  src/stores/             Pinia 共享状态
  src/api/                请求客户端与类型化 DTO
  src/types/
presets/                  保留七个专业 Skill
docs/                     当前架构、规范、计划与证据
```

以上为新栈目录职责示意，实际文件以仓库为准。旧`studio/`和`frontend/`仅用于历史对照与必要迁移读取，不得与新栈共同写入同一项目；切换应明确归档位置和唯一启动入口。

## 4 MySQL 数据与提交

原型九类数据保留业务含义，新栈使用有序迁移、独立预算账户及明确的领域记录。高频关联、状态、版本、费用使用明确字段和索引，复杂章卡/账本/快照使用JSON。正文保持外部不可变文件。以下为迁移契约，不能替代实际schema核对。

- ID 保留原始 UUID 字符串；标识列使用大小写明确的二进制比较规则，文本使用 utf8mb4。角色名称不作为主键。
- `canon` 保留 `(project_id, chapter)` 唯一约束；候选、任务和版本增加项目范围索引。需要的外键随迁移显式建立，不能只开一个连接参数就声称具备完整性。
- 系统时间统一 UTC、DATETIME(6)；故事时间仍是独立领域字段，不和系统时间混合。
- 新费用记录用整数微元人民币：1 元 = 1,000,000 微元。旧浮点估算按十进制定点转换并向保守方向处理，保留原值和价格版本供核对。
- 不使用 MySQL `REPLACE INTO` 模仿 SQLite 替换记录，避免删除再插入的语义影响引用；按明确用途用 INSERT、UPDATE 或受控 upsert。

正史提交顺序：

1. 事务外写临时正文、校验 hash、形成不可变文件。
2. Go `BeginTx`，在同一 `sql.Tx` 上锁项目行并比较 `expected_revision`，再校验候选、前章与证据。
3. 更新正史指针、账本、设定、版本和检索文档；提交事务。
4. 数据库提交失败或结果不明时不返回保存成功；查询既有操作/修订 ID 确定结果，不能直接重复写入。

费用预留锁定同一账户预算行，原子检查累计用量、在途和未知占用，再插入唯一调用记录；网络请求在事务提交后执行。确定数据库死锁回滚时只重试无外部副作用的短事务，不能连带重新请求模型。[Go 事务指南](https://go.dev/doc/database/execute-transactions)、[MySQL 锁定读取](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html)。

## 5 检索替换

MySQL 的 ngram parser 支持中日韩文本全文检索。初版使用明确配置的二元切分，并将配置写入环境检查；语义、排序与 jieba/FTS5 不保证一致。[MySQL ngram 文档](https://dev.mysql.com/doc/refman/8.4/en/fulltext-search-ngram.html)。

查询先按项目、正式修订、目标章之前、未撤销和非待复核范围过滤；精确身份/别名与必需账本优先，全文分数只用于补充证据排序。单字名、别称、罕见专名必须有精确关联回退，不能因为 ngram 召回不到就认定不存在。

不在同一个未提交事务内依赖刚写入文档的全文搜索结果来决定确认。提交后验证索引可用；失败应暴露索引状态并可重建，不丢正文。使用相同小说样例对比迁移前后相关证据，而非只检查 FULLTEXT 索引存在。

## 6 Go Agent 与费用实现

角色由 Temporal Go 工作流调用版本化方法和独立输入，不为每个角色建服务；本地嵌入使用受控专用工具进程，Go 拥有全部业务接口和状态。预设 Markdown 继续使用，Schema 改为版本化 JSON Schema 和对应 Go struct，双向契约测试防止字段漂移。

所有调用使用 `context.Context`、超时和类型化错误。启动任务时保存快照，不把浏览器请求上下文直接当作整个后台任务生命周期；页面关闭不会悄悄取消或重发请求。用户取消由持久任务状态决定，已发出的调用即使 transport 被取消仍可能计费，继续保持 unknown 费用。

数据库锁保护费用，Temporal 管理持久执行；业务提交仍需 operation ID、generation 和版本条件，不能仅靠 Worker 串行声称并发正确。收费请求结果未知不得被 Activity 自动重试重新发送。

## 7 备份与迁移数据

MySQL 不能继续使用 SQLite backup API，也不能打包运行中的数据文件目录代替一致备份。

新项目备份采用版本化逻辑包：manifest、类型化记录 JSONL、正文 blob 与撤销快照。应用在同一 InnoDB REPEATABLE READ 一致读取事务中取项目所有记录，备份期间禁止 schema 迁移；正文已不可变，按快照引用打包。导出前后核对撤销版本，发生相关撤销则重试或拒绝导出。引用垃圾回收必须保护进行中的备份。[MySQL 一致性读取](https://dev.mysql.com/doc/refman/8.4/en/innodb-consistent-read.html)。

恢复先在 staging 中验证 schema、大小、路径、hash、身份和引用，再导入非活动项目、重建索引，最后一次切换为可见。当前撤销日志独立保留。用户恢复仍创建副本，不覆盖旧项目。

SQLite 到 MySQL 是一次性迁移：

1. 停止旧实例写入和 Worker，备份数据库、正文、当前管理日志；未知调用保持未知，不重发。
2. 使用旧版可信导出工具产生迁移包；旧项目 ZIP 没有完整费用/调用记录，不能单独用它迁移应用账本。
3. Go 导入器验证记录并进入空目标库，保留项目/实体/修订 ID、正文 hash、技能 hash、全部费用记录；活动任务转安全暂停或 unknown。
4. 比较所有项目的记录数、关键 ID、正文 hash、正式章指针、Skill 与费用总额，验证检索和隔离恢复。
5. 验收后切换唯一启动入口；不自动删除原库。

回退只在新库未产生用户新写入时可直接切回旧快照；新库已有创作后须先导出并完成反向核对，不能用旧库启动造成新内容丢失。迁移读取可临时用旧 Python 环境；新版业务由 Go 运行，本地 BGE-M3 工具拥有独立锁定的 Python 推理环境。

## 8 原型迁移子步骤（不代表五期交付边界）

| 阶段 | 交付 | 退出条件 |
|---|---|---|
| M0 环境与契约 | 确认 Go 工具链、本机 MySQL；固定接口、Schema 和测试场景 | 独立空测试库可连接，凭据不入代码，付费关闭 |
| M1 Go 存储与 API | 项目、草稿、版本、证据、导出、备份 | 真 MySQL 集成测试通过；不能用 SQLite 替身代替 |
| M2 Go 工作流 | 七角色、真实 Provider 适配、预算和恢复 | 本地 HTTP 模型夹具驱动完整流程；未知/取消/并发预算回归通过 |
| M3 Vue 工作台 | 同样的创建、写作、设定、审查、版本与费用交互 | vue-tsc、构建、组件测试和真实浏览器流程通过 |
| M4 数据与启动切换 | 数据迁移、对照、Windows 启动、回退说明 | 空库与有数据两种场景通过；正式旧数据核对；一次仅一个写入入口 |
| M5 真实模型验收 | 新栈生成并审查一章 | 用户明确授权具体费用后执行，与迁移工程验证分开 |

表中M0—M5是迁移检查维度，不是五期方案中的漫剧M01—M08；各维度的验证不能互相替代，也不能证明完整小说质量。新栈工程实现已经存在，存量数据迁移和真实模型验收须按实例分别记录。新增本机MySQL使用锁定社区版，不自动开通云数据库。

迁移验证使用独立本机端口与测试库，同名数据目录不得与旧应用并写。Vue生产构建由Go同源提供；开发服务器仅在开发模式通过本机代理访问API，不为了调试放宽正式Origin校验。非Windows凭据保护返回ErrVault，进程隔离明确不支持非Windows；安装锁限定Windows，现有Host/Origin/loopback限制不构成公网部署方案。Linux、Docker和公网部署仍需独立实现与验收。

## 9 专项验收清单

- 中文、emoji 与换行在 Python→Go→JS 间保持原文 hash；字符锚点按 Unicode code point，Go 用 rune 语义，不能把 byte offset 当字符位置。
- 两个并发确认请求最多一个成功；两个费用预留请求不能一起越过账户上限。
- 关闭数据库、超时、提交回执丢失不报告假成功，不重复费用或丢失正文。
- 带前缀的谎言不能升为事实，手改首章保留有效候选设定与章卡。
- 刷新、重开、切项目与新章操作不会被旧轮询响应覆盖。
- 备份在并发正常写入下仍一致；坏包、旧撤销和副本删除回归通过。
- 新应用业务确实由 Go/Echo/Vue/MySQL 运行，无 SQLite 回退；专用嵌入工具与业务写权限隔离，Temporal 和真实向量检索通过验收。

本方案补充当前架构和编码规范。变更的是实现技术，小说优先顺序、IP 统一原则、完整角色工作流和付费授权边界继续有效。

# F06 允许来源内自动发现

文档核对：2026-10-28。本文定义专项契约与验证边界；当前工程实现、安装和资格状态见 [实施状态](IMPLEMENTATION_STATUS.md)。

允许来源发现模块提供公开 GitHub 目录的持久发现、完整包导入和静态研究；发现成功不授予专业模型质量、保留集评测、执行资格或完整 Recipe 运行资格。

## 作者操作

在“Skill 研究与编辑”的“允许来源内自动发现”登记仓库、分支/提交和允许目录。默认免费公开来源示例为 `anthropics/skills` 的 `main` / `skills`。初始开关关闭；可以启用每日发现或点击“立即发现”。关闭网上发现不会撤销或删除随系统预置的方法包，也不会修改已经保存的本地编辑。

只在登记的公开来源内枚举包含 `SKILL.md` 的目录，不宣传遍历全网。目录关键词在本地匹配，不发送小说正文、目标描述或关键词给搜索服务。远端只收到公开仓库、分支、提交和文件路径。代码不使用任何模型 API，也不执行下载包内的指令、脚本或安装文件。

后台任务在 MySQL 保存来源配置快照、固定 commit、目录清单和项级进度，通过事务 outbox 交给 Temporal。页面关闭不影响任务。可暂停、恢复、取消；停止期间已导入的候选保留。修改来源或停用来源会阻断使用旧来源配置的任务。提交每个候选时重新校验权限、来源版本与当前任务状态，取消后的晚到文件不能成为新候选。

新版本是独立研究候选，保留旧来源包和旧包的本地派生版本。列表显示旧/新包 ID；点击候选可查看实际静态报告、完整文件和 SHA256。存在上游变化时填入三方比较的基线/新上游，需要选择已有本地派生版本并人工处理冲突。

## 边界和限制

- 每项目最多登记 10 个来源，每次最多 5 包，项目每天最多预留 20 包；失败与取消任务的预留仍计入当天限额。按 UTC 日计数。同一来源最多一个未结束任务。
- 每次发现最多考虑排序后的 1,000 个匹配目录，报告保留匹配总数；已成功导入的同 source/commit/path 会跳过，后续批次可以继续其余目录。
- 完整包沿用既有上限：128 文件、每文件 2 MiB、包合计 8 MiB；不以截断文件冒充完整包。
- 固定提交后抓取全部包内文件，按 Git blob SHA1 和字节数核对，再生成包及每文件 SHA256；保留包内许可和可用的仓库根许可。
- 截断 Git tree、符号链接、子模块、危险路径、私有/保留 IP、重定向、来源域名变化、超限及 hash 不符均拒绝。来源许可是静态证据，不自动授予使用权。
- 静态分析记录方法说明、工具请求、脚本文件、许可证据和发现项。输出始终为 `STATIC_ONLY` 的 `CANDIDATE`，不能直接变成可执行能力。
- 每日 Temporal 日程按 UTC 日边界唤醒；首次登记时也检查当前开关。数据库暂时失败后有限重试，再持久等待一小时恢复。任务 GET 最多由活动重试三次；不重试收费请求，因为该流程不存在收费请求。
- 每周独立模型/保留集评测、专业质量资格、激活和完整 Recipe 运行由对应能力与调度模块承接，不能由本发现模块的完成状态推定。

## 接口与状态

项目权限沿用既有工作区角色：读取需可见项目，登记/修改/启动/控制需编辑权限；客户端不能指定执行身份。

| 接口 | 作用 |
| --- | --- |
| `GET /api/projects/:id/skill-discovery` | 来源、最近30任务和最近100候选 |
| `POST /api/projects/:id/skill-discovery/sources` | operation_id幂等登记；expected_revision必须为0 |
| `PUT /api/projects/:id/skill-discovery/sources/:source` | operation_id与expected_revision防止覆盖他人修改 |
| `POST /api/projects/:id/skill-discovery/sources/:source/runs` | operation_id幂等启动，事务内预留当天配额 |
| `POST /api/projects/:id/skill-discovery/runs/:run/control` | action为pause/resume/cancel，状态持久化 |

任务状态为 QUEUED、RUNNING、PAUSED、CANCELLED、BLOCKED、FAILED、COMPLETED。`COMPLETED` 仅表示该任务限额内的完整包静态研究完成。发现失败保留原因和此前候选，可排查后启动新任务。定期刷新只更新列表，不更新来源表单或方法编辑器的未保存内容。

## 固定版本在线验证记录

2026-10-25 用生产 Fetcher 对 [Anthropic 官方 Skills](https://github.com/anthropics/skills) 只读发现并抓取 `skills/brand-guidelines`，固定提交 `dbd4588f9e1033efb41dad4bef2f7947c8993d44`，完整2文件均通过远端字节数/Git blob hash校验。包 SHA256：`38e10e6e937289df9e2fc9be2b575ac2186f87d342aec94d5dde463d9879e25b`。结果为 STATIC_ONLY；没有执行任何文件或模型请求。

Git tree模式和截断行为参考 [GitHub 官方 REST Git trees 文档](https://docs.github.com/en/rest/git/trees)。在线证据与本地测试夹具分别记录，夹具不作为线上真实性证明。

## 持久失败收尾与并发进度契约

- 公共GET最多3次失败后，最终FAILED收据在Temporal内持续重试落库（每次最多20秒、退避最多1分钟、无总次数截止）。即使业务数据库长时间不可用，也不会结束workflow并遗留阻塞同源的新任务；恢复只重试写收据，不重新抓取远端。
- 替代活动已推进项级进度时，旧活动返回专用superseded结果。Workflow持久等待后按已保存进度继续，不将并发进度变化误记为权限撤销/BLOCKED，也不启动失败收尾。第一次持久保存的inventory与commit不会被晚到的另一份远端清单覆盖。

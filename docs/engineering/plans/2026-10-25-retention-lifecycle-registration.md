# 删除期限的权威时间与持久登记

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

> 执行 分阶段实施与定向验证；按既有范围实施，复用现有保留契约，不重复已完成作者根检查。

**Goal:** 从受保护删除账本获取可证明的时间，在实际项目删除事务中登记生命周期；重放、重启和旧数据不得延长期限或复活访问。

**Spec:** retention-schema32-contract.md 的 SourceUTC/LegacyRegistered 和 project_lifecycle；retention-live-inventory.md Task 2。

**Architecture:** 受保护 events 增加带默认 LEGACY 的 clock_basis，升级不更改旧事件。新 Append 显式写 UTC_TIMESTAMP 和 UTC_V1；未升级账本兼容旧接口但不声称有 UTC 证明。DeletionEvidenceTx 绑定当前服务/源库/账本/项目。retention.RegisterProjectLifecycle 从真实证据计算、锁定、校验并保存 pending 生命周期表。正式 retention 迁移登记后 DeleteProject 自动在同事务调用；未登记保持原有撤销，当前不激活 pending 表。

**Ruling:** 不用已有 DATETIME 的外观或当前连接时区反推旧时区；首次旧删除登记固定宽限时刻，但没有回收权限。新删除的七天窗口严格从受保护 UTC 事件开始。迁移标记已存在而配套表缺失时整个删除回滚，不写出半份撤销。完整在线清理仍须后续表兼容、读取门禁和执行器，不能只凭本片启用。

- [x] RED：真实MySQL +08:00连接仍取得UTC删除时刻；旧证据重复登记不延长且无恢复窗口；T0/重试同事务生命周期；失败登记回滚删除。
- [x] 实现受保护时间字段和读取、生命周期登记、删除服务连接。
- [x] 定向真实数据库和相关Go检查，记录兼容及部署边界。

Pre-flight: 受保护账本不进入业务备份；旧Current读取列保持不变，不能把旧业务恢复出的deleted_at作为T0。pending表未改为正式迁移，schema35目录不变。

## 实现细节

- Provision以可信管理连接给受保护events新增clock_basis ENUM('LEGACY','UTC_V1')，默认LEGACY，旧证据不回填UTC。Append显式写UTC_TIMESTAMP(6)；有正确元数据列时标UTC_V1，重复事件保留原条目。不依赖业务库deleted_at。
- DeletionEvidenceTx核对源库、当前账本、创建/删除顺序和项目身份，并用FOR SHARE读取当前提交记录，不能因旧RepeatableRead快照漏掉删除。时钟来自数据库UTC查询；没有可靠时区的历史值仅作为原始墙钟证据保留。
- RegisterProjectLifecycle锁项目和已有登记；活跃登记到删除时保留首次核验时刻、递增清单版本并清除旧扫描标记。已有删除登记必须与受保护事件完全匹配；不一致拒绝，不覆盖修补。旧时间以首次看到删除证据的时刻固定宽限，不设置recover_until。所有DATETIME参数以明确UTC文本写入，规避驱动Loc转换。
- DeleteProject首次与重试同事务登记。仅精确的0036_retention.sql迁移记录激活（目前只预留）；同样收紧旧请求准入的迁移识别。正式迁移缺表/损坏时返回未就绪并回滚业务删除、受保护事件和操作收据。pending测试表只安装在一次性库，主服务没有启用。

自查复现并修正：旧快照读取历史权威、模糊迁移名误激活，以及本地驱动Loc导致UTC持久值偏移8小时，均有真实MySQL失败→通过证据。一次限定只读审查未发现额外Critical/Important问题，没有重复全仓交叉验证。

## 验证与续接

最终七项真实MySQL9.7.4生命周期专项一次全部通过，18.106s；包含活跃登记→删除的版本变化、正式删除服务重试、登记缺表回滚、旧快照、旧账本升级与驱动时区。受保护账本三项真实数据库/受限账号/进程中断恢复通过1.345s；子进程helper在主测试进程按设计跳过，由对应中断测试实际调用，不计独立成功场景。retention全单元0.722s、service包1.932s及authority/retention/service vet通过。

专用runner为外层work/test-retention-lifecycle.ps1，支持Scope authority或默认七项生命周期；凭据只从专用13317实例DPAPI读取，不打印。没有实际供应商调用、付费、业务库迁移或正式数据删除；没有前端变更，不重复浏览器验收。

当前实现的是项目删除生命周期登记，不是所有资料引用的期限登记、实际过期或完整回收功能。后续直接继续F08：将完整来源引用及派生副本原始期限接入持久登记，再连接原子过期/对象回收、七天回收新副本和恢复启用；正式0036只能在备份/日志/读取兼容一起通过后激活。之后继续完整小说，不能重复本片时钟研究或据此宣布公共底座完成。

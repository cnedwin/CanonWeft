# 社区免费技术基线 v1.7.1

维护日期：2026-10-28。版本策略优先保证现有必需组件兼容、来源可核验和部署可复现。以下是仓库锁定基线，不代表各项目的全球最新版本；产品范围见[分期方案](DELIVERY_ROADMAP.md)，当前功能与验收边界见[实施状态](IMPLEMENTATION_STATUS.md)。

| 组件 | 锁定版本/发行 | 许可与选择理由 |
|---|---|---|
| MySQL | **9.7.4 LTS Community Server** | 社区免费GPL发行；不使用Enterprise或付费云数据库。[版本](https://dev.mysql.com/doc/relnotes/mysql/9.7/en/news-9-7-4.html)、[社区版](https://www.mysql.com/products/community/) |
| Python | **3.12.15，标准GIL构建** | PSF及随附开源许可；锁定的RapidOCR1.4.4要求Python<3.13，现有embedding/OCR依赖闭包已在此版本验证。该证据限定于锁定组件，不构成对其他Python版本的普遍不兼容结论。[版本](https://www.python.org/downloads/release/python-31215/)、[许可](https://docs.python.org/3/license.html) |
| Go | **1.27.3** | BSD-3-Clause；与当前后端依赖和构建配置配套锁定。[版本](https://go.dev/doc/devel/release)、[许可](https://go.dev/LICENSE) |
| Vue | **3.5.44** | MIT；使用已验证Composition API/TypeScript组合，不切换预发布版。[版本](https://github.com/vuejs/core/releases/tag/v3.5.44)、[许可](https://github.com/vuejs/core/blob/v3.5.44/LICENSE) |

Python.org的3.12安全更新仅提供源码；Windows采用已校验的Astral python-build-standalone完整发行，保留解释器与捆绑依赖许可，不能把社区二进制称为Python.org官方Windows安装器。其余基础设施沿用开源自托管版本；不新增付费企业功能、强制商业账户或云托管依赖。可选模型API仍遵守原有单独费用授权，软件免费不能被当作模型调用免费。

## 配置与实际运行

- 主目标锁：deploy/locks/runtime.json、toolchain.json，以及backend/go.mod和web/pnpm-lock.yaml。
- MySQL9.7.4实物锁：deploy/locks/mysql-lts.json；官方ZIP摘要、Oracle签名、实际版本已有核验记录。独立隔离实例已通过schema35记忆HTTP闭环与备份恢复；该验证没有覆盖原数据目录。
- 新安装默认9.7.4。scripts/mysql-package.ps1将既有未标记8.4.13数据继续锁在原引擎；只有经验证的新目录/迁移标记才选择9.7.4。26.10目录不能交给9.7降级。原业务8.4实例的最终迁移与Temporal组合资格仍须完成，不能把独立测试写成生产切换。
- Python采用独立版本化venv，迁移时保留旧环境供回退。完整依赖安装、pip check及功能验证须与锁定环境一致；运行中的Go进程继承的ingest环境必须随受控重启切换，不能仅改脚本就认定旧进程已切换。
- 旧mysql-next.json/26.10及python-next.json/3.15仅保留实验与历史证据，不再是必须追赶的目标。

## 已记录的本机验证（2026-10-25）

- MySQL9.7.4：schema35记忆采用/上下文失效/隐私隔离及备份恢复测试通过；Temporal1.32.1在两个随机隔离库完成持久化schema1.19和visibility schema1.14的建表、升级及重入，8次调用成功，临时库已清理。这不代替Temporal服务/工作流端到端及存量迁移资格。
- Python3.12.15：embedding21个、ingest20个锁定依赖均通过pip check和包文件完整性校验；BGE-M3真实推理、原生PDF、扫描PDF及PNG中文OCR通过。该快照记录的嵌入进程使用3.12.15；文档解析环境与已有Go进程的切换仍须按实际进程核对。
- Go1.27.3：对照SHA256已验证的官方ZIP逐项匹配15,657个安装文件后补齐安装收据，四工具完整性和实际版本联合检查通过。没有以重新计算现有目录哈希掩盖差异。
- 启动选择器4项检查通过：新安装选9.7.4、旧数据保持8.4.13、拒绝26.10降级、尊重9.7.4数据标记。实验版本目录不能作为正式安装或迁移完成的证明。
- 上述记录不包含真实模型API调用或文学质量评测；Go/Vue业务构建证据限于对应版本与改动范围，不是全平台回归证明。

## 支持环境边界

安装锁面向原生Windows amd64、本机loopback部署。当前凭据保护与工具进程隔离依赖Windows实现：非Windows的`credentials/protection_other.go`返回`ErrVault`，`isolation/process_other.go`明确拒绝运行。Host、Origin和本机会话限制也不能直接用于公网多用户部署。Linux、Docker和公网部署均未取得可运行资格；可选Linux服务或实验记录不改变这一支持边界。

## 开发和迁移规则

先验证独立环境与必要业务闭环，再完成数据副本备份/恢复、Temporal和日志组合检查，最终受控切换旧业务。依赖锁固定并可复现；安全补丁在相同兼容分支评估，新次版本只有明确收益和完整组件验证后才采用。修改锁文件本身不证明服务切换或存量数据迁移成功。

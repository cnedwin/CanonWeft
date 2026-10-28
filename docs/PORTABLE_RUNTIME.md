# Windows 便携工具环境

项目提供固定版本的 Windows x64 PowerShell、Node.js、CPython 和 Go。普通用户使用根目录 `Install-Platform.cmd` 安装、`Start-Platform.cmd` 启动；应用脚本使用便携 PowerShell 7，不依赖开发工具注入的 PATH。完整操作和停止顺序见 [LOCAL_INSTALL.md](LOCAL_INSTALL.md)。

工具安装在仓库旁的 `work/runtime/toolchain`，路径从脚本所在仓库推导。工具链只设置当前启动进程及其子进程的环境，不修改注册表、用户或系统 PATH。仓库及父目录均须可写。当前凭据库依赖 Windows DPAPI；此 Windows 包的凭据保护和工具隔离流程不支持直接转为 Linux 部署。

## 固定版本与来源

精确 URL、SHA-256、许可证参考和记录日期保存在 [deploy/locks/toolchain.json](../deploy/locks/toolchain.json)。版本固定用于复现，不能据此推断为上游最新版本；本次整理不升级依赖。

| 工具 | 锁定发行包 | 上游来源 |
|---|---|---|
| PowerShell | 7.6.5 Windows x64 ZIP | [Microsoft 发布页](https://github.com/PowerShell/PowerShell/releases/tag/v7.6.5) |
| Node.js | 24.19.0 Windows x64 ZIP | [官方校验清单](https://nodejs.org/dist/v24.19.0/SHASUMS256.txt) |
| CPython | 3.12.15，Astral 20261021 Windows x64 install_only | [python-build-standalone 发布页](https://github.com/astral-sh/python-build-standalone/releases/tag/20261021) |
| Go | 1.27.3 Windows amd64 ZIP | [Go 官方下载页](https://go.dev/dl/) |

CPython 使用 Astral 维护的完整便携发行包，支持普通 venv/pip 工作流。保留各包附带的许可证和第三方声明；Python 发行包许可说明见 [发行文档](https://docs.astral.sh/python-build-standalone/distributions/)。MySQL 9.7.4 Community LTS、Vue 3.5.44、Temporal 及其他应用依赖另由 [运行时锁文件](../deploy/locks/runtime.json) 和各依赖锁文件固定。

## 安装和启动链

要求 Windows x64、Windows 自带的 Windows PowerShell 5.1 与 `System32/tar.exe`。启动引导链为 CMD → 系统 PowerShell 5.1 → 校验/准备便携工具 → 便携 PowerShell 7 → `Start-Platform.ps1`。首次准备工具需要访问锁文件指定的发行站点；BGE 模型、应用依赖、数据库及隔离工具由完整安装入口另行准备。

```powershell
# 在仓库根目录，仅安装/校验四种工具，不启动应用。
& "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-portable-tools.ps1

# 只校验已有工具；不下载，不启动应用。
.\Start-Platform.cmd -VerifyOnly

# 完整安装与普通启动。
.\Install-Platform.cmd
.\Start-Platform.cmd
```

普通启动可能在四种工具缺失时下载并准备工具，因此首次完整安装应先执行 `Install-Platform.cmd`。`-NoBuild` 只跳过应用构建，不跳过权限、日志、数据库或工具校验；`-VerifyOnly` 即使与 `-Launch` 同用也不会启动应用。工具缓存齐全且校验通过时，工具校验本身可离线运行，不能据此推断整个安装流程无需联网。

引导阶段将进程内 `PSModulePath` 指向当前 PowerShell 自带模块，避免继承其他版本模块后遮蔽 `Get-FileHash`。该设置不写入用户或系统配置。

## 完整性与目录保护

四种工具归档必须匹配锁文件 SHA-256 后才能解压。新包先在同盘随机短暂存目录解压，检查路径、记录文件 hash、复验后再重命名发布。已有工具目录只验证，不覆盖或删除；receipt 缺失/损坏、原包文件被改写或缓存包校验失败都会停止并保留现场。安装文件锁阻止并发安装，没有自动递归删除工具目录的逻辑。MySQL 的既有校验和可执行文件签名要求由其安装脚本独立维持。

校验清单用于下载完整性和意外损坏检查，不能防御能够同时改写源码、锁文件和 receipt 的本机攻击者。已发布的工具目录不应运行 `pip install` 或 `npm install -g`；Python 依赖只装入专用 venv。每次文件校验可能需要一些时间，不能用忽略错误来缩短启动。

## 脚本接入契约

以下用于 `scripts/` 内的 PowerShell 7 脚本；普通使用者直接运行 CMD 入口即可。

```powershell
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$tools = Enable-PortableToolchain
# 返回经过文件校验的 Python / PowerShell / Node / Go 绝对路径。
& $tools.Python -I -B -m venv $newVenvPath
if ($LASTEXITCODE -ne 0) { throw 'venv creation failed' }
```

`Enable-PortableToolchain` 不下载，也不改写已有 venv。它校验文件清单、前置进程 PATH，设置 `STUDIO_PYTHON`、`GOROOT` 和 `GOTOOLCHAIN=local`，关闭 Python 用户包与工具目录字节码写入，并清除外来的 `PYTHONHOME`/`PYTHONPATH`。`Get-PortableToolchain` 只校验并返回路径，不设置环境；安装器完成后另行核对四个工具的精确版本。

Python 服务和验证入口显式使用 `python.exe -I -B ...`。`-I` 会忽略 `PYTHONDONTWRITEBYTECODE`，所以环境变量不能代替 `-B`。原发行包中的 `.pyc` 仍属于完整 hash 清单，不能为了容忍运行时改写而豁免。

## 已有环境与验证边界

工具安装器不会自动迁移旧 venv，也不会停止正在运行的嵌入、摄取、数据库或应用。升级前先正常停止所有相关应用和 worker，保存备份、原 Python 版本、`pyvenv.cfg` 和依赖清单。解释器版本或依赖集合不一致时，不要仅改路径或直接执行 `venv --upgrade` 冒充迁移；应按锁文件在独立目录重建并验证后再切换。

重新创建 Python 环境后，需要核对 `sys.base_prefix`、pip、依赖与实际服务导入，并再次校验基础 Python 包文件。凭据保护、数据库迁移和服务恢复还需各自验证，工具校验不代替这些检查。

维护用的轻量校验脚本为 `scripts/test-portable-tools.ps1`；完整工具链测试入口为 `tests/portable-tools.tests.ps1`，其中 `-Integration` 会实际调用已有工具并创建独立 venv。入口参数传递与模型联网默认值由 `scripts/test-start-entrypoints.ps1` 在隔离目录检查，在第一个依赖边界停止，不启动实际服务。

本次公开整理只执行脚本语法检查和修改行为对应的隔离定向测试；未重新执行安装、下载、数据库迁移、真实服务启停或模型调用。历史机器上的安装记录不构成新的干净机器安装验证，也不构成五家模型真实质量结论。

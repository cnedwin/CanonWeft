# Windows 本地安装、启动与停止

当前应用使用 Go/Echo、Vue 和 MySQL；BGE-M3 与 PDF/OCR 使用独立 Python 环境。支持入口为 Windows x64，应用脚本使用项目便携 PowerShell 7。首次引导使用 Windows 自带的 Windows PowerShell 5.1 和 `tar.exe`。当前凭据保护依赖 Windows DPAPI，隔离工具也有专用运行时；此安装包不能直接作为 Linux 部署包使用。

## 从干净克隆安装

将仓库放在自己有写入权限的目录，仓库的父目录也须可写；安装器会在仓库旁创建 `work/`。路径从脚本所在目录推导，不依赖固定用户名、盘符或开发工具缓存。首次引导在校验固定归档后安装便携 PowerShell 7，再由该已校验入口继续安装和完整性检查，兼容 Windows PowerShell 5.1 的长路径限制；不会改用系统 PATH 中未经校验的 PowerShell。仍建议使用较短目录，以兼容数据库和其他工具的路径限制。

1. 双击根目录的 `Install-Platform.cmd`，或在仓库根目录运行 `.\Install-Platform.cmd`。
2. 安装器下载并校验锁定的 PowerShell、Node.js、Python、Go、MySQL Community、Temporal、测试存储、本地嵌入模型、文件解析和隔离工具，按锁文件安装依赖并构建应用。首次安装需要联网和足够的磁盘空间，也会安装验收使用的 Chromium。
3. 安装成功后双击 `Start-Platform.cmd`，或运行 `.\Start-Platform.cmd`。
4. 等待终端提示应用地址后，在浏览器打开 [本机工作台](http://127.0.0.1:18765)。保留启动窗口，以便查看错误并正常停止应用。

安装只准备依赖和应用构建，不创建个人账号，不调用外部生成模型，不初始化业务数据库。启动时才初始化本机数据库、执行结构迁移、准备独立撤销账本和日志保护、启动检索服务与应用。权限、数据库身份或完整性检查失败时会停止；不要绕过检查继续运行。

固定基线为 MySQL **9.7.4 Community LTS**、Python **3.12.15**、Go **1.27.3**、Vue **3.5.44**；确切下载地址和校验值见 [运行时锁文件](../deploy/locks/runtime.json) 与 [工具链锁文件](../deploy/locks/toolchain.json)。不自动升级依赖版本。第三方软件和模型各自适用其许可证，安装前应阅读相应锁文件及上游许可；安装入口不会代替用户接受新的许可协议。

工具和下载缓存位于仓库旁的 `work/`，业务数据库、正文对象和本机凭据位于仓库内 `.runtime/`。运行目录不属于源码发布内容。不要通过删除 `.runtime/`、覆盖数据库目录或复制他人的凭据来重新安装或恢复账号。移动已有安装前先停止所有服务并备份；便携工具链不代表 DPAPI 凭据可以跨 Windows 用户或机器直接使用。

## 账号和模型设置

**没有默认账号或默认密码。** 全新数据库第一次打开时，由使用者自行创建个人账号和工作空间。已有数据库显示登录页，使用原有账号登录；账号密码以单向哈希保存，不能从数据库或项目文件取回原密码。忘记密码时保留现有数据，按维护流程处理，不要为了进入系统删除数据库。

模型设置提供 OpenAI、Claude、DeepSeek、Qwen 和 Gemini 的适配配置。保存或替换 Key 只保存加密凭据与配置，不会自动测试连接或发送模型请求；协议适配不代表五家模型均已完成真实生成质量验收。

全新工作空间的付费预算默认关闭。标准启动入口在未明确配置 `STUDIO_PROVIDER_NETWORK` 时将外部模型联网设为 `disabled`；用户已明确设置的值会保留。联网开关、模型连接配置和费用授权各自独立：保存 Key 或允许联网都不会自动开启预算，也不会替作者确认生成任务。需要真实调用时，先确认服务商、模型、价格和预算，再明确设置联网与任务授权。

## 启动选项

```powershell
# 只校验四种便携工具；不下载、不启动数据库或应用。
.\Start-Platform.cmd -VerifyOnly

# 使用已有构建启动；仍执行工具完整性与运行时前置检查。
.\Start-Platform.cmd -NoBuild
```

普通启动默认重新检查 Vue 类型并构建前后端。运行中的应用须先正常停止，再安装、重建或重新启动，避免覆盖正在使用的程序。不要从第二个窗口启动另一套数据库来绕过端口或初始化错误。

`Start.cmd` / `Start.ps1` 是当前平台的兼容入口，转交给 `Start-Platform.cmd`，支持 `-NoBuild`、`-VerifyOnly`；历史 `-NoBrowser` 参数仍可使用。当前平台不自动打开浏览器。旧 Python/React 原型源码与历史数据保留，不由这些入口启动。

若直接使用 PowerShell 脚本，应使用已校验的项目 PowerShell 7，并先加载便携工具链；通常直接使用 CMD 入口即可，避免依赖系统 PATH 中的其他 Go、Node 或 Python。

## 正常停止

1. 在运行 `Start-Platform.cmd` 的窗口按 **Ctrl+C**，等待应用退出、终端返回提示符。应用内 worker 随应用退出；先确认其他工作窗口或测试服务没有继续使用这些依赖。
2. 在仓库根目录打开一个 PowerShell 窗口，使用已安装的项目 PowerShell 7，依次停止本地嵌入服务及 MySQL/Temporal：

```powershell
$platformPowerShell = '..\work\runtime\toolchain\powershell-7.6.5\pwsh.exe'
& $platformPowerShell -NoProfile -File .\scripts\embedding-runtime.ps1 -EmbeddingAction stop
& $platformPowerShell -NoProfile -File .\scripts\runtime.ps1 -Action stop
```

停止工具复用进程身份检查，MySQL 使用受控的正常关闭。检查失败时保留错误信息，不要用进程名批量强杀，也不要先停数据库而留下应用和 worker。上述命令要求该安装已经初始化；未启动过的干净克隆不需要执行停止命令。如果手动启动过测试服务器，先使用 `scripts/stop-test-server.ps1` 停止它。

## 验证与排错边界

`-VerifyOnly` 只证明四个工具的文件和版本校验通过，不证明数据库、小说流程或真实模型质量。文件格式、OCR 和识别限制见 [FILE_INGEST.md](FILE_INGEST.md)，便携环境契约见 [PORTABLE_RUNTIME.md](PORTABLE_RUNTIME.md)。

`scripts/verify-platform.ps1` 是维护者的完整集成验收入口，会使用真实本地依赖、构建和测试服务，并对受保护的测试数据库运行清理/恢复场景。它属于维护验证，不适合作为普通启动命令。运行前应阅读脚本和测试环境要求，停止相关应用和 worker，确认测试库与业务数据隔离。

遇到安装或启动失败，保留终端输出和 `.runtime/` 中对应日志；分享前移除账号、凭据和作品隐私。已有工具目录校验失败时应定位损坏的安装，不放宽 hash、权限、日志或数据库保护。安装说明依据脚本契约；新的操作系统、机器或运行目录仍需干净安装与恢复验收，文档和静态检查不能代替实际部署验证。

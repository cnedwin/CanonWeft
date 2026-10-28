# F08 独立 Linux 工具环境

本目录实现 Windows 普通用户可运行的 QEMU TCG + Alpine Linux 一次性工具环境。它提供真实 Python 3.12 和 BusyBox POSIX shell，不要求 Docker、WSL、Hyper-V、管理员安装或付费服务。它不是整个 F08 的验收结论，也不自动启用网上取得的 Skill。

## 安装与运行

从仓库根目录执行：

```powershell
./scripts/install-isolation.ps1
./scripts/verify-isolation.ps1
./scripts/run-isolation.ps1 -Request ../work/approved-request.json
```

安装脚本使用已有 `.venv/Scripts/python.exe`；也可用 `-Python` 指定 Python 3.12。本次实际验证构建器为 Python 3.12.14，其他版本只有重建哈希一致才会被接受。它只把免费运行时保存到 `../work/isolation-runtime`。第一次安装需要访问固定的官方来源；缓存齐全时不联网。下载和缓存均按 `sources.lock.json` 检查 SHA-256，QEMU 同时检查发布方 SHA-512。7-Zip 只解包安装文件，安装文件不会被执行。生成的完整运行时清单必须匹配已审核镜像哈希，否则失败。

请求例子（`input` 为批准输入字节的 Base64）：

```json
{
  "language": "python",
  "script": "import sys; print(sys.stdin.read().upper())",
  "input": "YXBwcm92ZWQgaW5wdXQ=",
  "timeout_ms": 5000,
  "max_output_bytes": 8192
}
```

完整工具包使用互斥的 `files` + `entrypoint` 模式，文件内容 `data` 同样是 Base64；不接受宿主路径或符号链接。例如：

```json
{
  "language": "python",
  "entrypoint": "main.py",
  "files": [
    {"path": "main.py", "data": "aW1wb3J0IGhlbHBlcjsgcHJpbnQoaGVscGVyLlZBTFVFKQ=="},
    {"path": "helper.py", "data": "VkFMVUUgPSAnaGVsbG8n"}
  ],
  "input": "",
  "timeout_ms": 5000,
  "max_output_bytes": 8192
}
```

Go 对应 `Files: []isolation.File{{Path: "main.py", Data: approvedMain}, ...}` 与 `Entrypoint: "main.py"`，`Script` 必须为空。最多 128 个文件，每文件 2 MiB、合计 8 MiB；入口必须存在、非空、UTF-8 且不超过 256 KiB。仅复制显式批准字节，文件没有权限/链接/宿主挂载参数。

包内路径必须是相对 UTF-8 路径，以 `/` 分段；拒绝 `..`、空段、反斜线、绝对路径、控制字符、Windows 保留设备名、尾部空格/点、重复和大小写冲突（包括目录），以及文件/目录相互覆盖。路径最多 1024 字节、32 层，每段 255 字节。CLI 与客体 JSON 解码上限均为 16 MiB，先限额再解码；文件数和解码后的大小另行限制。

包位于 root 所有的 `/input/package`，文件 0444、目录 0555；UID 1000 工具不能改写、创建、删除或替换包内容。包模式工作目录是包根，所以 Python/POSIX 可使用相对资源路径；Python 保留 `-I -B`，只显式加入包根和入口所在目录以支持同包 import。嵌套入口可通过 `__file__` 定位自身资源。可写结果放到 `/work`，通过 stdout/stderr 返回候选。单脚本模式仍在 `/work` 运行。不会自动安装 pip/apt 依赖；未内置且未以源码提供的依赖真实失败，没有宿主回退。

`language` 只接受 `python` 或 `posix`。CLI 只输出 JSON；`stdout` 和 `stderr` 是 Base64 字节，保留非 UTF-8 工具输出。`exit_code`、`reason`、`image_hash`、`input_hash` 由监督器/主机执行器返回。CLI 对脚本失败、超时、输出超限返回非零退出码。结果仍是未经发布的候选数据。

Go 调用入口：

```go
runner, err := isolation.NewRunner(runtimeRoot, isolation.ApprovedImageSHA256)
result, err := runner.Run(ctx, isolation.Request{
    Language: "python",
    Script: approvedScript,
    Input: approvedInput,
    Timeout: 5 * time.Second,
    MaxOutputBytes: 8192,
})
```

调用方必须先校验当前项目/用户权限、工具包与脚本哈希、能力和撤销状态，完成结果后再重新校验。只使用审核过的镜像哈希，不从用户请求或可编辑的运行时旁文件取得“可信哈希”。`image.sha256` 是便于核验的输出，生产 CLI 使用代码固定的 `ApprovedImageSHA256`。

成功完成需要同时满足 `err == nil`、`result.Reason == "completed"`、`result.ExitCode == 0`，再检查业务输出 schema。禁止只看 `err == nil`。执行器没有数据库、正文发布或正式版本修改接口。

## 实际边界

| 项目 | 固定限制 |
|---|---|
| 主机 | 创建前配置 Windows Job Object，通过 CreateProcess 的 Job List 原子加入；单个 QEMU、768 MiB，Job 关闭即终止 |
| 并发 | 同一 Go 服务进程内所有 Runner 合计 1 个 VM；排队可取消 |
| 客体 | 256 MiB 内存、1 vCPU、16 MiB TCG 缓存，纯 TCG，无硬件虚拟化要求 |
| 脚本用户 | UID/GID 1000，无附加组，no_new_privs，不能访问 root 监督器或串口 |
| 脚本资源 | 地址空间 96 MiB、最多 32 进程、64 文件描述符、16 MiB 单文件 |
| 可写工作区 | `/work` tmpfs 共 16 MiB，最多 2048 inode，nosuid/nodev；其余根文件归 root |
| 时间 | 脚本 100 ms—30 s；主机总限额 = 脚本时间 + 30 s 启动/关闭；取消终止整个 VM |
| 输入 | 单脚本/包入口与 stdin 各最多 256 KiB；包最多 128 文件、每个 2 MiB、总 8 MiB；只复制批准字节 |
| 输出 | stdout + stderr 总计 1 byte—1 MiB；超限立即停止，协议与启动诊断也有上限 |
| 网络 | 无虚拟网卡、无 user networking、无端口映射；客体没有默认网络 |
| 持久化 | 不挂接块设备/共享目录/9p/virtiofs；每次创建独立 initramfs，退出后删除输入副本 |

主机只把固定运行时、批准输入副本交给 QEMU。输入由固定名称的 `request.json` 附加到一次性 initramfs，以 root 0600 保存；worker 只取得 `/input/script` 或只读 `/input/package` 和标准输入。串口仅用于可信监督器返回带 Base64 输出的结果。worker 的标准输出通过管道捕获，因此输出伪造 `STUDIO_RESULT:` 不会改写执行状态。

每次启动前验证内核、基础 initramfs、QEMU 可执行文件、DLL、固件的全部清单哈希，并拒绝额外文件和符号链接。运行镜像清单与规范化批准请求各有 SHA-256。包请求按路径排序，InputHash 覆盖全部路径、文件字节、入口、stdin、语言和执行限额；改变任何一项会改变输入哈希，重排文件不会。Job 限制先于子进程创建，Windows `PROC_THREAD_ATTRIBUTE_JOB_LIST` 在 `CreateProcess` 内原子关联，消除“先启动再加入 Job”的孤儿进程窗口。句柄继承白名单仅包含 NUL、stdout、stderr，Job 句柄不被客体进程继承。该 API 不可用时创建失败，不回退为暂停后关联。脚本退出、错误、超时或取消均终止整个客体，因此另建会话的子进程也不会在主机上残留。

没有任意 QEMU 参数、共享目录、联网开关、宿主脚本回退或隔离降级开关。超出上述范围的工具定义必须拒绝执行；registry 中声明更大的内存/超时并不会扩大此执行器权限。

## 验证与可信范围

`verify-isolation.ps1` 不访问业务数据库。它要求真实运行时存在，运行完整独立测试、Go vet 和格式检查；缺失/校验不符即失败。常规 `go test ./...` 未设置 `STUDIO_ISOLATION_TEST_RUNTIME` 时显式标注 real-VM suite skipped，所以不能把该次普通 Go 测试当作隔离验收。第一期总验收必须调用这个独立脚本。

真实测试覆盖 Python/POSIX、256 KiB 输入完整性、宿主凭据环境与文件不可读、控制串口/请求不可读、无网卡和 TCP 连接失败、内存/磁盘限制、输出超限、超时/子进程、取消/清理、协议伪造、跨次文件不保留、进程数限制和 detached 子进程清理。新增真实包测试覆盖 Python helper/资源/只读权限、POSIX 资源、嵌套入口 import、缺依赖失败和 8 MiB 最大包；单元测试覆盖路径/重复/超限拒绝、输入哈希和有界 CLI 解码。独立测试还覆盖运行时缺失、镜像篡改和额外 DLL。创建阶段强杀专项另启一个独立执行器，在 OS 创建子进程刚返回、尚未执行后续管理时阻塞它，再强制终止执行器，验证子进程自动死亡；该测试不依赖 VM 镜像，也不能被脚本内取消测试替代。

可信基础包含宿主 Windows、固定 QEMU 构建、Linux 内核、监督器与官方包。测试证明这些场景的实际隔离行为，不证明不存在未知的 QEMU/内核漏洞，也不保护已被同一宿主账号攻陷的运行时或主机。主机进程异常退出时 Job Object 杀死客体；已经写出的批准输入临时副本在主机崩溃情况下可能需要清理，未声明安全擦除或抗整盘损坏。

运行时来源是 [QEMU 官方下载页](https://www.qemu.org/download/#windows) 指向的 [Stefan Weil Windows 构建](https://qemu.weilnetz.de/w64/2026/)，锁定 2026-08-11 构建（实际版本串 `11.1.0 (v11.1.0-12130-ge470268ff4)`）。发布方提示其 Windows 构建为实验性、使用过期签名证书；本项目采用 HTTPS 来源与发布方 SHA-512 校验，不宣称具有有效代码签名认证。Alpine 内核与软件包来自 [Alpine 官方 CDN](https://dl-cdn.alpinelinux.org/alpine/v3.22/)，其 SHA-256 为首次获取后审核固定；未额外验证 APK/内核的 detached 签名。7-Zip 来自 [官方 GitHub release](https://github.com/ip7z/7zip/releases/tag/26.04)。所有锁固定完整 URL 和哈希；源变化需重新审核、重建并重新验证，运行时不会自动升级。

机制参考：[QEMU 参数文档](https://www.qemu.org/docs/master/system/invocation.html)、[Linux initramfs](https://www.kernel.org/doc/Documentation/filesystems/ramfs-rootfs-initramfs.txt)、[Microsoft 创建时 Job List / Handle List](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。

当前完整批准镜像 SHA-256：`4215bb05ca9d8c1c86ca109caf39889f47f7d798ec8382d4916cbbc3f637ae7d`。来源包版本与资源限额不变；本次镜像变化为客体只读包准备和执行入口。

# 本地 PDF 与图片文字提取（F05）

文档核对：2026-10-28。本文定义专项契约与验证边界；当前工程实现、安装和资格状态见 [实施状态](IMPLEMENTATION_STATUS.md)。

范围：TXT/Markdown/DOCX 保留现有解析；PDF 原生文字与无文字页面 OCR、PNG/JPEG/WebP OCR 使用真实本地解析器。原始上传字节、来源 hash、逐页报告和用户编辑稿分别保存；解析成功不发布正史，也不跳过候选采用、版本或权限检查。本文覆盖摄取专项；解析器检查不代替完整 F05 或公共底座验收。

## 安装和启动契约

Windows x64 / Python 3.12，全部免费且 CPU 运行。先按项目安装说明安装固定便携工具链（`scripts/install-portable-tools.ps1`），再在仓库根目录执行：

```powershell
./scripts/install-ingest.ps1
./scripts/ingest-runtime.ps1 status
. ./scripts/ingest-runtime.ps1 env
./scripts/verify-ingest.ps1
```

安装器通过 `portable-tools.ps1` 校验项目固定的便携 Python，并创建独立环境 `../work/ingest-venv`，使用 `tools/ingest/requirements.lock` 固定全部 Windows wheel 版本与 SHA256，缓存放在 `../work/ingest-wheels`。安装只访问官方 PyPI 软件包源，不处理附件。运行时不会安装依赖、下载模型或访问外部服务。所有解释器入口显式使用 `-B`，禁止隐式重写已固定的 Python 字节码缓存（`-I` 会忽略同名环境变量，不能只靠环境设置）。已安装环境重复执行仍逐项校验版本、安装文件 RECORD hash 及三个 OCR 模型 hash；校验不通过会失败，不会静默忽略。

`ingest-runtime.ps1 env` 仅设置 `STUDIO_INGEST_PYTHON`、`STUDIO_INGEST_SCRIPT` 和默认 `STUDIO_INGEST_OCR=enabled`，不启动常驻服务。`status` 运行离线完整校验，失败抛出错误、非零退出；成功输出已校验文件数量，不表示接口或数据库已验收。Go 在没有显式路径时会向上定位仓库脚本与默认独立环境；缺失或错误配置返回可读错误，不能登记为空成功。

`STUDIO_INGEST_OCR=disabled` 是明确的文字层模式：PDF 仍逐页提取原生文字；无文字页按 `page:N` 记录 OCR 已关闭。图片按 `image:1` 登记待识别。OCR 模型缺失或 hash 不匹配也只登记对应页为待处理。整个 PDF 解析环境不可用时，页数无法可靠确定，因此拒绝导入并提示安装；不会伪造页数。

## 依赖决定及官方依据

| 依赖 | 锁定版本 | 用途与许可证 |
| --- | --- | --- |
| pypdfium2 | 5.14.0 | Windows wheel 内置 PDFium；原生文字、页数与渲染。pypdfium2 Apache-2.0/BSD-3-Clause 双许可，PDFium BSD 及其第三方 notices 随 wheel 保留 |
| rapidocr-onnxruntime | 1.4.4 | 包内自带 PP-OCRv4 中英文检测/识别与方向模型，Apache-2.0 |
| onnxruntime | 1.23.2 | CPU 推理，MIT；不启用 CUDA、DirectML 或付费 API |
| Pillow | 12.1.0 | PNG/JPEG/WebP 解码、EXIF 方向处理、透明图在白底上合成，MIT-CMU |
| NumPy / OpenCV | 2.2.6 / 4.12.0.88 | OCR 数组及图像处理；其余传递依赖也全部固定 |

完整来源 URL、wheel hash、模型 hash、版本与上游许可证元数据见 `deploy/locks/ingest.json`。选择 RapidOCR 1.4.4 是为了直接使用经校验的软件包内模型，避免运行时隐式下载；不宣称其为最新或通用最佳 OCR。

官方资料：[pypdfium2 安装与许可证](https://pypi.org/project/pypdfium2/5.14.0/)、[PDFium 页面/文字 API 与进程约束](https://pypdfium2.readthedocs.io/en/stable/python_api.html)、[RapidOCR 1.4.4 发布与 Apache-2.0](https://pypi.org/project/rapidocr-onnxruntime/1.4.4/)、[RapidOCR 源码](https://github.com/RapidAI/RapidOCR/tree/v1.4.4)、[PaddleOCR 模型上游](https://github.com/PaddlePaddle/PaddleOCR)、[ONNX Runtime CPU](https://onnxruntime.ai/docs/execution-providers/CPU-ExecutionProvider.html)。

PDFium 不支持并发线程调用，所以每页由独立子进程顺序处理。使用普通 wheel（无 V8/XFA 构建），不调用 PDF JavaScript、动作、表单执行或外链打开接口。

## 覆盖和质量语义

- PDF 一页一个 `page:N` 单元，先取原生文字；该页无文字时才渲染为 144 DPI 图像并进行 OCR。图片为 `image:1`。不会以空字符串标为已提取。
- `kind=pdf_text` 和 `kind=ocr` 分开；OCR 保存最低行置信度。低于 0.80 的页保留候选 OCR 文本但记为 `pending`，不自动拼入正文。界面显示候选内容，用户可核对原图后手工编辑。
- PNG/WebP 的 RGBA、LA、调色板透明度在 EXIF 方向校正后合成到白底，再送入 OCR；完全透明像素下的隐藏 RGB 不作为可见文字。该解码与实际 1280 预处理修订记录为解析器 `pdfium-5.14.0+rapidocr-1.4.4-cpu-v2`，旧报告保留原有版本号。
- 高置信 OCR 也只是文字候选，不能证明文字正确或其中陈述为事实。`complete` 仅表示所有已登记文字单元完成提取，不代表图画、图表、布局、公式或整页语义已经理解。
- 无可识别文字的图片保留为视觉参考并记为待处理，不生成臆测描述。PDF 有原生文字的页不另做整页视觉识别；DOCX 内嵌图像保留原有待核对记录，可单独导入图像。
- 若提取失败、超时或文本达到上限，剩余页全部保留锚点与原因；不静默截断最后一个单元，也不声称已覆盖遗漏页。部分报告必须明确确认，才能把当前编辑正文形成候选。
- 加密 PDF（包括空用户密码）、损坏文件、超页数、超图片尺寸、动画图片明确拒绝。错误对外使用固定中文映射，不透传原始库错误、路径或附件内容。

## 资源和运行边界

| 限制 | 数值与行为 |
| --- | --- |
| 输入 | 32 MiB，PDF 最多 100 页 |
| 图像 | 每图/渲染页最多 2500 万像素，边长最多 12000；不以悄悄缩小巨大页面绕过限制 |
| OCR 检测 | 检测图长边最多 1280；在全局预处理将输入图长边约束到 1280 后再检测与裁剪识别，识别裁剪来自缩放后的图；不声称保留原图分辨率，识别质量需要逐稿核对 |
| 输出 | 合计 256 KiB UTF-8 候选文本；Go 协议读取另有 2 MiB 硬上限 |
| 时间 | 单页墙钟 30 秒、单文件 40 秒；Go 外层 45 秒兜底，等待解析名额最多 2 秒且支持取消，给 HTTP 60 秒响应窗口保留存储余量 |
| 内存/CPU | 页解析 Job 1 GiB 提交内存、每进程 30 秒 CPU；外层整个解析树 1.5 GiB，最多 6 个进程；ONNX/BLAS 最多 2 个计算线程 |
| 并发 | Go 进程一次只执行一个文件解析，防止多份扫描件同时耗尽主机资源 |

Go 使用 Windows 10+ `PROC_THREAD_ATTRIBUTE_JOB_LIST` 在创建进程时原子加入 kill-on-close Job，覆盖 venv 启动器、解释器和页面子进程；不是创建后才分配 Job。取消会终止整棵解析进程树。页面子进程在运行解析前加入内层 Job，超时终止该 Job 后继续登记其他页。

临时附件放在 `.runtime/work/ingest/document-*`，完成或取消后清理。解析子进程仅获得必需 Windows 临时目录/系统目录和线程设置，不继承数据库、模型供应商密钥或代理配置。附件只通过本地文件路径传入，OCR 接收解码图像而非 URL，Python 网络连接/解析入口有拒绝审计钩子。

这是可信固定解析器的资源隔离，不是任意恶意代码的通用安全沙箱；Python 审计钩子也不是原生代码网络防火墙。没有开放附件链接、执行文档脚本或调用外部识别服务的产品路径。

## 验证与样本

`tools/ingest/tests/fixtures/` 是自写中文/英文样本：两页文字 PDF、两页扫描 PDF、混合 PDF、PNG/JPEG/WebP、空白图、超页数/超像素、带密码与空密码加密 PDF。图像使用本机系统字体生成，不分发字体文件，无外部版权语料。`make_fixtures.py` 可重建一般样本；加密样本由 bundled Python 的 pypdf 将同一自写 PDF 加密生成。

`verify-ingest.ps1` 必需执行完整安装文件校验、真实 Python 解析/OCR/资源测试和 Go 文档适配器测试/vet；它会设置 `STUDIO_TEST_INGEST=1`，因此不会把 Go 默认跳过本机运行时的结果算作通过。该脚本不使用共享数据库。

实际验证另包含真实 MySQL/HTTP 导入集成：损坏 PDF 的 400 中文错误、关闭 OCR 后逐页覆盖、部分内容确认门、原始 PDF hash、编辑候选和采用发布。数据库测试必须取得共享测试库使用权，不能与其他测试并发重置 schema。

这些样本证明本机路径工作，不是对所有扫描质量、特殊字体、排版顺序、长篇语料或 OCR 准确率的资格认证。完整 F05 的私有知识索引与检索质量另行验收。

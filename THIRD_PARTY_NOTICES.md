# 第三方与许可说明

CanonWeft（砚台）的主代码采用仓库根目录的 [Apache License 2.0](LICENSE)。第三方材料继续适用各自许可证及通知；项目许可证不会将整个依赖、字体、模型或外部 Skill 快照重新许可。根目录 [NOTICE](NOTICE) 提供随仓库保留的部分归属索引。

## 第三方材料分类

| 类别 | 来源与版本依据 | 处理方式 |
|---|---|---|
| Go依赖 | `backend/go.mod`、`backend/go.sum` | 保留各模块原始许可证与适用通知 |
| 前端依赖 | `web/package.json`、`web/pnpm-lock.yaml` | 按锁定包的许可证处理 |
| PDF 字体 | [霞鹜文楷来源与版本](backend/internal/novel/fonts/SOURCE.md) | SIL Open Font License 1.1；保留[完整 OFL 及版权通知](backend/internal/novel/fonts/OFL.txt)，不改为 Apache 许可 |
| PDF 导出库 | [PDF 第三方许可索引](backend/third_party/pdf/README.md)、`backend/go.mod`、`backend/go.sum` | gopdf、gofpdi 为 MIT，pkg/errors 为 BSD-2-Clause；分发时保留对应许可文件及通知 |
| 数据库与任务运行时 | `deploy/locks/runtime.json`及相关锁文件 | 独立发行物，保留其许可、签名及来源；不改为项目许可证 |
| Python、嵌入、OCR与模型权重 | `deploy/locks/`、`tools/embedding/requirements.lock`、`tools/ingest/requirements.lock` | 校验固定包/权重，保留各自许可与使用条件 |
| 外部Skill候选 | `backend/presets/external/`中的版本化快照与来源清单 | LICENSE、NOTICE、来源提交及逐文件哈希不可变；候选不自动采用或执行 |
| 官方API和外部资料 | 对应功能文档中的来源引用 | API账户、内容和服务使用条件独立于项目代码许可 |

## 分发边界

公开仓库仅收录具备相应分发条件的源码、配置、文档与候选资料。运行环境、凭据、模型下载缓存、私人作品、数据库、备份、临时评测响应和本机执行记录不进入源码分发。

外部 Skill 的上游 `LICENSE`、`NOTICE`、来源提交与逐文件哈希继续保留在对应版本快照内；项目采用 Apache License 2.0 不改变其内容、许可条件或候选资格。字体与 PDF 库的许可也不要求作者创作的小说正文采用开源许可。

本说明是材料索引，不替代完整依赖许可证清单或发布时的分发审查。第三方文件不能仅因被项目引用或同步，就视为获得修改、转授权、商业使用或执行许可。

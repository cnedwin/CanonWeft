# CanonWeft · 砚台

[中文](README.md) · [English](README.en.md) · [GitHub](https://github.com/cnedwin/CanonWeft)

CanonWeft（砚台）是本地优先的故事、角色与共享世界创作工作室，围绕共享 IP 设定、版本与分支、知识检索、任务编排和创作记忆组织小说工作流。当前主要创作入口为小说；漫剧、游戏及跨媒介制作属于后续开发范围。

## 项目状态

项目处于开发阶段。当前发行与安装脚本面向 **Windows x86-64 本机运行**；Linux、Docker 和公网域名部署尚未完成适配。已提供的创作流程与真实模型、长篇文学质量的验证状态分别记录，详见[实现状态](docs/IMPLEMENTATION_STATUS.md)。

- 八套题材预设、一句话开书与一页可编辑方案确认。
- 世界与故事规划 → 全书/分卷章节安排 → 本章细纲 → 正文候选 → 记账与独立审稿 → 明确采用。
- 可编辑角色卡、关系与版本化共享设定；角色变更影响分析和局部修订。
- 阶段写作台、本章写作卡、按需上下文、选区/整章精修及最多十章批量候选。
- MySQL 权威持久化、本地 BGE-M3 混合检索、分层记忆与来源追踪。
- 方法与能力版本、外部 Skill 候选同步、任务暂停/恢复、预算/对账、备份及隔离恢复。

预设与测试替身不代表真实模型生成效果。平台审核结果、文学品质、连续长篇一致性和指定图片模型的实际效果仍需独立验证。

## 本地安装

从仓库根目录运行：

```powershell
.\Install-Platform.cmd
.\Start-Platform.cmd
```

浏览器访问 **http://127.0.0.1:18765/**。首次运行创建工作室与个人账号；没有预置公共账号或默认密码。安装会下载并校验固定版本依赖，不会购买云资源或调用文本/图片模型。

安装条件、依赖下载、停止服务、已有数据保护和平台支持范围见[本地安装说明](docs/LOCAL_INSTALL.md)。

## 模型与费用

支持 OpenAI、Anthropic、DeepSeek、Qwen 和 Gemini 的现有协议适配。模型 Key 由工作室所有者在本机后台填写，保存 Key 不发起生成。文本试写可先配置一个供应商，并为所需用途绑定该配置。

模型调用需要有效配置、价格版本、明确启用的预算以及服务端联网设置。默认启动保持供应商联网关闭；配置与预算保存不会充值。请求结果未知时保留预算占用，不自动重复发送。

普通内置规划和首章候选试写不要求先完成完整评测套件；使用已认证能力及正式独立审稿发布时，仍校验相应用途和版本资格。详见[模型配置](docs/MODELS_AND_NOVEL_ENTRY.md)与[能力评测](docs/CAPABILITY_QUALIFICATION.md)。

## 技术栈

| 组件 | 当前基线 |
|---|---|
| 前端 | Vue 3.5.44、TypeScript、Vite |
| 应用服务 | Go 1.27.3、Echo v5、database/sql |
| 数据库 | MySQL 9.7.4 Community |
| 持久任务 | 自托管 Temporal |
| 资料处理 | Python 3.12.15、ONNX CPU 嵌入与 OCR |
| 数据与文件 | MySQL、不可变本地对象、受保护撤销记录 |

实际包版本、来源、哈希和适用平台以 `deploy/locks/`、`backend/go.mod` 与 `web/pnpm-lock.yaml` 为准。Python 是检索及资料处理工具，不是另一套业务后端。旧原型代码与数据仅作历史兼容参考。

## 文档与开发

- [文档索引](docs/README.md)：按产品、实现、架构和运维分类。
- [产品设计](docs/FINAL_PLAN.md)、[需求追踪](docs/REQUIREMENTS_TRACE.md)、[路线图](docs/DELIVERY_ROADMAP.md)。
- [架构](docs/ARCHITECTURE.md)、[统一记忆](docs/UNIFIED_MEMORY_ARCHITECTURE.md)、[编码规范](docs/CODING_STANDARDS.md)。
- [贡献指南](CONTRIBUTING.md)、[安全说明](SECURITY.md)、[第三方与许可说明](THIRD_PARTY_NOTICES.md)。

## 许可

项目主代码采用 [Apache License 2.0](LICENSE)。第三方字体、PDF 库、依赖、模型与候选 Skill 保留各自许可证及通知，详见[第三方与许可说明](THIRD_PARTY_NOTICES.md)和[归属索引](NOTICE)。项目许可证不改变这些材料的许可，也不要求作者创作的小说正文采用开源许可。

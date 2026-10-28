# F04 五家原生流式传输

文档核对：2026-10-28。五家原生流式传输已接入生成任务、持久预览、读取 API 和 Vue 页面。所列验证来自本机 HTTP 流夹具、真实 MySQL 与浏览器，不能替代真实供应商请求、账户资格或模型质量验收；具体状态见 [实施状态](IMPLEMENTATION_STATUS.md)。

## 任务与页面

新请求支持 `stream:true`，Vue 新准备的文本任务默认启用；旧任务未带该字段时仍走旧解析与计费契约，不改变历史幂等指纹。统一调度的单次发送约束不变。

`generation_previews` 保存至多 4 MiB 的 UTF-8 私人工作文字、递增序号和摘要。运行中约每 250 ms 检查当前权限、设定、证据包、能力撤销、凭据和预算，并在内容变化时更新；结束时再保存尾部。刷新或重建服务实例能读取已入库部分；异常进程退出仍可能损失最后不足一个保存周期的文字。暂存不等于供应商已完整响应。

只有原作者或工作室所有者可查看；读取时再次检查项目、当前设定、上下文与能力状态。页面标明“尚未通过完整校验，不能采用或发布”，正式输出只来自通过终态、用量和 schema 校验的完整结果。任务取消仍可查看合法的已暂存片段，但不会生成可采用输出。版本或权限过期会清除当前显示。

监视器同时覆盖供应商暂时不发送任何分块的情况，取消或撤销会停止连接。监视器正常结束不被当作生成失败。发送后缺少完整费用回执保留 UNKNOWN 与预算占用，不自动重发；完整费用回执与是否可采用内容分开处理。运行中只能取消，暂停沿用原有仅在准备/排队阶段可操作的规则。

私人流式预览由数据库版本 26 引入；该历史兼容约束不表示当前运行结构仍为 26。备份保留私人暂存数据，但禁用恢复身份与任务执行；版本 24/25 档案分别经固定字段升级，禁止冒充旧档案注入新表。预览摘要、UTF-8、大小和序号在恢复前检查。物理清理与保留期执行由 F08 现行流程承接，状态见 [实施状态](IMPLEMENTATION_STATUS.md)。

专项证据：`TestGenerationStream*` 连接真正的本地分块 HTTP 服务与 MySQL，验证完成前持久读取、独立服务实例恢复、完整/缺失回执、一次发送、取消、停用配置、正史变更、账号禁用及私人隔离。浏览器 `generation-stream.spec.ts` 使用明确标注的 UI 响应夹具验证刷新、迟到响应和过期清除；不能把该 UI 夹具当成供应商验收。实际流式传输由前述服务测试单独证明。

## 调用契约

```go
type Delta struct { Text string `json:"text"` }
func (*Adapter) GenerateStream(
    ctx context.Context, config domain.ModelConfig, key []byte,
    input Request, authorize func(context.Context) error,
    onDelta func(Delta) error,
) (Result, error)
```

`authorize` 必须先持久预留预算并标记已发送；nil 默认拒绝。只有授权成功才进行 HTTP。`onDelta` 同步收到可见文本，nil 表示仅收集。每次回调最多 16 KiB，保持完整 UTF-8；输入和 schema 不被修改。回调应及时返回，并在持久化时使用调用方 context；同步回调中的任意阻塞代码无法由适配器强制终止。

增量始终是临时候选；不可依增量发布正式内容或结算。调用方应按执行代号检查任务是否仍有效，并拒绝取消后的迟到回调。适配器只解析协议，不负责数据库或发布权限。

`Result` 额外包含 `UsageKnown bool`（JSON `usage_known,omitempty`）：

| 返回 | 用量与调用方处理 |
|---|---|
| nil error | 完整终态、最终必需计数、回执 ID、干净 EOF 和 schema 全部通过；UsageKnown=true |
| ErrInvalidOutput | 明确拒绝、截断、空输出或 schema 不符；完整用量保留，UsageKnown=true；不得采用正文 |
| ErrUnknown | 断线、取消、回调错误、协议错误、缺终态/用量、越界、超时等；UsageKnown=false；可能返回部分计数供诊断，但不得当成完整账单，也不得自动重发 |
| ErrRejected | 供应商非成功 HTTP（408/5xx 除外）；不读取或转述其错误正文；无自动重试 |
| ErrPaidClosed / 本地校验或授权失败 | 请求未发出 |

旧 `Generate` 保持原有行为与结算契约，未为旧解析器补充 UsageKnown；调用方不能因为旧路径该字段是 false 就改变旧路径语义。只有流式调用依赖新旗标。

## 协议与用量

| 供应商 | 请求 / 可见增量 | 终态和用量要求 |
|---|---|---|
| OpenAI | `/responses`, `stream:true`, `store:false`；仅 `response.output_text.delta` | `response.created` 后 `response.completed`（或明确 incomplete/failed）；终态 output_text 必须与增量一致；input_tokens/output_tokens 必须明确；缓存取 input_tokens_details.cached_tokens |
| Anthropic | `/messages`, `stream:true`；x-api-key、anthropic-version、可选 workspace；仅 text block 的 text_delta | message_start → block start/delta/stop → message_delta → message_stop；初始 input/output 与后续累计 output；后续输入及缓存字段若提供则替换累计值；总输入=普通输入+cache_read+cache_creation，另保留缓存读/写分类 |
| DeepSeek | `/chat/completions`, `stream:true`, `stream_options.include_usage:true`；仅 choices[0].delta.content | 单 choice finish_reason + 最终 usage + `[DONE]`；接受当前官方最后 finish chunk 自带 usage，也兼容 usage-only chunk；prompt/completion、cache hit/miss、total 必须明确；hit+miss=prompt，prompt+completion=total，若有缓存 details 必须与 hit 一致 |
| Qwen | 已校验地域的 `/compatible-mode/v1/chat/completions`，同上 | finish_reason 后 usage-only chunk，再 `[DONE]`；prompt/completion 必须明确；缓存取 prompt_tokens_details.cached_tokens |
| Gemini | `/models/{model}:streamGenerateContent?alt=sse`，密钥仅 x-goog-api-key header；仅 thought=false 的 text parts | 单 candidate finishReason 或明确 promptFeedback blockReason；最终 usageMetadata 输入/候选输出必须明确；总输出=candidatesTokenCount+thoughtsTokenCount，总输入=promptTokenCount+toolUsePromptTokenCount；缓存取 cachedContentTokenCount；无 DONE，干净 HTTP EOF 必需 |

各协议只把正常 stop/end_turn/STOP/completed 当可采用输出。思考、签名、工具参数、拒绝说明、错误正文和未知事件字段不传给回调。工具调用不作为正文成功。有效未知扩展事件可忽略；错误事件、冲突回执、终态后的数据与非法 JSON 失败关闭。

所有必需计数用可空指针解析，区分明确零与缺失；负数、非整数、溢出和超过本地计数合理性上限（每字段 10^12）拒绝。协议可选缓存/思考/工具计数缺省表示未报告该可选类别，按零类别处理；若缓存 details 对象存在，cached_tokens 必须明确。DeepSeek 的 cache hit/miss 与 total 是必需项，缺失不得当零；明确 hit=0 且 miss=prompt 才表示无缓存命中。缓存读量不得超过对应输入。Gemini 中途部分 usage 不构成最终账单；最终必需字段缺失仍为未知。Anthropic output、input、cache_read、cache_creation 每个累计分类均不允许比已接受计数倒退；缺省后续字段保留上一已报告值，全部校验通过后才替换。

## 有界与安全约束

沿用域层 HTTPS endpoint/地域白名单、输入/schema 限制和 180 秒 HTTP 总超时。`STUDIO_PROVIDER_NETWORK=disabled` 阻止默认网络适配器在授权前发起外部请求；显式注入的传输仅用于受信本地夹具。HTTP 只尝试一次，不跟随 redirect，移除 GetBody 重放能力，不使用带重试的供应商 SDK。自定义传输必须同样遵守无重试约束。

必须返回 `text/event-stream`。SSE 支持 CRLF/LF、注释、多 data 行、跨 HTTP 分块 UTF-8；整个流含注释与未知事件最多 4 MiB，单事件/行最多 256 KiB，文本累计也受 4 MiB 限制。未闭合事件不由 EOF 补成完整事件。为拒绝终态后的冲突数据，终态后仍需干净 EOF；连接保持超过超时会保留未知费用状态。密钥不放 URL、日志或错误文本，回调和传输的原始错误文本不外传。

## 官方依据与实施假设

- [OpenAI streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)：语义事件、output_text.delta、completed/incomplete/failed 与最终 response usage。
- [Claude streaming messages](https://platform.claude.com/docs/en/build-with-claude/streaming)：block 生命周期、message_delta 用量累计与 message_stop。
- [Claude API overview](https://platform.claude.com/docs/en/api/overview)：当前官方同时支持 API key 的 Authorization Bearer 与 x-api-key。旧非流式 Bearer 路径与流式 x-api-key 路径分别遵循对应鉴权契约。
- [DeepSeek Chat Completions](https://api-docs.deepseek.com/api/create-chat-completion/)：当前文档明确最后 finish chunk 携 usage，随后 DONE。未依赖其一定发送独立 usage-only chunk。
- [Qwen streaming output](https://www.alibabacloud.com/help/en/model-studio/stream)：选 OpenAI 兼容协议，include_usage 返回独立 usage chunk；不混用 DashScope 原生非增量格式。
- [Gemini GenerateContent / streamGenerateContent](https://ai.google.dev/api/generate-content)：SSE endpoint、candidate finishReason、promptFeedback 与 usageMetadata 的候选/思考/缓存/工具计数。

本实现使用已配置模型，不修改模型目录或推断账户权限。严格完整性检查可能把供应商非标准、缺字段或未来协议变化降为未知，需带证据人工对账，不能自动降级或重发。官方文档和本地夹具不等于真实账户连通验收。

## 本地验证

`go test ./tests/models -count=1` 包含五家真实 `httptest.Server` 分块 HTTP：服务器等待首 delta 回调后才发送最终块，逐字节切开 Unicode；覆盖多行 SSE、授权与 kill switch、nil 回调、用量缺失/负数、schema/截断/拒绝、迟到 usage、累计用量、取消、回调失败、超限、未知事件、错误事件、冲突回执和一次请求/不跟随重定向。没有外部密钥或用户内容发往供应商。

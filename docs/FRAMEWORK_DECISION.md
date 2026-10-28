# Go 后端框架选型：Echo v5

维护日期：2026-10-28。后端采用 Echo v5，`backend/go.mod`锁定`github.com/labstack/echo/v5 v5.4.2`。本决定替换早期直接使用标准库net/http的路线；Vue、MySQL、模块化单体和小说优先次序不变。

状态：Echo已用于实际Go服务；具体功能和验证层次见[实施状态](IMPLEMENTATION_STATUS.md)。依赖更新须检查安全公告并同步go.mod/go.sum，不在正常启动时自动升级依赖。

## 为什么选它

Echo基于net/http，提供路由、中间件、输入绑定和集中错误处理。本项目选择v5.4.2作为固定依赖；版本发布时间及后续安全信息以[官方仓库](https://github.com/labstack/echo)和[发布记录](https://github.com/labstack/echo/releases)为准。

选型依据是net/http兼容性、常用HTTP能力、依赖维护成本和模块边界。以下是针对本项目的工程取舍，不是公共性能排名。

| 方案 | 有吸引力之处 | 本项目判断 |
|---|---|---|
| **Echo v5** | 基于 net/http，提供路由、绑定、中间件和集中错误处理；官方有流式响应示例 | **采用**。适合单人维护的 API 与本地 Vue 工作台，保持标准库测试和网络能力 |
| Fiber v3 | Express 风格，功能丰富，基于 fasthttp | 可用，但与标准 net/http 的不同约定和适配对本项目没有足够收益；不因 HTTP 基准性能增加理解成本 |
| Hertz | CloudWeGo的HTTP框架，支持Windows | 可用，其微服务和网络性能侧重点不是本项目当前主要需求 |
| 直接 net/http | 依赖少、控制直接 | 可行；本项目采用Echo集中处理常用HTTP工作，保持模块化单体 |

比较依据：[Echo 官方介绍](https://github.com/labstack/echo)、[Fiber v3 官方文档](https://docs.gofiber.io/next/)、[Hertz 官方介绍](https://github.com/cloudwego/hertz)。不以跨机器跑分推断本项目速度。预计章节生成主要耗时来自模型调用，需实际测量，不能声称换框架就能加快模型写作。

## 业务如何落地

```text
Vue 3 工作台
  → Echo v5：路由、输入、会话、错误和响应
    → Go 应用服务：小说 / IP / 版本 / 上下文
      → MySQL：权威记录、事务、费用、任务
      → 不可变正文文件
      → Temporal Go 工作流 / Activity Worker → 专业角色 Skill → 模型适配器
```

- Echo 只在 `backend/internal/httpapi` 和启动组装中出现。domain、service、storage、worker 不接收 Echo Context，以普通类型和 context.Context 协作。
- 迁移时保留原型24个API操作的业务语义；后续扩展以实际路由为准。文档路径参数`{pid}`对应Echo注册路径的`:pid`。当前错误契约见[编码规范](CODING_STANDARDS.md)，接口升级须同步UI和测试。
- 正史确认、费用预留、幂等和恢复继续由 Go 服务与 MySQL 事务负责，框架不代替领域关卡，也不充当 Agent 调度器。
- 保留任务轮询；流式生成使用独立订阅和持久任务状态。SSE实现可参考[Echo示例](https://echo.labstack.com/cookbook/sse/)，功能状态以[实施状态](IMPLEMENTATION_STATUS.md)为准。
- SSE 启用时保持会话令牌校验，可用 fetch 流携带现有头部；不把令牌放查询参数。连接断开只结束订阅，后台任务仍由持久状态管理。重连读取已落盘阶段，不能重发收费生成请求；暂态片段不冒充已保存正文。
- Vue 构建产物由同一个 Go 服务提供；API 未匹配要返回 API 错误，不能被 SPA 路由回退吞成 HTML 成功响应。

## 框架编码规则

1. 使用 v5 包路径和 API，按 v5 文档实现；禁止复制 v4 中间件后靠删类型检查强行适配。外部中间件逐项检查 v5 支持，不批量安装全家桶。
2. Handler 只做边界转换。JSON 使用严格解码、大小限制和业务验证；请求 DTO 与数据库实体分开，避免绑定把客户端字段写进权威状态。
3. 不把 `*echo.Context` 保存到任务或传给后台 goroutine。入队前复制经过校验的输入并落盘，Worker 自有生命周期；框架请求对象的用法以 [v5 Context 文档](https://echo.labstack.com/guide/context/) 为准。
4. 配置 Request ID、脱敏日志、Recover、Body Limit 和统一错误映射；保留本机 Host/Origin/令牌保护。关闭请求正文与密钥日志，不默认启用宽泛 CORS 或信任代理头。
5. 普通请求设置合理超时。若启用 SSE，单独设计长连接与写超时策略、心跳和断线清理；不能把短请求超时原样套用到流，也不能全局无限等待。
6. HTTP handler 中禁止等待完整收费章节生成；创建任务后返回既有任务契约，由 Worker 执行。服务器退出先停止接单、保存阶段，再结束 HTTP 与数据库连接。
7. 依赖框架的代码用真实 Echo 路由栈和 httptest 验证；不能只绕过中间件测试 service。MySQL 测试保持真实数据库，模型仅替换外部传输。

## 验收与维护边界

迁移必须验证正常 API、输入过大/无效、越权/跨站、旧版本确认、取消及未知费用、正文下载和备份上传；验证 SPA 回退不会吞 API 错误。SSE 若实现，另验断线重连不重发模型、无授权不能订阅、缓慢客户端不阻塞 Worker。

工具链按[社区兼容基线](COMMUNITY_STACK_BASELINE.md)锁定Go1.27.3。使用go.mod/go.sum固定依赖，版本调整时检查漏洞并运行相关回归；依赖更新以安全修复和实际需要为准。

完整交付遵循[五期方案](DELIVERY_ROADMAP.md)，旧原型迁移只是其中的子任务。当前支持范围是原生Windows本机loopback；采用Echo不意味着Linux、Docker或公网部署已经就绪。真实模型调用由部署者明确启用，并受凭据、价格卡和预算限制。

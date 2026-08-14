# new-api 官方 v1.0.0-rc.24 合并改动报告

**报告日期：** 2026-08-14<br>
**目标分支：** `main`<br>
**合并方式：** 非快进合并（`--no-ff`），不推送远程

## 1. 执行结论

已将 QuantumNous/new-api 官方标签 `v1.0.0-rc.24` 的功能合并到当前 `main`，56 个冲突文件已全部解决。决策原则为：默认采用官方实现；对会破坏现有自定义路由、计费、流式转换和超时策略的部分进行组合式解决。

未发现无法共存或必须牺牲一方的功能性冲突，也未发现自定义功能被删除的情况。所有主要后端、RelayKit 和前端验证均已通过；仅保留全仓库前端 lint 的历史/官方存量告警，详见第 8 节。

## 2. Git 基线

| 项目 | 值 |
|---|---|
| 合并前 `main` | `da270b8bc423071163cc337447559d5c765e0747` |
| 官方标签 | `v1.0.0-rc.24` |
| 标签对象 | `21ee1f565b47663b2d9f791c0ddf593b096ebfe2` |
| 标签指向的官方提交 | `5c3abffe8572aa8a49f15c3916707d2019d66af4` |
| 共同基线 | `1721144221ec5c94dd87891a7ae1bee228e7bb63` |
| 官方分支独有提交 | 57 |
| 当前分支独有提交 | 70 |
| 源码合并规模（报告文件除外） | 639 个文件，32,519 行新增，12,604 行删除 |

`v1.0.0-rc.24` 是附注标签，因此合并过程中 `MERGE_HEAD` 记录标签对象，最终合并提交的第二父提交应为其剖离后的官方提交 `5c3abffe...`。

## 3. 已接入的官方功能

### 3.1 RelayKit 模块化

- 接入官方独立 `relaykit` Go 模块，将 DTO、协议转换、原因映射和公共类型从根模块抽离。
- 接入 OpenAI Chat、OpenAI Responses、Claude Messages 和 Gemini 之间的请求、响应及流式转换框架。
- 接入转换元数据、安全设置、终止事件和 golden snapshot 测试。

### 3.2 自动分组与渠道调度

- 接入 API Key 请求级 Auto 分组、分组顺序编辑、后端上下文传递和分组倍率资格校验。
- 接入前端 Auto 分组组合框、可视化标记与表单验证。
- 接入渠道内联接数、每 Key 负载等新的调度参数。

### 3.3 HTTP 传输与请求可靠性

- 接入每渠道 HTTP 传输控制和 HTTP/2 分片 transport。
- 为可重放请求设置 `Request.GetBody`，使 HTTP/2 上游流重置后可透明重试。
- 接入 zstd 请求解压、Bedrock 客户端断开取消以及请求 body 重放元数据。

### 3.4 渠道、协议与计费

- 接入 New API、Sub2API、Tencent TokenHub 等渠道支持。
- 接入 DeepSeek Responses API、Advanced Custom Responses Compact 和新 Gemini 图像模型。
- 接入工具调用实际计费、可配置工具价格、Alpha Search 计费和最终分组结算修复。
- 修复兑换码额度精度损失和多级重试切组的计费一致性。

### 3.5 管理端与运维

- 接入自定义 OIDC 登录显示名、跨窗口 OAuth 绑定模式判定修复。
- 接入用户关键路径限流、流式状态日志可见性、慢 SQL/错误 SQL 参数化日志。
- 接入 JSON 编辑器、模型分类、渠道表格更新、使用日志费用展示等前端更新。
- 接入官方 CI、RelayKit 校验、Issue Form 和发布同步工作流。

## 4. 保留的自定义功能

### 4.1 Kimi K3 与 TNT Tencent

- 将自定义 Kimi K3/TNT 转换器迁移至官方 `relaykit/relayconvert` 架构，没有保留两套并行实现。
- 保留 Kimi K3 thinking/reasoning 语义、隐藏 reasoning 用量过滤、流式不阻塞处理和断开状态传递。
- 保留 TNT Responses 转换、元数据、终止原因和工具调用语义。

### 4.2 路由、重试与过载保护

- 保留输入 token 路由、模拟缓存路由、渠道亲和、多 Key 最少请求调度与缓存亲和。
- 保留渠道/每 Key 过载保护、同优先级重试、跨渠道循环防护和空输出重试。
- 将官方 Auto 分组纳入现有分层重试流程，请求级分组选择与自定义调度状态可同时生效。

### 4.3 计费安全与用量归一化

- 保留分层/动态计费、缓存用量归一化、缺失输入 token 估算、流中断计费和差额结算。
- 保留 quota 饱和转换与审计标记，使工具调用官方计价逻辑不绕过现有的溢出保护、预扣费和结算链路。
- 保留“缺失用量”与“显式零用量”的区分，避免将有效零值错误地触发估算。

### 4.4 传输超时与响应策略

- 合并官方 HTTP/2 分片传输与自定义 SOCKS 直连回退。
- 保留响应头超时的自定义默认值：默认启用，180 秒；同时保留用户明确关闭时的持久化语义。
- 保留 body 停滞超时、流式超时作用域、下游写入失败传播与客户端断开的软错误处理。

### 4.5 输出兼容性

- 保留下游模型名重写、流结束状态和内容 fallback 策略。
- 保留 OpenAI reasoning 到 Claude thinking block 的流式与非流式转换。
- 保留同一 delta 中 reasoning、text 和 tool calls 的完整性，避免在后续 usage-only chunk 到达时丢失先前内容。

## 5. 冲突解决摘要

| 领域 | 官方变更 | 自定义差异 | 最终决策 |
|---|---|---|---|
| Relay 架构 | RelayKit 抽离 | Kimi/TNT 位于原 service 树 | 采用官方架构，迁移自定义转换器 |
| HTTP 传输 | 分片 transport、HTTP/2 重放 | SOCKS 回退、body/响应超时 | 组合两套能力，统一进入 transport 策略 |
| 自动分组 | 请求级 Auto 组 | token 路由、亲和、过载与分层重试 | Auto 作为新的分组入口，继续使用自定义调度管线 |
| 计费 | 工具实际调用计价 | 归一化、动态价格、中断计费、饱和审计 | 官方价格输入进入现有安全结算链路 |
| 流式转换 | 新转换器与终止事件 | Kimi/TNT 过滤、reasoning 和断开语义 | 保留自定义语义并补入 RelayKit 测试 |
| 响应头超时 | 官方传输控制 | 自定义默认开启 180 秒 | 保留自定义默认和显式关闭语义 |
| API Key 前端 | Auto 组 UI/测试 | 冲突解决中 badge 曾被注释 | 恢复 `AutoGroupBadge`，与官方功能和测试一致 |

冲突主要集中在 `controller/relay.go`、`middleware/distributor.go`、`model/channel.go`、`service/channel_select.go`、`service/http_client.go`、`service/text_quota.go`、`relay/channel/*`、`relaykit/relayconvert/*` 及前端渠道/多语言文件。完整冲突清单已保留在该合并提交的 Git 合并元数据中。

## 6. 额外兼容性修复

冲突解决后的编译与测试暴露了一组跨分支接口适配问题，已完成以下修复：

- 清除 RelayKit 测试对根模块 `common`/`relaycommon` 的反向依赖。
- 补齐协议转换器新增的 metadata 参数。
- 修复 OpenAI reasoning 到 Claude thinking block 的流式及非流式保留。
- 修复 OpenAI 到 Claude 的 finish chunk 在后续 usage-only chunk 到达时丢失 reasoning/text delta 的问题。
- 修复同一流式 delta 同时包含 reasoning、text 和 tool calls 时的内容保留。
- 将 `ChatToResponsesStreamEvent` 内部记账字段标记为 `json:"-"`，防止泄漏到下游协议。
- 增加 `RelayInfo.DownstreamModelName` 对空 `ChannelMeta` 的安全处理。
- 使 Claude 测试满足官方 `max_tokens` 必填契约。
- 更新 Auto 分组路由测试 fixture，满足官方分组倍率资格规则。
- 更新 RelayKit golden snapshot，覆盖恢复后的 reasoning-to-thinking 行为。
- 同步 `en`、`zh`、`zh-TW`、`fr`、`ja`、`ru`、`vi` 七种前端语言，缺失键、多余键和未翻译项均为 0。

## 7. 功能性分歧与风险说明

### 7.1 需要通知的功能性冲突

**无未解决的功能性冲突。**

两处在解决过程中需要特别判断的差异已经消解：

1. API Key Auto 标记曾在 UI 冲突处理中被注释，但官方行为和测试都要求展示，已恢复。
2. 响应头超时的初始测试分歧来自过期的自定义重复测试，而不是官方设计与自定义功能不可共存；已以当前运行契约更新测试。

### 7.2 上线前建议关注

- Auto 分组新增了分组倍率资格判定，原有配置中倍率缺失或不合法的分组可能不再入选。
- Claude 原生请求按官方契约要求 `max_tokens`，依赖缺省值的外部调用方应确认请求构造。
- RelayKit 已成为独立 Go 模块，后续修改协议 DTO 或转换器时需同时执行根模块和 `relaykit` 模块的校验。
- 自定义响应头超时默认仍为 180 秒，该值与纯官方部署可能不同，是为保留现有运行行为而有意保留的差异。

## 8. 验证结果

| 范围 | 命令/检查 | 结果 |
|---|---|---|
| 根 Go 模块 | `go vet ./...` | 通过 |
| 根 Go 模块 | `go build ./...` | 通过 |
| 完整后端套件 | `make test` | 通过（含根模块与 RelayKit） |
| RelayKit | `go vet ./...` | 通过 |
| RelayKit | `go build ./...` | 通过 |
| 前端依赖 | `bun install --frozen-lockfile` | 通过 |
| 前端 i18n | `bun run i18n:sync` | 通过，7 种语言均已同步 |
| 前端测试 | `bun test` | 238 通过，0 失败 |
| 前端类型 | `bun run typecheck` | 通过 |
| 前端生产构建 | `bun run build` | 通过 |
| 手工修复的前端冲突文件 | 定向 lint | 通过 |
| Git 合并索引 | 未解决文件、`git diff --cached --check` | 0 个冲突，无空白错误 |

`make test` 在允许本地套接字的环境中执行，因为 HTTP/miniredis 测试需要本地端口。

### 全量 lint 剩余风险

`bun run lint` 仍会因仓库范围的历史及官方存量规则违反失败，主要包括 `no-import-type-side-effects`、`no-array-index-key`、`curly`、嵌套三元表达式与 Promise 规则。这些问题不集中于本次手工解决的冲突，且全量修复会产生大规模无关重构，因此本次合并不扩大范围处理。类型检查、测试和生产构建均已通过。

## 9. 最终交付状态

- 官方 `v1.0.0-rc.24` 功能已合并。
- 所有 Git 文本冲突已解决。
- 自定义路由、计费、Kimi K3/TNT、超时、重试和流式行为已保留。
- 核心后端、RelayKit 和前端校验已通过。
- 本报告同时交付 Markdown 和 PDF 版本。
- 合并提交完成后保留在本地 `main`，未推送远程。

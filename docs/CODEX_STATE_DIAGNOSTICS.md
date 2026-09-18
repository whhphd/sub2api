# Codex 状态链路诊断与对照工具（第一版）

## 目的与边界

被动观察真实状态链路，定位中转丢头、换号后错误回带、HTTP/SSE 上游错误和取消。没有状态采集 keeper，没有固定一小时 TTL，没有跨轮注入，不改变指纹、调度、额度、重试或计费。HTTP 292 仅按实际状态记录，200/292 不代表模型质量标签。

官方参考：`openai/codex` 提交 `7498521d288b9b3b96ffba4eedf089d8d6e06a84`，`codex-rs/core/src/client.rs:270-297` 与 `core/tests/suite/turn_state.rs`：同轮保持路由状态，下一轮重置，认证归属变化时清除。实现沿用现有 KlN 移植模块，不引入另一份状态缓存。

本版只增加服务器配置和命令行工具，没有管理页面、数据库表或数据迁移。旧配置默认关闭，不会隐式对号池发探测请求。

## 开启诊断

部署后在服务器配置中设置，重启生效。需明确指定最多 100 个 OpenAI OAuth **本地账号行 ID**，API Key/setup-token/Grok 不参与。影子账号需将实际转发行 ID 也加入选择范围。

```yaml
gateway:
  codex_state_diagnostics:
    enabled: true
    account_ids: [12345] # 替换成专用测试账号，不能直接照抄
    sample_percent: 100
```

等价环境变量：`GATEWAY_CODEX_STATE_DIAGNOSTICS_ENABLED`、`GATEWAY_CODEX_STATE_DIAGNOSTICS_ACCOUNT_IDS`（逗号分隔）、`GATEWAY_CODEX_STATE_DIAGNOSTICS_SAMPLE_PERCENT`。默认分别为 false、空列表、10；开启但无账号或采样为 0 时配置校验失败。先用少量账号和短时间窗口，完成后关闭。

需要全账号、全请求观察时，设置 `all_accounts: true`、`sample_percent: 100`、`full_capture: true`（环境变量分别为 `GATEWAY_CODEX_STATE_DIAGNOSTICS_ALL_ACCOUNTS`、`GATEWAY_CODEX_STATE_DIAGNOSTICS_SAMPLE_PERCENT`、`GATEWAY_CODEX_STATE_DIAGNOSTICS_FULL_CAPTURE`）。`all_accounts` 自动覆盖当前及后续加入的 OpenAI OAuth 账号，仍排除 API Key、setup-token 和其他平台；此时可不填 account_ids。`full_capture` 要求采样比例为 100，并绕过下面的诊断事件限速，避免“请求全量但事件被丢弃”。应用日志必须为 info，且关闭应用日志采样；可通过现有管理员运行日志接口临时设置，记录原值并在窗口结束后恢复。这里的全量指符合条件请求的元数据观察，仍不记录正文或完整 state，仍保留解析大小边界。

日志名为 `codex_state_diagnostic`，组件为 `service.codex_state_diagnostics`。写入既有应用日志，显式跳过 Ops 数据库系统日志索引，保留时间遵循现有日志轮转。进程级限速 10 条/秒、突发 20 条；`suppressed_events` 表示日志被限速丢弃，不能把不完整日志解释成丢失请求头。采样按服务端请求标识确定，同一请求的换号/重试共用选择结果；没有请求标识时退回按账号采样。

字段说明：

| 事件 | 观察内容 |
| --- | --- |
| `http_send` | 实际 HTTP 出站尝试、端点类别、模型/推理强度、发送状态摘要 |
| `http_headers` | 原始上游状态，包括 292；收到的标准/文章提及的备用状态头是否存在；响应头等待时间 |
| `http_first_output` | 第一个文本、工具参数或可见推理摘要 delta 到达时间；不是模型内部首 token |
| `http_body_end` | EOF/提前 close/读取异常/取消/超时、终止事件、固定错误分类、耗时 |
| `http_transport_end` | 无正常响应时的传输错误、取消或超时；不打印异常原文 |
| `http_state_guard` / `ws_state_guard` | 原有守卫处理前后状态是否存在及摘要 |
| `http_relay` / `http_staged_commit` | 进入既有下游响应头转交/延迟提交位置的状态 |
| `ws_frame` | 出站尝试、上游接收或下游转交边界的关键事件；用 boundary 区分，避免将改写后的错误当成上游原始错误 |
| `ws_frame_state_stripped` | 既有来源守卫剥离了不同凭证域的状态 |
| `ws_observation_truncated` | 帧超过诊断大小上限，跳过内容观察 |

`request_ref`、`account_ref`、`user_ref`、`proxy_ref`、`session_ref`、状态摘要均使用进程随机密钥的 HMAC，不输出原始账号/用户/会话/状态值。`egress_config_ref` 是配置摘要，**不是实测出口 IP**；多个直连请求的实际 SNAT IP 是否相同，不能从该字段推断。摘要只能在同一个进程生命周期内关联。

`attempt` 是进程内 HTTP 出站序号，不是精确重试次数。按同一 `request_ref` 聚合尝试：不同账号摘要表示换号，同一账号的多个尝试不自动等同于限流重试，也可能是正常端点调用。关闭响应体仅表示读取结束，不保证用户已收到完整响应；需结合既有 usage/error 日志判断最终业务结果。

## 观察限制

- 诊断默认关闭时不读取请求体副本、不包装响应体。开启并被采样时只从 `GetBody` 副本读取最多 64 KiB，用于模型/推理强度；支持现有 zstd 请求。副本读取失败不阻断转发。
- HTTP 响应只在业务方实际读取时被观察，不预读、不吞字节、不改错误或状态。SSE 支持跨读取分片、CRLF、多行 data；单行和单事件各最多 64 KiB，超出时标记截断，不能用缺失终止事件推断上游失败。非 SSE 只保留最多 64 KiB 的内存前缀用于错误分类，随后释放，不写正文。
- WS 关键帧日志不等于完整帧跟踪，也不是独立的握手/首字延迟监控；沿用既有 WS/usage 指标查看这些耗时。不会为了诊断新增连接或更新状态归属。
- 完整状态、请求/响应正文、Authorization、Cookie、代理 URL/密码不写新增日志。现有历史调试日志的配置和行为不在本次修改范围。
- “overload”复用现有容量错误识别；不能识别的 429 标记 `rate_limited_unclassified`，不猜额度耗尽或 IP 风控。

## 对照工具

`backend/scripts/codex_state_compare.py` 只使用 Python 标准库，提供 A/B 交错的 Responses HTTP 协议测试。它**不是官方 Codex 客户端，也不模拟其完整设备/TLS/WS 指纹**；结果不能单独证明官方客户端体验或“降智”。

1. 准备两条测试路径和专用测试账号，复制 `codex_state_compare.example.json` 到本地私有文件，填入两边都实际支持的同一模型。凭据仅通过配置中指定的环境变量读取；配置文件不存明文凭据。
2. A 路为专用 OAuth 账号直达 Codex Responses，B 路为该账号经 Sub2API。通过现有测试分组确保 B 只选择同一上游账号，并人工核对代理/出口、推理强度、模型映射、指纹开关。工具不会创建分组或修改生产调度。
3. 默认不使用环境中的 HTTP_PROXY/HTTPS_PROXY。只有 arm 明确设置 `proxy_env` 时使用对应代理；设置了但变量缺失会停止，不悄悄直连。已禁用重定向，防止认证头被转发到其他目标。GitHub 下载使用本机代理的约定与这里的实验出口选择无关。
4. 先做无网络配置检查：

```sh
python3 backend/scripts/codex_state_compare.py /private/path/compare.json
```

5. 明确运行真实请求（会消耗账号额度），默认 3 轮，最多 10 轮：

```sh
python3 backend/scripts/codex_state_compare.py /private/path/compare.json --run --trials 3 --output /private/path/report.json
```

每轮 4 个固定测试：算术、排序约束、上下文标记读取、只读工具续链。工具只返回固定标记，**不执行模型生成的代码或任意命令**。每题每路径最多两次请求，因此每轮最多 16 次上游请求；没有自动重试、换出口、采集续期。每题新建 session 并清空 state，仅同题工具续链回带第一个收到的状态。

报告只保存 A/B、题号、通过与否、真实 HTTP 状态、状态存在性、固定错误分类、时间与 token 数，不保存答案正文、完整 state、认证或 endpoint URL。报告文件独占创建、权限 0600，不覆盖已有文件。报告包含试题摘要和 `account_parity` 提醒：账户/出口一致性需由运维核实，不能因 URL 相同就认定相同。

少量固定题只能检验协议和明显回归，不能量化通用模型智力。正式质量结论还需扩展题集、重复/交错测试，并补充真实官方 Codex 客户端的相同任务对照。不能按回答长短、模型自述或单次 292 宣称收益。

## 测试与回退

```sh
python3 -m unittest discover -s backend/scripts -p 'test_codex_state_compare.py' -v
cd backend
go test ./internal/config ./internal/service -run 'TestCodexDiag|TestCodexTurnState|TestCodexDeviceWire|TestCodexBridge'
```

上述 Python 测试只使用 mock/本地回显服务器，不调用付费上游。上线前另跑后端完整/标签测试和相关 race、lint；本版没有前端改动。回退先关闭诊断配置，必要时恢复旧镜像，无数据库回滚操作。

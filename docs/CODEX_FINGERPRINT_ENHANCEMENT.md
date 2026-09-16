# CallAI Codex 指纹增强移植记录

基线：CallAI `ca830f960`（Sub2API v0.2.5）。本轮只移植指纹相关模块，保留现有配额、自动重置卡、调度、Grok、图片直调及 Partner API 行为。没有新增业务表。

## 来源与许可证

- KlN-4096/sub2api：[`01f2c71f47ec36c66a219e4f433f4a89db441f90`](https://github.com/KlN-4096/sub2api/tree/01f2c71f47ec36c66a219e4f433f4a89db441f90)，v0.2.5-klno.2。保留原作者 KlN-4096 的实现、原注释、测试和仓库 LGPL-3.0 许可证；改动点见下文。
- LuckyKuang/sub2api-plus：[`1f06d5871f52e55d2378f38f15b09c5484411aae`](https://github.com/LuckyKuang/sub2api-plus/tree/1f06d5871f52e55d2378f38f15b09c5484411aae)，v0.2.4+custom.005。复用账号普通编辑遗漏字段时保留保存模式的回归测试，字段常量适配本地名称。
- 没有整体合并二开仓库；没有移植 CPR/Rust、客户端准入系统、账号类型或配额调度替换，也没有引入新的 TLS 模板。

主要移植文件位于 `backend/internal/service/openai_codex_*`，包含身份与嵌套元数据、Responses/compact 字段顺序、zstd 请求编码、环境时间与搜索位置、turn-state 来源、辅助请求。WS 原始文本帧、出站快照、连接兼容性和各入口适配分别位于现有 `openai_ws_*`、`openai_outbound_snapshot.go`、`openai_compat_bridge_identity.go` 及 gateway 文件。

`tools/run-codex-fingerprint-offline.sh` 原样取自 KlN，保留 Linux 网络命名空间隔离；未引入与本部署无关的 Windows/WSL 驱动脚本。

## 开关与账号配置

复用 `GET/PATCH /api/v1/admin/settings/openai-oauth-runtime`，新增：

```json
{"openai_oauth_codex_fingerprint_enhancement_enabled": true}
```

同一份运行配置内原子处理互斥：开启增强会关闭 `openai_oauth_rate_limit_proxy_rotation_enabled`；开启后者会关闭增强。同时显式提交两项为 true 返回 400。关闭一项不会开启另一项。其他未提交字段保持不变，数据库条件更新冲突最多重试五次。

旧 JSON 缺少新字段时增强为 false，不做隐式启用。保存会立即更新本实例缓存；迟到的旧查询不能覆盖更新后的缓存。其他实例沿用现有 30 秒缓存过期机制。实际执行短时 429 换代理前（包含候选代理选择完成后）会重新读权威设置，读取失败或增强已开启时不换代理。

增强打开时，OpenAI OAuth（包含 PAT、Agent Identity、凭证影子）每次转发创建固定策略与账号配置快照，以 device 模式执行。账号保存的 session/device/full/off 及有效设备种子不被改写。关闭后恢复保存模式。账号编辑弹窗说明当前生效模式，显式关闭账号模式保存为 off，普通编辑省略模式时保留原值。

API Key、setup-token 维持既有策略。普通短时 429 同账号重试和原重试预算、额度耗尽暂停、代理故障恢复、自动重置卡仍按原规则执行。

## 本地适配

### 入口与连接

- Responses、HTTP 透传、Chat/Messages 兼容桥、compact、搜索、模型列表和图片经 Responses 转发路径使用各自端点规则。
- 图片直调保持现有开关、模型默认值及路由；没有采用 KlN 固定关闭直调的改动。相关移植测试显式选择 Responses 路径。
- 仅 Responses 请求体使用 zstd；compact、模型 GET、WS 文本帧不压缩。禁止账号请求头覆写伪造 Content-Encoding。
- 增强关闭时保留本地已有 WS 会话身份处理及默认 UA，不套用新增设备线格式。
- WS 连接兼容性包含增强策略、身份、代理、端点、UA 和版本。活动 turn 完成后，若策略或代理已变，下一个 turn 返回 1013，要求客户端重连。旧连接不会交给不兼容的新请求。
- 额度查询只补同一请求的配置快照及代理校验，保留 CallAI 原有额度解析、暂停、补偿与重置卡流程。绑定代理解析失败时，额度查询和 OAuth 刷新不能静默直连。

### 出口信息

继续使用现有 ip-api 探测器，新增 IANA timezone。没有引入 ipinfo，也没有增加 AI 上游主动健康探测。

- Redis 使用独立 `proxy:codex-exit:<摘要>` 缓存，24 小时过期，保留采集时间。摘要绑定代理 ID 和连接配置，不包含明文代理凭据。
- 现有代理健康探测顺便缓存出口信息；请求只读短超时缓存，缺失时异步查询，不等待外部网络。
- 外部查询最多四路，按出口配置 singleflight 去重，跨实例使用现有 leader lock。账号持久化用单次查询前的代理配置和旧采集时间作条件，JSONB 只合并相关键，写调度事件并刷新快照。
- 手动换/清空代理、回退代理及故障恢复会触发有界后台刷新；新请求也会检查出口配置。旧代理迟到结果不能写入新配置，过期、未来时间或不同代理的观测不能投影。
- 无有效出口信息时保留客户端环境并继续请求。只处理有明确结构依据的环境元数据和搜索位置，不扫描普通用户文本、代码或工具输出。
- `settings/user` 辅助 GET 沿用 KlN 的线程去重、2 小时去重窗口、10 秒超时、10 万条目上限及 1 MiB 响应读取上限；使用同一次尝试的身份、账号和代理，失败不阻断推理。

## 验证

验证在 `/home/debian/callai-codex-validation` 隔离 Git worktree 中进行。Go 1.27.0 / golangci-lint v2.13.0；集成测试临时 PostgreSQL 18.1、Redis 8.4，未连接生产数据库。

2026-09-16 最终结果：

| 检查 | 结果 |
| --- | --- |
| `go test ./...` | 通过 |
| `go test -tags=unit ./...` | 通过，包含最后补充的代理选择期间切换策略用例 |
| `go test -tags=integration ./...` | 通过，CI 模式实际启动隔离 PostgreSQL/Redis |
| 指纹、运行策略、WS、代理健康和 429 相关 `-race` | 通过；HTTP client 包完整 race 另行通过 |
| `golangci-lint run ./...` | 0 issues |
| `go generate ./cmd/server` / `go build ./cmd/server` | 通过，生成文件与本机一致 |
| Linux 断网回显 | 260 个顶层测试通过，IPv4/IPv6 出口关闭，仅允许 loopback |
| 本机与服务器代码一致性 | 96 个待提交代码/测试文件 SHA-256 全部一致 |

记录保存在工作区 `.artifacts/codex-fingerprint-20260916`，服务器保留相同任务的验证日志目录。测试容器及独立测试 worktree 在提交后清理。

前端直接运行已有 node_modules 中的工具：全量 287 个测试文件、2190 条用例通过；vue-tsc、Vite 构建及修改文件 ESLint 通过。浏览器使用实际新增卡片模板、Toggle、翻译和生产构建 CSS 的隔离夹具，检查桌面、390px 手机、深色模式，以及两项开关双向互斥、两项同时关闭；没有使用生产设置 API。

离线回显验收编译 Linux 测试二进制后使用：

```sh
cd backend
CGO_ENABLED=0 go test -c -tags=unit -buildvcs=false -o /tmp/codex-fingerprint.test ./internal/service
cd internal/service
sudo ../../../tools/run-codex-fingerprint-offline.sh /tmp/codex-fingerprint.test '^Test(Codex|ApplyCodex|StageCodex|ApplyStagedCodex|RewriteCodex|ShouldResolveCodex|GetOpenAIUserAgent|OpenAIWSConnPoolConfiguration)'
```

测试二进制放在可遍历的临时目录，并从 `backend/internal/service` 运行，供保留的图片定价测试读取本地资源。该脚本先验证只有 loopback、无 IPv4/IPv6 出口，再执行测试，包含实际 HTTP/WS 回显和请求压缩解码检查。测试通过说明协议实现满足回归断言，不代表上游对请求的接受率或生产首字延迟已验证。

## 后续部署与回退

本轮实施不自动部署、不自动开关生产设置、不做全库备份。

获得部署指令后，使用 Git 提交短哈希构建镜像，旧容器保持运行，构建成功再 recreate，保留旧镜像及必要设置快照。新版本健康后，通过设置接口显式开启增强，检查返回值中增强为 true、短时换代理为 false。

上线验收检查真实 HTTP/SSE/WS 请求、429、首字延迟、WS 重连和并发槽位。生产验证用正常业务流量，不主动制造限流。若需回退，先关闭增强，核对已恢复账号保存模式，再按需切回旧镜像；关闭增强不会自动恢复短时换代理开关。

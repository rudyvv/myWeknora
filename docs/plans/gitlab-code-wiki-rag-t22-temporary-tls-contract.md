# T22 现场阻塞：限定 GitLab 的临时 TLS 调试合同

用户于2026-10-05询问临时关闭证书验证的方法并要求继续。真实连接已证明 https://gitlab.p.it/api/v4/user 因 unknown authority 在 TLS 阶段失败；令牌尚未验证。本修改准备可审查的临时调试能力，**不自动启用、不导入证书、不修改系统信任或重启现有应用**。

## 实现决定

仅 GitLab 专用 HTTP client 支持 operator 进程环境配置：`GITLAB_TLS_INSECURE_ORIGIN` 为一个精确 HTTPS origin（无路径、userinfo、query、fragment，禁止wildcard），`GITLAB_TLS_INSECURE_UNTIL` 为 RFC3339 明确截止时间；必须成对设置并且未来截止不得超过24小时。空配置默认正常验证，过期自动正常验证，非法配置 fail closed 不隐式关闭验证。规范化只允许标准 HTTPS 默认端口等价，不允许子域/其它端口/HTTP降级扩大例外。

TLS 例外是对**每一次请求**的 origin 与当前时间判断，不能只在 client 创建时判断后永久使用 insecure transport。到期后使用独立的正常验证 transport，旧不安全 keep-alive 不被复用。GitLab API client 与 Git 的现有 loopback HTTP bridge 使用同一专用 policy；后者仍由 Go HTTP 拉取 HTTPS，不设置全局 git sslVerify/GIT_SSL_NO_VERIFY、不把令牌写进子进程环境。

保留 SSRF URL/dial/DNS/timeout/size规则；例外请求禁止跨origin重定向和降级，也不得携带 PRIVATE-TOKEN/Authorization 到其它origin。共享 `NewConnectorHTTPClient` 及模型/其它connector行为不修改。源头凭据、请求/响应正文、令牌、CA私钥均不写日志或文档。必要的诊断只包含host和例外截止/启停状态；部署文档给明确风险和移除环境恢复验证方法。

## 所有权与验收

原main/T09已接受API工具94e1de44，新的实施窄审起点为accepted集成24ea2183（正式T22仍1d32）。只拥有 NEW `internal/datasource/gitlab_tls.go` / `gitlab_tls_test.go`、GitLab client.go最小构造调用、source/git_preview.go bridge最小构造调用与必要 NEW 专用 bridge TLS测试、deployment source-operations.md。可按真实结构调整内部helper命名，不能更改共享utils安全策略/Wiki119/UI/settingsschema或新增全局TLS跳过。

根独立 Sol/high 双轴与正常宿主真实 httptest TLS 行为：默认拒绝自签名、精确origin且未到期可连、其它origin/HTTP降级/跨originredirect拒绝且无凭据、已创建client跨过截止后拒绝自签名并不复用旧insecure连接、非法/过长/不完整config拒绝。证明API配置client与bridge使用同一policy，并保持受保护Git进程环境。测试用合成证书/令牌，不接触真实GitLab凭据。

实际启用和现有应用部署仍待最后确认，并明确丧失服务端证书身份校验的风险。长期解法是由GitLab管理员提供可信CA/修复证书链；临时例外不能当作正式TLS预检通过。保留用户已有UI中令牌，不读取到聊天或shell。

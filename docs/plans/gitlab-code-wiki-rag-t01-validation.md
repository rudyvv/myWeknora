# T01 验证与审查记录

日期：2026-09-28。Ticket：[GitHub #9](https://github.com/rudyvv/myWeknora/issues/9)。分支：`codex/gitlab-code-wiki-rag`。审查基点由用户确认：`5e4b6de5`；初始实现：`e957974e`，随后修复包含在同一分支中。

## Standards

按 README 的贡献约定、AGENTS/CONTEXT/相关 ADR 及 code-review 的代码气味基线检查。

- 测试使用用户已确认的数据源服务、HTTP API 和编辑器操作入口；复查时把新增的内部 KBService 替身改成真实 KBService 加外部存储边界夹具。
- Git 对象访问隔离在 source 包，GitLab 身份/分支解析留在 connector，服务负责能力预检，未扩大原文档后缀或执行目标代码。
- 修改的 Go 文件经 gofmt；差异无 whitespace 错误，前端类型检查通过。未发现未处理的文档标准违反项；没有为后续 tickets 预先建立空泛接口。
- 独立 Standards 子代理因账号用量限制未能执行，以上是主代理人工复查，不能当作独立审查通过。

## Spec

对照 T01 六项验收及 Spec/ADR 的相关边界检查。

- 已覆盖旧配置默认文档、源码同步隔离、单项目/指定分支、版本化过滤规则与固定 SHA 完整预览；不按 dist/plugins 名称整体排除，不生成 Knowledge/Wiki。
- HTTP 范围校验沿用既有租户/KB/API-key 行为，生产路由接入现有 Admin 组；Git 请求使用 SSRF 安全客户端且不在命令/环境中传 token。
- 复查发现模型 ID 不足以证明 Embedding 可用，已增加模型类型、状态、维度与所有权验证，并覆盖不存在及跨租户模型。
- 复查发现已选分支删除会阻止凭据轮换，已分离只读凭据验证与分支预览；公开服务测试先复现 404 失败，再验证轮换成功而预览仍失败。
- 无配置的预览返回错误而非空指针；源码界面隐藏“分支可选”的文档提示。T01 明确尚无解析/入库流水线，`can_sync=false`。
- 独立 Spec 子代理同样因账号用量限制未能执行，以上是主代理人工复查。真实模型效果、源码解析/发布及代表仓库性能未在本 ticket 验收。

两轴人工复查已知问题均已修复；独立子代理审查仍未完成。

## 验证结果

- Go：数据源公开服务源码配置/预览/凭据轮换测试通过；HTTP 预览租户/KB/API-key 范围测试通过。
- 回归：GitLab connector 和数据源包、检索服务、router/types 测试通过；文档入库、Wiki 后处理、更新/回滚与版本保留的选定回归通过。PostgreSQL repository 包本身没有测试，预检的 SQL 由公开服务测试的外部数据库夹具验证。
- 前端：`vue-tsc --build` 通过；DataSourceEditorDialog 8 项测试通过。沙箱中 tsx 因 `uv_os_get_passwd ENOMEM` 无法启动，放宽沙箱后正常完成。
- UI：在 localhost 挂载真实编辑器，以明确标注的本地 GitLab/API 夹具检查源码模式、固定 SHA、规则、能力提醒和文件分类。截图保存在 `docs/images/gitlab-source-preview-t01.png`。
- 全量前端：585 项中 584 通过，POSIX-shell CLI 测试需要 `/bin/sh`，在当前 Windows 环境失败。
- 全量 Go：已运行 `go test ./...`，未全通过。SQLite 向量 Cgo 缺少 `sqlite3.h` 导致 server/desktop/container/sqlite retriever 编译失败；三个 SQLite 数据源清理测试受 Windows 文件锁影响；若干 skill/Agent shell、路径和 sandbox symlink 测试依赖 POSIX 或 Windows 未授予的符号链接权限；未修改的飞书 Wiki 日志汇总和部署能力键文本检查也失败。未在基点单独重跑这些失败，不能将其全部断言为已证明的基线失败。
- `golangci-lint` 未安装，未执行；未通过重复运行全量套件掩盖上述限制。

真实 Git 夹具和公开预览已验证 T01 行为。T02 将使用真实锁定解析器、真实 PostgreSQL/ParadeDB 及受控 Embedding 验证发布和检索，不以本 ticket 的能力预检替身代替。

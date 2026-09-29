# 首批并行集成验证（T04 / T07 / T14）

集成分支：`codex/gitlab-code-wiki-rag`。共同审查起点：用户确认的 `461029f6`。本批保留三项独立验收和原 22 项依赖；不代表整体源码模式已经发布。原目录 `D:/Project-Weknora/WeKnora` 保持 `main`，所有实现与检查位于独立 worktree。

## 提交与审查

| Ticket | 独立分支提交 | 集成功能提交 |
| --- | --- | --- |
| T04 / #12 | `3b47cf5d` | `dbfb6530` |
| T07 / #15 | `82bdbd76`、`335b3d59` | `b6638c72` |
| T14 / #22 | `bd7a43ac` | `aaf7cb58` |

最终集成补充 `22456cf6`：共用固定 SHA 的 GitLab URL helper，以真实 Preview → DataSource 过滤配置更新 → 完整空发布替换历史 Wiki 测试中的成员故障，并补部署说明。A/B 三个共享文件的合并审查、T14 完整审查及最后两文件增量审查均已完成。Standards / Spec 最终各 0 项未解决问题；初审发现均已修复并复验。最终只改变完整发布后的来源引用保护，没有放宽通用源码 reader。

## 合并后的实际检查

| 检查 | 结果 |
| --- | --- |
| 三项公开交叉集成 | PASS，52.031 秒：最后文件排除的完整空发布、旧 Wiki revision 按 file + tag 读取 exact version 且 clear 优先、跨源同模块身份及空格 / # GitLab 路径。 |
| 完整 Source 公开服务 / 工具 / HTTP 集成 | PASS，53 个匹配 Source 用例，401.699 秒；包含普通文档 / FAQ、共享 KB、文件 / 标签 / 多源范围、两路真实索引、并发发布与问答固定快照、全部 SourceWiki / HTTP 回归。 |
| `go test -p 4 -timeout 15m ./... -count=1` | 所有包完成编译，测试 exit 1；24 项失败，分布在 5 个包，详情见下。 |
| `gofmt -l`（起点后 Go 文件）及 `go vet ./...` | PASS，无未格式化文件或 vet 错误。 |
| `go build -o .source-batch-server.exe ./cmd/server` | PASS。 |
| `node --import tsx --test --test-concurrency=4` | 完整自动发现 TS / mjs：915 tests，910 pass / 5 fail，110.354 秒；0 skipped。 |
| `vue-tsc --build` | PASS，exit 0。 |
| `vite build` | PASS，2 分 57 秒；保留既有大 bundle 警告。 |
| `git diff --check` | PASS。 |

本机公共源码验证使用真实本地 Git、锁定且预取的 Java/JavaScript/TypeScript/TSX parser、独立 PostgreSQL / ParadeDB schema；GitLab/model/queue 仅作为受控外部边界。共用专用测试容器，不访问生产数据库，不运行接入仓库的构建脚本。模型 fixture 的通过不代表真实生成或 Embedding 模型的质量已经验收。

## 全量测试失败边界

Go 24 项失败集中于 Windows 的 SQLite TempDir 文件锁、符号链接权限、POSIX shell / Python 环境、Docker host 的 `npipe` 不支持，以及未改动的飞书日志汇总与 CRLF 部署能力断言。完整日志保存于工作树 `.source-batch-go-full.log`；没有在 `461029f6` 独立重跑，不能把全部失败宣称为已证明的基线，也不宣称全仓测试通过。

失败包：

- `github.com/Tencent/WeKnora/internal/agent/tools`
- `github.com/Tencent/WeKnora/internal/application/service`
- `github.com/Tencent/WeKnora/internal/datasource/connector/feishu/wiki`
- `github.com/Tencent/WeKnora/internal/handler`
- `github.com/Tencent/WeKnora/internal/sandbox`

失败测试：

- `TestMCPSchemaDoesNotLoadExternalResources`
- `TestReadFileCombinesSourcesWithoutGrantingHostAccess`
- `TestShellStdinPreservesLiteralDataForTheEntireCommand`
- `TestSkillPythonPackageRecoveryInstallsWithoutPip`
- `TestSkillPackageCommandsRefuseMissingDirectory`
- `TestDataSourceServiceDeleteSQLiteCleansUpAfterSoftDelete`
- `TestDataSourceServiceDeleteKeepsCleanupStateWhenSoftDeleteFails`
- `TestDeleteKnowledgeBaseCleansUpSQLiteDataSources`
- `TestSanitizeSandboxConfigRefusesSecretsWithoutAESKey`
- `TestCreateAcceptsDockerNamedSandboxBackend`
- `TestDeleteMarksBuildingSnapshotsWhenProviderHasNoSnapshotClient`
- `TestCacheBudgetGuardKeepsSmallWipesBig`
- `TestCleanImageScratchCommandShape`
- `TestSeededSkillHelperIsDirectlyExecutable`
- `TestRuntimePrerequisitesResolveSkillLocalCLI`
- `TestSkillPythonVerifier`
- `TestSkillPythonVerifierNeverExecutesTheSkill`
- `TestSkillPythonVerifierReportsAnUnreadableScript`
- `TestSkillPythonVerifierNeverJudgesImports`
- `TestSkillPythonVerifierAcceptsTheOfficeToolkitLayout`
- `TestFetchAll_LogsSummaryWithSkipBreakdown`
- `TestDeploymentCapabilityKeysMatchFrontend`
- `TestWorkspaceBootstrapDoesNotRemoveOrMoveFiles`
- `TestResolveEffectiveConfigMapsDockerNoneToDeniedEgress`

前端 5 项失败：

- SandboxConfigEditorDrawer.network.test.mjs 的 stored secret recoverability、deny-all classifier、moveCubeRule 三项静态匹配要求 LF，而当前 Windows checkout 的未改动 Vue 文件是 CRLF。
- SandboxTerminal.test.mjs 的 Bash alias 用例将 Windows 路径传入 Bash，报路径不存在。
- cliIntegration.test.ts 的 POSIX shell 参数用例找不到 `/bin/sh`，spawn status 为 null。

上述测试文件和对应 Sandbox Vue 在审查起点与 HEAD 的 Git blob 完全一致；没有独立在基点复跑，不把这项比对等同于基线测试通过/失败证明。完整前端日志 `.source-batch-frontend-full.log`，type/build 日志 `.source-batch-typecheck.log`、`.source-batch-vite-build.log`。此前只按 TS 文件枚举的测试记录不覆盖全部 mjs；本批以 Node 自动发现的 915 项结果为准。

## 范围与后续

本批完成增量发布、JS/TS 结构检索及首张小模块技术卡片。仍有首期 100 selected files / 16 MiB 的同步限额，技术卡片 evidence 上限 16 files / 32 KiB。代表仓库约 3,844 文件 / 125 万行、真实内网 GitLab 与真实模型相关性 / 吞吐还未验收，整体能力需要后续 tickets 与 T22。

GitHub #12 / #15 / #22 在最终完整 Source 验证后统一勾选验收、记录结果并关闭；父 Spec #8 保持 open。下一批可复用空闲 worktree，按依赖安排 T05、T08、T10；T06、T09、T13 等也已进入可排期范围，不提前关闭仍未实现的 ticket。

## GitHub 发布验收

2026-09-29 已逐项保存验收勾选、发布完成证据并确认 T04 / #12、T07 / #15、T14 / #22 关闭。父 Spec #8 保持 open，原生子 Issue 进度为 6 / 22；尚未实施的票保持 open。

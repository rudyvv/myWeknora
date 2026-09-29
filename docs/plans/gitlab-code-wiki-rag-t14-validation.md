# T14 实施与验收记录

Ticket：[GitHub #22](https://github.com/rudyvv/myWeknora/issues/22)。父 Spec：[GitHub #8](https://github.com/rudyvv/myWeknora/issues/8)。独立实现分支 `codex/source-wiki`；审查起点 `461029f6c11d5ca87fc3397e2fdd25898b7f3046`。本记录覆盖 T14，2026-09-29 实现冻结。根任务安排的 Standards / Spec 两轴均已最终复审，0 项剩余问题；交根任务进行三分支集成。全仓与整体源码模式验收尚未完成。

## 最终行为与验收映射

| T14 验收项 | 实现与公开验证 |
| --- | --- |
| 单模块身份 | 用户明确选择 source、相对模块目录、主题；页 slug 带 source UUID 和规范目录 hash。同名目录的两个源分别生成两页，不逐文件建页。真实 Git 两仓库公开生成 / ListByType / GetPage 验证。 |
| 固定版本证据与独立 QA | 复用 T03 BeginSourceRead，嵌套调用不重新选 publication。通过公开 GetSourceFile 读取固定 file version / SHA，最多 16 文件、合计 32 KiB 原始字节。模型仅输出服务端提供的 evidence IDs；服务器再次核验完整原文 SHA-256、片段 hash、SHA、raw 字节区间与行范围。综合与语义 QA 是独立调用；严格 JSON，QA 的 section 索引必须完整、唯一、有效。动态 / 推断关系标记不确定。虚构 evidence、越界 / 损坏 raw、QA unsupported / duplicate / invalid index 由公开模型 fixture 拒绝。 |
| 原 Wiki 与回答适用性 | 合格卡片走原 Wiki 搜索、阅读、目录及三 Agent 公开 Wiki 工具；引用打开应用内受保护源码 reader。draft / failed / unverified 与未验证适用的 stale 当前页均不进入回答。源码新发布后人类阅读显示 stale，固定旧问答保持旧 SHA，新问答不复用 stale 卡片。 |
| 初始修订与原文保护 | 首张卡片即登记 current + 初始 revision 对应来源、typed evidence / metadata；000105 owner rows 对文件版本 / 快照采用 FK RESTRICT。普通编辑沿用 Wiki 版本与冲突 / 回滚行为；技术正文 / 标题 / 摘要变动标记 unverified。拒绝直接伪造 provenance / metadata 证据；机器关联编辑通过正常版本写入保留旧 evidence owner。历史 reader 先授权整个 page / revision，再读取其已登记的 exact version，调用者提供任意 version 不产生授权。 |
| 全实际来源范围 | 每个 source ref 都必须由某一个完整原始 target alternative 接受，source / file / tag 约束不交叉拼接。正文、摘要、关联摘要、列表、轻量投影、搜索、图、目录、历史、attempt 状态在分页 / 返回前限制。混入普通文档贡献的技术页也核验所有实际 refs；不裁剪混合正文。原纯文档 Wiki doc / tag union 行为保持。合并目录区分真正空 folder 和被范围遮蔽的有页 folder，不返回隐藏页标题或计数。公开 narrow / cross / all / mixed-document fixture 覆盖。 |
| 有界失败与手动重试 | 每次手动请求最多 18 provider calls、360000 token charge、3 分钟和 2 次 repairs；单 provider 阶段最多 3 次 retry、输出上限 32768 字节。预算在调用前预留 prompt UTF-8 字节 + 4096 completion；响应实际 usage 超限也失败。失败记录 reason / counters，保留已有 ready 正文，不影响源码发布。UI 显示 reason 与有界手动重试；只为生成 endpoint 配置 190 秒超时，KB 切换丢弃旧 pending 结果 / reason。公开 provider error / token budget / cancellation 与真实 DOM deferred fixture 验证。 |
| 写入适用性与三 Agent | 生成完成仍需新鲜 publication、source config / lifecycle / UpdatedAt、实际 Wiki 模型 / 参数与 KB WikiConfig / UpdatedAt、base page version。事务 SHARE locks 保护最终比较和写入。模型调用受控暂停后，公开 source sync / source update / model update / KB SynthesisModelID update / Wiki edit 改动均拒绝旧任务覆盖 ready 页。原 RAG、Wiki、hybrid 三配置通过公开 tools 使用同一个 question scope。 |

标题与摘要没有独立 evidence-ID 字段。本次采取保守登记：页与修订保存提供给综合和 QA 的**全部**证据，即使某个文件没有被 section 引用也算整页来源。这样范围可能比实际正文引用更窄，避免标题或摘要借用未登记文件后被窄范围读取。不会用模型的 `supported=true` 替代服务器来源登记。

历史 Wiki 源码与当前通用源码 reader 的授权分离。000105 的 `source_read_wiki_scopes` 仅记录由公开 BeginSourceRead 授权后的完整规范 alternatives；T03 source_read_scopes、HasSources、SnapshotSQL 不变。Wiki owner 身份与 registered evidence 约束 exact-version 读取，live source-mode、当前 publication 存在、当前 file / tag 和原始 KB 权限都重新核验；明确 clear / 撤销优先，包括 raw SQL 返回后的 fresh 检查。普通源码 reader 不能借此任意读取历史版本。

GitLab 外链通过公共 `source.GitLabBlobURL` helper 对每个路径段 PathEscape，固定 SHA 和行范围来自 evidence registry。真实 Git 文件含空格及 `#` 的公开 reader case 通过；Windows 不支持问号文件名，本次没有声称真实 `?` 文件测试。

## Red → Green 事实

测试位于公开服务、HTTP、Agent tools 和 source reader 边界。真实 Git、锁定 Java parser、独立 PostgreSQL / ParadeDB schema；外部 GitLab、models、queue 可控。共享容器和数据库未重启或重建，fixture 只创建并清理自身随机 schema。

- 新公开 API 初始编译 red → 第一张验证卡片公开工具 / reader green（8.254 秒）。
- 新发布后回答仍错误 ready red → current-answer / own-revision evidence green（15.591 秒）。
- 标题 / 摘要漏登记另一个 model-visible 文件、QA duplicate-index red → 全来源保守登记与严格 QA green（相关 batch 23.186 秒）。
- 相同模型 ID 但参数已变仍发布 ready red → publication / model / KB SynthesisModelID / source config / base page version gate green（15.535 秒）。
- UpdateMeta 可伪造 refs red → bookkeeping 拒绝与机器编辑 revision green（8.969 秒）。
- 窄范围 attempt reason / title 泄露 red → 完整 attempt evidence 范围过滤，draft 不序列化。
- 历史已过滤 FileID / tag reader 与 clear 优先 green（12.121 秒）；这是基点上受控当前成员故障，不代表已跑过 T04 全排除完整空发布流程。
- 两 source 同 module 的公开列表只返回一页 red → 全来源 predicate 修正；identity / whole-page / mixed-document 相关 batch green（20.997 秒），临时 SQL 诊断已移除。
- 合并 summary + concept 目录将隐藏技术页目录当空目录返回 red（10.077 秒）→ 原 owner tenant / KB 限定内部 presence + 真正 empty / narrow / cross 公共回归 green（包含在最后 Wiki suite）。
- GetGraph(nil) 公开边界 panic red（4.223 秒）→ nil guard 保持在 scope 初始化之前，最后 Wiki suite green。
- 普通 Wiki 修订已选中仍显示选择提示的真实 DOM red（1.871 秒）→ 保留原 template / v-else 链，源码证据区独立，组件 green。

## 两轴审查

根任务复用两位独立 reviewer，按上述固定基点审查完整最终 diff。Standards 轴关闭 GetGraph nil guard、HTTP fixture 复制、普通修订选择提示 3 项；Spec 轴关闭 merged folder 范围泄露，公开 red → green 见上。最后两轴均报告 0 项剩余问题，包含后续 router / SQLite schema / UI 最小修复。

## 最后相关验证

| 命令 / 范围 | 结果 |
| --- | --- |
| `go test -tags integration -timeout 10m ./internal/application/service -run '^(TestSourceWiki\|TestWiki\|TestFolder)' -count=1` | PASS，76.577 秒。包含 13 个 SourceWiki 服务公开用例、迁移后的 external-package HTTP 用例、公开 nil request 与原 Wiki / folder 回归。 |
| `go test -p 2 -timeout 10m ./internal/application/repository ./internal/container ./internal/router -count=1` | 三包 PASS：4.631 / 5.575 / 3.652 秒。发现并修复 router wrapper 不支持 Use 的编译错误；SQLite 手写 Wiki DDL 补当前 provenance / revision 列后完整 repo 回归通过。 |
| `node --import tsx --test src/components/SourceWikiModules.test.ts src/components/SourceCodeView.test.ts src/components/SourceSnapshotRunView.test.ts src/utils/wikiRevisionDiff.test.ts` | PASS，11 tests，2.981 秒；真实 Vue 模板与 DOM，覆盖 retry reason / evidence SHA substitution / KB pending switch / 普通修订提示。fixture 的非交互 TDesign tags 有 Vue resolve warnings。 |
| `vue-tsc --project tsconfig.app.json --noEmit --incremental --tsBuildInfoFile <temp>` | PASS，exit 0；最终 session 89454。 |
| `git diff --check`；diagnostic 搜索 | PASS，无 WIKIDIAG / WIKIDETAIL 临时日志。 |

更广的相关测试也实际执行，不能宣称全部绿：

- `go test -timeout 15m -tags integration ./internal/application/service -run 'Wiki|Source' -count=1`：230.322 秒，T14 用例无失败；3 个 SQLite cleanup 测试失败，均为 Windows TempDir 清理时数据库文件被占用：TestDataSourceServiceDeleteSQLiteCleansUpAfterSoftDelete、TestDataSourceServiceDeleteKeepsCleanupStateWhenSoftDeleteFails、TestDeleteKnowledgeBaseCleansUpSQLiteDataSources。
- tools / repository / handler / source / types / container / router 完整相关包 batch：初次出现本次 router compile 与 SQLite DDL 错误，上述最终三包重跑已绿。tools 尚有 5 项失败：TestMCPSchemaDoesNotLoadExternalResources（Windows file URI）、TestReadFileCombinesSourcesWithoutGrantingHostAccess（symlink privilege）、TestShellStdinPreservesLiteralDataForTheEntireCommand、TestSkillPythonPackageRecoveryInstallsWithoutPip、TestSkillPackageCommandsRefuseMissingDirectory（POSIX shell / 环境）。handler 唯一失败 TestDeploymentCapabilityKeysMatchFrontend（读 CRLF 文本后 frontend keys 带尾部单引号逗号）；原 HTTP T14 用例已迁移且最后 service suite 通过。types PASS（2.920 秒），source / interfaces 无测试。
- 这些失败与 T03 的 Windows 记录相符，但没有独立在 461029f6 重跑，**不宣称全部已证明为基线失败**。未跑本分支全仓 suite；根任务在三分支集成后统一执行一次 Go / frontend 全仓测试。

临时日志保存在系统 TEMP，主要文件：`weknora-t14-wiki-final-green.log`、`weknora-t14-router-repository-green.log`、`weknora-t14-ui-final-green.log`、`weknora-t14-typecheck-final.log`；更广失败：`weknora-t14-service-relevant-final.log`、`weknora-t14-modules-final.log`。

## 集成交接与界限

共享必要 hunks：repository/source_read.go 在 lease transaction 登记完整 Wiki alternatives；datasource_source_integration_test.go 向真实 DataSource service 传未启动 Scheduler；source_fixture_export_test.go 支持可选 extraFiles，HTTP 测试复用同一真实 fixture。000105 为 T14 保留编号；根集成将 A 的 inline GitLab URL 拼接替换为 source.GitLabBlobURL。不得修改源 publication 成功条件。

根任务待补跨分支公开 case：Generate → T04 真实 DataSource 排除全部文件 → 成功完整空 publication → 按旧 file + 当前 tag 范围读取 Wiki revision 原文；通用 reader 拒绝旧 version；clear 优先。本分支基点没有 T04 空发布能力，当前受控过滤历史用例保留，不把未跑的交叉用例计为已通过。

T14 未包含 T15 全仓 skeleton、T16 增量重生成、T17 长期预算 scheduler、T18 完整 history GC。大模块超过文件 / 字节预算时要求选更小模块；此处的初始 evidence owner FK 保护不替代后续 GC / clear 工作流。内部真实模型质量、私有 GitLab 端到端与代表仓库规模验收仍属于后续工作。

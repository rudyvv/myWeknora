# T04 实施与验收记录

Ticket：[GitHub #12](https://github.com/rudyvv/myWeknora/issues/12)。实施基点：T03 已验收提交 `461029f6c11d5ca87fc3397e2fdd25898b7f3046`。工作树：`source-incremental/WeKnora`；分支：`codex/source-incremental`。实现及针对性验收完成，已按用户确认的基点完成双轴审查：Standards 0 项，Spec 0 项；等待根任务集成后的统一验证、推送与 Issue 关闭。

## 已实现行为

连续固定提交的完整 manifest 与上一发布 manifest 对账，统计新增、变更、删除/排除和重命名。原路径成员沿用稳定 SourceFile / Knowledge 身份。仅在一个消失路径与一个新增路径之间存在唯一相同 Git blob 时判定 rename，保留原身份并记录 previous_path 与判断原因；歧义或无法确认的情况明确记为 delete plus add。原路径在 rename 后重新出现会取得新身份，不窃取已移动文件的标签、说明或版本。发布事务才更新 SourceFile 的当前路径及 Knowledge 元数据，用户说明和标签不被覆盖。

解析产物缓存键包含原始字节 SHA-256、逻辑路径/扩展名语言路线、worker health 的 grammar/parser 版本、受控结构/上下文/切块/token 版本及有效 rules_version。向量缓存键包含实际 SourceIndexText（路径、符号、分段上下文和正文）的摘要、模型 ID / 实际配置名称、provider/source、完整配置的摘要、模型 UpdatedAt 受控代数和实际维度。凭据只参与摘要，不写入运行/UI。相同 SHA 不跳过完整扫描与规则/模型核验；规则变化可重解析，实际向量文本与模型身份完全一致时继续复用向量。模型配置或维度改变会重新 embedding。

缓存限定 tenant/source，仅复用不可变解析结果和数值向量。每次快照仍生成新的 fileVersion、chunk、typed reference 及该次 snapshot/SHA/路径证据；不会把旧 chunk 的 SHA 元数据当成新成员。缓存本身没有发布可见性。当前实现仍创建每次快照的原文副本与索引记录，复用计数表示免去解析/模型计算，不表示存储去重。

准备与任一路失败期间继续保留上一发布指针。发布核验前一指针、完整成员、文件/块/向量数量、实际 BM25 就绪状态、数据库锁定的 KB/model 配置及模型摘要，再原子切换。首次接入仍需要至少一个受支持文件；已有发布允许所有文件被排除或删除后发布完整空版本。预览与保存排除规则均不直接删除旧文件或原文，只有新快照发布后退出当前查询。旧原文没有回收路径，T03 持久化读取租约仍可读取原快照；T18 的引用回收仍未实现。

已成功同步任务的重复投递根据持久化 tenant/source/sync_log 的 terminal published 快照幂等返回。ProcessSync 在源码分支先核验 payload tenant、log tenant/source 与 DataSource 身份；不把重复成功任务设为 running/failed，不覆写 DataSource 最新结果，不将新发布回退为旧 SHA。未完成任务恢复、同源租约与触发合并仍属于 T06。

运行 API/UI 展示当前处理 SHA、准备期间的上一发布 SHA、发布后的新 SHA、完整成员状态，以及变化、实际解析、复用文件/块、新向量/复用向量计数。成员标记 rename 判断、previous_path 和解析复用。仅发布的 parsed 成员可打开只读原文。

## 验收覆盖

| T04 条款 | 公开行为证据 |
| --- | --- |
| 完整增删改、过滤、确定/不确定 rename | IncrementalCompleteManifest、AmbiguousContentRename、RecreatedOldPath tests；真实 Git 创建/修改/删除/移路径 |
| 旧版本准备可读、失败保留、请求成员一致 | UpdateKeepsPublishedVersionDuringParsingAndVectorFailure；UpdateKeywordFailureRetainsPreviousCompletePublication；完整 T03 pinned reader/tools/HTTP 回归 |
| 解析/块/向量受控复用 | RepeatedCompleteSnapshotReusesParsingAndVectors；真实解析器 HTTP 次数及外部模型次数不增加；路径移动重解析/重 embedding；当次独立证据验证 |
| 同 SHA 的规则/模型/维度改变 | SameCommitChangedRulesAndModelUseControlledArtifactVersions；SameCommitDimensionChange；PendingEmbeddingModelChange tests |
| 预览排除、新空版本、保留原文与 UI | ExclusionPreviewCanPublishEmptyCompleteManifest；持久化读租约读取旧原文；真实 Vue 模板测试 |
| 两路实际索引、混合版本、重复投递与旧任务 | 独立 PostgreSQL/ParadeDB schema 全链；DuplicateSuccessfulDelivery、ReplayRequiresPersistedRunTenantAndSourceIdentity；T03 162 个混淆块在 topK 前范围测试 |

新增 12 项 T04 集成测试均经公开 DataSource / KB / source reader 服务观察结果。外部模型和 GitLab HTTP 可控；解析器是锁定真实 Python/grammar，Git 对象是真实本地 Git 经 upload-pack 获取，关键词与向量使用专用 PostgreSQL/ParadeDB。没有 mock 内部解析、检索或发布实现；没有通过缓存表字段定义测试成功。索引故障通过专用 fixture schema 的真实 BM25 索引注入，未修改共享测试容器。

## Red → green 记录

- 重复同完整快照：原行为模型调用从 1 增至 2；实现后模型与真实解析器调用均不增加，两路只返回新快照。
- 最后一个文件排除：原预览 CanSync=false；实现后预览允许完整空更新，发布后当前两路空，旧租约仍可读。
- UI 复用状态：真实模板缺“实际解析/复用文件/复用向量/已发布 SHA”断言失败；实现后 2 项源码 UI 测试通过。
- 成功投递重放：原行为违反 source_snapshots.sync_log_id 唯一约束并覆盖成功日志；实现后保持原成功运行且不会回退新的发布。
- 重放身份：原 tenant 不匹配 payload 返回 nil；实现后 source run identity mismatch，成功日志保持。

其他验收在这些纵向切片上加入完整变化、模型/维度和故障行为，复用已建立的公开真实运行链路。

## 验证结果

- 完整相关源码集成：`go test -tags integration ./internal/application/service`（选择 T02–04 Source、T03 Source reader/HTTP/问答和普通文档回归）通过，357.037 秒，记录 `.source-t04-integration.log`。包含新增 12 项 T04、两路失败、真实 parser 暂停、同 SHA 配置、维度 3→4、模型变更窗口及独立源身份。
- 完整相关模块：`internal/source`、`internal/datasource`、`internal/datasource/connector/gitlab`、`internal/application/repository`、`internal/application/repository/retriever/postgres`、`internal/types` 编译/测试通过，记录 `.source-t04-modules.log`。source 与 postgres 包无独立单元测试，其行为由上述公开真实索引集成覆盖。
- Source/DataSource 公开单元回归包含 4 项 Windows 环境失败，记录 `.source-t04-public-units.log`：3 项 SQLite 临时文件清理锁（DeleteSQLiteCleansUpAfterSoftDelete、DeleteKeepsCleanupStateWhenSoftDeleteFails、DeleteKnowledgeBaseCleansUpSQLiteDataSources），1 项 ReadFileCombinesSourcesWithoutGrantingHostAccess 的符号链接权限。handler 回归通过。未在基点独立重跑，不声称已经证明是基线失败，也不声称该批全过。
- 服务端 `go build -o .source-t04-server.exe ./cmd/server` 通过，记录 `.source-t04-build.log`。
- 真实 Vue 模板的 SourceSnapshotRunView / SourceCodeView：2 项通过，8.108 秒，记录 `.source-t04-ui.log`。
- `git diff --check` 无错误；只有既有 LF/CRLF 提示。
- 按根任务协调，全仓 Go/前端套件由最终集成工作树统一执行，本分支没有重复全仓套件。

## 集成接口与边界

迁移 `000104` 新增 tenant/source 内解析与向量产物表、run 变化/复用字段，移除 source_files 的路径唯一约束（路径可移动且可被新文件重新使用）。快照成员和 T03 chunk/ref/fileVersion 归属规则不变。down 迁移不会重新创建路径唯一约束，保留的 rename 历史可能合法冲突。

SourceSnapshotRepository 扩展 GetPublished、GetParsedArtifact、SaveParsedArtifact、GetEmbeddingArtifacts、SaveEmbeddingArtifacts、UpdateProgress。source.ParseJava、解析语言实现以及 T03 读取契约没有修改。与 B 分支重叠：datasource_source_sync.go 的 ParseJava 调用/语言准入，以及 datasource_source.go 的预览 Java 条件；集成应保留 B 的 ParseFile/语言路由与 A 的缓存、版本校验及已有发布空更新条件。C 分支可继续依赖每次新 fileVersion / snapshot / typed evidence。

仍保留首期 100 文件 / 16 MiB 总量限制，首次 Java 支持由 B 扩语言；未扩展 T12/T22 规模，也未完成 T05 force-push、T06 durable triggers、T18 GC。所有历史原文与旧索引现阶段保留，容量治理和证据 owner GC 属后续工作。真实内网 GitLab / 真实模型质量 / 代表仓库吞吐尚未验收。

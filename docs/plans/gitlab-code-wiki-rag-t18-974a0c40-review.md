# T18 974a0c40 双轴复审

冻结完整干净 SHA `974a0c40f4281a4fd2630067668bbaad84d472ed`，用户批准起点 `a1d11b956f2292290c70f08c1c18bdaa60ed8a39`。两位独立 Sol/high 审查者分别检查 Standards / Spec；根独立实际运行历史资源 PostgreSQL 和普通 Wiki 回滚反例。正式复审完成后才向原 Luna/xhigh 执行对话送达两项修复，冻结测试不混入后续 moving-tree 结果。

## Standards

硬性违例 0。精确证据 owner、事务内历史读取 pin、旧索引与原文寿命拆分、限制性 FK 和有界回收总体符合已批准接口合同。

判断性 P3 一项：导出的 RegisterSourceWikiAttemptEvidence / ReleaseSourceWikiAttemptEvidence 仅委托内部同名实现，可能存在 Middle Man。此启发式不阻塞，不要求扩大本票重构。

## Spec

- **P2：GC 最后 owner 释放的回收通知可能丢失。** `internal/application/repository/source_snapshot.go:786` 的 remaining 计数与删除候选不在候选行锁内。独立修订裁剪或读取 owner 释放在计数后、候选 DELETE 前执行 112 触发器 UPSERT；触发器不改变 claim_token，collector 随后删除新通知。原文仍存在却无 owner、无候选重试。违反 Spec §34 最后引用消失后的回收及 T18 的释放/回收要求。需串行化候选生命周期与释放，或用通知代数防丢失，检查发布/owner/候选锁顺序。此项为已核对可达事务交错，根本轮没有声称完成该竞态的实际屏障夹具；已要求执行者先用真实 PostgreSQL 红测固化。
- **P2：普通 Wiki 回滚改变原有来源规则。** `internal/application/service/wiki_page.go:426` 无条件复制历史 SourceRefs、ChunkRefs、PageMetadata；批准基线仅在历史修订或当前页面有 SourceProvenance 时复制。普通页面 nil/nil 路径因此被改变，超出 T18 源码证据回滚条款，并违反 Spec §34 普通文档 Wiki 仍沿用原有规则。根实际夹具已确认：v1 doc-old、v2 doc-current，回滚正文至 v1 后引用实际变 doc-old，预期保留普通文档当前引用 doc-current。

其余历史精确证据、运行 attempt pin、授权、旧索引退出、50 自动/200 全部修订窗口未发现新增确定性偏差。

## 根独立验证

- `TestSourceWikiRevisionLeasePinOutlivesPruneAndGCDropsOnlyOldIndex` 实际 PostgreSQL PASS 12.93s。
- `TestSourceWikiRollbackRevalidatesRestoredEvidenceAgainstCurrentSnapshot` PASS 8.77s。
- `TestSourceWikiRunningAttemptPinsExactRawVersionAfterReadLeaseExpiry` PASS 8.89s。
- `TestSourceWikiRevisionOwnersFollow50And200PostgresWindows` PASS 7.77s；四项包总计 42.703s，使用用户指定独立 localhost:57521/source_test 及锁定实际 parser。
- 根临时 overlay 的 `TestRootT18OrdinaryRollbackPreservesCurrentReferences` 实际 FAIL 0.03s，包 4.159s；夹具只在本地 Temp，不改冻结树。此为产品行为回归，非测试环境失败。
- 执行者已报告相关包编译与四项 PG 通过；额外全库编译的 sqlite-vec-go-bindings 缺 sqlite3.h 属未变环境限制，不安装无关依赖、不将失败算 PASS、不为本票重复宽套件。

## 决定

未通过，不集成、不关闭 #26、不派下一票。两项必修已交原 T06 执行对话的当前 T18 分支；只需新增竞态、普通回滚及受影响源码 GC/回滚的针对性验证，随后提交完整干净新 SHA。T17 可继续独立 ledger/113，最终证据 pin 接入仍等待 T18 验收。

Standards 硬 0 / 判断 1（P3）；Spec 2（P2）。累计仍 14/22。

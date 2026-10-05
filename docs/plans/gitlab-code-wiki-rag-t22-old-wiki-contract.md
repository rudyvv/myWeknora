# T22：同次问答的已验证 Wiki 卡片固定合同

用户于 2026-10-05 明确选择：旧问答继续读取已验证旧卡片。正式 T22 审查起点仍为 `1d32c08e`；本轮修复窄起点为已接受的 `bae1c65a`。这是 Hybrid 实际失败的产品修复，不改变新问答排除 stale、权限撤销及明确清除立即优先的合同。

## 决定

在创建源码读取租约时，先保存全部范围及固定发布快照，在现有 publication SHARE 锁释放前，以**同一个 MVCC 观察**捕获当时通过完整回答谓词的源码 Wiki 卡片。保存精确正文、标题/摘要/链接/类别等阅读字段、全部来源贡献及其适用快照、全部证据身份和原始版本所有权。捕获后的租约投影不可修改，不依赖后续 current row 或版本号相同的页面修订来还原。一个 SQL statement 的 materialized CTE 与原子写入优先；不得在默认 READ COMMITTED 下分多次读再拼装。

捕获须满足现有整页门禁：published、所有贡献 ready 且可归因、完整原始证据/范围/hash/所有权核验、每个贡献适用快照与本租约匹配。后来第一次验证成功的卡片不属于该旧问答。不存在可靠捕获证明时 fail closed，不回退到当前 stale 正文。

源码支持的 Wiki 回答搜索、分页/目录、单卡片、邻接、图与引用均复用这一固定投影。每次读取重新校验当前 KB 授权、活动租约、来源 query enabled/未清除、文件与标签范围、全部混合来源、原始证据完整性；当前发布可以不同，但来源必须仍可用。普通文档-only Wiki 保留现有读取路径。导航/编辑页不自动获得这个回答例外。

租约持有自己的原始证据 owner，页面修订裁剪不得使活动问答丢失正文或 raw。release、到期、清除沿用真实 CAS/GC 生命周期；新表 FK/cascade 和 existing evidence-owner GC 路径必须配合。不制造页面版本或改写 ADR-0010 的原始证据/适用性字段。

## 有界实现

捕获按租约全量、全有或全无，不静默截断：初始内部硬预算 1024 张源码支持卡片、16 MiB 序列化投影、32768 个证据 owner。所有指标按实际保存内容计算，含所有来源贡献；一次一致观察中判定预算。超限记录明确 `capacity_exceeded`，不保留部分卡片，不退回 stale，也不影响该租约原有源码 RAG。Wiki 回答路径必须给出可识别容量失败；普通文档读取保持原合同。这些是资源上界，不是代表仓库实测吞吐结论。

## 实施所有权

原 T06 对话实施：repository 的源码租约与 Wiki 公共读取模块、NEW `source_wiki_read_projection.go`、必要 types 内部投影、NEW 000119 up/down migration，以及已有 Wiki fixture 按真实 119 migration 接入。允许沿既有通用 readDB seam 修改 wiki_page/wiki_revision 查询，避免各工具另写授权 SQL。不得触碰 acceptance runner、批准题集、生产连接凭据或其他已绿测试断言。实现疑问直接问 root。

原 T10 对话随后独占 NEW `source_wiki_read_projection_review_integration_test.go`，根据实际公共 seam 编写独立门禁；不改 product/migration/shared fixture，不运行 PG。Root 保持唯一 PG57822 执行权并独立双轴审查，实施者不自行宣告整票通过。

## 必须证明

1. 租约创建时 READY，第一次 Wiki 读之前源码发布并 fence：旧问答搜索/阅读仍是精确旧正文和旧 raw。
2. 后续再生成正文：旧租约仍读旧正文，新租约读新 READY；同页同版本 applicability carry-forward 也不混淆。
3. 新发布后创建的租约拒绝旧 stale；旧租约拒绝在其捕获之后才第一次验证的卡片。
4. 当前撤销共享授权、来源 query disabled/清除立即阻断旧问答；混合 A+B 必须全部授权，文件/标签范围收窄有效。
5. 活动租约经历史 prune/GC 仍可读原证据，release 后真实 owner 清理；超限无部分泄漏、可观测容量错误、源码 RAG 不受影响。
6. 原 Hybrid 门禁及普通文档 Wiki 邻接用例正常通过，不放宽原断言。

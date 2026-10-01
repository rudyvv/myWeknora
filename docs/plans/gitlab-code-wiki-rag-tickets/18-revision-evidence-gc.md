# T18：[CodeWiki] 技术 Wiki 修订证据、回滚与引用回收

已发布：[Issue #26](https://github.com/rudyvv/myWeknora/issues/26)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；实现待审。

## What to build

用户阅读或回滚旧卡片时得到该正文的旧版本代码；现有版本窗口裁剪后无引用对象回收，而当前或其他引用仍可读。

## Acceptance criteria

- [ ] 修订来源/证据/验证版本与正文一起保存和回滚，再判断当前适用性，不继续使用当前页面的来源指向旧正文。
- [ ] 沿用自动/旧空来源 50、所有来源硬 200 的页面版本窗口，不解释为天数或无限归档。
- [ ] 当前快照、页面、保留修订和运行读取分别保护原文；旧证据独立于 Git 缓存/远端可达性并保持当前授权。
- [ ] 旧索引退出当前检索后不因页面历史无限保留；原文按有效引用去重，候选回收前复核，引用失败保留重试。
- [ ] 页面/修订裁剪、文件移除、任务结束和 KB 删除释放正确 owner，不误删其他授权引用仍使用的内容。
- [ ] 历史查看/回滚 UI 与公开来源 API、真实资源引用数据库夹具验证 50/200 边界、并发阅读和共享对象回收。

## Blocked by

- [Issue #22 — 单模块技术 WikiPage 的生成、证据校验与范围阅读](https://github.com/rudyvv/myWeknora/issues/22)
- [Issue #12 — 源码增删改、重命名与配置变化的完整版本更新](https://github.com/rudyvv/myWeknora/issues/12)

## Retention implementation seam

- `source_wiki_evidence_refs` owns exact raw versions for current pages and retained revisions. `source_read_wiki_evidence_refs` pins the exact body evidence under an authorized, expiring read lease; its copied page/revision/evidence identity and path let an already-started read finish if pruning wins afterward.
- `source_wiki_attempt_evidence_refs` owns exact `(attempt, file version, snapshot)` selections independently of ordinary read-lease expiry. Register it transactionally after evidence collection and before model calls; transfer to page/revision refs or release it in the same transaction as a terminal attempt status.
- Retiring a publication queues `source_snapshot_gc_candidates`. `SourceSnapshotRepository.CollectRetiredSourceVersions` is the bounded retryable collector, run at startup and by source-trigger reconciliation. It serializes with publication/read acquisition, removes old chunks/embeddings/relations/membership first, rechecks exact raw owners, and deletes raw versions/snapshot metadata only after the last owner releases. Owner-release generations wake dormant candidates without periodic owner polling, including releases racing collection; restrictive foreign keys remain the final safety fence.
- Historical evidence reads use the revision-owned exact raw row and current authorization; they do not require old search membership, embeddings, Git cache, or remote reachability. Unbind alone does not revoke existing knowledge; actual permission revocation or explicit purge remains authoritative.

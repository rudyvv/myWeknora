# T18 97f5b148 最终复审与验收

完整干净冻结 `97f5b148c24d966803fa339fd357e800989e124c`，用户批准起点 `a1d11b956f2292290c70f08c1c18bdaa60ed8a39`。两位独立 Sol/high 审查者分别复核完整票及七文件返修 delta，未要求 Luna 重复正式审查。

## Standards

文档化硬性违例 0。普通 Wiki 回滚恢复原有 provenance 条件。112 迁移及显式入队 helper 都维护 enqueue_generation；collector 在领取时捕获代数，在完成 owner 检查后才更新候选，保留并发通知，不引入提前候选锁。

判断性 P3 一项仍为导出 RegisterSourceWikiAttemptEvidence / ReleaseSourceWikiAttemptEvidence 转调内部函数的 possible Middle Man；非阻断，未要求扩大重构。未发现新增确定性 Standards 问题。

## Spec

Actionable finding 0，上一轮两项 P2 均已修复。普通文档 nil/nil 回滚保留当前引用、块引用及 metadata；源码页面/修订仍完整恢复对应证据并重算当前适用性。GC 保留 owner-blocked 候选为 dormant；释放事件增加代数并唤醒，释放发生在计数后、候选收尾前也不会丢通知。原 FK、授权、精确 raw owner、50/200 修订窗口及索引/原文寿命拆分继续保留。T17 完整恢复与页面 CAS 不扩进本票。

## 根独立验证

- 冻结树普通 Wiki 回滚实际 PASS 0.03s，包 4.247s，原 doc-old/doc-current 反例已绿。
- 冻结树真实 PostgreSQL 并发最后 revision owner 释放/候选唤醒/原文回收 PASS 11.82s，源码证据回滚与当前适用性 PASS 5.26s；包 21.240s。
- 无冲突合并至 `f2a6ae5ea434ba5e84929490f41d79c7cb687358`；自动合并保留 T13 的 SQL 检索投影及 T18 owner/回收意图。夹具同时加载 110/111/112。
- 合并后实际 PostgreSQL 历史 lease pin→裁剪→GC 保留原文并删除旧索引 PASS 11.03s，并发 owner 释放/回收 PASS 9.84s；包 25.354s。
- 合并后普通 Wiki 回滚 unit 包 PASS 4.529s。
- 初次根命令没有 integration build tag，仅运行普通回滚 unit；未将其包 PASS 冒充 PG 验证。随后使用正确 integration tag 实际完成上述冻结及合并 PG。既有上轮四项 PG/50-200 检查结果沿用，未重复宽套件。
- 执行者同一真实 PG 屏障先对旧无条件删除代码 red、恢复代数修复 green；根主动指导修正夹具候选选择，不能把 limit=1 处理了其他受保护快照误判为原文未回收。最终夹具限定目标退役候选并有界清理，根实际复验通过。

## 决定

双轴及必要真实合并验证通过，允许发布现有功能分支并关闭 #26，累计 15/22。T17 原批准 review base 仍为 `f040e5e8`，接入本次已验收集成及 exact attempt owner，再完成页面 CAS/恢复生命周期；不重新审已验收 T18。原 T06 执行对话待依赖已通过后接续下一张批准票，T15 仍被未验收 T11 阻塞，不提前派发。

Standards 硬 0 / 判断 1（P3）；Spec 0。

# T06 d56ab04c 双轴复审与独立验证

2026-09-30。干净冻结 d56ab04ca3e99fa4df22c0860b18d94cf72de12d，用户批准起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。diff `git diff 7f4fd1dc...d56ab04c`，提交239ba2dd、53344c50、d56ab04c，共35文件。Sol/high 两位独立 reviewer 检查完整冻结范围，功能修复仍由原 Luna/xhigh 对话执行。结论：未通过，Issue #14 保持 open，不集成或计为完成。

## Standards

硬违反2，判断性建议2；硬违反最严重P2，判断性建议最严重P3。

- P2：并发取消仍可被覆盖。datasource_repo.go:286 的非锁定 EXISTS 在日志 UPDATE 的 statement snapshot 读取 source_sync_states。配置失效事务取消日志并推进状态，旧结果先读到旧状态后阻塞，取消提交后仍可回写 success。违反 implementation-plan.md:106 的数据库租约/fencing 保护及:110 过期任务不能提交。应按统一锁顺序在同一事务锁定并核验当前协调状态后写日志。此影响状态，不声称绕过 publication 事务；原先顺序执行的缺陷已经修复。
- P2：旧 claimant 能取消当前配置运行。source_sync.go:230 在检查旧日志是否 canceled 之前，用 caller 曾加载的 ds 调用 invalidateSourceGeneration。配置A的延迟 claimant 在B保存并运行后可将 fingerprint 推回A、推进代数并取消B，随后自己返回 unclaimed。违反 implementation-plan.md:106 代数及串行保护。推进状态前须核验数据库中的最新配置。
- P3 possible Duplicated Code：source_sync.go:105 与:193 重复取消active/pending、推进代数/token、清除租约和指针。可抽取共同事务步骤。
- P3 possible Duplicated Code：datasource_service.go:1521 与:1551 重复状态/错误/结果和审计准备。可共享准备逻辑，保留各自 fencing 持久路径。

## Spec

wrong/partial3，missing0，scope creep0；最严重P1。

- P1：真实恢复路径漏掉失败和paused运行。T06 ticket:14 要求“崩溃、失效租约、队列重复及丢信号后对账恢复”。失败保存ds为error，手动paused运行保持paused（datasource_service.go:1521–1526）；scheduler启动:60–68和reconcile:208–220只FindActive，而 datasource_repo.go:143 排除这些状态。尽管retry_wait已持久登记，丢重试信号后无法自动恢复。新增回归只直接调用 RecoverSourceTriggers，另一个scheduler用例预先Resume，未覆盖实际路径。
- P2：结果fence仍有取消竞态。T06 ticket:11 要求“配置代数拒绝旧 worker 写入”。datasource_repo.go:286–294 的非锁定 EXISTS 与 source_sync.go:105–123 配置失效事务可形成旧statement snapshot回写；新增顺序测试先等Advance结束再Update，未覆盖等待锁的时序。应在同一事务锁并核验当前状态后回写。
- P2：延迟旧配置claim反向失效新运行。T06 ticket:11 的代数保护及:12“不无限取消已开始任务”。source_sync.go:226–231 用旧caller ds失效状态后才在:237–238检查旧日志terminal；真实保存B后，持有A的旧claim会取消B。这是当前新工作被取消，独立于旧日志复活。

其他五个旧finding已确认修复：typed Wiki outbox的事务接受与恢复/backoff、相同目标no-op、批准的T14预算归属/保留、stage加工身份不兼容拒绝、公开datasource错误/结果。T06仅持久接受源码Wiki待更新工作，T15/T16消费和生成的边界保持。

## Root独立取证

只使用用户批准的独立loopback数据库57521/source_test及已存在的Python/grammar缓存，无旧容器凭据读取，无修改共享服务。测试运行于d56冻结代码的临时导出；临时反例不是执行分支改动。

八项真实PG关键回归 PASS52.451s：顺序旧状态拒绝、失败持久恢复及摘要、加工/embedding身份拒绝、sameSHA no-op、typed接受与ack回滚/启动恢复/幂等、旧目标淘汰、config/source/KB/clear失效、租约并发fencing。

三个新增预期行为反例实际失败：

1. TestRootT06FailedRunSchedulerRecovery：active失败变error和paused两子场景，真实Scheduler.Start随后Stop，均0次重投递（期望1），6.49s。用真实公开ManualSync和ProcessSync产生失败及队列不可用。
2. TestRootT06ConcurrentCanceledRunCannotRevive：事务A真实AdvanceSourceConfig(enabled=false)保持未提交；B旧UpdateResult通过pg_blocking_pids确认正在被A锁阻塞；提交A后B返回nil，stored_status=success（期望ErrSourceSyncLeaseLost/canceled）。
3. TestRootT06StaleClaimCannotCancelCurrentConfig：ManualA/loadA、真实UpdateDataSource B、ManualB/claimB，再ClaimA；B从running变canceled。使用从持久run读取的正确delivery_generation。

后两项合计5.820s。初始临时夹具曾因接口类型断言、没有实际改变配置及误用delivery_generation而未有效测试；修正后上述失败明确来自合同断言，未将初始setup失败作为产品finding。

Worker20PG PASS146.368s及datasource/repository/service检查结果见原validation；四POSIX依赖测试在Windows排除的限制保留。本轮不声称全仓检查通过，也不因八项绿色抵消真实反例。

## 修复派发

三项必修问题、真实反例位置与锁/恢复合同已直接送回原T06对话，恢复Luna/xhigh修复。恢复枚举与创建默认定时任务的资格分开，保留paused，不复活cancel/clear/delete；状态事务统一state→log锁顺序；Claim/Register/Recover等不能将caller旧配置作为推进状态的权威。修复后交新干净完整SHA主动READY，等待下一次双轴复审。判断性P3不独自阻挡验收。

两轴计数独立保留：Standards硬2/判断2，最严重P2；Spec3，最严重P1。当前仍8/22通过。

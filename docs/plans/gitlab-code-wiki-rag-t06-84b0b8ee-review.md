# T06 84b0b8ee 双轴复审

冻结 `84b0b8ee4f1442866a912e5b5951bf27b1aa1bd7`，工作树干净；用户批准起点 `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d`。完整差异 37 文件、六提交。两位独立 Sol/high 审查 Standards/Spec。旧 `072086a0` 的两项 P2 原反例均已修。结论：**未通过，不集成、不关闭 #14**。

## Standards

硬性仓库规范违反 0。判断性 P3 possible Duplicated Code：`internal/application/repository/source_sync.go:148`、`internal/datasource/scheduler.go:153`、`internal/application/service/datasource_service.go:339` 多处重复解析配置并判源码模式；修改默认或错误语义需跨层同步。建议在合适的 datasource 边界集中共享谓词。此项不独立阻断验收。

## Spec

- **P2，已投递事件遇凭据轮换后失去有效 Wiki 信号。** `internal/application/repository/source_snapshot.go:458-466` 将唯一 event ID 和旧配置代数写入 `task_pending_ops` 并标记 `delivered`。凭据轮换增配置代数后，同目标 no-op 调 `EnsurePublishedSourceWikiUpdate`，但 `source_snapshot.go:350-352` 对 delivered 直接返回；旧 pending-op 仍携旧代数，新的 current-generation 通知缺失。按 Spec 第 31 项和实施计划的 Wiki 写入围栏，未来消费者不能用旧代数写页。须在已发布快照、当前源身份与租约有效时重挂可消费的新代数信号；保持幂等，且清凭据、删除、换快照不能复活。用真实 PG 覆盖 publication→relay→轮换→same-target no-op。
- **P3，成功 no-op 的公开运行阶段写为 failed。** `datasource_source_sync.go:208-226` 成功返回时未写 `published` 阶段；`source_sync.go:496-498` 在 release 时将所有非 published 阶段置 `failed`。因此公开日志 `status=success, source_run_phase=failed`。请在现有 no-op PG 测试读取公开 JSON 断言一致性。

两轴统计：Standards 0 硬/1 判断，最严重 P3；Spec 2，最严重 P2。两项 Spec finding 已派回原 T06 Luna/xhigh 对话，等待新干净完整 SHA。

## 独立验证

Root 在用户指定的独立 `localhost:57521/source_test` PostgreSQL 跑凭据轮换（relay 前）、失败重试和 B/C/D 追赶三个关键集成测试，`go test` PASS 15.743s；前端 `DataSourceSyncLogs.test.ts` 在捆绑 Node 24.19.0 下 PASS 1/1。普通沙箱的 `tsx` 因 `uv_os_get_passwd` 报 ENOMEM，切换获批沙箱外只读执行后通过。Worker 记录 30 个 PG 集成测试 PASS 183.182s；这不是 Root 全组独立结果。已投递→轮换路径尚无现成回归，代码路径与唯一性约束足以判定 P2，需在修复时新增实测。

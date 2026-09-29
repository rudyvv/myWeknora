# T06 96a4c418 双轴复审

冻结提交 96a4c41860bc727283a1166a145fbbb0cac2ecb7，用户批准审查起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。完整差异 35 文件、四提交。两位 Sol/high 审查员独立检查 Standards 与 Spec；未通过，Issue #14 保持 open，不集成。

## Standards

硬违反 1 项，最严重 P2；判断性建议 1 项，P3。

- P2：datasource_service.go:255 先持久化协调状态的新配置指纹 B，再于 :258 写数据源 B。两次事务之间崩溃，或写失败且沿用取消的请求 context 导致补救失败，会留下持久数据源 A、协调状态 B；source_sync.go:167、:586 拒绝未来触发与恢复。违反实施方案第 112 行的重启恢复和配置代数要求。需要以持久数据源为权威的启动/周期对账，不能复活已取消工作。
- P3 possible Duplicated Code：source_sync.go:122 和 :216 重复取消运行、推进代数与 fencing、清租约/指针的逻辑；可抽取共同事务步骤，不单独阻挡验收。

## Spec

错误/部分实现 1 项，P1；缺失 0、越界 0。

- T06 ticket 第 12 行要求崩溃、队列丢信号后的对账恢复。上述先围栏后写配置的崩溃窗口使同步永久停摆：预围栏清除 active/pending，RecoverAllSourceTriggers 只选择有指针的状态，手动与定时后续 Register 使用持久 A 时被 B 指纹拒绝。运行中写失败的 restoreSourceConfigurationFromStored 不能覆盖进程崩溃，也不能可靠使用已取消的 ctx。需要在真实 PostgreSQL 注入窗口并测试重启、自愈、后续手动同步。

此前三个审查问题，即 error/paused 的启动与周期恢复、并发取消结果 fence、过时配置 claim 反向取消新运行，当前代码已有对应修复和回归。迁移前无 state 但存在 queued 日志的旧兼容分支暂未计缺陷：目前没有找到真实可产生该前置状态的已有路径。

Worker 在独立 localhost:57521/source_test 报告 25 项真实 PG 集成 PASS105.294s，datasource/repository 和适用于 Windows 的 service 包测试通过；四项 POSIX shell 依赖测试被显式排除。Root 试图独立复跑关键五项，但执行对话开始修改同一工作树后，冻结性无法保证，故中止；不将其记为独立 PASS。结论由完整代码路径和双轴独立审查支持。新缺口已发给原 Luna/xhigh T06 对话，等待新干净完整 SHA。审查计数独立保留：Standards 硬1/判断1、最严重P2；Spec1、最严重P1。父任务仍 8/22。

# T06 704c1ed7 双轴复审

完整干净冻结 `704c1ed7d1e04237d6aee1cadb3c770dfc89a292`，用户批准固定起点 `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d`，七提交、37 文件。两位独立 Sol/high 审查完整差异。上轮已投递事件遇凭据轮换的旧代数与成功 no-op 阶段错误均有真实 PG 回归先红后绿。此轮仍**未通过**，#14 保持开放、不集成。

## Standards

硬性规范违反 1：`README.md:344` 要求 Conventional Commits，但七条中的六条提交标题无类型前缀。最终集成可由 root squash 成符合约定的提交，无须在执行工作树重写历史。判断性 P3 possible Duplicated Code：源码模式的 ParseConfig/ContentMode 判定散布 `internal/application/repository/source_sync.go:148`、`internal/datasource/scheduler.go:153`、`internal/application/service/datasource_service.go:339`，无效配置的处理语义存在分叉，可考虑共享谓词。

## Spec

**P2，已领取 op 原地刷新可丢失当前代数信号。** `internal/application/repository/source_snapshot.go:350-376` 在 delivered op 凭据轮换后沿用同一个 `task_pending_ops.id`，改写 payload 并把 `claimed_at` 设 NULL。`internal/application/repository/task_queue.go:247-317` 因此可将其重新 claim 给新消费者，旧消费者完成后仍可由 `DeleteByIDs:333-342` 按 ID 删掉当前代数 op。当前 T06 没有源码 Wiki 消费者，故这是与 T15/T16 集成时的确定性 handoff 竞态；T06 票据承诺可恢复有效 Wiki 信号，不能让旧 claim 删除新通知。需要真实 PG 夹具覆盖 claim→轮换→同目标 no-op→第二 claim/旧 ack，按代数独立身份或原子 claim 版本围栏修复，并保持重复 relay 幂等及生命周期取消。

两轴统计：Standards 硬1/判断1，最严重 P2；Spec1，最严重 P2。已把可复现路径交原 T06 Luna/xhigh 执行对话；等待新完整干净 SHA。原 704c 的 delivered-op（未领取）与 no-op phase 修复保留。

## 验证边界

Worker 报告 31 个真实 PostgreSQL 集成测试 PASS 228.550s、repository/datasource/service 包 PASS（service 排除四项 Windows 缺 POSIX sh 的无关测试）。Root 以独立 `localhost:57521/source_test` 跑三项关键 PG 回归，结果仍在进行；结束前不记为 PASS。现有集成测试只覆盖未领取 op，缺并发 claim/旧 ack 的反例。

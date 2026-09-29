# T06 072086a0 双轴复审

冻结 072086a0b3e9de15eb2a03834ab3a1e50955f7c2；用户批准起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。完整差异 35 文件、五提交。Sol/high 两位独立审查 Standards/Spec；未通过，Issue #14 保持 open，不集成。

## Standards

硬违反 0；判断性 1，P3 possible Duplicated Code：repository/source_sync.go:147 的 sourceModeEnabled 与既有 ContentMode 逻辑平行判定模式，规则变化时可能分叉。初评所称“升级前无 state 的 queued 日志无法恢复”为假设兼容路径；批准基点 7f4 没有 queued 生产者，旧 ManualSync 创建 running、入队失败转 failed，新注册原子写 log+state，故撤回硬违反，不将不可达状态夸大为缺陷。

## Spec

错误/部分实现 2 项，均 P2；缺失 0、越界 0。

- 发布后 Wiki 更新信号活性：source_snapshot.go:215–249 原子写 publication/outbox；凭据轮换在 relay 前增配置代数但当前发布快照仍有效。relay 在 source_snapshot.go:311–312 仅凭旧代数把唯一事件 superseded；同 SHA/manifest/rules/processing/embedding 的后续同步在 datasource_source_sync.go:208–224 直接 no-op success、不再 Publish 或插入 outbox。当前快照因而永无 Wiki pending-op，违反 T06“发布事务后信号仍可恢复投递”。修复须同时维持取消、clear、删除、旧快照不可再通知；Wiki 生成消费仍属 T15/T16。
- UI 未区分首次待配送、追赶、失败重试。首次手动入队/入队失败与失败后持久 retry_wait 均为 queued，但 DataSourceSyncLogs.vue:287–290 和多语言文案统一显示“等待追赶”。票据要求显示运行、等待追赶、失败/重试；目前用户无法判断是否重试。需以持久协调 phase/活动关系产生公开展示状态，并用公开状态测试验证。

上轮配置预围栏→崩溃缺口已由 RecoverAll 扫描所有有 coordinator 的 live source、按持久配置对账及四项 PG 回归修复；删除/禁用/清凭据未见旧触发复活。Worker 报告 29 项真实 PG 集成 PASS128.934s、相关包通过，四项 POSIX shell 测试在 Windows 排除。Root 本轮仅静态复核新失败路径，未将 worker 测试写成独立 PASS；新两项需真实 PG/UI 反例。已派回同一 Luna/xhigh T06 对话，等待新干净完整 SHA。Standards 0硬/1判断，最严重P3；Spec2，最严重P2。父任务仍8/22。

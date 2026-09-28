# T06：[CodeWiki] 手动与定时源码更新的串行、追赶和重启恢复

已发布：[Issue #14](https://github.com/rudyvv/myWeknora/issues/14)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

手动和定时触发进入同一持久流程，同源串行加工并追赶最新提交；服务重启后可以继续，不重复发布或被旧任务覆盖。

## Acceptance criteria

- [ ] 默认每小时可配置检查与手动触发持久登记；同源租约/fencing 和配置代数拒绝旧 worker 写入，不只依赖 TaskID。
- [ ] 正在处理 B 时 C/D 合并为最新待追赶目标；B 可发布后追赶 D，不要求发布每个中间提交，不无限取消已开始任务。
- [ ] 完整发布与后续 Wiki 信号采用事务 outbox 或等价持久 pending-op，事务成功后信号仍能恢复投递。
- [ ] 阶段完成记录可重用；崩溃、失效租约、队列重复及丢信号后对账恢复，既有预算计数不重置。
- [ ] UI 显示运行、等待追赶、失败/重试和最后成功，不把入队当成索引发布。
- [ ] 用公开触发/API 和真实 PostgreSQL 并发/重启故障夹具验证，仅产生预期发布及有效更新信号。

## Blocked by

- [Issue #12 — 源码增删改、重命名与配置变化的完整版本更新](https://github.com/rudyvv/myWeknora/issues/12)

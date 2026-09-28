# T21：[CodeWiki] Docker 离线部署、资源额度与运行观测

已发布：[Issue #29](https://github.com/rudyvv/myWeknora/issues/29)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

运维人员在标准 Docker 环境运行完整源码链路，观察阶段消耗与故障，解析或容量压力不会挤垮普通文档或破坏已发布证据。

## Acceptance criteria

- [ ] 解析 Python/官方 Node/grammar 在构建阶段锁定预置，离线运行；Lite 可连同一外部服务，不强塞全部运行时进单应用。
- [ ] 解析进程、传输、文件/运行、源码/文档/Wiki/Embedding 并发及共享模型额度受限，超时/取消可见并保留旧发布。
- [ ] 缓存、staging、原文、向量容量分别统计和处理；无可回收空间明确失败，不截断完整扫描或删除受保护原文。
- [ ] 运行界面显示检测/目标/发布 SHA、阶段计数、质量、Wiki 覆盖和最后成功；指标含阶段耗时、复用、token/调用、租约恢复和清理残留。
- [ ] 日志不泄露凭据/整份源码，RPC 不能读任意服务器路径；内部 CA/连通、实际模型限制在部署预检记录。
- [ ] 用受限 Docker 端到端夹具演示解析崩溃/内存压力/磁盘满/重启恢复和普通文档回归，不宣称未经测量的性能。

## Blocked by

- [Issue #14 — 手动与定时源码更新的串行、追赶和重启恢复](https://github.com/rudyvv/myWeknora/issues/14)
- [Issue #20 — 超大源码与模板配置的有界切块和质量展示](https://github.com/rudyvv/myWeknora/issues/20)
- [Issue #25 — Wiki 累计预算、并发编辑与生成恢复](https://github.com/rudyvv/myWeknora/issues/25)
- [Issue #27 — 暂停、解绑与明确清除仓库知识](https://github.com/rudyvv/myWeknora/issues/27)

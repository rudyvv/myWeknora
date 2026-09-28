# T17：[CodeWiki] Wiki 累计预算、并发编辑与生成恢复

已发布：[Issue #25](https://github.com/rudyvv/myWeknora/issues/25)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

卡片生成遇到瞬时失败、服务重启或人工编辑时能够有限恢复，预算不会被重置，也不会覆盖更新后的正文。

## Acceptance criteria

- [ ] 生成、检查、修复、瞬时重试及嵌套任务共享持久调用/token/时间消费，卡片初次后最多两次修复。
- [ ] 模型预检固定有限额度；18 次调用为当前建议起点而非无限子任务许可，骨架/整批也统计全部子任务。
- [ ] 达到预算保留草稿和原因，手动重试建立新的有限尝试，不回滚源码发布。
- [ ] 写页核验源代数、目标 published 与基础 page.version；并发人工/其他源编辑后读新正文合并且计入预算。
- [ ] 不增加人工保护区/审批，也不保证人工段落永久不被调整；UI 仍能编辑、查修订和理解失败原因。
- [ ] 受控模型与真实持久运行夹具覆盖崩溃、限流、预算耗尽、旧目标写入和并发编辑，公开工具只读合格当前页。

## Blocked by

- [Issue #22 — 单模块技术 WikiPage 的生成、证据校验与范围阅读](https://github.com/rudyvv/myWeknora/issues/22)
- [Issue #14 — 手动与定时源码更新的串行、追赶和重启恢复](https://github.com/rudyvv/myWeknora/issues/14)

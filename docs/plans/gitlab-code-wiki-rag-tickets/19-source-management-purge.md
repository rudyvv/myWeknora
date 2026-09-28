# T19：[CodeWiki] 暂停、解绑与明确清除仓库知识

已发布：[Issue #27](https://github.com/rudyvv/myWeknora/issues/27)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

管理员可停止连接但保留知识，也可明确清除某仓库的源码、索引和历史贡献；其他来源的独立知识继续保留。

## Acceptance criteria

- [ ] 暂停/解绑停止新调度并使旧回调/写任务失效，保留已有快照、技术卡片及引用可读。
- [ ] 明确清除是独立操作，先撤销源的查询范围并递增代数，再持久异步清理；旧 worker 不能复活内容。
- [ ] 清除源码、索引、历史证据/缓存引用与该源页面贡献；pin 不永久阻止管理员清除。
- [ ] 多源当前页仅保留其他来源独立支持的正文；包含被清除源内容/证据的修订删除，不保留旧正文只断引用。
- [ ] 撤回期间阻止受影响页继续返回被清除内容，失败显示待清理且可恢复，不能恢复旧查询范围。
- [ ] 公开管理/UI、当前/历史工具阅读及故障重试验证隔离；KB 删除不误删其他有效授权 owner。

## Blocked by

- [Issue #14 — 手动与定时源码更新的串行、追赶和重启恢复](https://github.com/rudyvv/myWeknora/issues/14)
- [Issue #24 — 源码变更驱动受影响技术卡片更新](https://github.com/rudyvv/myWeknora/issues/24)
- [Issue #26 — 技术 Wiki 修订证据、回滚与引用回收](https://github.com/rudyvv/myWeknora/issues/26)

# T04：[CodeWiki] 源码增删改、重命名与配置变化的完整版本更新

已发布：[Issue #12](https://github.com/rudyvv/myWeknora/issues/12)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；实现、集成验收及双轴审查完成，2026-09-29 已推送并关闭 Issue。

## What to build

管理员更新仓库或过滤规则后发布新的完整源码版本，旧版本在准备期间可用，删除文件退出当前检索，未变产物被复用。

## Acceptance criteria

- [x] 连续完整清单覆盖新增、变更、删除和过滤变化；可确定 rename 延续源文件身份，否则删除加新增并说明判断。
- [x] 解析/索引暂存期间仍读上一完整版本，任一路失败保留旧指针，成功切换后所有工具只读本次请求成员。
- [x] 未变块和向量按内容、实际 embedding 文本、解析/规则/模型受控版本及维度复用；路径/签名变化不能盲目按 blob hash 复用。
- [x] 同 HEAD 的过滤/规则/模型配置变化可重新加工，不把 SHA 相同视为全部工作已完成。
- [x] 排除单文件通过预览和新快照生效，不直接删除现有源文件及受引用原文；处理计数、复用和 SHA 状态在 UI 可见。
- [x] 用真实两路索引验证混合新旧版本、重复触发、删除和更新中查询；读任务及已登记证据所需旧原文不被回收。

## Blocked by

- [Issue #11 — 多仓库源码查询范围与同次问答快照一致性](https://github.com/rudyvv/myWeknora/issues/11)

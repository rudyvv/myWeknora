# T22：[CodeWiki] 代表仓库检索、Wiki、增量及性能验收

已发布：[Issue #30](https://github.com/rudyvv/myWeknora/issues/30)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

用代表仓库与四语言补充语料完成整体验收，提交可复核的检索、引用、卡片、恢复和资源报告，作为源码模式发布依据。

## Acceptance criteria

- [ ] 固定 30 个人工核验问题，10 符号/路径、10 业务链、10 前端/SQL；至少 27 题 top10 找到正确证据，不误称完整 Recall。
- [ ] getPushSchedule 与 FreeTutor 两链、巨型 Java/Mapper、Vue 2、独立 TS/Python 语料均验证，重要卡片说明有语义支持且原文/SHA/path/行号一致。
- [ ] 全量以过滤预览实际范围为准，规划约 3,844 个自有文件/125 万物理行，不把行数当 tokens 或承诺完成时长。
- [ ] 测量全量及 1/10/100 文件增量的分阶段耗时、峰值内存、块/token/模型消费，与同预算文本基线比较并记录实际硬件/模型限制。
- [ ] 增删改/rename/force-push、异常鉴权/分支、双索引故障、重复/持续通知、崩溃、旧 Wiki 写入、历史/GC/清除专项矩阵通过。
- [ ] 任何范围外/非请求发布成员内容混入、精确位置错误或无界模型恢复均视为失败；原文档和三种 Agent 回归通过。
- [ ] 提交可重复验收步骤及未完成限制，性能阈值依实测固定；无凭据/整仓源码进入报告，现场 Hook 连通结果单独注明。

## Blocked by

- [Issue #13 — Force-push、分支与鉴权异常的源码对账](https://github.com/rudyvv/myWeknora/issues/13)
- [Issue #16 — Python 结构检索和原始位置引用](https://github.com/rudyvv/myWeknora/issues/16)
- [Issue #21 — 代码标识符与路径检索的排序和融合](https://github.com/rudyvv/myWeknora/issues/21)
- [Issue #28 — 可选 GitLab Push Webhook 触发与定时对账](https://github.com/rudyvv/myWeknora/issues/28)
- [Issue #29 — Docker 离线部署、资源额度与运行观测](https://github.com/rudyvv/myWeknora/issues/29)

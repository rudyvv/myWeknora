# T14：[CodeWiki] 单模块技术 WikiPage 的生成、证据校验与范围阅读

已发布：[Issue #22](https://github.com/rudyvv/myWeknora/issues/22)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；实现与本分支相关验收已完成，Standards / Spec 最终复审均 0 项剩余，待根任务集成。详见 [T14 验收记录](../gitlab-code-wiki-rag-t14-validation.md)。

## What to build

对一个小模块生成第一张可读技术卡片，重要说明追溯到服务端证据；引用或质量失败保留草稿，源码检索照常可用。

## Acceptance criteria

- [x] 主题带源/模块身份，不按同名类跨仓库误合并；不为每个文件自动生成摘要或独立页。
- [x] 收集受限固定版本证据，模型只返回已提供的 evidence ID；核验 hash/SHA/原文区间并执行独立语义 QA。
- [x] 合格卡片接入原 Wiki 搜索/阅读及应用内源码引用；草稿、失败和未验证适用的过期页不参与当前回答。
- [x] 从首张页开始保存与正文对应的来源/证据和修订字段；编辑/回滚体验沿用原 Wiki，当前页/修订登记必要原文保护。
- [x] 含源码贡献的正文、摘要/关联摘要/目录/历史只在全部实际来源满足请求范围时整页返回，不自动裁剪；普通文档 Wiki 保持原行为。
- [x] 生成有有限调用/token/时间上限和最多两次修复，失败不覆盖就绪页或阻塞源码发布；可从 UI 看到原因并启动有界手动重试。
- [x] 通过 Wiki 公开工具与来源阅读 API 验证虚构证据、范围外来源、更新后适用性和原三种 Agent 配置。

## Blocked by

- [Issue #11 — 多仓库源码查询范围与同次问答快照一致性](https://github.com/rudyvv/myWeknora/issues/11)

## 验证状态

相关公开 Wiki / HTTP / Agent / source reader 与 UI 验证已完成；2026-09-29 实现冻结且两轴最终复审通过。宽相关包的 Windows 失败、跨 T04 空 publication 待集成 case 与全仓统一验证边界详见验收记录，不宣称全仓全绿。

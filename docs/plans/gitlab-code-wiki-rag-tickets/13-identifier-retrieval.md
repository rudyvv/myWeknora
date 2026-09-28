# T13：[CodeWiki] 代码标识符与路径检索的排序和融合

已发布：[Issue #21](https://github.com/rudyvv/myWeknora/issues/21)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

用户以方法名、限定符号、Mapper ID、路径或中文业务问题检索时，得到当前允许源码的可靠关键词与向量结果。

## Acceptance criteria

- [ ] 完整标识符与 CamelCase/snake_case/限定名/路径段可检索，中文业务与代码字段分别分析，不改原文。
- [ ] 精确符号/路径匹配、BM25 和向量按现有融合组合，可选 rerank 失败可退回有效结果。
- [ ] 真实 ParadeDB 两路范围在 topK 前生效，不能因旧/staging 或其他仓库候选占位损害允许范围命中。
- [ ] 工具及 UI 展示证据与必要签名/上下文且不超 token 上限；普通文档分词和结果不回归。
- [ ] 人工核验标识符/路径/自然语言问题及同预算文本基线，明确记录候选命中效果而非完整 Recall。

## Blocked by

- [Issue #11 — 多仓库源码查询范围与同次问答快照一致性](https://github.com/rudyvv/myWeknora/issues/11)

# T08：[CodeWiki] Python 结构检索和原始位置引用

已发布：[Issue #16](https://github.com/rudyvv/myWeknora/issues/16)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。已完成并关闭（2026-09-29）；集成提交 `7f4fd1dc`，见 [验收记录](../gitlab-code-wiki-rag-t08-validation.md)。

## What to build

将 Python 模块、类、函数、方法和装饰器纳入同一同步、检索和阅读链路，让常用 Python 仓库也能建立源码知识。

## Acceptance criteria

- [x] 预装锁定 Python grammar，模块/类/函数/方法/装饰器及父结构保留原文位置，不运行导入或项目代码。
- [x] 独立语料覆盖缩进、多行字符串、装饰器、async、Unicode/CRLF 与语法错误，降级状态可见。
- [x] 发布后的关键词/向量查询、证据阅读及 UI 质量状态贯通；只读和版本范围沿用公共规则。
- [x] 不把 language-pack 未提供的 Python 框架语义误写成通用装饰器语法已被充分理解。
- [x] Java/JS 等已有语言和普通文档行为不回归，候选库能力验收记录可复现。

## Blocked by

- [Issue #10 — Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10)

# T07：[CodeWiki] JavaScript 与 TypeScript 结构检索和原始位置引用

已发布：[Issue #15](https://github.com/rudyvv/myWeknora/issues/15)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

将 JS/TS 模块、函数、类和类型纳入同一源码同步链路，用户可按符号或自然语言检索并阅读固定版本原文。

## Acceptance criteria

- [ ] 预装锁定 JS/TS grammar，支持模块、函数、类、导入/导出、类型声明和父结构，质量与位置沿公共结构契约。
- [ ] 常见语法、Unicode、CRLF、装饰语法和坏语法明确验收，错误区域有质量标记且可读内容不静默丢失。
- [ ] 实际同步、双索引、查询、工具输出和原文阅读贯通，源文件仍由 Git 管理，不使用目标项目插件。
- [ ] TS 用独立人工核验语料；代表仓库没有 TS 不能宣称已验证其结构能力。
- [ ] 新语言无需改变已发布快照/范围语义，现有 Java 和普通文档回归通过。

## Blocked by

- [Issue #10 — Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10)

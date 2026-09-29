# T03：[CodeWiki] 多仓库源码查询范围与同次问答快照一致性

已发布：[Issue #11](https://github.com/rudyvv/myWeknora/issues/11)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。已实现并通过针对性验收与双轴复审；[验收记录](../gitlab-code-wiki-rag-t03-progress.md)列明全量测试限制。

## What to build

用户按仓库、文件和标签检索源码，同一问答的搜索、阅读和补全始终使用同一组发布快照，其他范围和暂存内容不会进入结果。

## Acceptance criteria

- [x] 关键词与向量在 topK 前共同限制租户/共享 KB、仓库、文件/tag 和已发布成员；真实索引混淆集及查询计划证明不是返回后过滤。
- [x] 请求固定源快照并保护读取，搜索、grep、块列表、文档信息、分页、相邻/父块/关系补全和引用输出都使用同一范围。
- [x] 当前权限变化和明确清除优先于读取保护；直接持块/原文句柄不能绕过授权，未授权内容不出现在结果或摘要。
- [x] 多个源中同名类/路径不会互相替代，返回身份可区分；普通文档与源码可混合而不改变原文档检索行为。
- [x] 公开写接口和 UI 对 Git 管理的源文件执行只读约束；跨库接入使用独立源，原文件标签与描述管理仍有效。
- [x] 用 RAG Agent 的公开工具调用及内部代码阅读 API 验证所有范围路径，记录对代码来源的漏路径回归。

## Blocked by

- [Issue #10 — Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10)

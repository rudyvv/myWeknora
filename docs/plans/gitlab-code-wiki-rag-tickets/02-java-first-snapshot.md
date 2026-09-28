# T02：[CodeWiki] Java 小范围首次同步、双索引检索与固定版本代码阅读

已发布：[Issue #10](https://github.com/rudyvv/myWeknora/issues/10)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。已实施并通过双轴复审；验证及环境限制见 [T02 验证记录](../gitlab-code-wiki-rag-t02-validation.md)。

## What to build

对明确选择的小型 Java 文件范围完成一条真实链路：手动同步固定 SHA，经独立解析服务形成块和双索引，发布后搜索方法并在应用内阅读原文。

## Acceptance criteria

- [x] 用锁定离线 grammar 的 Python/Tree-sitter 解析类、方法、签名和注解，专用结构接口传内容/hash而非任意主机路径或凭据。
- [x] 保留原始编码/字节与一基行号，区间可由原文验证；索引文本与附加上下文不冒充一个连续原文片段。
- [x] 稳定源文件身份、不可变文件版本和完整成员清单可从运行/查询结果观察；Go 不执行目标仓库、构建、过滤器或插件。
- [x] 关键词、向量准备完成才原子发布；任一路失败无首次发布，staging 不可由搜索/grep/块列表等公开入口读取。
- [x] 发布检索与阅读满足基础 tenant/KB/文件/tag 范围并返回仓库、SHA、路径、行号、符号与质量；同 SHA GitLab 外链可见。
- [x] 源文件原文/块的普通编辑、重解析、开关、删除和跨库移动/复制服务端拒绝；标签/说明仍管理，不自动每文件生成 Wiki。
- [x] 标准 Docker 的最小解析组件和 UI 运行状态能演示这条小范围链路；真实 PostgreSQL/ParadeDB 加受控 Embedding 验证，不用内部解析 mock 代替真实结构验收。

## Blocked by

- [Issue #9 — GitLab 源码模式配置与固定提交过滤预览](https://github.com/rudyvv/myWeknora/issues/9)

# T12：[CodeWiki] 超大源码与模板配置的有界切块和质量展示

已发布并关闭：[Issue #20](https://github.com/rudyvv/myWeknora/issues/20)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。已验收，集成提交 `fa0dcad79646dcd08dd39dfe17cfc8eeaaf185b9`；验证与限制见 [审查记录](../gitlab-code-wiki-rag-t12-f94b4a08-review.md)。

## What to build

超大 Java 方法、Mapper XML、页面模板和配置在完整覆盖下可检索；结构识别失败可见降级，不可读内容明确失败。

## Acceptance criteria

- [x] 方法/statement 超预算按内部结构继续拆分，超大叶子/注释/错误区域明确降级，覆盖摘要可与原文核对。
- [x] HTML/JSP/FreeMarker、样式、YAML、Dockerfile 等纳入范围文本具可验证块及位置，不承诺全部嵌入语义。
- [x] 按实际 Embedding tokenizer/限制控制正文和索引头，路径/签名上下文分别计入上限；不把 bytes 当 token。
- [x] 编码转换、Unicode/CRLF、原缩进和不连续片段保持可信坐标，不格式化重构后当原文。
- [x] 可读降级文件仍可完成索引并显示质量；未明确排除的不可读文件阻止完整发布且保留旧版本。
- [x] 代表仓库巨型 Java/Mapper 与混合模板压力夹具贯通 UI 质量、同步、双索引和阅读，不一次把整仓传给模型。

## Blocked by

- [Issue #10 — Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10)
- [Issue #18 — Java Mapper 与 MyBatis XML/SQL 关联检索](https://github.com/rudyvv/myWeknora/issues/18)

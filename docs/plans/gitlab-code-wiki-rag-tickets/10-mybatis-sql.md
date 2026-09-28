# T10：[CodeWiki] Java Mapper 与 MyBatis XML/SQL 关联检索

已发布：[Issue #18](https://github.com/rudyvv/myWeknora/issues/18)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

用户从 Java Mapper 方法或 SQL statement 查询到对应 XML、结果映射与可确定数据访问，并阅读准确固定位置。

## Acceptance criteria

- [ ] 成熟 XML 解析提取 namespace、statement ID、include/resultMap 和可确定表访问，禁用外部实体/网络解析。
- [ ] Java Mapper 接口与 XML statement 有可核验关联，重复 ID/namespace 或缺失目标保持不确定和质量提示。
- [ ] 动态 SQL 不当作完整运行 SQL，嵌入 Java SQL 在可确定时提取，未知分支不生成确定关系。
- [ ] XML/SQL 原文区间、索引字段、关联上下文与引用阅读贯通，不以只有起始行的解析结果猜测结束范围。
- [ ] 代表仓库 PushSchedule 两个 Mapper 及 FreeTutor 复杂 SQL 可验证；超大 XML 不丢失 statement。

## Blocked by

- [Issue #10 — Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10)

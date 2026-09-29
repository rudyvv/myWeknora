# T10 parser 检查点与 T09 接口决定

2026-09-29。本记录不代表 T10 整票验收；Go 生产链路仍由执行对话接线。总协调继续使用 GPT-6 Sol / high 规划与审查，三个执行对话使用 GPT-6 Luna / xhigh。T06、T09 的实现和审查起点 7f4fd1dc 已由用户明确确认。

T10 审查冻结提交：36d5ffff7a95c085973b7b6c808c1ec405676a4b。固定用户批准起点：3adaa587d16651a6d48f123d73c1e5cceaf75271。完整 diff 为 `git diff 3adaa587...36d5ffff`；提交 fc826c19、fd61229c、132ae696、68305937、36d5ffff。两个独立 reviewer 仅检查冻结 blob，未使用执行对话继续编辑的文件，也未修改其工作树。

## Standards

Parser 文档硬违反 0；判断性 finding 2。原注解类型遮蔽的硬违反已修复：源文件中声明的短名称在接受官方导入前被拒绝并带诊断。原始字节覆盖、SQLGlot 固定依赖和独立 parser 分工保留。旧 Go 语法模块移除和生产接线仍属于已知未完成工作。

- P2 possible Primitive Obsession：mybatis_parser.py:331 将嵌套 CTE 名称扁平为字符串集合，349/360 按裸 table.name 排除实体表，丢失 qualification 与词法作用域。建议保留限定名与真实 scope/source；此轴为静态正确性推断，没有执行反例。
- P3 possible Duplicated Code：511–542 在 statement / fragment 重复 SQL 解析、降级诊断、placeholder 过滤及 table fact 创建。建议共享 helper 并显式传递 owner 与 dynamic context；该局部建议不是文档硬规则。

## Spec

Parser checkpoint 剩余 finding 2，均 P2；未发现新增范围膨胀。原两个 P1 反例已修复：嵌套自定义注解不再生成假 SQL，两类 multi-table DELETE 与 UPDATE JOIN 保留物理表并排除目标别名。

- P2 注解遮蔽判断扩大到整个文件：95–102/150 的全局 declared_types 导致 Good 中合法导入官方 Select 被无关顶层 Other 的成员 @interface Select 误伤。违反 T10:13“嵌入 Java SQL 在可确定时提取”。根据 [JLS §6.3](https://docs.oracle.com/javase/specs/jls/se21/html/jls-6.html#jls-6.3)，成员类型范围属于其包含类型；要求保留真实 enclosing scope 并验证兄弟类型与实际遮蔽两个方向。
- P2 CTE 全局名字过滤遗漏实体表：`WITH orders AS (SELECT * FROM staging) UPDATE db.orders SET x=(SELECT MAX(id) FROM orders)` 只返回 staging；嵌套 CTE 也可能屏蔽外层 target。冻结 runtime 将其报告为 structural、无诊断。违反 T10:11“可确定表访问”；要求带限定名、nested scope 和真实 CTE 引用的回归。[MySQL WITH 文档](https://dev.mysql.com/doc/refman/8.4/en/with.html)支持 UPDATE 与分层作用域。

Spec reviewer 独立执行冻结 parser 探针，验证旧 P1、CTE/derived/function 基础排除、Unicode/CRLF/BOM 原文位置，以及 1,200 statements / 1,200 table facts / 全字节覆盖。31 项 HTTP tests 是 worker 记录，两位 reviewer 均未重复执行该整套测试。公开检索/引用贯通、代表仓库验证仍待 Go 生产接入，不能据此关闭 #18。

两轴计数：Standards 0 硬违反、2 判断性 finding（最严重 P2）；Spec 2 finding（最严重 P2）。总协调已派发全部修复，并补正派发消息中一处把 CTE 写成 P1 的笔误，没有改变两轴原评估或合并排序。

## T09 合同决定

执行对话可按 additive JSON 合同继续，不加新 migration：SourceRegion 含 Kind / Language / Quality，以及可选 ExternalSource / ExternalStatus / ResolvedPath；ParsedSourceChunk、SourceEvidence、SourceSymbol 上可选 region，使用已有 symbols JSONB 持久化 sfc_region marker。Kind 为 template / script / style / custom，tag/gap chunk 可无 region，原文件全字节覆盖仍必需。

Marker、签名和 chunk 使用实际 UTF-8 body/range，Go 继续核验原文坐标。官方 Vue 2.7.16 parser 在独立固定 Node package 中运行，关闭 pad/deindent；JavaScript UTF-16 offset 映射到原 UTF-8 字节，不执行仓库脚本、插件或编译。

ExternalSource 仅原文件 src 字面引用；Go 只在同来源、同固定快照的准入成员内匹配。外部源码依其独立 fileversion/evidence 与既有授权读取，禁止拼成 .vue 连续代码证据。manifest 准入不等于当前问答 file/tag 权限；公开阅读和检索证据中的 ResolvedPath / ExternalStatus 先核验目标是否属于同一组合范围，不获准与未知统一输出原引用及 unchecked，不泄露目标存在性。只有目标在当前范围内才输出固定快照的 resolved / missing。

五语言现有 fingerprint 保留，Vue 官方组件版本及提取规则纳入新增能力版本；必须覆盖同 SHA 缓存重建和 scoped external 公开回归。T10 关系表由 T10 管理，T09 通过独立 region helper 接线，公共类型冲突由总协调集成。
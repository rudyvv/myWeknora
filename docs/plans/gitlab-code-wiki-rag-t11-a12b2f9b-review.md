# T11 a12b2f9b 返修复审

root 核对干净提交 `a12b2f9b4d4759e7e9a29f320f7e1d8afc2ab3d4`；批准起点 `04d99577814d3786b7e004f81124885638dffced`。两个独立 Sol/high 子 Agent 分别检查 Standards 与 Spec，比较完整票范围并重点检查相对 `546bd413` 的返修；root 复验精确代码。worker 的匿名真实链审计仍在本机运行，不能以已经提交或套件报告替代完整验收。

## Standards

旧两个硬性 P2 已在已覆盖路径修复；剩余硬性 P2 一项：`sourceparser/mybatis_parser.py:348` 先把 JAVA_LANG_TYPES 的简单名解析为 java.lang，再检查已经声明的同包类型。在同包声明 String 的合法源码中，接口参数 String 与实现候选参数 java.lang.String 被错误视为相同，`source_relations.go:412` 标出 certain 实现边。违反 CONTEXT.md:75 / ADR-0005 的结构唯一、有证据才确认关系规则。

该名称规则经 [Java 8 JLS §6.4.1](https://docs.oracle.com/javase/specs/jls/se8/html/jls-6.html#jls-6.4.1) 核对：type-import-on-demand 不遮蔽其他声明，同包/局部类型需正确解析。首期不要求完整编译器，但不能以错误类型确定性生成导航。

判断性 P3 两项沿用：Spring 注解提取重复、跨 Java/HTTP 家族的大关联函数；不因此要求广泛重构。

## Spec

Spec 审查者剩余 actionable finding 为 0：配置已限定请求模块、HTTP 方法集合保留限制、确定参数签名与具体目标比较修复原三个 P2。曾撤回的“确定性跨 app”P1仍不成立，不能重新升级；api_prefix 保持 uncertain。

## root 独立反例

- 通过真实锁定 HTTP parser 生成旧三个场景的新 facts，再把它们交给本次真实 Go CorrelateSourceFacts。DELETE→GET/POST 限制、run(int)→run(String)、appB 配置删除 appA direct route 三项实际全部 PASS。
- 加入合法单文件 `package demo; class String {} interface I { void run(String value); } abstract class C implements I { public void run(java.lang.String value) {} }`。parser 生成错误相同参数签名，真实 Go 返回 certain `demo.I#run → demo.C#run`。新反例实际 FAIL，包 3.704s。
- 初次临时 overlay 漏 encoding/json import，仅编译失败、不算产品证据；root 修正临时文件后实际运行上述 PASS/FAIL。未改冻结工作树。
- 上轮真实 HTTP/PG/Agent/UI 的已通过结果继续作为未改范围证据；本轮因已有实际阻断，没有重复跑全部宽套件。匿名代表链审计脚本已恢复在本机 Temp，尚在运行，未把它记为已通过。

## 决定

未验收、未集成、#19 open。root 已直接交回原 Luna/xhigh 对话做类型名称绑定的针对性红绿修复；本 SHA 不重复审。要求同包/嵌套/跨文件可见声明具有明确优先级，无法闭合解析时保留 uncertain，必要新 lexical 参数证据先向 root 提案。保留可复跑的两条匿名真实链脚本，无需反复全仓扫描或新冷 cache。

Standards 硬 1 / 判断 2（最严重 P2）；Spec 0。

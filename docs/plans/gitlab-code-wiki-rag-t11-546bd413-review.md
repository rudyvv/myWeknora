# T11 546bd413 双轴审查与独立复验

干净冻结 `546bd4132453da157f22c1838970bcc271a5fd19`，树 `C:/Users/28211/.codex/worktrees/a8ea/WeKnora`；用户批准固定起点 `04d99577814d3786b7e004f81124885638dffced`。根协调两个独立 GPT-6 Sol/high Standards/Spec 审查，执行对话保持 Luna/xhigh。T11/#19 未验收。

## Standards

文档硬违例两项 P2：Spring 显式多个 HTTP method 被压成空值并当 any；接口实现仅同名不验证参数签名却成为 certain 可导航关系。分别位于 `sourceparser/mybatis_parser.py:491/515`、`internal/source/source_relations.go:482/388`。违反 CONTEXT.md 源码关系定义与 ADR-0005 静态分析要求：关联必须有结构依据，不能把候选关系作为已证实调用。

判断性 P3 两项：方法/类型 Spring 注解提取逻辑重复；单一长方法集中 Java 注入/调用/实现和 HTTP 路由多类关联。P3 不作为本次返修阻断或扩大重构依据。

## Spec

三项 P2：

- **前端配置跨应用污染。** `source_relations.go:444` 在快照出现任意 prefix/proxy 后替换每个请求的 direct route。appA 的 `/detail` 有正确 Spring 映射；加入不相干 appB 配置后该 certain 关系消失，并可能展示 appB 的不确定候选。不满足票第 2/3 项按配置证据关联及不串服务要求。需证明请求使用配置的 app/module/client 归属，不能全局套用或仅猜 basename。
- **Spring method 约束丢失。** 多 method `{GET,POST}` 输出空 method 且 certain，correlator 把 DELETE 匹配为 certain。需区别未声明的 any 与显式 method 集合，保留类/方法组合约束或清晰不确定性；不能把正常映射一律降级。
- **实现方法签名丢失。** 合法静态输入 `interface I { void run(int v); } abstract class Impl implements I { public void run(String v) {} }` 被连为 certain。需足够签名/类型证据，不同 overload/default/继承无法确认时无导航目标；静态模式不执行目标项目或 javac。

审查初稿称第一项是 P1 certain 跨 app 误连。根检查 producer 发现当前 prefix 恒 uncertain，不具该 P1 的实际可达性；Spec 审查者确认并撤回。最终记录只采用上面的实际 P2，不夸大风险。

## 独立验证

- 真实锁定 parser HTTP 23 项通过，75.622s；source 2.209s、modelcontext 4.358s、Agent source analysis 4.549s 通过。
- questionnaire 固定 snapshot / 授权读取真实 PostgreSQL 测试通过，21.6s（包总 27.575s）。
- SourceCodeView 实际 UI 三项通过，本轮不需 Windows os.userInfo shim；app vue-tsc 通过。
- 根用真实 parser HTTP 解析三组合成 Java/JS 文件，将事实保存为临时 JSON，再通过 Go overlay 输入同一冻结 correlator。三项边界反例均实际 FAIL（包 3.883s）：DELETE→GET/POST-only、run(int)→run(String)、appB config 移除 appA 的原 certain 直接关系。未改冻结树，没有复制真实业务源码或 SQL。
- worker 报告本地只读核验两条代表链；后续冻结需提供可复验本地脚本及匿名分段 certain/uncertain 统计，根不把单一 questionnaire 合成测试等同完整两条真实链验收。未验证前缀/.do 应保留不确定信息。

## 决定

三个必修及实际反例直接交回原 T09 对话继续 T11，不集成、不关 #19、不派下一票。同 SHA 不重复审查。随后额度中断，用户恢复后 root 重新唤醒同一执行对话保留现场；执行者误启动重复双轴已纠正，根负责正式审查，Luna负责实现/返修。Standards 硬 2 / 判断 2；Spec 3，最严重均 P2。

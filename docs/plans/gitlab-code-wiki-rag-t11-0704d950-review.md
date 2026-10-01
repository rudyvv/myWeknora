# T11 0704d950 双轴复审

完整干净冻结 `0704d950d267e29eb618e6fdad0072691029398d`，用户批准起点 `04d99577814d3786b7e004f81124885638dffced`。两位独立 Sol/high 审查者检查完整票及 60f2b0a1 后十五文件 delta；未重复未变宽套件。

## Standards

文档化硬性违例 0。新增 java_supertype_reference 保留原名、精确原文字节范围和原因，ParseFile 校验，Agent/UI allowlist 贯通；候选 relation 不设置目标文件/版本导航 ID。

既有判断性 P3 两项：Spring mapping 提取重复（possible Duplicated Code）；Java/HTTP 关联器职责持续增长（possible Divergent Change）。候选扫描的规模担忧没有实际测量，不据此提升为硬性失败。

## Spec

**P2：未解析候选吞掉已知实现的方法候选。** `internal/source/source_relations.go:451-464` 遇到 unresolvedMethodCandidates 后提前 continue，使明确 import 的已知实现未进入接口方法候选结果。根实际 HTTP 混合语料：left.Contract / right.Contract 同名，Known 明确 import left.Contract，Unknown 多 wildcard import 后 implements Contract。Known 的 type_supertype certain 声明仍存在，但接口 fetch 方法仅展示 Unknown 候选，Known 方法候选丢失。违反 T11 规则提取与不确定关系展示要求（ticket 11:11-12）。

准确修复合同是保留 Known、Unknown 的匹配方法候选；存在可能第二实现时，接口选择保持 uncertain 且没有唯一可导航目标。不能把已知声明误当成接口唯一 certain 选择。审查者初稿“保留旧确定实现导航”的措辞经根实际证据校准后撤回，避免修复引入错误确定性。

新增父类型原始事实/范围/原因没有其他确定性偏差。两条真实链允许静态确定和不确定证据，不要求动态完整确定图；已知 implementation→Mapper/XML/table 独立后端事实仍需列示，不能因接口跳歧义省略。

## 根独立验证与限制

- 冻结树真实 HTTP→Go 父类型候选契约 PASS 9.455s。
- source、modelcontext、Agent 定向测试分别 PASS 3.980s、3.890s、4.310s；UI 3/3 实际 PASS 3.157s，未使用 shim/安装共享依赖。
- 根临时 overlay 的真实 HTTP 混合 Known/Unknown 反例 FAIL 5.31s，包 8.563s，唯一失败为已知方法候选消失；已知 type_supertype 声明、未解析方法候选及禁止唯一导航断言通过。夹具只在本地 Temp，未修改冻结树。
- Worker 两个 Vue 候选失败源于审计误传主机绝对路径；换为仓库相对路径后实际解析成功 degraded，不计 Vue parser 产品问题，不改已验收 T09。
- 实际 Flow A 的接口/注入歧义及 Flow B 的 Controller→Mapper→XML / 动态 SQL 不确定性保留；本轮未重复全仓审计或把不确定关系伪装为确定。

## 决定

未通过，不集成/关闭 #19，不解锁 T15。单一 P2 已交原 Luna/xhigh 对话用真实 HTTP 混合候选先红后绿，随后提交完整干净新 SHA；未变通过结果沿用，仅新增行为做定向回归。T18 已验收，累计仍 15/22；T17 已接续其已验收集成。

Standards 硬 0 / 判断 2（P3）；Spec 1（P2）。

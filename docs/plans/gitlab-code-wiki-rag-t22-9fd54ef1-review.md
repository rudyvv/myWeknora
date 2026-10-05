# T22 API 工具修复审查：9fd54ef1

正式整票起点 `1d32c08e`；本轮窄 diff `230ed66f...9fd54ef188bcb4a22099bbe338af927a3289aa53`，三个 owned files，commit9fd54ef1。此前的字典启动崩溃与五项 Spec finding 分开计。实现候选未接受。

## Standards

独立 Sol/high：新增文档硬违规0，新增 possible Duplicated Code 判断1：两个数字转换函数重复 CLR 类型检查；新增 full_run 字典是旧测量映射重复判断的延伸，不重复计新finding。旧 Divergent Change、缓存身份重复非阻断判断保留。格式化类问题不重复人工审查。

## Spec

独立 Sol/high：剩余1P2：metric converter189-208允许 double/decimal/single，只判断数学整数，导致 JSON55.0、5.5e1 都转为整数55；与文档严格 JSON integer token 合同不符。其它已报跨仓真实SourceIDs、canonical path、有限score/闭集match enum、SSE闭集且终态complete/done、full_run nullable当前发布身份绑定均结构解决。整票通过不从工具status推断。

## 根实际复验

`go test ./scripts -run '^TestSourceAcceptanceRunner' -count=1 -v`：23顶层22PASS/1FAIL，pkg155.864s；session16871完整收取，日志 `%TEMP%/weknora-root-t22-runner-final.txt`。唯一 FullRun Int64 上界红来自 **Go测试辅助函数** json.Unmarshal(report,&any)→float64→MarshalIndent 的精度损失，不能据此归咎生产serializer；原输入及 pwsh ConvertFrom/To-Json 的 Int64 上界保持，测试须 UseNumber 或原始 JSON bytes。

根另外实际 AST 提取 frozen Stop-Acceptance/Convert-OptionalNonNegativeInteger 运行纯合成 token55.0、5.5e1，两个都 accepted=true/output55，确认真正 Spec P2。未读取真实源码/凭据、不改产品 instrumentation。两个问题一次交同原Luna只修三ownedfiles；原Int64上界、0、nullable和其它已绿断言保留，增加decimal/exponent的metric/phase/match_type负例。后续仅定向回归与窄双轴，避免重跑155s整套。无PG/GitHub操作。

## 94e1de44 最终修复与接受

冻结 `94e1de44c849b1c7af344926c8bb175ec88dc2cf` 保留严格整数 CLR 类型、非负及 Int64 范围检查；report test helper 校验 JSON 后返回原始 bytes，避免 float64 往返。独立 Sol/high 窄 Standards 0 新硬违规/0 新判断，Spec 0 未解决问题。

根五个定向 HTTP tests 全 PASS，pkg20.399s，session50283 全收；其中严格整数五子例、match_type decimal/exponent、nullable、非有限测量和 full_run Int64 上界均真实执行。另一个 phase decimal/exponent 用例首轮 pattern 拼错导致 no tests，只算编译；改为 `TestSourceAcceptanceRunnerRejectsIntegralFloatAndExponentMetricTokens` 后四 scalar/phase 子例实际 PASS，pkg6.834s。无重复整跑155s；已绿行为和最后修复按实际证据合并。Root merge `24ea2183073765dd34fe24302e2460f19d6498af` 接受 API 工具 slice，整票 T22 仍未接受。

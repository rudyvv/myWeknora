# T09 f18b1261 双轴复审

冻结提交 `f18b12612e726600e231beb1f98ff11890f47fbc`，用户确认的固定起点 `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d`。两位独立 GPT-6 Sol/high 审查完整差异，root 对新修复做必要复验。此轮未通过，#17 保持 open。

## Standards

硬性规范违反 0。判断性 P3 possible Duplicated Code 1：`frontend/src/components/SourceCodeView.vue:16` 与 `SourceRegionBadge.vue:5` 重复五项质量文案；与 T10 合并时由 root 提取共享 helper。上轮 Node health/parse 环境净化重复已经收敛。

## Spec

P2：`sourceparser/runtime.py:335-380` 对 `<script lang="coffee" src="./x.js"></script>` 判定 `unsupported_preprocess` 后仍在 `external_script` 分支把区域和正文质量设为 `degraded`，没有标记 `unknown_preprocess`。违反 ADR-0008 按块类型与声明语言判未知预处理及 T09 明确说明要求。外置目标的 `unavailable` 公共读取语义已经修复，缺失/越权没有泄露存在性。T09 同一工作树的 Luna/xhigh 子 Agent 接手此单点修复，因跨独立对话发送反馈的工具遭自动审批拒绝；不将此 SHA 集成、关票或派下一票。

## Root 独立验证

- Node 官方 Vue SFC 解析 10/10 PASS。
- `go test -p 1 ./internal/source ./internal/modelcontext ./internal/application/repository -count=1` 三包 PASS。
- 使用原有前端依赖的只读 junction 后，`SourceCodeView.test.ts` 实际组件测试 1/1 PASS，`vue-tsc --build` PASS。junction 为忽略项，不属于冻结提交。
- Worker 的隔离 PostgreSQL SFC 测试、锁定 Vue HTTP 17/17 和 Node SFC 10/10 PASS；HTTP 排除受沙箱进程可见性影响的 process-tree 用例。root 未重复运行本轮集成 PG。

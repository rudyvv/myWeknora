# T09 a81bc0db 双轴复审与集成门槛

冻结提交 `a81bc0dbe504800259be48982949694f1f71b71c`；用户批准的固定审查起点 `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d`。完整功能差异在 `41373dcb` 由两个 GPT-6 Sol/high 审查轴分别检查；后续 `88086719`、`a81bc0db` 仅修正 Vue HTTP 回归测试的短暂 429 容量窗口，root 核查了完整增量。工作树干净、`git diff --check` 通过。

## Standards

明文规范违反 0。判断性 P3 possible Duplicated Code 1：`SourceCodeView.vue` 与 `SourceRegionBadge.vue` 各自维护五项质量文案；T10 的 `KnowledgeChunksList.vue` 另有同类文案。合并时抽取统一展示函数，并用前端现有测试验证。这项建议不阻断 T09 的功能合入。

## Spec

可行动缺口 0。`41373dcb` 保证外置 CoffeeScript 的非空、空和自闭合 `<script>` 都标 `unknown_preprocess`、保留原文字节范围，外置引用保持字面量和 `unchecked`，不虚构目标可读性。后续测试改动只在 Vue HTTP 契约 `parse()` 中对精确的 `429/parser capacity reached` 做最多四秒有限重试；`request()` 和服务端容量语义不变，`422` 等解析错误立即返回。

## 验证与限制

- root 独立按原故障顺序重跑容量错误、外置引用和未知预处理三项：3/3 PASS；`41373dcb` 时旧外置引用测试曾出现四个 429 子例，确认测试稳定化必要。
- 执行者新增重试回归先红后绿，并连续验证目标；Vue HTTP 契约 19/19 PASS，排除 Windows 进程树可见性受限的既有用例。
- 先前冻结 `f18b1261` 的 Node SFC 10/10、Go 三包、实际 `SourceCodeView` 组件 1/1 与 `vue-tsc --build` 已通过；最终集成仍须解决与 T10 的差异并重跑交叉验证。

本票达到进入集成复验的门槛；只有集成检查通过并推送后才关闭 #17。

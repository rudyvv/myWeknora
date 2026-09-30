# T10 1d8c9978 双轴复审

完整干净冻结 `1d8c9978539417ac55ffdb475c3e9ea1b2f53499`，用户批准固定起点 `3adaa587d16651a6d48f123d73c1e5cceaf75271`。差异 41 文件、12 提交，`git diff --check` 通过。两位独立 Sol/high 审查完整票；此轮未通过；#18 保持 open，不集成。

## Standards

硬性规范违反 0。旧每关系逐边查端点问题已由 `internal/application/repository/source_snapshot.go:115-175` 的快照/租户范围批量读取修复。判断性 P3 possible Duplicated Code：`frontend/src/components/SourceCodeView.vue:21-27` 与 `frontend/src/views/chat/components/tool-results/KnowledgeChunksList.vue:61-67` 的质量/事实标签分别维护、文案已不一致。T10 协调文件要求集成时与 T09 共享 helper 合并。

## Spec

一项 P2 部分实现：`internal/application/repository/source_file.go:95` 返回任一端相关的关系，但 `frontend/src/components/SourceCodeView.vue:52-78,187-193` 和 `internal/agent/tools/source_analysis.go:62-79` 总打开/验证 `to_*`。对 Java Mapper→XML statement 关系，从 XML statement 开始阅读时，`to_*` 已是当前 XML；操作只回到自身，无法打开匹配 Java 方法的固定版本和范围。须依据当前阅读端选择另一端，再通过原授权/已发布快照/版本核验，不能把不确定边当作确定证据。

两轴统计：Standards 硬0、判断1，最严重 P3；Spec 1，最严重 P2。Spec finding 已派回原 T10 Luna/xhigh，对方须加真实 PG、UI 和 Agent 双向回归，冻结新完整干净 SHA。

## 验证状态

Worker 报告针对 245 条关系的单次快照成员批量读取、源文件固定版本阅读与删除目标的独立 PostgreSQL 集成测试、前端渲染与类型检查通过。Root 独立运行 `go test -p 1 ./internal/source ./internal/modelcontext ./internal/application/repository -count=1` 三包 PASS（实际包测试分别 1.745s、4.726s、4.137s）；这不覆盖反向 XML→Java 导航，故保持 Spec finding。Root 未在此冻结 SHA 上重跑整套真实 PG。

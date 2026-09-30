# T10 e8ac6518 双轴复审与独立验证

冻结提交 e8ac6518151d29b7ef2c8f343d5dd4817be9fce0，用户确认基线 3adaa587d16651a6d48f123d73c1e5cceaf75271。完整差异 39 文件、11 提交；工作树干净，`git diff --check` 通过。两位 GPT-6 Sol/high 审查员分别审 Standards 与 Spec。本轮未通过；#18 保持 open，未集成或派发下一票。

## Standards

硬违反 0；判断问题 2（最严重 P2）。

- P2，possible Duplicated Code：`internal/application/repository/source_snapshot.go:115-129` 的 `StageRelations` 对每条边分别查起点和终点的三表 JOIN COUNT。代表样本 245 条边至少数百次往返，超大 XML 线性放大，可能拖慢发布；应在同一事务批量取合法端点并复用校验，保留非法端点拒绝。
- P3，possible Duplicated Code：`frontend/src/components/SourceCodeView.vue:18` 与 `frontend/src/views/chat/components/tool-results/KnowledgeChunksList.vue:61` 分别维护解析质量标签。T09 拥有共享 quality helper，由根集成时合并。

## Spec

部分实现 1（P2），缺失/错误/越界 0。

- T10 ticket 的“Java Mapper 方法…查询到对应 XML…阅读准确固定位置”及 AC4 需要跨文件引用阅读。`SourceCodeView.vue:140-145` 仅同文件目标提供按 `to_range` 跳转；跨文件确定关系只显示路径，无法按已核验 `to_file_id`、`to_version_id`、`to_range` 打开目标。Agent 的 240 字符片段也不能代替完整固定位置阅读。应复用受限版本读取并补 UI/权限边界回归。

本轮损坏 XML 的 `text_fallback`、原文/诊断保留、DTD/外部实体拒绝路径已静态核对，未发现新的确定缺陷。旧 ffc98fde 的解析阻断问题已由新增回归覆盖。

## 复验与处置

Root 使用独立临时 Go 缓存运行 `go test -p 1 ./internal/source ./internal/modelcontext`，两包通过（1.317s、4.031s）。首次运行因默认 Go cache 访问被拒未形成测试结果，换临时缓存后通过。Root 尝试运行锁定 parser HTTP 契约：初次缺 `SOURCE_PARSER_CACHE`，设置原有 grammar cache 后发现现有本地 Python venv 缺 `sqlglot`，因此本轮没有独立 parser HTTP/PG 通过证据；Docker daemon 当前不可用。Worker 已报告两项真实 parser HTTP→PostgreSQL 测试通过，作为其证据保留但不写成 root 独立通过。

两项必修已发回同一个 T10 GPT-6 Luna/xhigh 执行对话。等新完整干净 SHA 再按原基线复审；e8 本身不集成、#18 不关闭。父任务仍 8/22。

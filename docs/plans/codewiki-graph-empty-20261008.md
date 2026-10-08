# Wiki 图谱空白诊断（2026-10-08）

目标 KB：`65658207-a2ec-47fb-bf0f-11e7b685369e`；工作树 `source-integration/WeKnora`，分支 `codex/gitlab-code-wiki-rag`。

## 原因

图谱接口 HTTP 200，返回 6 个节点（5 张内容页面和索引），`edges:null`。这些 Wiki 页面当前没有互链。后端 `computeGraphSubset` 将零条关系保存在 nil Go slice 中，JSON 编码为 null。

前端 `renderGraph` 直接遍历 `graph.edges`，抛出 `TypeError: graph.edges is not iterable`。异常被 `loadGraph` 捕获后，`graphReady` 仍为 false，于是显示“暂无图谱数据，请先上传文档”。此提示不符合实际数据状态。

这不是没有源码或 Wiki 数据，也不依赖 Neo4j。该标签展示 Wiki 页面之间的链接，已有源码证据引用不会自动构成 Wiki 互链或源码调用图。

## 修复

提交 `1a3fbe67`：后端在零关系时返回 `edges:[]`；前端 API 边界将旧后端的 null 数组规范化为空数组，兼容 overview、ego 及后续邻域合并读取，保留节点、真实边和元数据。

没有为图谱效果人为新增页面关系，没有修改 Wiki 正文、源文件、范围校验或发布状态。

## 验证

- 实际浏览器重现了接口含节点但页面显示空提示，并捕获上述 TypeError。
- Go 空边 JSON 回归用例在修复前因 `null` 失败；修复后所有 `TestComputeGraphSubset_*` 通过。
- 前端 API 用例在修复前因数组遍历 TypeError 失败；修复后两个用例通过，覆盖原始/包装响应、overview/ego、保留真实边与元数据。
- 前端生产构建通过，有既有大 chunk 提示。
- 实际前端兼容旧响应后显示 6/6 个节点，SVG 标签包含 5 张技术卡片及 Index；点击课后反馈的节点圆点打开 Wiki 正文与固定证据链接。

本机开发后端使用现有 `.codewiki-dev-start.ps1 -Restart` 从修复源码重编译更新。最终现场检查结果追加于下方。

最终检查：修复后端 PID 38596，前后端健康检查通过；新进程图谱接口返回 6 节点、`edges:[]`，HTTP 200。新标签页 SVG 实际显示六个标签且没有“暂无图谱数据”提示。截图位于本聊天 artifact 目录 `codewiki-graph-fixed.png`。重启过程中旧验证标签页短暂进入连接拒绝错误页，最终验证使用同一浏览器的新标签页完成。

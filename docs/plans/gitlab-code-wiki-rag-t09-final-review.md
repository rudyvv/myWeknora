# T09 完整冻结提交双轴审查与修复合同

2026-09-29。用户要求审查已停止的 T09。冻结提交为 5d43d30e358451b4f72c38b58b323761a43cd5c2，冻结时工作树干净。沿用已批准起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d，diff 为 `git diff 7f4fd1dc...5d43d30e`。四项提交：90680131、b0d9bcb4、f96c4b95、5d43d30e；28 文件，1701 插入、66 删除。本轮结论不包含随后派发的修复。

Root 已通过 gh 核对 GitHub Issue #17 与本地 tickets/09-vue-sfc.md 的五项 AC。按 code-review 技能分别派发独立 GPT-6 Sol / high Standards 和 Spec reviewer；修复由执行对话 GPT-6 Luna / xhigh 完成。旧 parser-only 90680131 的问题已修复，不能把旧审查当本轮完整实现结论。

## Standards

硬违反 0；判断性建议 1；最严重 P3。

- **P3 possible Duplicated Code。** `frontend/src/components/SourceCodeView.vue:16` 与 `SourceRegionBadge.vue:5` 重复定义 structural、unknown_preprocess 等质量显示映射。建议抽取共同 helper，避免文件质量与区域质量文案分叉。此为判断性建议，不是硬违反。

已依据 CONTEXT、README、ADR 0003/0005/0007/0008/0009 检查：原文切片及位置映射、外置 script 的固定快照/文件/标签范围、现有 Wiki 历史证据与权限路径未发现新增硬冲突。先前空签名、属性长输入及只终止 Python 的问题在完整冻结中已修。Reviewer 独立运行了内存 Node 与 Python 映射检查；其主机 Node 26.1.0 不是部署版本，部署 Node 24 的实际结果另列于总控复验，不混称 reviewer 完成 Docker/数据库验证。

## Spec

partial 2，missing/wrong/scope creep 0；均 P2，最严重 P2。

1. **P2：Agent 实际模型输出遗漏区域、质量和符号。** AC4：“区域块经实际双索引及代码阅读可返回质量、符号/区域和原始位置”。同步已产生 SourceEvidence.Region，工具 Data 也包含证据；但 `internal/modelcontext/source_evidence.go:22–29` 的 sourceEvidenceAttrs 仅输出身份、路径、行号和 URL，Observe → Registry.ModelToolResultForTool 的实际路径会丢失 Region / Quality / Symbols。应在共享模型格式化路径输出有界、正确转义的区域 kind/language/quality 及必要符号，覆盖实际模型文本而非只断言 Data。Reviewer 此项为冻结静态核验；总控随后已运行真实函数反例，结果见下。
2. **P2：代表仓库 Vue 2 组件链尚未完整验证。** AC5：“代表仓库 Vue 2 组件链及独立 lang=ts 语料都可验证”。现有数据库用例是合成 BookingPanel，独立 lang=ts 已有语料，但没有真实代表组件贯穿解析、双索引、公开读取及坐标的记录。T22 不能替代本票明确验收。应增加 opt-in 代表路径验证及可复现统计报告，不提交业务源码或 SQL。

真实只读候选已核对存在：代表仓库 evip_mobile/src/pages/User/ConfirmTimetable 的 index.vue（6303 bytes / 211 lines）、list.vue（9558 / 303）、qr.vue（9083 / 322），均含 CRLF 与本地 API 相关引用。这些统计不表示整条链已通过；真实内容不发布。外置 script 的固定快照/文件/标签权限过滤静态符合合同，不把此静态结论说成真实撤权测试。

## 总控独立验证

使用冻结导出，未改执行工作树，也未掺入审查之后的修改。冻结导出补齐 third_party 和 docreader 后完成 Go 本地模块编译。

- 固定解析镜像 ID sha256:ed863f31cc23d2109b73c955efdb2c0b91657ee5a29e46e3801200b3bc69bb7b；runtime.py、server.py、parse_sfc.cjs 的换行归一化 SHA256 与冻结提交一致，测试额外只读挂载冻结文件。镜像与 Git 原始字节因 CRLF 差异不能直接判为旧镜像。
- Node 24.19.0：7 项 SFC tests PASS，214.854ms。
- 无网络、只读容器 HTTP/Python 合同：35 tests PASS，46.379s。含 Linux 子进程树退出后才释放并发槽，独立 Node 哨兵保持运行。
- frontend tsconfig.app.json 和 tsconfig.node.json 的 vue-tsc 检查 PASS；Vite build PASS，1m30s。依赖来自既有共享安装，只读使用；构建和 tsbuildinfo 写入独立临时目录，没有安装或改共享包。构建提示已有大包体积，不将警告记为失败。
- 真实独立 PostgreSQL / 固定解析容器：TestSourceVueSFCRegionsPublishAndScopeExternalScriptResolution PASS，14.780s。覆盖区域发布、双索引及外置 script 范围过滤；合成 fixture 不能充当真实代表组件链通过。
- 临时反例 TestRootT09ModelRegionContract：实际调用 NewRegistry(true).ModelToolResultForTool("knowledge_search", result)，先确认路径/commit 进入结构化模型输出，再断言元数据；FAIL，3.492s。四项 marker unknown_preprocess、template、pug、ReviewPanel.render 均缺失。此反例仅使用合成数据，未提交为产品修改。

数据库使用用户选择的独立 localhost:57521 测试 fixture，每个用例独立 schema；未提取旧容器凭据。Root 只停止自己创建的 weknora-t09-root-review-5d43 解析容器，共享数据库及执行者容器不动。

## 修复与通信状态

两项 P2 及可选共享质量映射已交 T09 继续修复。完成后执行者须提交干净新 SHA，直接向 root 发送 READY_FOR_REVIEW，列明基线、测试和已知缺口；只有 root 审查/复验/集成通过并发 REVIEW_PASSED 才进入下一票。Issue #17 保持 OPEN，未合并实现、未增加完成计数。

用户已明确授权总控和三个执行对话双向通信，通信规则与游标记录见 gitlab-code-wiki-rag-coordination-loop.md。T06 已直接询问持久 Wiki 信号接口，T10 已直接询问测试环境与 UI 分工，root 均已答复。T10 现有 facts/diagnostics/relations/游标 UI 修改保留供独立审查；T09 负责 Region/模型输出/统一质量显示。交叉 SourceCodeView/API 合并由 root 处理，保留两票能力，避免要求执行者删除已完成验收范围。

两轴计数：Standards 0 硬违反 + 1 判断性建议，最严重 P3；Spec 2 partial，最严重 P2。未合并或重新排序两轴 finding。

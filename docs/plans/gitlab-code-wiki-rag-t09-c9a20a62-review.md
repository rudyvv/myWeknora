# T09 c9a20a62 双轴复审与独立验证

冻结 c9a20a62519a2c1ba439acc055b6d713cde87673；用户批准起点 7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d。完整差异 34 文件、八提交。Sol/high 两位独立审查 Standards/Spec；未通过，Issue #17 保持 open，不集成。

## Standards

硬违反 0、判断性 2，最高 P3。possible Duplicated Code：SourceCodeView.vue:16 与 SourceRegionBadge.vue:5 重复五态质量文案。possible Repeated Switches：modelcontext/source_evidence.go:89 与 source/parser_client.go:196 平行维护两种诊断代码的静态说明。两者均不单独阻挡验收。

## Spec

错误/部分实现 2 项，均 P2；缺失 0、越界 0。

- 描述符警告仍没有可靠传到真正被检索的正文块。runtime.py:343–363 只按警告起点所在 chunk 贴诊断。锁定官方 Node/HTTP 实测：未闭合 template 的警告位于开标签，开标签通用块降级而正文 template 块仍 structural/无诊断；未闭合 script 的警告位于 EOF，找不到任何 chunk，正文仍 structural。长 template 的首块被标记时，共享 region 字典又把其余未受影响 chunk 的 region.quality 一并改为 degraded，与 chunk.quality=structural 矛盾。违反 T09 区域块质量与父 Spec 显式降级。需保留可核验原始 warning range，把边界警告归属相应正宽可检索块，并避免无关块污染。
- source_sfc.go:43–51 判断外置 script 目标是否 resolved 时只查 snapshot member 和 SnapshotSQL，未像公开代码阅读 source_file.go:38–39 要求目标 knowledges.deleted_at IS NULL、数据源仍可读。软删除目标但已发布 snapshot 保留 member 时可披露不可读目标路径，违反 T09 仅关联当前获允许的文件。

## Root独立证据

锁定解析镜像与冻结 runtime/SFC rules2 只读挂载、无网络；官方 Vue HTTP 合同真实执行 11 项，9 PASS、2 FAIL。一项是上述共享 region/诊断错误；另一项 preprocessor 测试错误要求 TS 结构解析一定产出 supportedScriptMarker 变量符号，真实 pack 合法地产生 export/module 符号，script 区域/块结构质量和原文范围正确，应修测试预期。初次只挂 runtime、未挂 rules2 曾整组 skip，未计 PASS。直接 parser probe 进一步证实开标签与 EOF 警告、受影响/未受影响块质量。

Root 专用 localhost:58083 parser 与用户批准独立 localhost:57521/source_test 执行两项真实 PG Vue 集成用例 PASS19.077s，覆盖正常发布、双索引、引用阅读和代表组件。第一次夹具未指定 SOURCE_TEST_PARSER_URL 而启动了本机缺 Vue 的 Python，预览失败属测试环境设置，修正后通过；未作产品 finding。专用 parser 已停止，共享58082未动，无旧容器凭据读取。两项 P2 和错误测试断言已交回同一 Luna/xhigh T09 对话；等待新干净完整 SHA。Standards 0硬/2判断，最严重P3；Spec2，最严重P2。父任务仍8/22。

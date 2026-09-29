# T09 44423910 双轴复审与独立验证

2026-09-30。干净冻结44423910800c625b22e7952eb134ff964b0e7652，用户批准base7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d；`git diff 7f4fd1dc...44423910`，提交90680131、b0d9bcb4、f96c4b95、5d43d30e、c56b1c22、44423910，32文件。两位独立Sol/high reviewer分别看Standards与Spec，root另做锁定依赖/真实PG复验。上轮c56的空外置script证据和Badge测试setup已修，但此冻结未通过；Issue#17仍open、不集成、不计完成。

## Standards

硬违反0、判断1，最严重P3。完整冻结对照AGENTS、CONTEXT、ADR0003/5/7/8/9、domain/dev-guide/implementation-plan及Fowler 12 smell baseline。SFC官方组件、锁定Node/JS/TS解析、原文字节范围、仅同snapshot/授权关联外置目标、模型白名单/有界转义均未发现新增明确文档冲突。

- P3 possible Duplicated Code：frontend/src/components/SourceCodeView.vue:16与SourceRegionBadge.vue:5重复五态qualityText映射，建议提取共用文案以免扩状态时分叉；判断性建议，不单独阻挡验收。

Reviewer本轴只读冻结源码，不将新HTTP测试错误另计硬标准，也不声称静态核验替代真实部署。

## Spec

partial/wrong2，missing0，scope creep0；两项均P2。

- P2：未知预处理可错误显示structural。T09 ticket:13“不能读取的目标或未知预处理明确说明”。runtime.py:241跨template/script/style/custom共用语言allowlist，:253–254/:282–285仅筛不在总表的语言。真实官方Node/HTTP输入 `<template lang="ts">template_marker</template>` 与 `<style lang="json">style_marker</style>` 均零diagnostic、file/region/chunk=structural，而相应block类型没有执行模板TS或样式JSON处理器。应按block kind判断known raw/结构语言，不运行目标插件，保持script TS真实结构解析及SCSS等既定原文检索。
- P2：descriptor warning没有进入被检索区域证据。T09 ticket:14“区域块经实际双索引及代码阅读可返回质量”，父Spec:84“可读但不可可靠解析的文本显式降级”。真实官方Node/HTTP `<template><div>first</template>` 有1个sfc_diagnostic、file=degraded，但template chunk/Region=structural，chunk不带该警告（仅原region symbol）；runtime.py:308–325只给文件降级、添加零宽marker后排除它参与chunk。datasource_source_sync.go:213持久chunk只取part.Quality/Region，故搜索/模型看到结构可靠而不知解析警告。应依据可信原始警告位置关联正宽受影响区域/块，使用受控代码有界解释，不全局污染无关块，不复制原始诊断消息。

上轮空/whitespace外置script wrapper的正宽原文字节、unchecked/rejected状态及模型输出已修复。上述两项为新核验出的不同质量合同缺口；不是要求编译目标Vue项目或读取外部引用。

## Root冻结复验

只用d冻结代码临时导出、旧已核锁镜像sha256:ed863f31…挂载新runtime/server/SFC锁文件只读、禁网络/限进程内存CPU；用户选定独立localhost57521测试库和root专用58083 parser。未安装依赖、未修改或重启共享58082/其他容器、未读取旧容器凭据。受限grammar缓存先复制到可执行临时区；起初因挂载noexec导致整组skipped，修正后真正执行，未把skip算PASS。root自有58083已在验证后停止。

通过：官方Node SFC测试7/7；真实Vue HTTP合同9项中的8项（含empty/whitespace/out-of-scope外置脚本、unknown/preprocessor、Unicode/CRLF、bounded timeout子进程回收）；Go internal/source PASS1.433s与internal/modelcontext PASS4.218s；真实Badge SourceCodeView组件测试PASS，frontend app/node两个typecheck均通过。真实PG合成Vue端到端 TestSourceVueSFCRegionsPublishAndScopeExternalScriptResolution PASS15.411s，核实外置引用作用域、索引与读源。代表真实Vue2三文件 TestSourceVue2RepresentativeAcceptance PASS11.125s，按测试断言关键词/向量3/3、公开读取6/6、模型证据6/6，验证完整字节/快照/区域。只报告受控统计，不披露原业务代码。

真实HTTP唯一失败是新增test_vue_http_contract.py:96断言Health parser_version包含明文 `vue-sfc-node-24.19.0-compiler-2.7.16-rules-2`，但runtime.py:95设计返回`source-pack-1.19.0-rules-4-<verified hash>`；健康响应Ready且languages含vue。错误在测试预期，不能为了使测试通过改正确生产指纹，修复合同已发worker。8/9不能写作全HTTP通过。

另外两个质量反例使用同一个真实专用容器HTTP实际执行并核对file/region/chunk质量及diagnostic。中间T06/T10独立分支活动，未与T09冻结变更混合。

## 修复派发

原T09 Luna/xhigh执行对话已收到两项必修质量映射、正式HTTP→持久检索→模型回归要求及错误指纹测试修正。P3可选。修完交新clean完整SHA主动READY，再由root双轴复审、必要复验、集成和关#17。没有创建新对话/重复票。

两轴计数独立：Standards硬0/判断1，最严重P3；Spec2，最严重P2。父任务仍8/22通过。

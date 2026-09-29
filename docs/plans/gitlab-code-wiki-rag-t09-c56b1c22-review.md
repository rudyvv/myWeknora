# T09 c56b1c22 修复提交双轴复审

2026-09-30。T09 主动 READY_FOR_REVIEW：干净冻结 c56b1c22e026ab5571ab97779ba5e3f65eb0e88d。批准起点7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10d，完整命令 `git diff 7f4fd1dc...c56b1c22`，提交90680131/b0d9bcb4/f96c4b95/5d43d30e/c56b1c22。独立两轴均GPT-6 Sol/high，worker继续GPT-6 Luna/xhigh。冻结后已派发的修改不混入本轮。

## Standards

硬违反0，判断1，最严重P3。

未发现与README、CONTEXT及ADR-0003/0004/0005/0007/0008/0009冲突；固定提交、静态分析、独立解析、只读原文与scoped外部脚本保留。新证据使用白名单、有界字段/扫描、XML转义；真实组件测试显式opt-in，静默logger覆盖GORM Debug。

- **P3 possible Duplicated Code。** SourceCodeView.vue:16和SourceRegionBadge.vue:5重复五态qualityText映射。提取共同文案可降低后续分叉，此为启发式判断，无硬规则要求。

Standards reviewer只读冻结blob，未执行suite。Root gofmt -l新model文件为空；环境无gofumpt，未为此安装依赖，不把tooling强制事项计finding。

## Spec

partial1，wrong0，scope creep0，最严重P2。旧2项P2已修：四路真实ModelToolResultForTool输出已有bounded/escaped Region/Quality/Symbols，代表三Vue组件的完整验证已补。

- **P2 partial：空外置script的未读取说明没有进入模型证据。** T09:13“不能读取的目标或未知预处理明确说明”。runtime.py:253只为非空body产生区域块，:281–284的相交条件不能把零宽marker附到wrapper。实际工具读取chunk evidence，source_evidence.go:33没有从file.Symbols补说明。模型见src字面量和structural，看不到目标未读取/拒绝；UI badge有状态不能替代此出口。修复保留正宽原始wrapper区间，携带真实降级与安全状态，不强制暴露目标路径或范围外存在性。

Spec reviewer在固定部署镜像ed863f31…中实际运行离线、只读Node/Python内存probe：合成 `./missing.js` 与 `../private.js` 空外置声明的文件quality均degraded，marker分别unchecked/rejected；两种声明的两块wrapper却均structural、Region=nil、Symbols空。模型消费链为静态核查。另实际核验未知/pug模板中文、emoji、CRLF切片和完整覆盖；固定快照及file/tag scope、不泄露未授权ResolvedPath静态符合，独立TS/直接JS/TS回归路径已核但未重复suite。

## Root独立冻结验证

- 完整internal/modelcontext套件PASS1.869s，包括四个实际工具入口、XML转义、界限及region-quality fallback。
- TestSourceVue2RepresentativeAcceptance PASS11.252s；关键词3/3、向量3/3、公开文件读取6/6、模型证据6/6。代表project SHA 15d9575ebea6d82d9bb1b69dfe2b9950c6eb4cd5；临时fixture SHA a8385c576262bb3f62344c972d935dd7c7a77259；parser source-pack-1.19.0-rules-4-157ac577d0b6037f6ed86429cac6b77b。三文件hash/bytes/CRLF/chunk/symbol统计与worker报告一致，fixture临时commit可因创建时间不同而不同。
- SourceCodeView.test真实FAIL：手动SFC加载器找不到新增 @/components/SourceRegionBadge.vue。仅说明测试setup未适配，不能推断产品运行故障。已要求加载实际子组件并补Region DOM断言，保留只读、逃逸、同commit/range及请求代数原断言。
- 本次未重复无production parser/frontend改动的Node7/HTTP35/typecheck/Vite；其冻结5d43独立通过记录仍有效。新parser修复后必须执行相关合同回归，不能沿用此未变更判断。

使用用户指定独立测试DB/schema和锁定parser，原代表工程只读，不执行其插件或构建，不输出业务原文/SQL。Root前端依赖只读共享，临时导出复验，不改worker或共享cache。

## 已派发修复

T09继续同一轮：实际wrapper chunks保留可信正宽坐标，携带空/whitespace外置区域的未读或拒绝状态/降级；模型formatter输出有界转义安全说明，不能声称未经固定快照/授权验证的目标已resolved，不泄露ResolvedPath/范围外存在性。更新受控processing/rules fingerprint，保留T08五grammar和锁定Node；仅用既有SFC组件映射，不编译目标、不自建语法。增加真实HTTP和实际模型输出回归。前端test同时补实际badge加载/Region显示，P3抽映射可选。

新cleanSHA再主动READY，root复验通过才集成/关闭#17并按依赖派下一票。当前#17 OPEN，完成数仍8/22。

两轴计数：Standards硬0/判断1，最严重P3；Spec partial1，最严重P2。各轴独立保留，不合并重排。

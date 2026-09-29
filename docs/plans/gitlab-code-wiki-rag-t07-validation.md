# T07 实施与验收记录

Ticket：[GitHub #15](https://github.com/rudyvv/myWeknora/issues/15)。审查起点：`461029f6c11d5ca87fc3397e2fdd25898b7f3046`。实施限于 `codex/source-languages` 的独立 worktree；实现及针对性验收已完成，双轴复审均为 0 项未解决发现；初审三项 Spec P2 已修复，独立复验 5 项 HTTP 契约通过（6.955 秒）。待根任务集成后的统一验证、发布与 Issue 关闭。本文只记录 T07，不代表首期五语言或代表仓库规模验收完成。

## 成品解析与离线准入

继续使用 requirements.lock 中的 `tree-sitter-language-pack==1.19.0` / `tree-sitter==0.26.0` 和上游结构 chunker，不执行目标仓库脚本、依赖、插件或构建。自写内容限于成熟 CST 节点提取、原始坐标/证据适配及语言路由。

构建阶段对固定平台 grammar archive 的 SHA-256 验证后预装 Java、JavaScript、TypeScript、TSX 四 grammar，并记录各二进制完整 SHA-256。运行时只加载已验证的本地 grammar。新 worker 健康响应列出全部验证语言；旧 Java-only lock/cache 继续支持 Java，不能准入 JS/TS。缺失或篡改锁定 grammar 使健康检查未就绪。所有文件的 ParserVersion 与本轮 health.ParserVersion 相同，指纹来自完整已验证 grammar 集合、pack release 和提取规则，排序稳定；便于 T04 的工序核验和配置加工版本识别。

路由：`.java` → Java；`.js/.jsx/.mjs/.cjs` → JavaScript；`.ts/.mts/.cts` → TypeScript；`.tsx` → 独立 TSX grammar。公开预览与实际同步均根据选中文件核验 worker 的真实 grammar health，未知扩展明确拒绝。现有 1–100 文件、16 MiB、built-in PG 与 tokenizer 限制保留；T04 的空增量发布逻辑由根集成保留。

Windows 自有 cache 保留于 worktree `.source-parser-grammar`，未改共享 Java cache/venv。Windows bundle SHA：`1fd72fbba863c57445f0c570804561a4c569584edf79067d0651e450b6222609`。Linux x86_64 Docker build 校验 bundle SHA：`86995c25a95d59a1235276c8bdfc5156f7ffb1db1d53653c9e58a60e92d4346e`。官方 grammar MIT 许可证已随 worker 加入；TypeScript 官方说明区分 TS/TSX，见 [upstream](https://github.com/tree-sitter/tree-sitter-typescript/blob/master/README.md)。

## 结构、原文与独立 TypeScript 语料

提取模块、导入/导出、声明/默认匿名函数、箭头函数、类/方法、可调用 class field、namespace、interface/type alias/enum，qualified_name 保留父结构。JS `export default` 对象中 `methods.reserve`、箭头成员与嵌套对象方法均有结构。Decorator 沿既有 annotations 公共契约返回独立原文区间，不新增 SQL 或公共类型。

`sourceparser/tests/fixtures/reservations.ts` 是人工逐行/字节核验的独立 TS 语料；代表 Java/Vue 仓库不存在 TS 不能作为此项覆盖。Fixture `.gitattributes` 保留 CRLF。JS `createBooking`：原始 UTF-8 [62,137)、L3–5。TS `@trace`：[243,249)、L7；`reserve` evidence 从 byte 243/L7 开始，限定父结构为 `<path>.Reservations.Scheduler.reserve`。测试 literal 期望来自原文人工核验，不由解析器生成。

HTTP 覆盖 Unicode/emoji、CRLF、装饰语法、JSX/TSX、坏语法、UTF-8 BOM、超大 Unicode 叶子及有界 signature context。正文逐字节拼回原文；每块与单独 context 均按原始区间读取。坏语法文件与块明确 syntax_error，不静默丢失错误之后的可读文本。Java 注解、坐标、超大结构与错误降级契约保持回归。

## 公开同步、检索、工具和读取

新增公开 service 集成用例经过真实本地 Git、锁定 parser HTTP、独立真实 PostgreSQL/ParadeDB schema。外部 GitLab transport、embedding model 与 queue 可控；不 mock 内部 parser/检索。JS/TS 与 Java 在同一完整快照发布：符号关键词及人工标注自然语言的向量 top1 各命中正确 JS/TS 文件，解析签名/父结构/decorator 保留，固定提交原文下载保持字节一致，GitLab URL/SHA/路径/原始 byte 与 line 坐标符合原文。

公开 grep 和 source reader 工具的 ToolResult.Data.source_evidence 保留相同 snapshot/fileVersion/SHA/range；XML 本身沿用既有正文格式，未扩展 formatter。既有 modelcontext/引用封装处理最终展示元数据。JS/TS 文件仍由 Git 管理，公开 chunk 删除拒绝；文件范围限制不会被新语言绕过。另验证旧 Java-only cache 拒绝包含 TS 的预览/同步、不发布部分内容；坏 JS/TS 同步后仍可检索/读取原文且显示 syntax_error；JS/TS 新发布后同一次问答仍搜索/读工具旧 SHA，新问题读新 SHA，显式旧 fileVersion 在当前读范围被拒绝。

## Red → green 与最终验证

- JS HTTP 请求先因仅支持 Java 返回 400；新增预装 grammar 与语言路由后通过。
- TS decorated method 签名先丢失兄弟 decorator；接入 Tree-sitter 节点后 signature/annotations/original coordinates 通过。
- JS 默认对象成员先缺少 default.methods 父结构，默认匿名函数与 TS callable class field 先缺符号；逐项公开 HTTP red 后补规则变 green。
- 公开同步先 PreviewSource.CanSync=false 拒绝 JS/TS；真实语言 health/ParseFile 路由接通后双索引/工具/原文通过。
- 四语言 file.ParserVersion 与 health 不一致先产生四个 HTTP failures；统一集合/规则指纹后全部通过。
- 首轮本机完整 HTTP contracts：15 项通过；首轮 Linux Docker 离线 HTTP contracts：15 项通过，13.217 秒。容器 network none、non-root、read-only、cap-drop ALL、no-new-privileges、768 MiB/2 CPU/64 pids，部署 `/tmp` noexec。测试拷贝已校验 grammar 到独立 `/contract-cache`，必须显式 exec 以 dlopen 该可信 library；第一次缺少 exec 的临时 cache 导致 legacy-only case 503，补测试 tmpfs 参数后通过，不改变生产 `/tmp`。
- 完整相关 `go test -tags integration ./internal/application/service -run '^TestSource' -count=1`：通过，188.690 秒；含新 JS/TS、原 Java、普通文档/FAQ、多仓库、tag/file/tenant、跨发布 pin、工具与授权/撤销/HTTP 回归。此前该完整回归亦通过，245.755 秒。
- `go test ./internal/source ./internal/datasource ./internal/datasource/connector/gitlab -count=1`：通过（source 包无单元测试，行为由公开 integration/HTTP 覆盖）。
- 普通相关 service `-run '^Test(DataSource.*Source|Source)'`：通过，4.286 秒；`go build ./cmd/server`：通过。`git diff --check`：通过。
- 未自行运行全仓/全前端 suite；由根任务在三分支集成后统一执行。未宣称已知 Windows 14 failures 全为基线或全仓通过；本项不改前端。

## 集成边界

`internal/source/parser_client.go` 新增 `LanguageForPath` / `ParseFile`，保留 `ParseJava` wrapper。`datasource_source.go` 所选语言集合与 health 核验、supportedOnly 及准入文案需与 A 的空发布条件合并。`datasource_source_sync.go` 仅改 readiness 错误文字、ReadGit callback 扩展名准入和每种语言一次实际 health 缓存、ParseJava → ParseFile 调用及原 Java 限制文案；不重构 staging/index/publication。具体 hunk 当前行 32–39、93–119、150。Integration fixture 仅扩展外部 embeddingForText 和 advanceFiles Git 变更边界；无公共产品类型/迁移修改。

局限：结构提取不宣称 runtime 调用图、跨文件类型解析、动态 dispatch 或完整装饰器语义；语义检索测试使用受控外部 embedding，不代表真实模型或内网 GitLab 的业务相关性。Python/T08、Vue SFC/T09、MyBatis、代表仓库吞吐/预算与 T22 指标均未提前实施。Linux aarch64 archive 仍锁定但本次未在 aarch64 runtime 测试。


## Spec 审查修复：提取规则 v3

Spec 轴指出三个 P2，均先通过真实 HTTP 写 red，再最小补规则：

1. `export const api = { reserve() {} }` / `const otherApi = { reserve: id => id }` 之前把两个成员都写成 `<path>.reserve`，丢失变量绑定。现在仅对明确 identifier/property 名称的 object 值沿既有 pair/object 规则保留 `api.reserve`、`otherApi.reserve`。JS 和 TS 红测各失败一次；同名对象在不同函数内和嵌套同名对象的限定结构通过。解构的 RHS 对象不根据 binding pattern 猜测变量身份，仍保留完整原文及现有未承诺的语义边界；未新增公共 quality 值或前端状态。
2. 直接 `export default (name) => {...}`（含 TS 类型版本）之前只有 module/export，缺函数符号。现在只对 export_statement 的直接 arrow value 赋予 default 函数身份与原始签名/区间，分块上下文仍单独可核验；不给普通匿名 callback 猜默认名称。JS/TS 各产生 0 function 红测，随后通过。测试字面原文 JS arrow [15,152)、TS arrow [15,168)，均 L1–4（含 100-byte 注释制造结构拆分）。
3. TS `import foo = require("foo");` 的 source 字段属于 `import_require_clause`，原规则遗漏 import。现在按成熟 CST 的该子节点读取静态 source。红测 1 import != 2 imports，补规则后与普通 ES import 同时通过；动态 `require(selectedModule)` 不被声称为静态 import。人工重新核对 ASCII statement 是 28 bytes，range/signature_range 均 [0,28)、L1，后续 CRLF 不包含在两者；修正测试人工误计数，不改变正确的 parser 坐标。

三项改变提取语义，因此所有健康/产物工序升级 `source-pack-1.19.0-rules-3-<grammar-set fingerprint>`，仍由完整 grammar SHA、release、规则组成稳定一致版本，避免沿用 rules2 产物。Compose image 更新 `source-1.19.0-rules3`，本次实际镜像 `weknora-source-parser:t07-rules3`；README 可复现命令同步更新。原自有预装 grammar cache 保留。

受影响最终复验：

- 本机全部 18 HTTP contracts 通过，13.494 秒。
- Linux `network none`、只读、非 root、限资源四 grammar Docker 全部 18 HTTP contracts 通过，19.719 秒；沿首轮相同测试隔离策略。
- 公开真实 JS/TS 全部相关集成 `go test -tags integration ./internal/application/service -run '^TestSource(Script|BrokenScripts)' -count=1` 通过，31.689 秒；包括双索引/tool/read、缺 grammar 拒绝、坏语法保留和跨发布 request pin。
- 服务端 `go build ./cmd/server` 与 `git diff --check` 通过。按根任务要求未重复 188 秒完整 Source 或全仓 suite。

当前仍未 commit/push，待 Spec finding 复审；本轮没改服务/publication、SQL、公共类型、工具 formatter 或前端。

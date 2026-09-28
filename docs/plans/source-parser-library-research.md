# 源码解析与结构分块库调研

调研日期：2026-09-28。状态：方案研究，尚未选定依赖或完成性能验收。

本次只阅读本地项目元数据、官方文档及第一方源码；未安装依赖、执行候选库、运行或编译代表仓库。首批编程语言为 Java（以 Java 8 为代表）、JavaScript、TypeScript、Python；Vue 2 SFC 是包含脚本、模板等区域的文件格式，MyBatis XML / SQL 是辅助结构。本研究不改变既有三种 Agent 模式。

## 建议

复用成熟解析器，不自行编写 Java / JS / TS / Python 语法解析器。以 Tree-sitter 作为跨语言结构与代码位置的共同基础，同时优先验收 `tree-sitter-language-pack` 当前内置的结构分块器，避免默认重写通用 AST 分块算法。Vue SFC 使用 Vue 官方解析器提取区域，再解析 JS / TS 脚本；MyBatis 使用成熟 XML 解析能力和项目关系规则。

这个组合在结构、容错和位置依据上比单纯正则切块更有支撑；是否更快、是否提升本项目 RAG 效果，目前没有实测结论。现成分块器仍需通过代码证据要求，不能将“支持 AST”视为已经满足精确引用。

自写部分应限于：源码快照和证据封装、语言节点提取规则、框架关系映射、必要的预算与降级适配，以及 WikiPage / 检索接入。通用库不会替 WeKnora 决定 GitLab 提交版本、知识库权限、过期 WikiPage、引用修订或索引发布流程。

## Tree-sitter：解析基础

Tree-sitter 实际生成 concrete syntax tree，支持增量解析并在语法错误存在时尽量保留可用结构；官方列有 Go、Python 绑定和本次所需语言 grammar。其设计目标包含低延迟，但这不是本仓库吞吐量的证明。解析结构也不等于完成跨文件类型解析或调用图。第一方来源：[项目说明](https://tree-sitter.github.io/tree-sitter/)。

节点提供原始字节偏移及行列坐标；行从零开始，列以字节计，结束字节为区间的右边界。引用正文应从原始输入切片取得，不能用 AST 格式化输出代替。行列转换、CRLF、中文及非 UTF-8 输入需要统一约定。来源：[基础解析与位置定义](https://tree-sitter.github.io/tree-sitter/using-parsers/2-basic-parsing.html)。

| 范围 | 可复用的第一方 grammar | 许可证与验收边界 |
| --- | --- | --- |
| Java | [tree-sitter-java](https://github.com/tree-sitter/tree-sitter-java) | [MIT](https://github.com/tree-sitter/tree-sitter-java/blob/master/LICENSE)；需要用 Java 8 注解、泛型、匿名类、lambda 等代表代码验收，并非本次已验证全部兼容 |
| JavaScript | [tree-sitter-javascript](https://github.com/tree-sitter/tree-sitter-javascript) | [MIT](https://github.com/tree-sitter/tree-sitter-javascript/blob/master/LICENSE)；包含 JSX，最新 ECMAScript 特性仍应按版本验收 |
| TypeScript | [tree-sitter-typescript](https://github.com/tree-sitter/tree-sitter-typescript) | [MIT](https://github.com/tree-sitter/tree-sitter-typescript/blob/master/LICENSE)；TS 与 TSX 为不同 grammar，应按文件及区域选择 |
| Python | [tree-sitter-python](https://github.com/tree-sitter/tree-sitter-python) | [MIT](https://github.com/tree-sitter/tree-sitter-python/blob/master/LICENSE)；必须约定目标语法版本并验证新语法和错误节点 |

维护选择上优先官方绑定和上述 grammar，锁定运行时、grammar 与提取规则版本。不能混用不兼容的 grammar ABI，也不能把上游 `master` 示例版本直接认作本项目部署版本。

## 现成分块器：先验收成品，再决定补多少适配

### 首选验收候选：tree-sitter-language-pack

旧 `kreuzberg-dev/tree-sitter-language-pack` 地址当前重定向到 [xberg-io/tree-sitter-language-pack](https://github.com/xberg-io/tree-sitter-language-pack)。其封装层采用 [MIT](https://github.com/xberg-io/tree-sitter-language-pack/blob/main/LICENSE)；封装许可证不能替代所选 grammar 及二进制依赖的许可证清单。

当前官方分块接口提供原文内容、起止字节、起止行及上下文、定义符号、错误节点等元数据。文档描述按声明收集、预算打包、超大结构递归细分；`chunk_max_size` 是字节预算，不能直接当成 embedding token 预算。它比只有解析树的官方绑定更接近可接入的分块成品。来源：[Chunking for LLMs](https://docs.tree-sitter-language-pack.xberg.io/guides/chunking/)。

| 比较项 | 官方 Tree-sitter 绑定 + grammar | 当前 language-pack 内置 chunker |
| --- | --- | --- |
| 已提供的能力 | 解析树、查询、原文节点区间 | 解析封装、分块、区间和结构上下文 |
| 仍需 WeKnora 提供 | 提取、打包和降级规则，全部业务元数据 | 框架提取、快照及证据封装，必要预算适配 |
| 控制程度 | 直接控制 parser / query 生命周期与结果 | 更少通用分块代码，但需接受或适配成品输出契约 |
| 当前未确认项 | 本语料的覆盖率、错误与性能 | 原文逐字节一致性、超大叶子处理、框架提取覆盖、绑定兼容与性能 |

该包当前原生运行时会在首次需要语言时下载 parser 并缓存，离线生产环境应在镜像构建阶段预置指定语言和版本。下载的是解析器，仍需禁止分析任务运行时临时获取或执行目标仓库依赖。来源：[Download Model](https://docs.tree-sitter-language-pack.xberg.io/concepts/download-model/)。本次没有安装发布包，因此最新文档与最终选定 release 是否一致仍需验证。

### 其他成品的必要比较

| 候选 | 已核验的行为 | 对本方案的判断 |
| --- | --- | --- |
| [LlamaIndex CodeSplitter](https://github.com/run-llama/llama_index/blob/main/llama-index-core/llama_index/core/node_parser/text/code.py)（[MIT](https://github.com/run-llama/llama_index/blob/main/LICENSE)） | 源码递归切分 Tree-sitter 子节点，支持字符 / token 预算及超大叶子硬切；返回字符串并调用 `strip()`。当前实现还检查 parser 的 `parse` 方法，并提示不兼容时使用 language-pack `<1.0` | 可作结构分块对照基线；不能直接用字符串输出承担精确代码区间，也不能假定适配当前 language-pack。若使用，应注入兼容 parser 并保留区间 |
| [ASTChunk](https://github.com/yilinjz/astchunk)（[MIT](https://github.com/yilinjz/astchunk/blob/main/LICENSE)） | [builder 源码](https://github.com/yilinjz/astchunk/blob/main/src/astchunk/astchunk_builder.py)仅接 Python / Java / C# / TSX grammar；预算按非空白内容计算，可递归、重叠、扩展上下文 | 范围未直接覆盖本次全部语言 / SFC，适合作为研究对照；不作为首批默认依赖 |
| [LangChain RecursiveCharacterTextSplitter](https://github.com/langchain-ai/langchain/blob/master/libs/text-splitters/langchain_text_splitters/character.py)（[MIT](https://github.com/langchain-ai/langchain/blob/master/LICENSE)） | `from_language` 选择语言分隔符 / 正则，实际是递归文本切分 | 可作简易基线或显式降级；不能据此认为具有 AST 关系或可靠的方法边界 |

ASTChunk 还有两个与代码证据直接相关的实现边界：超预算节点会递归其子节点，超大无子节点未见独立保留分支，存在丢失该内容的风险；[chunk 源码](https://github.com/yilinjz/astchunk/blob/main/src/astchunk/astchunk.py)会按坐标补空格和换行重建文本，默认输出有行号但无字节区间，并且祖先名称规则仅识别 `class_definition` / `function_definition`。本次未运行复现，不能作为已经验证的错误数量或性能结论。

这些产品通常称“AST 切块”，但底层可使用 Tree-sitter 的 CST。是否保留原文、是否丢内容、输出是否带精确区间，比名称更影响引用可靠性。

## Vue 2 SFC：官方区域解析 + 脚本解析 + 坐标转换

Vue 2 的 `vue-template-compiler` 有 `parseComponent`，可取得 SFC descriptor；`pad` 会人为补行或空格。来源：[Vue 2.6.14 compiler 文档](https://github.com/vuejs/vue/blob/v2.6.14/packages/vue-template-compiler/README.md)。Vue 2.7.16 确实存在名为 `@vue/compiler-sfc`、描述为 Vue 2 编译器的包，不能仅根据包名将其视作 Vue 3；也不能默认使用 `@vue/compiler-sfc@3` 等价解析所有 Vue 2 项目。来源：[2.7.16 包元数据](https://github.com/vuejs/vue/blob/v2.7.16/packages/compiler-sfc/package.json)。Vue 2 源码采用 [MIT](https://github.com/vuejs/vue/blob/v2.7.16/LICENSE)，其开源维护已进入 [EOL](https://v2.vuejs.org/lts/)，因此应锁定解析器并用代表 SFC 验收兼容，而非无条件追最新版本。

Vue 2.7 [parseComponent 实现](https://github.com/vuejs/vue/blob/v2.7.16/packages/compiler-sfc/src/parseComponent.ts)保存块的 `start` / `end`，正文来自原始 JS 字符串的 `slice`，并可能执行去缩进 / 补齐。建议 `pad=false`、`deindent=false`，直接依据 descriptor 区间取原文，再按 `lang` 选 JS / TS grammar。JS 字符串偏移是 UTF-16 单位，Tree-sitter 常用 UTF-8 字节：须显式转换块起点并将脚本内区间提升到整个 `.vue` 原文，中文、emoji、CRLF 是必要验收样例。

模板、样式及 custom block 保留自己的原始区域；第一批优先解析脚本内组件、方法、导入及 API 引用，模板到方法的关系只提取能确定的静态信息。遇到 `src` 外置脚本时关联仓库内目标文件，预处理语言和解析失败明确降级。不执行 `compileToFunctions`、目标项目插件或构建脚本。这里的区域拆分 / 坐标适配需要自写，Vue 的语法解析不需要自写。

## MyBatis XML / SQL：复用 XML，编写框架关系规则

MyBatis 官方配置说明允许通过 mapper `namespace` 和语句 `id` 关联 Java mapper，且存在 `resultMap`、`sql` / `include` 等结构；这些是首批值得提取的关系。来源：[Mapper XML](https://mybatis.org/mybatis-3/sqlmap-xml.html)。`if`、`choose`、`where`、`foreach` 等动态 SQL 依赖运行时条件，静态抽取不能宣称还原了最终 SQL 或全部实际表访问。来源：[Dynamic SQL](https://mybatis.org/mybatis-3/dynamic-sql.html)。

已有 Python docreader 声明 `lxml>=6.1.0`，可复用其 XML 结构能力；[lxml 许可证](https://github.com/lxml/lxml/blob/master/LICENSE.txt)为 BSD-3-Clause。但其 [`sourceline`](https://lxml.de/apidoc/lxml.etree.html)仅给原始行号或未知值，不能单凭它产生准确结束字节 / 行号。精确引用仍需经过验证的区间解析或 XML token 定位方案，保留 CDATA、注释、`include` 片段及原始文本，不能用序列化 XML 当代码原文。关闭外部实体 / DTD 网络解析，并将动态或无法解析关系标为不确定。SQL 进一步分析应复用方言匹配的 SQL parser；首批不自写完整 SQL 语法，也不承诺动态 SQL 完整依赖图。

## 语言专用解析器：作为增强选项

| 库 | 已核验能力 / 许可证 | 首批取舍 |
| --- | --- | --- |
| [JavaParser](https://github.com/javaparser/javaparser) | README 支持 Java 1.0–25，core 做 AST，独立 SymbolSolver 做声明关联；LGPL / Apache 双许可可选择 Apache-2.0 | Java 语义增强候选；引入 JVM 和符号解析配置，不必为了首批结构分块就引入。节点 range 到原始字节映射仍须验收 |
| [TypeScript Compiler API](https://github.com/microsoft/TypeScript/wiki/Using-the-Compiler-API) | `createSourceFile`、节点遍历、位置转换，结构遍历可不创建 type checker；[Apache-2.0](https://github.com/microsoft/TypeScript/blob/main/LICENSE.txt) | 需要深入 TS / JS 类型关系时再增强；单文件静态解析无需运行目标构建，位置单位仍须转换 |
| [Python ast](https://docs.python.org/3/library/ast.html) | 行号、UTF-8 列偏移及 `get_source_segment`，结束位置可能缺失；`feature_version` 仅 best effort；[PSF 许可](https://docs.python.org/3/license.html) | Python 专项增强 / 对照。可解析语法受运行时限制，语法错误需降级；`unparse` 不能替代证据原文 |

以上能力不意味着在无依赖环境下可精确解析所有反射、动态调用或框架注入关系。首批应输出可验证静态边和置信依据。

## Go / Python 执行位置

本地 `go.mod` 声明 Go 1.26.0，`docreader/pyproject.toml` 声明 Python >=3.10.18；它们不是实际部署运行时已经兼容候选包的证据。

- **Go 官方绑定**：能留在 Go 处理链内，减少跨进程序列化；但使用 CGO / 原生内存，官方要求 Parser、Tree、Query 等对象显式 `Close`，不能依赖 finalizer。[绑定文档](https://github.com/tree-sitter/go-tree-sitter)、[当前 go.mod](https://github.com/tree-sitter/go-tree-sitter/blob/master/go.mod)显示 Go 1.23 最低声明。需要考虑平台编译、隔离与超大文件内存。
- **Python 官方绑定或分块 worker**：可沿用 docreader 现有部署能力，但建议源码分析以独立任务 / 资源限额执行，避免重型源码分析占满文档读取队列。官方 Python 绑定有主流平台 wheel，运行时和 grammar 分包；[当前元数据](https://github.com/tree-sitter/py-tree-sitter/blob/master/pyproject.toml)要求 Python >=3.10，[绑定说明](https://github.com/tree-sitter/py-tree-sitter)采用 MIT。language-pack 是另一套封装，不能假定其当前对象与官方 Python Parser API 可互换。

若团队希望最快验证现成分块器，先采用 Python worker 候选较顺现有生态；若部署希望统一 Go，官方绑定仍可行。是否多进程更快、IPC 是否成为瓶颈，需要测量后决定，不能以 Python / Go 名称直接推断。Vue 官方 SFC parser 是 Node 生态依赖，这一小型解析步骤也需要明确打包位置；无论放哪里都不运行目标仓库代码。

后续补充核查：language-pack 的当前第一方接口同时有 Python `process(source, ProcessConfig)` 和 Go `Process(source, ProcessConfig)`，两者均提供成品分块配置；选择成品 chunker 并不强制 Python。Go 包使用 CGO，仍需原生构建与生命周期管理。来源：[Python API](https://docs.tree-sitter-language-pack.xberg.io/reference/api-python/)、[Go API](https://docs.tree-sitter-language-pack.xberg.io/reference/api-go/)、[Go 包说明](https://github.com/xberg-io/tree-sitter-language-pack/tree/main/packages/go)。Python 当前结构项的 decorators 字段未实际提取，Java 注解与 Spring 关系仍需项目规则；不同绑定的位置文档还存在版本漂移，最终精确区间必须针对锁定 release 验收。未安装或执行这些 API。

用户在 Q26 确认标准 Docker 部署优先与独立解析进程，Lite 可连接同一外部服务；Q29 进一步确定独立 Python 解析服务与固定版本官方 Node SFC 组件。这消除了首期必须将全部解析运行时内置到 Lite 单应用包的约束；具体依赖 release 与平台兼容仍需验收。

## 代码证据契约与后续验收

这些是建议的 WeKnora 适配要求，尚非已实现接口：每个源码块保存代码同步源、源码快照 / 固定 commit SHA、仓库内路径、内容哈希、原始字节区间、展示行区间、语言 / 区域、符号及父结构、解析器 / 规则版本、降级或错误标记。块正文必须能回到这一快照的原始片段。补充的签名 / 上下文用于检索时与证据正文区分，不能因此声称正文具有单个原文区间。

超大类 / 方法依声明、语句或块细分；超大字符串、注释、错误节点需要保持内容覆盖并安全降级。embedding 预算应按实际 tokenizer 校验，不能用字节、字符或非空白长度替代。增量首先依据 Git diff / blob hash 复用未变化文件产物；Tree-sitter 的 edit / reparse 属于单文件解析优化，不等同于 GitLab 增量同步。

后续在获得实现 / 实验授权后，使用同一份代表快照对比“官方 Tree-sitter + 适配规则”“language-pack 成品 chunker”及一个简单文本基线，避免同时扩大候选范围：

1. **正确性**：原文切片一致、行号一致、可读源码覆盖；Java 8、四种语言及 Vue 脚本分别统计成功 / 降级比例，验证重复片段、Unicode、CRLF、语法损坏、外置脚本、超大方法和叶子。
2. **性能与成本**：全量及小改动同步的墙钟时间、吞吐、峰值内存、失败 / 超时、块数、token 分布；分开统计解析 / 分块、embedding、索引和 Wiki 生成，防止模型网络开销掩盖解析差异。
3. **检索效果**：用人工标注的类 / 方法 / 路径 / Mapper ID 查询和中文业务问题，比对 Recall@k、MRR、证据区间正确率及上下文 token 成本；同 embedding、同检索配置和预算。
4. **发布约束**：锁定 binding / grammar / chunker 版本，验证目标镜像平台和离线运行，产物记录许可证清单；变化规则后重新计算派生产物版本。

当前未验证：目标部署平台 wheel / CGO 组合、language-pack 发布版本的精确输出和超大叶子行为、全部 Java 8 / Python 语法覆盖、MyBatis 区间方案、SFC 旧项目兼容率及实测性能。上述候选具备减少语法和通用分块维护工作的依据，但没有本项目 benchmark；此处不报告速度倍率或效果提升百分比。

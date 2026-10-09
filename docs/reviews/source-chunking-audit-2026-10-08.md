# 源码分块审查：全部受支持格式

日期：2026-10-08。分支：`codex/gitlab-code-wiki-rag`，HEAD `47889f9e`。

## 结论

当前方案能保留原文字节、控制单块上限，并验证引用坐标和索引 token 预算；但不能保证块有足够的独立语义。截图中的 `let`、`store =`、`new Vuex.Store` 确实是入库块，并非前端把同一个块显示成多个卡片。

**大小不一致正常；关键字、括号、纯空白独立进入检索索引不合理。** 小文件或简短的完整声明也可以只有几十个字符，不能把所有短块都判成错误。长函数、配置对象、SQL 则应在超出预算后按内部语义边界拆分，并携带所属声明上下文。

本次完成审查与复现，没有修改分块算法、重建快照或调用 embedding 服务。检索效果的影响是基于内容与索引路径作出的推断，未测量召回率或回答质量的变化。

## 检查范围与方法

以 `internal/source/parser_client.go:24` 的 `LanguageForPath` 和 `sourceparser/server.py:389` 的实际 HTTP 校验为边界，覆盖全部 **40 种受支持后缀**，以及 Dockerfile、Containerfile 和其带后缀名称。另单独检查常见隐藏文件名 `.env`。

- 对部署中的真实解析器执行 120 次 HTTP 检查：60 个合成样例，每个使用 4096 和 512 字节预算。
- 补充 24 次检查：Vue TS/TSX、未知预处理器、`.d.ts`、中文/emoji、UTF-8 BOM、CRLF、超长 Vue 单行和 `.env`；预算为 64 和 4096 字节。
- 总计 144 次请求，140 次成功；4 次失败均为 `.env` 的 HTTP 400。所有成功请求的原文重建及字节预算检查通过；补充检查中的字节范围、行号、连续覆盖也通过。
- 对分支本地代码与部署容器的 `runtime.py` 做 SHA-256 比对，完全一致：`98f91e7ca9eb3f8022c94ea047067a281602fd47fd9243286d5f37276203a2ef`。
- 解析版本为 `source-pack-1.19.0-rules-11-2b80ad82f0e6727e440d3175d7905f0e`，实际 `store.js` 入库版本也相同，排除了仅由历史解析器造成的问题。
- 8 项现有 Go 路由、模型预算、索引文本和上下文裁剪测试通过。它们保障预算与来源，不检查碎片是否有独立语义。

合成样例不会执行目标源码，未读取或保存仓库中的凭据。共享文本回退格式不做语法验证；检查其后缀路由、文本切分和字节保真，不能据此宣称具备该语言的结构解析能力。本文也不构成对所有项目语法变体的穷尽验证。

## 全格式检查结果

| 格式 | 当前分块路线 | 检查结果与不足 |
| --- | --- | --- |
| `.java` | Tree-sitter / language-pack | 大方法可拆出独立的类签名、方法签名和 `{`、`}`；小类能保持整块。 |
| `.js`、`.jsx`、`.mjs`、`.cjs` | JavaScript grammar | 全部复现 `let`、`store =`、构造表达式前缀、括号独立成块；额外 JSX 元素样例也出现 `export`、`const`、箭头前缀。 |
| `.ts`、`.mts`、`.cts` | TypeScript grammar | 全部复现 `export`、`const`、`;` 碎片；`.d.ts`、BOM/CRLF 样例坐标通过。 |
| `.tsx` | TSX grammar | 大 JSX 树拆出 `export`、`const`、`View =`、`() =>`、`;`。 |
| `.py` | Python grammar | 大 async 函数的装饰器和签名单独成块；长多行字符串拆出赋值前缀和三引号。小装饰器函数保持整块。 |
| `.vue` | SFC 区域 + script grammar + 原文切片 | JS/TS/TSX script 继承上述问题。template/style 不按元素或规则切分，只按字节；wrapper/gap 独立成块。短 SFC 样例仅约百字却产生 7 块。外部脚本与未知预处理器的降级标记保留，未加载外部文件。 |
| `.xml` | MyBatis 语句区域；普通 XML 降级 | statement/resultMap/sql 区域边界有价值，但每个区域间的空白也单独成块。200 条短 select 的样例产生 401 块，其中 200 块仅换行。超大 SQL 的 partial 标记与字节覆盖通过。普通 XML 样例为 text_fallback，无通用 XML 元素分块。 |
| `.html`、`.htm`、`.jsp`、`.jspx`、`.tag`、`.tagx`、`.ftl`、`.ftlh`、`.vm` | 文本回退 | 路由、字节覆盖和上限通过；优先换行，无法保证标签树、模板指令配对。 |
| `.css`、`.scss`、`.sass`、`.less`、`.styl` | 文本回退 | 逐一通过保真与上限检查；不认识选择器、规则块或 Sass/Stylus 缩进，可能切在同一规则内部。 |
| `.yaml`、`.yml`、`.json`、`.toml` | 文本回退 | 不按键路径或对象分块。单行 JSON 能保持字节，但会切在字符串或对象中间，缺少上层键上下文。 |
| `.properties`、`.ini`、`.conf`、`.cfg`、`.env` 后缀 | 文本回退 | `audit.env` 等文件正常；未保证配置段、键值完整。**隐藏文件名 `.env` 被解析器拒绝。** |
| `.sql` | 文本回退 | 不按完整 SQL statement、CTE 或子查询分块；MyBatis 内部的 SQL 边界规则没有用于独立 `.sql`。 |
| `.sh`、`.bash` | 文本回退 | 不按函数、命令或 heredoc 分块；文本尾块可能短，但短尾块不能单凭长度判错。 |
| `.md`、`.txt` | 文本回退 | 不按标题、段落或 fenced code block 分块；长段和长行仍可能在内部截开。 |
| Dockerfile、Dockerfile.*、Containerfile、Containerfile.* | 文本回退 | 路由与保真通过；不按指令及反斜线续行分块。 |

当前路由未包括 Go、Rust、C/C++、C# 等格式；本次不把它们当成已支持语言。

## 真实知识库抽查

在用户展示的源码知识库（`65658207-a2ec-47fb-bf0f-11e7b685369e`）中，查询有效 knowledge/chunk 行：

| 格式 | 文件数 | 块数 | 纯空白块数 |
| --- | ---: | ---: | ---: |
| JS | 53 | 2,027 | 0 |
| Java | 7 | 1,391 | 0 |
| Vue | 62 | 629 | 40 |
| XML | 4 | 3,376 | 784 |
| SCSS | 40 | 51 | 0 |
| CSS | 3 | 3 | 0 |
| HTML | 2 | 2 | 0 |
| JSON | 2 | 2 | 0 |
| Markdown | 2 | 2 | 0 |
| Stylus | 1 | 2 | 0 |
| YML | 1 | 1 | 0 |
| 合计 | 177 | 7,486 | 824 |

另有 1,563 个非空白块，去除首尾空格、换行、tab、CR 后不足 30 字符。这只是筛查指标，包含合法短声明，不能作为错误块数。

两份 `store.js` 分别有 9 和 43 块；均存在独立 `let`、`store =`、`new Vuex.Store`。43 块的版本还存在 `await`、`return`、单独括号等。实际 `let` 块只有 4 字节，质量为 `structural`，上下文文本为空，仅带模块符号。来源坐标准确，但独立检索信息不足。

## 按优先级列出的发现

### P1：结构切分没有重新组合语义单元

`sourceparser/runtime.py:883` 将字节预算传给 language-pack；`:1043` 直接使用其块范围，`:1164` 原样构造返回块，没有相邻碎片合并步骤。

超大声明被向内部节点拆分时，父声明的关键字、赋值、分隔符等也成为独立范围。小 JS 声明样例只有 1 块，大 Vuex 对象在默认 4096 预算下有 13 块，其中包含上述前缀与括号。这直接复现截图，说明主要原因是大节点拆分规则及缺少后续合并。

`internal/application/service/datasource_source_sync.go:438` 对每一个返回块创建存储块和索引。展示层隐藏碎片不能修复这条索引路径。预计会增加低信息召回和重复上下文，但尚未做召回评测。

建议：完整声明优先；超限后按成员/语句拆分，前缀、装饰器、结束分隔符附着在同一声明的相邻语义块；独立的完整 import、短方法或配置项不能仅因长度短而强行合并。合并必须再次验证 token 预算与来源范围。

### P1：MyBatis 与 Vue 的空白/包装片段被独立索引

MyBatis 的 `sourceparser/runtime.py:1089` 对每个结构区域前面的间隙调用 `append_bounded`，短换行自然成为独立块。Vue 的 `:525`、`:557` 和 `:577` 也保留包装和区域间隙。这些范围对完整原文覆盖有用，索引价值却很低。

建议：将空白附着到相邻语义块；Vue 正常 wrapper 与所属区域组合，避免跨 template/script/style 混合语义。对于有独立降级含义的外部脚本标签、未知预处理器区域，保留其证据与质量信息。若采用“证据切片”和“检索单元”两层模型，应明确坐标与索引关系，不能简单丢弃原文字节。

### P2：`.env` 的文件名判断不一致

Go 的 `path.Ext(".env")` 路由到 text；Python 的 `Path(".env").suffix` 为空，`sourceparser/runtime.py:688` 因此拒绝；HTTP 校验调用同一判定。`.env` 在 64、512、4096 预算下全部 HTTP 400，而 `named.env` 正常。

建议：在入口与 worker 中对齐文件名规则，并加入实际隐藏文件名的端到端路由检查。不要未经明确支持就把 `.env.production` 等所有复合名称一概纳入。

### P2：模板、样式、配置等目前只有文本保真，没有格式语义分块

普通文本用 `:694` 的 UTF-8 预算切片，在预算后半部分寻找换行；Vue 非 script 区域用 `:224` 的 `_split_raw`，甚至不优先换行。这些路径不会丢字节，但可能切开 JSON 值、CSS 规则、HTML 节点、SQL 语句等。

建议依格式补充确定性边界：配置的键/段落/对象，样式的规则，模板的元素/指令，SQL 的 statement/CTE，Shell/Docker 的完整命令和续行，Markdown 的标题段落与代码围栏。未知语法保留明确 text_fallback，长不可分单元才使用 bounded partial，不执行仓库代码。

### P2：质量标记不能代表语义完整

`:1139` 的 oversized_unstructured 判定要求 node_types 为空且实际字节长度达到 max_bytes。长字符串样例在 4096 预算下标 partial，在 512 预算下却标 structural；Python 长多行字符串也仍标 structural。现有 `test_large_script_leaf_and_decorated_context_remain_bounded_and_verifiable` 还明确断言 structural，说明这不是仅补一个测试就能修复的问题。

建议明确区分“语法解析成功”与“检索块语义完整”，针对被迫切开的字符串/注释/单个词法单元记录 partial 及诊断；不能将 structural 当成块可独立解释的保证。

### P2：token 适配重新降低整份文件的字节预算

`internal/source/parser_client.go:92` 从 4096 开始逐次减半；`:202` 只要发现一个块超限便重新解析整份文件。预算控制本身有必要，但当前做法会增加其他区域的分块数量，也无法解决短前缀问题。

建议只细分超限语义单元，按最终索引文本的真实 token 数装箱；路径、签名、上下文也应计入。保留当前拒绝超限及不可验证结果的保障，避免固定字符数代替 token 预算。

## 修复后的验收要求

1. 所有 40 种后缀与 Docker/Containerfile 名称都有路由检查；`.env` 实际名称与入口一致。
2. 短完整文件保持整块；大 Vuex 对象、嵌套函数、TSX、Java 方法、Python decorated/async 函数，不产生独立关键词/赋值前缀/括号检索块。
3. MyBatis 多语句与 Vue 区域无纯空白检索块；片段坐标、区域、降级标签仍可追溯。
4. 每块、每个上下文均为准确原文范围；连续重建不缺字节、不重复；UTF-8/BOM/CRLF/中文/emoji 与行号正确。
5. 不可分长字符串、注释、词法单元明确降级；大语义单元内的合法拆分保留所属签名/键路径。
6. 最终索引文本（正文、路径、签名、上下文）满足模型 token 限制；不降低不相关区域的预算。
7. 建立真实查询集，对文件/函数定位、上下文完整性、重复召回、块数、embedding 成本做修复前后比较。
8. 更新包含分块规则的 parser fingerprint。已有源码版本和索引需要通过新候选快照重新解析/索引再发布，不能只刷新前端或直接覆盖旧证据。

## 本地复现材料

合成检查脚本与 JSON 结果位于明确标记的忽略目录 `.codewiki-dev/`：

- `audit-source-formats.py` / `source-format-audit.json`
- `audit-source-edge-cases.py` / `source-edge-audit.json`
- `check-source-fragments.ps1`：当前实际入库 `store.js` 的关键词碎片检查返回 FAIL。

全格式脚本通过标准输入在运行中的解析器容器中执行，不更改容器文件：

```powershell
Get-Content .codewiki-dev/audit-source-formats.py -Raw |
  docker exec -i weknora-source-parser-root-t22-review python -
```

现有 Go 检查：

```powershell
go test ./internal/source -run '^(TestLanguageForPathRoutesSupportedSourceAndTextFiles|TestParseFileWithProfile.*|TestSourceIndexTokenCountsHeaderBodyAndCompleteText|TestNewIndexProfileRequiresAndBoundsConfiguredTokenizer|TestIndexProfile.*)$' -count=1
```

诊断技能的修复及修复后回归阶段本次不执行，因为本次任务是审查方案；问题尚未修复，不能以保真测试通过宣称分块质量合格。

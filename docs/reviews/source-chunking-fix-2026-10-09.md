# 源码分块修复与验证

日期：2026-10-09。分支：`codex/gitlab-code-wiki-rag`。

## 修复

- 新增 `sourceparser/chunking.py`。从已锁定的 Tree-sitter AST 选择完整声明、成员、语句和集合元素边界，按预算组合相邻原文范围；关键字、赋值前缀和括号不再作为独立分块单位。命名声明的完整签名可在预算无法同时容纳正文时成为边界。
- 超长不可分字符串、注释和强制切片标为 `partial`，语法错误仍优先保留 `syntax_error`。Vue 文件也传播 script 的 partial 质量。
- MyBatis 将能容纳的短空白间隙及 mapper 结束标签附着到相邻块，语句仍保持独立证据范围。
- Vue 正常标签附着到所属 script/template/style 正文；预留标签预算，不跨区域组合。外部脚本、未知预处理器和空标签的独立证据及诊断仍保留。
- 文本回退与 Vue 非 script 正文优先完整行；Shell/Docker 反斜线续行不提供优先切点。修复 worker 对字面文件名 `.env` 的拒绝。
- 新增构造器/函数调用中命名对象绑定的签名上下文，避免 `store.js` 只有文件名上下文。所有上下文仍是可验证的独立原文范围。
- 更新聚合 parser fingerprint 的分块规则版本及 Docker 构建文件，保证新模块被打包且下次源码同步不复用旧分块加工缓存。

Go 仍对正文、路径、签名及完整索引文本统计真实 tokenizer token；超限时仍逐次降低文件字节预算。该预算适配未在本次改成逐块重解析。文本回退格式仍没有新增结构语法：JSON/CSS/SQL/模板等可在超长行内发生有界切分，不能据此宣称每块都是完整对象或语句。

## 回归与集成验证

最终结果：正式构建镜像内的 **104 项解析器测试全部通过**；覆盖全部受支持格式的 **144 次 HTTP 检查全部通过**，无原文覆盖、坐标或预算失败；`go test ./internal/source -count=1` 在启用真实解析器集成检查后通过；`git diff --check` 通过。

已构建镜像：`weknora-source-parser:semantic-chunks-20261009`，digest `sha256:960966266099de1dac611ea4f38c997f21683d7b7bb162cb338d4986037cfb9c`。

新增 HTTP 回归覆盖所有 40 种后缀及 Dockerfile/Containerfile 名称，并检查：

- JS/TS/TSX、Java、Python 在 64/512/4096 字节预算下无独立关键词/括号碎片；短完整文件保持整块。
- Python 普通/增强赋值、JS/Python 数组元素有结构边界；可容纳的多行字符串保持完整，超长字符串明确降级。
- MyBatis 多语句间隙、Vue 正常标签归属、Vue partial 传播。
- UTF-8/BOM/CRLF/中文/emoji 的原文字节、连续范围和行号。
- 150 组固定随机种子的 Unicode 数据，分别检查 64/128/512/4096 字节预算。

新增 Go 集成检查 `TestSemanticParserWithRealTokenBudget`，通过真实 HTTP worker 调用生产 `ParseFileWithProfile`，使用 240 token 预算验证 8 类文件的完整索引文本、原文重建及碎片消除，不调用 embedding 服务。

完整解析器测试使用项目部署配置相同的 `--init`，在正式 Dockerfile 构建出的镜像中执行。首次未使用 init 的测试容器曾导致超时后孤儿进程无法回收；按 `docker-compose.source.yml` 中 `init: true` 重跑后，进程树清理检查通过，没有通过跳过该项来消除失败。

## 真实源码只读重放

从用户展示的知识库中读取有效块所引用的 198 份源码版本，送入独立新版 HTTP worker。该集合含引用版本，不等同于先前审查中的 177 个文档行；没有写入数据库或重建实际向量索引。检查全部通过，原文字节、连续范围、行号和 4096 字节上限保持正确。该次重放未出现纯空白块，亦未出现检查集合中的独立 `let`、`const`、`export`、`await`、`return` 和括号碎片。

两份实际 `store.js`：

| 原块数 | 新解析块数（4096 字节预算） | 独立关键词/括号碎片 |
| ---: | ---: | ---: |
| 43 | 4 | 0 |
| 9 | 2 | 0 |

这是解析器重放结果，实际模型预算可能进一步降低字节预算。块数下降不是检索效果评测；本次未测量问答召回率。

## 代码审查中修正的边界

1. 不将长字符串的解析成功等同于片段完整；用实际 AST 词法范围判断切点，保留 partial 诊断。
2. 不让 partial 覆盖 syntax_error，不遗漏 Vue script 的 partial 文件质量。
3. 当类/方法前缀与正文无法共同容纳时，允许完整签名边界，避免强制切进名称；包含可容纳的完整赋值和数组元素。
4. MyBatis 片段合并后的 partial 标记按范围重叠计算，不依赖合并前的精确范围 tuple。
5. 用排序、二分查找和有序间隙扫描替代对每块遍历所有词法区间，避免新增平方级范围处理；保留迭代 AST 遍历。
6. 构建阶段与最终镜像均包含新模块，使用已构建镜像再次运行完整检查。

## 生效范围

首次本机容器替换命令被自动审批拒绝。用户随后明确授权部署、重新建立全部四个已移除连接，并确认 nsb 仅同步当前发布的 11 个文件。后续部署与同步现已完成。

本机正式解析服务 `weknora-source-parser-root-t22-review` 已运行新版镜像，端口仍为 loopback 57823，健康状态 healthy。保留旧容器 `weknora-source-parser-before-semantic-20261009` 作为回退副本；部署使用 init、只读文件系统、原有 CPU/内存/PID 限制和能力限制。

恢复连接采用限定租户、知识库和四个原数据源 ID 的一次性维护事务。凭据通过已有 GitLab 登录验证后以 AES-256-GCM 存储；保留原文档、数据源和发布身份，同时推进配置代次及 fencing token，避免复制新连接产生重复文档或恢复旧任务。普通 PUT/Resume 的生命周期防护没有修改。

### 当前正式发布结果

| 数据源 | 文件数 | 当前块数 | 同步状态 | Wiki 派生 |
| --- | ---: | ---: | --- | --- |
| T22 source dashboard | 61 | 914 | 成功 | 完成 |
| T22 source mobile | 102 | 353 | 成功 | 完成 |
| CodeWiki MVP mobile system module | 3 | 13 | 成功 | 完成 |
| T22 source nsb | 11 | 1,113 | 成功 | 完成 |
| 合计 | 177 | 2,393 | 无失败文件 | 完成 |

通过 `source_publications` 的当前发布指针核对，177 个文件全部使用新版解析器 fingerprint；2,393 个当前块中，纯空白块及检查集合中的独立关键词/括号块均为 0。历史版本仍可被 Wiki 固定证据引用，不将保留历史行误算成当前块。

两份实际 `store.js` 在当前发布版本中分别为 4 块和 2 块。浏览器刷新后验证其中一份显示“共 2 个片段”，查看分块时仅显示“片段 1”“片段 2”。四个数据源页面均显示同步成功；源码文档数保持 177，没有增加重复文档。页面结果已保存为本机截图。

部署记录和完整核对指标见本机忽略目录 `.codewiki-dev/source-parser-deployment.json`、`source-sync-verification-20261009.json`。四次同步日志 ID：`8c91ab4c-3d9f-4629-a0e5-2240796fb3b7`、`db31c84c-fe08-4bd8-be51-22b400fa69b2`、`3a657dad-aa0f-4162-9494-00909bf94b11`、`10628d6f-1963-4fb6-8623-20bd367051f4`。

合成检查脚本、重放指标与测试日志在明确标记的本机忽略目录 `.codewiki-dev/`，不包含保存的仓库源码或凭据。主要日志：`chunk-all-tests-final.log`、`source-format-audit-fixed.json`、`source-edge-audit-fixed.json`、`source-corpus-replay.json`。

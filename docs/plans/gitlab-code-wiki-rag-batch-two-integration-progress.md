# T05 / T08 集成验收及三路继续执行

2026-09-29。用户因额度恢复要求继续当前总协调与三个执行对话；三个对话均已收到恢复指令，沿用 GPT-6 Luna / xhigh。当前对话继续负责规划、双轴审查、复验与集成。原工作目录 D:/Project-Weknora/WeKnora 保持 main；功能集成 worktree 为 C:/Users/28211/.codex/worktrees/source-integration/WeKnora。

## 已完成并关闭

| Ticket | 最终实现审查提交 | 集成功能与文档提交 | GitHub 验收 |
| --- | --- | --- | --- |
| T05 / #13 | d61f6f92；随后 ecbdfc74 只澄清受控模型 fixture 文案 | 21573a16、7e15f236、5814d90e、d1ed3634、583e017b | [已关闭及验收评论](https://github.com/rudyvv/myWeknora/issues/13#issuecomment-5886966278) |
| T08 / #16 | fd1f2799 | f1b699b7、9009546f、7f4fd1dc | [已关闭及验收评论](https://github.com/rudyvv/myWeknora/issues/16#issuecomment-5887100508) |

两项实现已推送 myWeknora/codex/gitlab-code-wiki-rag，GitHub 页面均显示 Closed；累计 8 / 22。父 Spec 与其余票保持 open。T05 完整清单对账及失败状态、T08 Python 原始结构及缓存版本修复通过各自全部 AC；没有声称真实大仓库、真实模型质量或整体源码模式已经验收。

## Standards

固定用户批准起点 3adaa587，reviewer 明确为 GPT-6 Sol / high，按最终冻结提交完整 diff 检查。T05：0 剩余文档违反、0 可执行判断性建议。T08：0 文档违反，1 条已接受的 P3 possible Duplicated Code（两种 import 语法的局部别名归一化）；该建议不是硬规则或正确性缺陷，总协调接受保留，不要求无必要的抽象。深递归风险与测试 loader 重复已修复。

## Spec

T05、T08 最终均为 0 剩余 finding。T05 先窄检查 repository 存在，再读取旧发布状态，之后进行完整 readiness；查询成功标记 publication_checked，界面区分已保留发布、已确认无发布、状态未知。T08 Python 提取规则 v4 与原四语言 v3 分离；同一锁定 grammar 的五语言 fingerprint 改变，而四语言 identity 保持，旧 same-SHA artifact 被重新解析并在公开阅读中保留 imports。独立 reviewer 核验 fingerprint 和完整冻结代码，不把 worker 测试记录称为 reviewer 独立执行。

## 总协调实际执行

在 7f4fd1dc 集成状态下：

| 检查 | 结果 |
| --- | --- |
| 11 项 service 公开及单元交叉用例，真实独立 PG / Git / parser | PASS，70.045s；覆盖 Python 同 SHA 缓存与双索引、固定阅读、force-push、readiness 保留、跨源同路径、问答快照与组合 file/tag、空发布、历史 Wiki 与 clear、普通文档混合以及 nil repository / artifact key 单元。 |
| 五语言 parser HTTP suite | 27 tests PASS，25.896s。 |
| SourceSnapshotRunView + SourceCodeView | 5 tests PASS，0 skipped，8.158s。 |
| Vue 类型检查 | vue-tsc --build PASS，exit 0。 |
| internal/source 单元回归 | PASS，3.374s。 |
| 集成前 T05 三项 PG 复验 | PASS，18.304s；四项 UI PASS，0 skipped。 |
| git diff --check | PASS。 |

PG 使用用户选择的独立 weknora-source-batch-two-test / 127.0.0.1:57521 / source_test，每测试独立 schema，不读取原容器密码或更改容器。真实模型、GitLab 与队列为受控外部边界，parser、Git 历史、PostgreSQL BM25/vector 行为实际执行。完整 Source、全仓 Go / 前端以及构建将在 T10 合并后集中执行，当前没有重复或宣称已通过这些完整检查；首批已披露的 Windows 测试限制继续保留。

## T10 仍在进行

原 fc826c19 尚未接入生产链路且使用自定义 Go 语法解析，已按总协调决定迁移到隔离 Tree-sitter / Expat / SQLGlot。parser 检查点 fd61229c、132ae696、68305937 分别记录阶段实现与修复；它们不代表整票验收。常量按类型、嵌套 binary namespace、重复 fragment 诊断、动态 fragment 保守降级与注解类型遮蔽均已要求真实反例。最新 parser-only 检查点 36d5ffff 修复 MySQL DML alias 和 UPDATE join 物理表提取，并将 XML statements/fragments 独立切块；worker 记录 31 项 HTTP tests PASS，两个 Sol/high reviewer 正复核冻结提交。Go typed facts、staging、原子发布、两端授权阅读与公开 PG 链路继续执行；没有提前关闭 #18。

## 复用对话继续并行

用户已确认 T06、T09 的实现与审查起点 7f4fd1dc。旧分支和已验收提交保留，工作树清洁且此前对话 idle 后才切换新分支，没有覆盖未提交内容。

| 对话 | Thread ID | worktree / 分支 | 范围 |
| --- | --- | --- | --- |
| T06 源码更新调度与重启恢复 | 01a0ebfe-56df-7223-8644-ca717bd658bd | b0df/WeKnora；codex/source-durable-triggers | #14；durable trigger、lease/fencing、追赶、恢复及 outbox；migration 000108 |
| T09 Vue SFC 区域解析与检索 | 01a0ebfe-8153-70e0-898e-2b7ffa91eaad | a8ea/WeKnora；codex/source-vue-sfc | #17；官方 Node SFC 2.7.16、UTF-16/UTF-8 原文件区域映射、公共检索及阅读；暂无 migration |
| T10 MyBatis Mapper 与 XML SQL 关联检索 | 01a0ebfe-da20-71d2-a963-fa965754b497 | 2119/WeKnora；codex/source-mybatis-sql | #18；SQLGlot/Expat/AST、快照关系及权限链路；migration 000107 |

三个执行对话均禁止自行改父 Spec、发规划/审查 Agent、push 或关闭 Issue。共享协议和合并由当前对话决定，各执行对话优先新增独立模块并交付固定 checkpoint，当前对话继续指出问题，Luna 修复，然后复验集成。

T09 官方实现依据：[Vue 2.7.16 parse](https://github.com/vuejs/vue/blob/v2.7.16/packages/compiler-sfc/src/parse.ts)、[parseComponent](https://raw.githubusercontent.com/vuejs/vue/v2.7.16/packages/compiler-sfc/src/parseComponent.ts)。显式关闭 deindent/pad，禁止用变换后区域猜测原文位置。T10 命名依据：[MyBatis XMLMapperBuilder](https://mybatis.org/mybatis-3/xref/org/apache/ibatis/builder/xml/XMLMapperBuilder.html)、[Java Class.getName](https://docs.oracle.com/javase/8/docs/api/java/lang/Class.html)；成员 mapper 采用 binary name，未知局部身份保守诊断。

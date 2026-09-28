# GitLab 源码知识：实施 tickets

对应 [GitHub Spec #8](https://github.com/rudyvv/myWeknora/issues/8)、[本地 Spec](gitlab-code-wiki-rag-spec.md) 与已确认的 [整体方案](gitlab-code-wiki-rag-implementation-plan.md)。用户已批准 Spec、测试入口和 22 项拆分；2026-09-28 已发布全部 tickets，应用 ready-for-agent，并登记原生子 Issue 与 36 条直接阻塞关系。

每项是一条可验证的业务路径。T01 保持文档兼容并隔离源码配置与处理路由，T02 建立小型 Java 首条真实链路，再扩大语言、更新、查询和卡片能力。源码模式整体发布须经过 T22，不能把中间切片当作全部功能完成。

## 已确认的测试入口

主要使用现有数据源/知识库公开 API 与 Agent 工具公开调用；外部 GitLab、时钟和模型可控，坐标使用真实锁定解析器，发布/并发/范围使用真实 PostgreSQL/ParadeDB。完整约定见 Spec 的 Testing Decisions。

## Tickets 与阻塞边

| Ticket | GitHub Issue | Blocked by |
| --- | --- | --- |
| T01 | [#9 GitLab 源码模式配置与固定提交过滤预览](https://github.com/rudyvv/myWeknora/issues/9) | 无 |
| T02 | [#10 Java 小范围首次同步、双索引检索与固定版本代码阅读](https://github.com/rudyvv/myWeknora/issues/10) | [#9](https://github.com/rudyvv/myWeknora/issues/9) |
| T03 | [#11 多仓库源码查询范围与同次问答快照一致性](https://github.com/rudyvv/myWeknora/issues/11) | [#10](https://github.com/rudyvv/myWeknora/issues/10) |
| T04 | [#12 源码增删改、重命名与配置变化的完整版本更新](https://github.com/rudyvv/myWeknora/issues/12) | [#11](https://github.com/rudyvv/myWeknora/issues/11) |
| T05 | [#13 Force-push、分支与鉴权异常的源码对账](https://github.com/rudyvv/myWeknora/issues/13) | [#12](https://github.com/rudyvv/myWeknora/issues/12) |
| T06 | [#14 手动与定时源码更新的串行、追赶和重启恢复](https://github.com/rudyvv/myWeknora/issues/14) | [#12](https://github.com/rudyvv/myWeknora/issues/12) |
| T07 | [#15 JavaScript 与 TypeScript 结构检索和原始位置引用](https://github.com/rudyvv/myWeknora/issues/15) | [#10](https://github.com/rudyvv/myWeknora/issues/10) |
| T08 | [#16 Python 结构检索和原始位置引用](https://github.com/rudyvv/myWeknora/issues/16) | [#10](https://github.com/rudyvv/myWeknora/issues/10) |
| T09 | [#17 Vue SFC 区域检索与整文件坐标](https://github.com/rudyvv/myWeknora/issues/17) | [#15](https://github.com/rudyvv/myWeknora/issues/15) |
| T10 | [#18 Java Mapper 与 MyBatis XML/SQL 关联检索](https://github.com/rudyvv/myWeknora/issues/18) | [#10](https://github.com/rudyvv/myWeknora/issues/10) |
| T11 | [#19 前端 API 到 Java 服务及 SQL 的静态业务链](https://github.com/rudyvv/myWeknora/issues/19) | [#17](https://github.com/rudyvv/myWeknora/issues/17)、[#18](https://github.com/rudyvv/myWeknora/issues/18) |
| T12 | [#20 超大源码与模板配置的有界切块和质量展示](https://github.com/rudyvv/myWeknora/issues/20) | [#10](https://github.com/rudyvv/myWeknora/issues/10)、[#18](https://github.com/rudyvv/myWeknora/issues/18) |
| T13 | [#21 代码标识符与路径检索的排序和融合](https://github.com/rudyvv/myWeknora/issues/21) | [#11](https://github.com/rudyvv/myWeknora/issues/11) |
| T14 | [#22 单模块技术 WikiPage 的生成、证据校验与范围阅读](https://github.com/rudyvv/myWeknora/issues/22) | [#11](https://github.com/rudyvv/myWeknora/issues/11) |
| T15 | [#23 系统、模块及业务流程骨架与首批卡片覆盖](https://github.com/rudyvv/myWeknora/issues/23) | [#22](https://github.com/rudyvv/myWeknora/issues/22)、[#19](https://github.com/rudyvv/myWeknora/issues/19) |
| T16 | [#24 源码变更驱动受影响技术卡片更新](https://github.com/rudyvv/myWeknora/issues/24) | [#12](https://github.com/rudyvv/myWeknora/issues/12)、[#23](https://github.com/rudyvv/myWeknora/issues/23) |
| T17 | [#25 Wiki 累计预算、并发编辑与生成恢复](https://github.com/rudyvv/myWeknora/issues/25) | [#22](https://github.com/rudyvv/myWeknora/issues/22)、[#14](https://github.com/rudyvv/myWeknora/issues/14) |
| T18 | [#26 技术 Wiki 修订证据、回滚与引用回收](https://github.com/rudyvv/myWeknora/issues/26) | [#22](https://github.com/rudyvv/myWeknora/issues/22)、[#12](https://github.com/rudyvv/myWeknora/issues/12) |
| T19 | [#27 暂停、解绑与明确清除仓库知识](https://github.com/rudyvv/myWeknora/issues/27) | [#14](https://github.com/rudyvv/myWeknora/issues/14)、[#24](https://github.com/rudyvv/myWeknora/issues/24)、[#26](https://github.com/rudyvv/myWeknora/issues/26) |
| T20 | [#28 可选 GitLab Push Webhook 触发与定时对账](https://github.com/rudyvv/myWeknora/issues/28) | [#14](https://github.com/rudyvv/myWeknora/issues/14) |
| T21 | [#29 Docker 离线部署、资源额度与运行观测](https://github.com/rudyvv/myWeknora/issues/29) | [#14](https://github.com/rudyvv/myWeknora/issues/14)、[#20](https://github.com/rudyvv/myWeknora/issues/20)、[#25](https://github.com/rudyvv/myWeknora/issues/25)、[#27](https://github.com/rudyvv/myWeknora/issues/27) |
| T22 | [#30 代表仓库检索、Wiki、增量及性能验收](https://github.com/rudyvv/myWeknora/issues/30) | [#13](https://github.com/rudyvv/myWeknora/issues/13)、[#16](https://github.com/rudyvv/myWeknora/issues/16)、[#21](https://github.com/rudyvv/myWeknora/issues/21)、[#28](https://github.com/rudyvv/myWeknora/issues/28)、[#29](https://github.com/rudyvv/myWeknora/issues/29) |

## 执行状态

ready-for-agent 是 triage 标记，只有 blockers 已完成的 ticket 才进入执行 frontier。T01 [#9](https://github.com/rudyvv/myWeknora/issues/9) 已实现、验证并完成双轴复审，代码已推送到 `codex/gitlab-code-wiki-rag`，Issue 已关闭；[验证记录](gitlab-code-wiki-rag-t01-validation.md)明确全量测试的环境限制。当前 frontier 为 T02 [#10](https://github.com/rudyvv/myWeknora/issues/10)，已开始核验真实解析与数据库运行时。尚未完成的 ticket 保持 open。

发布映射见 [记录](gitlab-code-wiki-rag-publication.json)。Spec 和 22 项 tickets 通过用户恢复的浏览器登录发布并逐项核验；父 Spec 正文未被 tickets 发布改写。父 Spec 与 T02–T22 仍保持 open。

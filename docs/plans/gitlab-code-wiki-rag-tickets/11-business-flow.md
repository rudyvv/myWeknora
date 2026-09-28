# T11：[CodeWiki] 前端 API 到 Java 服务及 SQL 的静态业务链

已发布：[Issue #19](https://github.com/rudyvv/myWeknora/issues/19)；父 Spec：[Issue #8](https://github.com/rudyvv/myWeknora/issues/8)。标签 ready-for-agent；未实施。

## What to build

用户查询 getPushSchedule 或 FreeTutor 问卷详情时，可沿 Vue/API、Controller、Service、Mapper/SQL 检索和阅读有证据的静态业务链。

## Acceptance criteria

- [ ] 规则提取 Spring 映射、可确定调用/注入、Java 导入/限定符号及 Vue API 请求，关系与文件产物一起版本化。
- [ ] 前端路径前缀和代理配置有证据才关联；反射、动态分派、未解析代理和动态 SQL 明确不确定。
- [ ] getPushSchedule 首条链、FreeTutor 第二条链通过真实文件夹具验证，不把不同业务服务误连为同一链。
- [ ] 相关结构/SQL 可用于命中补全，所有片段使用各自证据并受授权、快照和上下文预算约束。
- [ ] 结果/UI 可显示关系依据与不确定性；分析不运行目标业务、测试或构建。

## Blocked by

- [Issue #17 — Vue SFC 区域检索与整文件坐标](https://github.com/rudyvv/myWeknora/issues/17)
- [Issue #18 — Java Mapper 与 MyBatis XML/SQL 关联检索](https://github.com/rudyvv/myWeknora/issues/18)

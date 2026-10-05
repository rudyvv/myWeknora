# T22 验收工具与现场结果验证记录

正式审查起点：1d32c08e36d255400c21e8863f5249d23a91afd3。整票仍21/22，#30 OPEN；不将工具/data slice当作现场最终验收。

## 首批冻结与实际定向验证（2026-10-05）

- 题集 slice clean809cc959：30题分组10/10/10，真实本地三仓commit/hash/line/symbol定位校验通过；root标准库8测试PASS0.604s。TS/Python明确supplement，不假称代表仓库内存在。状态draft，无RAGhit/perf实测。
- 矩阵 slice执行者Git权限review超时后冻结三个ownedfile，root精确保存clean835cd520。root实际Pester4/4PASS0.519s，只验证PlanOnly；十组所有列出的目标Go test函数在指定文件存在。未跑PG，不计行为验收。
- API工具main stream断开，root恢复同一Luna/xhigh对话，不重新设计/新建tree；执行者Go cacheDenied，root接管实际test，不换cache绕过。题集与runner原schema不一致，root固定business_chain/evidence[]/explicitrepository mapping/normalizedhash合同，不能默认多仓题套单一source。

## Spec

Sol/high独立静态：题集1项P2：字段blacklist不能拒unknown raw_source/full_source等源码正文字段，failclosed不足。当前题集未发现实际源码泄露。

矩阵2项P2：子test skip忽略造成parentpass假阳性；Go正常包级事件缺Test属性，StrictMode读取抛出异常造成真成功假阴性。根用隔离Temp fake-go进程反例复现：parentpass+childrag skip报告scenario passed；加正常packagepass无Test报告runner_or_test_failed。不涉及PG或live模型。

Root合成unknown raw_source字段加到合法题集后，validator errors0，也确认第一P2。反例源码仅合成短文本，无实际项目正文/凭据发送或提交。

所有必修finding一次交同原Luna修；matrix同时要求有界流式terminal聚合/无原Output存储、异常及时清理自有子进程，并补process-level成功/skip/缺失/正常包事件/超限回归。Standards原指定Sol模型两次capacity错误没有有效报告；replacement Sol/high已启动，不降模型或冒充review通过。

现场运行环境用户问题仍待答，工具/data-independent work继续；不重复向用户索要内部runner或credential。三原工作树/分支继续，无共享依赖启动/旧Docker凭据读取/GitHub流水。

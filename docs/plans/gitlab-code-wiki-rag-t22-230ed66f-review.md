# T22 runner 230ed66f 与源码协调夹具 6c730aa5 审查

正式整票起点：1d32c08e36d255400c21e8863f5249d23a91afd3。工具候选：230ed66f27386d99046931730da8ba35c9c94e85；夹具窄起点：8dee1865，目标6c730aa586a6d69c6c2a61e3e56bf4cc9ab9f011。独立审查均为 GPT-6 Sol/high，执行者 Luna/xhigh。T22仍未验收。

## Standards

Runner：0项文档硬违反；3项非阻断判断：possible Divergent Change（单CLI承担HTTP、报告、证据、测量、Agent smoke）；possible Duplicated Code（四场景测量默认值/验证/投影）；possible Duplicated Code（cache identity与entry六字段重复）。最新字典工厂修复未增加新判断。源：AGENTS、CONTEXT、domain/issue-tracker、ADR0004/0007/0008/0010、Contributing；格式化不纳入人工finding。

协调夹具两文件：0硬违反、0异味判断。独立每fault fixture与真实Wiki outbox、schedule新run验证是同一门禁的必要设置，不扩产品。

## Spec

Runner完整identity half：3项P2。映射允许两repository IDs共用同source而伪多仓；路径未拒空segment/./非canonical Git相对路径；Agent仅非error/部分SSE也报completed，缺终态success。Data half：2项P2。SSE type/response_type及hit score/match_type未经类型/枚举核验直接写报告；数字字符串通过integer验证后仍按string输出。另有全量测量报告缺口：publication partial telemetry未明确峰值内存/块/实际token未知状态，需full_run nullable合同或独立可审查报告。全部一次交回原Luna修，不以14绿冒充审查通过。

根实际合成反例确认privacy两分支均marker写入report且exit0；JSON numeric strings亦exit0，三个断言红，pkg8.945s（Temp/weknora-root-t22-data-counter.txt）。仅Go overlay/隔离HTTP合成数据，无产品改动、真实源码或凭据输出。

协调夹具Spec：0发现。每远端故障独立第一次发布、queued/retry_wait、旧publication/lastsuccess/keyword/vector/raw保持；cron真实outbox先于启动cron，验证Wiki载荷与新scheduled run。根实际四顶层PG全PASS39.076s，三remote子用例也全绿。使用root专用PG57822和已验收Docker parser57823，无共享服务修改。

Standards：runner 0硬/3判断，最重possible Divergent Change；协调夹具0/0。Spec：runner5项P2及全量报告缺口未解决；协调夹具0。只允许协调夹具切片集成，runner保持不接受。
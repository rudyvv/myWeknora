# T22 代表性问题审阅稿

30 题分为符号/路径、业务链、前端/SQL各10题。位置和文件哈希已独立校验；仍等待人工确认题目，尚未执行模型检索。TS/Python两题明确使用批准的补充测试语料。

| ID | 分组 | 问题 | 证据仓库 |
| --- | --- | --- | --- |
| SYM-01 | symbol_path | 在确认课表首页组件中，定位按路由日程 ID 加载推送课表并更新确认状态的方法。 | evip_mobile |
| SYM-02 | symbol_path | 定位微信推送课表接口对缺少 pushScheduleId 和查无记录的处理分支。 | nsb |
| SYM-03 | symbol_path | 定位 AttendanceServiceImpl 组装推送课表主记录和明细列表返回值的方法。 | nsb |
| SYM-04 | symbol_path | 在免费辅导补排控制器中定位预校验请求的映射与服务委托。 | nsb |
| SYM-05 | symbol_path | 定位 FreeTutor 排课申请列表的分页查询服务方法及其 Mapper 调用。 | nsb |
| SYM-06 | symbol_path | 在大型 SignupServiceImpl 中定位 createOrder 的主订单输入校验和协议号重复检查。 | nsb |
| SYM-07 | symbol_path | 在大型 BusinessOrderDetailMapper XML 中定位订单明细分页查询及动态筛选块。 | nsb |
| SYM-08 | symbol_path | 定位移动端 FreeTutor Mapper 中生成问卷评分字段的查询投影。 | nsb |
| SYM-09 | symbol_path | 在标注为已批准补充语料的 TypeScript fixture 中，定位 Scheduler.reserve 的声明与返回表达式。 | weknora_approved_test_supplement |
| SYM-10 | symbol_path | 在标注为已批准补充语料的 Python contract tests 中，定位覆盖装饰器、异步方法、Unicode 和 CRLF 的测试。 | weknora_approved_test_supplement |
| BC-01 | business_chain | 从移动端确认首页出发，追踪推送课表请求如何经过接口和服务层形成页面所用的主记录。 | evip_mobile, nsb |
| BC-02 | business_chain | 追踪移动端确认按钮的重复提交保护、确认接口以及服务层更新主记录和明细记录的路径。 | evip_mobile, nsb |
| BC-03 | business_chain | 从确认详情页追踪日程明细列表的请求结果如何被格式化，以及其 SQL 如何筛选并排序日程。 | evip_mobile, nsb |
| BC-04 | business_chain | 追踪 FreeTutor 明细查询从控制器到服务和 Mapper：维度、导出标志及组织筛选如何影响查询分支？ | nsb |
| BC-05 | business_chain | 追踪 FreeTutor 审核通过操作如何校验申请及日期、生成服务安排并更新申请状态。 | nsb |
| BC-06 | business_chain | 从补排预校验控制器追踪到服务：请求字段如何进入学生查询与后续校验流程？ | nsb |
| BC-07 | business_chain | 追踪补排申请提交入口到服务前置校验：提交服务读取哪些必填对象并在哪里拒绝缺失值？ | nsb |
| BC-08 | business_chain | FreeTutor 学生候选查询当前如何准备中心范围并最终选择 Mapper 查询？请核对注释分支与实际调用。 | nsb |
| BC-09 | business_chain | 追踪非一对三 FreeTutor 重复申请检查：服务何时查询，Mapper 用哪些关联和状态过滤判断既有申请？ | nsb |
| BC-10 | business_chain | 追踪大型 SignupServiceImpl 的暑期历史查询到超大订单明细 Mapper：服务如何转发参数，SQL 如何约束订单明细？ | nsb |
| FSQL-01 | frontend_sql | 确认课表首页向推送课表 GET 接口发送了哪个路由查询字段？ | evip_mobile |
| FSQL-02 | frontend_sql | 确认课表首页如何从接口响应构造日期展示值并设置是否已提交状态？ | evip_mobile |
| FSQL-03 | frontend_sql | 确认详情页点击处理如何避免重复确认，并调用哪个更新接口？ | evip_mobile |
| FSQL-04 | frontend_sql | 确认详情页如何使用返回的明细数组并格式化日期、星期和起止时间？ | evip_mobile |
| FSQL-05 | frontend_sql | 推送课表主记录 SQL 返回哪些汇总字段，并用哪些课表状态条件限制明细？ | nsb |
| FSQL-06 | frontend_sql | FreeTutor 明细查询 SQL 的主要输出字段及基础审核、删除条件是什么？ | nsb |
| FSQL-07 | frontend_sql | FreeTutor 申请列表 SQL 如何投影审核状态并按请求参数追加筛选？ | nsb |
| FSQL-08 | frontend_sql | 移动端 FreeTutor 课表 SQL 如何关联问卷答案，并将答案题位转换为 surveyScore？ | nsb |
| FSQL-09 | frontend_sql | 问卷详情 SQL 如何把存储答案和问卷题目映射为编号字段，并按日程 ID 过滤？ | nsb |
| FSQL-10 | frontend_sql | evip_mobile 与 evip-dashboard 各自声明了什么 Vue 依赖版本范围？ | evip_mobile, evip_dashboard |

完整位置、固定 commit 和哈希模式见 [结构化题集](source-representative-questions.json)。27/30 top10 必须基于真实授权检索和正确证据评判，当前没有命中率或性能结论。

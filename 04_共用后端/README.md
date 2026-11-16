# 三端共用后端（Go + MySQL）

只有这一套后端，不需要为三个客户端分别启动三套服务。VS Code 打开本文件夹。

本机整理版已有 .env.local，沿用3307上的 campus_retail_test 演示库，服务端口18090。不要重置或导入覆盖原数据库。分享普通源码文件夹不含 .env.local，接收方须复制 .env.example 并填入自己的连接。

~~~powershell
.\scripts\backend.ps1 -Action Start
~~~

健康检查：http://127.0.0.1:18090/healthz 。看到 status=ok、schemaVersion=0012_courier_capacity_recovery.sql 后再启动前端。终端按 Ctrl+C 停止。

~~~powershell
.\scripts\backend.ps1 -Action Test
.\scripts\backend.ps1 -Action Build
~~~

本脚本加载 .env.local，迁移路径按当前目录计算、Go缓存放在 .run。不会自动 seed，当前 AUTO_MIGRATE=false：现有库已到0012；新库须由开发者显式配置迁移并初始化。仅使用 cmd/server 启动正式业务服务。后端包含 cmd/server、migrate、seed、smoke、loadtest；移除了写死旧路径/端口的历史辅助启动器。

cmd/seed 是写数据工具，会更新部分演示商品/账号，不能随意对已有库运行。它的新库默认账号与本机已经改过的123账号不同；源码打包不等于数据库导出。

业务规则、SQL迁移和测试均保持主工程版本。金额整数分；订单/支付/库存分离；只使用模拟支付。集成测试会清空目标测试库，不提供指向现有演示库的一键运行任务。

应用新增迁移：先备份数据库，再运行 `.\scripts\backend.ps1 -Action Migrate`。该命令仅迁移，不启动服务、不执行 seed。批量与定位接口见 docs/批量配送接口与测试.md。

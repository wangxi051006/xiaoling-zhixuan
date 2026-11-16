# 用户端

VS Code：文件 → 打开文件夹，选择本文件夹。终端应位于 README 所在目录。

首次安装：

~~~powershell
.\scripts\frontend.ps1 -Action Install
~~~

先启动 04_共用后端，再启动本端：

~~~powershell
.\scripts\frontend.ps1 -Action Start
~~~

浏览器打开 http://127.0.0.1:5174/user ，本机已有演示账号 12345679，密码 123123123。登录框不会自动填入旧账号。端口不是账号权限边界，角色权限由 Go 后端校验。

停止当前终端服务：Ctrl+C。测试与构建：

~~~powershell
.\scripts\frontend.ps1 -Action Test
.\scripts\frontend.ps1 -Action Build
~~~

也可选择 VS Code：终端 → 运行任务。后端地址在 web/.env.development 或终端 VITE_API_TARGET 中配置，默认18090；不要直接双击 web/index.html。

web/src 保留共享组件和三角色路由，避免拆散导入造成程序失效；本文件夹是可独立构建的协作副本。miniprogram 包含原生用户/骑手页面及前端回归所需依赖，商家主要使用网页。原生配置在 miniprogram/config.js，默认18090；真机需可访问的地址，微信工具/真机尚未在本轮验收。

分享源码请使用根目录 scripts/打包源码.ps1，普通源码文件夹不带 node_modules、dist 或本地环境密钥。

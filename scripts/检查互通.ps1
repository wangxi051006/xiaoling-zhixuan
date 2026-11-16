param([int]$PortOffset = 0)
$ErrorActionPreference = 'Stop'
$expected = '0012_courier_capacity_recovery.sql'
foreach ($port in @(18090,5173,5174,5175)) {
  $actual = $port + $PortOffset
  $reply = Invoke-RestMethod "http://127.0.0.1:$actual/healthz" -TimeoutSec 8
  if ($reply.data.status -ne 'ok' -or $reply.data.schemaVersion -ne $expected) { throw "端口 $actual 后端/迁移不匹配" }
  $catalog = Invoke-RestMethod "http://127.0.0.1:$actual/api/v1/products?storeId=1" -TimeoutSec 8
  if ($catalog.code -ne 'OK') { throw "端口 $actual 商品接口失败" }
  Write-Output "PASS port=$actual health+products schema=$expected products=$(@($catalog.data).Count)"
}
Write-Output '三端代理已连通同一个 Go 后端。此检查不创建订单、不修改商品；批量操作与定位送达请按本次升级说明继续验收。'

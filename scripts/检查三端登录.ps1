param([int]$PortOffset=0)
$ErrorActionPreference = 'Stop'
$targets = @(
  @{port=5173;account='12345678';login='staff/login';path='/admin/products';role='store_admin'},
  @{port=5174;account='12345679';login='login';path='/orders';role=''},
  @{port=5175;account='12345680';login='staff/login';path='/courier/tasks';role='courier'}
)
foreach ($target in $targets) {
  $base = 'http://127.0.0.1:' + ($target.port+$PortOffset) + '/api/v1'
  $body = @{phone=$target.account;password='123123123'} | ConvertTo-Json -Compress
  $login = Invoke-RestMethod ($base+'/auth/'+$target.login) -Method Post -ContentType 'application/json' -Body $body -TimeoutSec 8
  if ($login.code -ne 'OK' -or -not $login.data.accessToken) { throw "登录失败：$($target.account)" }
  if ($target.role -and $login.data.user.roles -notcontains $target.role) { throw "角色不匹配：$($target.account)" }
  $headers = @{Authorization='Bearer '+$login.data.accessToken}
  $reply = Invoke-RestMethod ($base+$target.path) -Headers $headers -TimeoutSec 8
  if ($reply.code -ne 'OK') { throw "业务读取失败：$($target.path)" }
  Write-Output "PASS account=$($target.account) port=$($target.port+$PortOffset) authenticated read=$($target.path)"
  if ($target.account -eq '12345678') {
    $dashboard = Invoke-RestMethod ($base+'/admin/reports/dashboard') -Headers $headers -TimeoutSec 8
    $count = @($reply.data | Where-Object active | ForEach-Object {$_.skus} | Where-Object {$_.active -and $_.available -le $_.lowStockThreshold}).Count
    if ($dashboard.data.lowStockCount -ne $count) { throw '库存预警与商品库存不一致' }
    Write-Output "PASS dashboard warning count=$count"
  }
}
Write-Output '仅本机已配置123演示账号适用。检查会记录正常登录日志，不提交订单/价格/库存变更。'

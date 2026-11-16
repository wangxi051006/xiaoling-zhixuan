# Only for the isolated UI fixture created during this acceptance, never the demo database.
param([switch]$ResumeAfterMerchant)
$ErrorActionPreference = 'Stop'
$base = 'http://127.0.0.1:19090/api/v1'
$log = Join-Path $PSScriptRoot 'http-batch-smoke.log'
function Record([string]$text) { $text | Tee-Object -FilePath $log -Append }
function Call($path, $token, $method = 'GET', $body = $null) {
  $args = @{Uri=$base+$path;Method=$method;ContentType='application/json';TimeoutSec=10}
  if ($token) { $args.Headers=@{Authorization='Bearer '+$token} }
  if ($null -ne $body) { $args.Body=ConvertTo-Json -InputObject $body -Depth 15 -Compress }
  $response = Invoke-RestMethod @args
  if ($response.code -ne 'OK') { throw "Unexpected result: $path $($response.code)" }
  return $response.data
}
function Key { return 'http_smoke_'+[guid]::NewGuid().ToString('N') }
function Assert($condition,[string]$message) { if(-not $condition) { throw $message }; Record ('PASS '+$message) }
function Login($phone,$staff) {
  $path=if($staff){'/auth/staff/login'}else{'/auth/login'}
  return (Call $path '' 'POST' @{phone=$phone;password='123123123'}).accessToken
}
Record ('START '+(Get-Date -Format o)+' dedicated fixture port=19090; simulated location and payment only')
$admin=Login '13800000001' $true
$rider=Login '13800000004' $true
$buyer=Login '13900000001' $false
$orders=@('6','7')
$before=@{}
foreach($id in $orders){
  $before[$id]=Call ('/orders/'+$id) $buyer
  Assert ($before[$id].customerOrderOrdinal -gt 0 -and $before[$id].totalCents -gt 0) "order $id has authoritative ordinal and cents before fulfillment"
}
$productsBefore=Call '/products?storeId=1' $buyer
$stockBefore=ConvertTo-Json -InputObject @($productsBefore | ForEach-Object {$_.skus} | Select-Object id,available) -Compress
foreach($action in $(if($ResumeAfterMerchant){@()}else{@('ACCEPT','READY')})){
  $items=@(foreach($id in $orders){$detail=Call ('/admin/orders/'+$id) $admin; @{orderId=$id;expectedVersion=$detail.version}})
  $body=@{requestId=(Key);action=$action;items=$items}
  $result=Call '/admin/orders/batch-actions' $admin 'POST' $body
  Assert (@($result.items | Where-Object result -eq 'SUCCESS').Count -eq 2) "merchant $action two successes"
  $replay=Call '/admin/orders/batch-actions' $admin 'POST' $body
  Assert ($replay.batchId -eq $result.batchId) "merchant $action original batch replay"
}
$tasks=@((Call '/courier/tasks/available' $rider) | Where-Object {$_.orderId -in $orders})
Assert ($tasks.Count -eq 2) 'two ready tasks visible to rider'
$taskIDs=@($tasks | ForEach-Object {$_.id})
foreach($action in @('CLAIM','PICKUP')){
  $items=@(foreach($id in $taskIDs){
    $task=if($action -eq 'CLAIM'){@($tasks | Where-Object id -eq $id)[0]}else{Call ('/courier/tasks/'+$id) $rider}
    @{taskId=$id;expectedVersion=$task.version}
  })
  $body=@{requestId=(Key);action=$action;items=$items}
  if($action -eq 'PICKUP'){$body.checked=$true}
  $result=Call '/courier/tasks/batch-actions' $rider 'POST' $body
  Assert ($result.successCount -eq 2) "rider $action two successes"
}
$capacity=Call '/courier/capacity' $rider
Assert ($capacity.activeTaskCount -eq 3 -and $capacity.taskLimit -eq 3) 'capacity includes one historical task plus two new tasks'
foreach($id in $taskIDs){
  $task=Call ('/courier/tasks/'+$id) $rider
  $null=Call ('/courier/tasks/'+$id+'/start') $rider 'POST' @{requestId=(Key);expectedVersion=$task.version}
}
$arrival=Call '/courier/arrivals' $rider 'POST' @{taskIds=$taskIDs;latitude=31.2304;longitude=121.4737;accuracy=10;observedAt=[DateTime]::UtcNow.ToString('o')}
Assert ($null -ne $arrival.arrivalToken) 'same test point arrival verified by server'
# Open the actual front-end proxy SSE endpoint before committing delivery.
Add-Type -AssemblyName System.Net.Http
$client=[Net.Http.HttpClient]::new()
$client.Timeout=[TimeSpan]::FromSeconds(20)
$request=[Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get,'http://127.0.0.1:5274/api/v1/notifications/stream?afterId=0')
$request.Headers.Authorization=[Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer',$buyer)
$streamResponse=$client.SendAsync($request,[Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
$null=$streamResponse.EnsureSuccessStatusCode()
$stream=$streamResponse.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
$reader=[IO.StreamReader]::new($stream)
try {
  $items=@(foreach($id in $taskIDs){$task=Call ('/courier/tasks/'+$id) $rider; @{taskId=$id;expectedVersion=$task.version}})
  $body=@{requestId=(Key);action='COMPLETE';items=$items;arrivalToken=$arrival.arrivalToken}
  $clock=[Diagnostics.Stopwatch]::StartNew()
  $result=Call '/courier/tasks/batch-actions' $rider 'POST' $body
  Assert ($result.successCount -eq 2) 'rider COMPLETE two successes without customer code'
  $received=@()
  while($received.Count -lt 2){
    $read=$reader.ReadLineAsync()
    if(-not $read.Wait(5000)){throw 'SSE message timed out'}
    $line=$read.Result
    if($null -eq $line){throw 'SSE stream closed before two messages'}
    if($line.StartsWith('data: ')){$received+=($line.Substring(6)|ConvertFrom-Json)}
  }
  Assert (@($received | Where-Object {$_.orderId -in $orders}).Count -eq 2) ('proxy SSE received both orders in '+$clock.ElapsedMilliseconds+'ms after request began')
  $replay=Call '/courier/tasks/batch-actions' $rider 'POST' $body
  Assert ($replay.batchId -eq $result.batchId -and $replay.successCount -eq 2) 'complete replay returns original batch'
} finally { $reader.Dispose(); $streamResponse.Dispose(); $client.Dispose() }
$inbox=Call '/notifications' $buyer
Assert ($inbox.items.Count -eq 2 -and $inbox.unreadCount -eq 2) 'offline inbox contains exactly two unread notifications'
foreach($id in $orders){
  $after=Call ('/orders/'+$id) $buyer
  Assert ($after.status -eq 'COMPLETED' -and $after.totalCents -eq $before[$id].totalCents -and $after.customerOrderOrdinal -eq $before[$id].customerOrderOrdinal) "order $id completed with amount and ordinal unchanged"
}
$productsAfter=Call '/products?storeId=1' $buyer
$stockAfter=ConvertTo-Json -InputObject @($productsAfter | ForEach-Object {$_.skus} | Select-Object id,available) -Compress
Assert ($stockAfter -eq $stockBefore) 'accept/ready/claim/pickup/delivery did not double-deduct stock'
$capacity=Call '/courier/capacity' $rider
Assert ($capacity.activeTaskCount -eq 1) 'two completions release capacity, historical task remains'
$first=$inbox.items[0].id
$null=Call ('/notifications/'+$first+'/read') $buyer 'POST' @{}
$null=Call ('/notifications/'+$first+'/read') $buyer 'POST' @{}
Assert ((Call '/notifications' $buyer).unreadCount -eq 1) 'mark-read idempotent'
Record 'EXIT 0'

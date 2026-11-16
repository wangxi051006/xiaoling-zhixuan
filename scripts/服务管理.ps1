param(
  [ValidateSet('Start','Stop','Status','RestartBackend')][string]$Action = 'Status',
  [int]$PortOffset = 0
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$run = Join-Path $root '.run'
$registry = Join-Path $run "services-$PortOffset.json"
New-Item -ItemType Directory -Force -Path $run | Out-Null
$targets = @(
  @{name='backend';folder='04_共用后端';script='backend.ps1';port=18090+$PortOffset},
  @{name='merchant';folder='01_商家端';script='frontend.ps1';port=5173+$PortOffset},
  @{name='user';folder='02_用户端';script='frontend.ps1';port=5174+$PortOffset},
  @{name='courier';folder='03_骑手端';script='frontend.ps1';port=5175+$PortOffset}
)
function Listening([int]$number) {
  $client = New-Object System.Net.Sockets.TcpClient
  try { $pending = $client.BeginConnect('127.0.0.1',$number,$null,$null); if (-not $pending.AsyncWaitHandle.WaitOne(500)) {return $false}; $client.EndConnect($pending); return $true } catch { return $false } finally { $client.Dispose() }
}
function Stop-Owned($records) {
  $failures = @()
  foreach ($record in @($records) | Sort-Object -Property port) {
    $process = Get-Process -Id $record.pid -ErrorAction SilentlyContinue
    if (-not $process) { continue }
    if ($process.StartTime.ToUniversalTime().Ticks.ToString() -ne $record.startTicks -or $process.ProcessName -notin @('powershell','pwsh')) { $failures += "PID 身份已变化，拒绝停止 $($record.pid)"; continue }
    & taskkill.exe /PID $record.pid /T /F | Out-Null
    if ($LASTEXITCODE -ne 0) { $failures += "停止 $($record.name) 失败。请在 VS Code 中手动终止对应终端。"; continue }
    Write-Output "Stopped $($record.name) port $($record.port)"
  }
  if ($failures.Count) { throw ($failures -join '; ') }
}
if ($Action -eq 'Status') {
  foreach ($target in $targets) { Write-Output ("{0}: http://127.0.0.1:{1} listening={2}" -f $target.name,$target.port,(Listening $target.port)) }
  exit 0
}
if ($Action -eq 'Stop') {
  if (-not (Test-Path -LiteralPath $registry)) { Write-Output '本整理包没有登记运行中的服务；不会结束其他工程进程。'; exit 0 }
  $records = Get-Content -LiteralPath $registry -Raw -Encoding UTF8 | ConvertFrom-Json
  Stop-Owned $records
  Remove-Item -LiteralPath $registry
  exit 0
}
if ($Action -eq 'RestartBackend') {
  if (-not (Test-Path -LiteralPath $registry)) { throw '缺少服务登记，无法确认后端归属；请在原 VS Code 终端重启。' }
  $records = Get-Content -LiteralPath $registry -Raw -Encoding UTF8 | ConvertFrom-Json
  $backendRecords = @($records | Where-Object { $_.name -eq 'backend' })
  if ($backendRecords.Count -gt 1) { throw '后端登记重复，拒绝停止进程。' }
  $target = $targets | Where-Object { $_.name -eq 'backend' }
  $backendScript = Join-Path $root '04_共用后端\scripts\backend.ps1'
  if (-not (Test-Path -LiteralPath $backendScript) -or -not (Test-Path -LiteralPath (Join-Path $root '04_共用后端\.env.local'))) { throw '后端启动文件或 .env.local 缺失，未停止现有服务。' }
  $shell = (Get-Command powershell.exe -ErrorAction Stop).Source
  Stop-Owned $backendRecords
  $records = @($records | Where-Object { $_.name -ne 'backend' })
  ConvertTo-Json -InputObject @($records) | Set-Content -LiteralPath $registry -Encoding UTF8
  for ($attempt=0; $attempt -lt 20 -and (Listening $target.port); $attempt++) { Start-Sleep -Milliseconds 250 }
  if (Listening $target.port) { throw '后端端口仍被占用，拒绝覆盖未知服务；三端前端保持运行。' }
  $stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fff'
  $arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $backendScript + '" -Action Start -Port ' + $target.port
  $process = Start-Process -FilePath $shell -ArgumentList $arguments -WorkingDirectory (Join-Path $root '04_共用后端') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $run ('backend-'+$PortOffset+'-restart-'+$stamp+'.out.log')) -RedirectStandardError (Join-Path $run ('backend-'+$PortOffset+'-restart-'+$stamp+'.err.log'))
  $newRecord = [pscustomobject]@{name='backend';pid=$process.Id;startTicks=$process.StartTime.ToUniversalTime().Ticks.ToString();port=$target.port}
  $records += $newRecord
  ConvertTo-Json -InputObject @($records) | Set-Content -LiteralPath $registry -Encoding UTF8
  for ($attempt=0; $attempt -lt 100; $attempt++) {
    Start-Sleep -Milliseconds 500
    $process.Refresh()
    if ($process.HasExited) { break }
    try {
      $health = Invoke-RestMethod ("http://127.0.0.1:"+$target.port+'/healthz') -TimeoutSec 2
      if ($health.data.status -eq 'ok') { Write-Output "Restarted backend http://127.0.0.1:$($target.port); 三端前端未重启。"; exit 0 }
    } catch { }
  }
  throw '后端重启未通过健康检查；查看 .run/backend-*-restart-* 日志。三端前端登记和原日志已保留。'
}
foreach ($target in $targets) {
  if (Listening $target.port) { throw "端口 $($target.port) 已被占用。先在原终端按 Ctrl+C；本脚本不会复用未知旧服务或自动换端口。" }
}
if (Test-Path -LiteralPath $registry) { throw '已有服务登记文件，请先执行 Stop 并检查 Status。' }
foreach ($target in $targets | Where-Object { $_.name -ne 'backend' }) {
  if (-not (Test-Path -LiteralPath (Join-Path $root ($target.folder + '\web\node_modules\vite\package.json')))) { throw "请先在 $($target.folder) 运行 .\scripts\frontend.ps1 -Action Install" }
}
$records = @()
try {
  $env:VITE_API_TARGET = 'http://127.0.0.1:' + (18090+$PortOffset)
  $shell = (Get-Command powershell.exe -ErrorAction Stop).Source
  foreach ($target in $targets) {
    $script = Join-Path $root ($target.folder + '\scripts\' + $target.script)
    $arguments = '-NoProfile -ExecutionPolicy Bypass -File "' + $script + '" -Action Start -Port ' + $target.port
    $process = Start-Process -FilePath $shell -ArgumentList $arguments -WorkingDirectory (Join-Path $root $target.folder) -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $run ($target.name+'-'+$PortOffset+'.out.log')) -RedirectStandardError (Join-Path $run ($target.name+'-'+$PortOffset+'.err.log'))
    $records += [pscustomobject]@{name=$target.name;pid=$process.Id;startTicks=$process.StartTime.ToUniversalTime().Ticks.ToString();port=$target.port}
    ConvertTo-Json -InputObject @($records) | Set-Content -LiteralPath $registry -Encoding UTF8
    $ready = $false
    for ($attempt=0;$attempt -lt 100;$attempt++) {
      Start-Sleep -Milliseconds 500
      $process.Refresh()
      if ($process.HasExited) { break }
      try { $health = Invoke-RestMethod ("http://127.0.0.1:"+$target.port+'/healthz') -TimeoutSec 2; if ($health.data.status -eq 'ok') { $ready=$true;break } } catch { }
    }
    if (-not $ready) { throw "启动 $($target.name) 失败，请查看 .run 中对应日志" }
    Write-Output "Ready $($target.name) http://127.0.0.1:$($target.port)"
  }
} catch {
  $reason = $_
  Stop-Owned $records
  if (Test-Path -LiteralPath $registry) { Remove-Item -LiteralPath $registry }
  throw $reason
}


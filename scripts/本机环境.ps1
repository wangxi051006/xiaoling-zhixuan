param([ValidateSet('Start','Stop','Status','Check')][string]$Action='Status')
$ErrorActionPreference='Stop'
$runtime='D:\备份文件\.零食店环境'
$workspace=Split-Path -Parent $PSScriptRoot
$project=$workspace
$alias=Join-Path $env:TEMP 'campus-retail-20261115'
$env:GOMODCACHE=Join-Path $runtime 'go-mod'
$env:GOPROXY='https://goproxy.cn,direct'
$env:PATH='C:\node;C:\Program Files\Go\bin;'+$env:PATH

function Ensure-Alias {
  if (Test-Path -LiteralPath $alias) {
    $item=Get-Item -LiteralPath $alias
    if ($item.LinkType -ne 'Junction' -or [string]$item.Target -ne $runtime) {throw "Temporary path is occupied: $alias"}
  } else {New-Item -ItemType Junction -Path $alias -Target $runtime | Out-Null}
}
function Port-Open([int]$Port) {
  $client=New-Object Net.Sockets.TcpClient
  try {$result=$client.BeginConnect('127.0.0.1',$Port,$null,$null); if (-not $result.AsyncWaitHandle.WaitOne(300)) {return $false}; $client.EndConnect($result); return $true} catch {return $false} finally {$client.Dispose()}
}
function Database-Owned {
  if (-not (Port-Open 3307)) {return $false}
  $uuidLine=Get-Content -LiteralPath (Join-Path $runtime 'mysql-data\auto.cnf') | Where-Object {$_ -match '^server-uuid='}
  $expected=($uuidLine -split '=',2)[1].Trim()
  $reply=& (Join-Path $alias 'mysql-engine\bin\mysql.exe') ("--defaults-extra-file=$alias/mysql-admin.cnf") --connect-timeout=3 --batch --skip-column-names '--execute=SELECT @@server_uuid' 2>&1
  if ($LASTEXITCODE -ne 0 -or ([string]($reply -join "`n")).Trim() -ne $expected) {throw 'Port 3307 is occupied by a different database. No process was stopped.'}
  return $true
}
function Archive-StaleRegistry {
  $file=Join-Path $project '.run\services-0.json'
  if (-not (Test-Path -LiteralPath $file)) {return}
  $records=Get-Content -LiteralPath $file -Raw -Encoding UTF8 | ConvertFrom-Json
  foreach ($record in $records) {
    $process=Get-Process -Id $record.pid -ErrorAction SilentlyContinue
    if ($process -and $process.StartTime.ToUniversalTime().Ticks.ToString() -eq $record.startTicks) {return}
  }
  Move-Item -LiteralPath $file -Destination (Join-Path $project ('.run\services-0.stale-'+(Get-Date -Format 'yyyyMMdd-HHmmss-fff')+'.json'))
}
function Start-Database {
  if (Database-Owned) {Write-Host 'MySQL is already running.'; return}
  $arguments='--no-defaults --basedir="'+$alias+'/mysql-engine" --datadir="'+$alias+'/mysql-data" --bind-address=127.0.0.1 --port=3307 --mysqlx=OFF --log-bin-trust-function-creators=ON --log-error="'+$alias+'/mysql-error.log" --console'
  $process=Start-Process -FilePath (Join-Path $alias 'mysql-engine\bin\mysqld.exe') -ArgumentList $arguments -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runtime 'mysql.out.log') -RedirectStandardError (Join-Path $runtime 'mysql.err.log')
  [pscustomobject]@{pid=$process.Id;startTicks=$process.StartTime.ToUniversalTime().Ticks.ToString();path=(Join-Path $alias 'mysql-engine\bin\mysqld.exe')} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $runtime 'mysql-process.json') -Encoding UTF8
  for ($i=0;$i -lt 60;$i++) {Start-Sleep -Milliseconds 500; if (Database-Owned) {Write-Host 'MySQL is ready on 127.0.0.1:3307.'; return}; $process.Refresh(); if ($process.HasExited) {break}}
  throw 'MySQL did not start. See .零食店环境/mysql.err.log.'
}
function Check-Project {
  & (Join-Path $project 'scripts\检查互通.ps1')
  if ($LASTEXITCODE -ne 0) {throw 'Connectivity check failed.'}
  & (Join-Path $project 'scripts\检查三端登录.ps1')
  if ($LASTEXITCODE -ne 0) {throw 'Login check failed.'}
}

Ensure-Alias
switch ($Action) {
  'Start' {
    Start-Database
    Archive-StaleRegistry
    $ports=@(18090,5173,5174,5175)
    $open=@($ports | Where-Object {Port-Open $_})
    if ($open.Count -eq 0) {
      & (Join-Path $project 'scripts\服务管理.ps1') -Action Start
      if ($LASTEXITCODE -ne 0) {throw 'Project startup failed. See the project .run logs.'}
    } elseif ($open.Count -ne 4) {throw 'Only some project ports are occupied. Check Status before restarting.'}
    Check-Project
    Write-Host 'Merchant: http://127.0.0.1:5173/admin/workbench'
    Write-Host 'Customer: http://127.0.0.1:5174/user'
    Write-Host 'Courier:  http://127.0.0.1:5175/delivery/tasks'
  }
  'Check' {if (-not (Database-Owned)) {throw 'MySQL is stopped.'}; Check-Project}
  'Status' {
    Write-Host ('MySQL: '+(Database-Owned))
    & (Join-Path $project 'scripts\服务管理.ps1') -Action Status
  }
  'Stop' {
    Archive-StaleRegistry
    & (Join-Path $project 'scripts\服务管理.ps1') -Action Stop
    if ($LASTEXITCODE -ne 0) {throw 'Project stop failed; MySQL was left running.'}
    if (Database-Owned) {
      & (Join-Path $alias 'mysql-engine\bin\mysqladmin.exe') ("--defaults-extra-file=$alias/mysql-admin.cnf") --connect-timeout=5 shutdown
      if ($LASTEXITCODE -ne 0) {throw 'MySQL shutdown failed.'}
      Write-Host 'MySQL stopped safely. All database files are retained.'
    }
  }
}


$ErrorActionPreference = 'Stop'
$root = 'D:\线上零食店小程序\校零智选_最新版_20261114'
$out = Join-Path $root 'verification\recheck-pendingcount-20261114-221231'
$portBusy = $false
try {
  $null = Invoke-WebRequest -Uri 'http://127.0.0.1:19090/healthz' -UseBasicParsing -TimeoutSec 2
  $portBusy = $true
} catch { }
if ($portBusy) { throw 'Port 19090 already serves HTTP; refusing to start over it.' }
$env:MYSQL_DSN = 'root@tcp(127.0.0.1:3307)/campus_retail_ui_20261114_2123_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true'
$env:APP_ENV = 'local'
$env:PAYMENT_MODE = 'simulated'
$env:HTTP_ADDR = '127.0.0.1:19090'
$env:SEED_DEMO = 'false'
$env:AUTO_MIGRATE = 'false'
$env:JWT_SECRET = 'local-only-not-for-production-change-me'
$env:MIGRATIONS_DIR = Join-Path $root '04_共用后端\db\migrations'
$process = Start-Process -FilePath (Join-Path $out 'review-server.exe') -WorkingDirectory (Join-Path $root '04_共用后端\backend') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $out 'review-server.out.log') -RedirectStandardError (Join-Path $out 'review-server.err.log')
@{ pid = $process.Id; startTicks = $process.StartTime.Ticks.ToString(); path = (Join-Path $out 'review-server.exe'); port = 19090 } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $out 'review-server-process.json') -Encoding UTF8
for ($i = 0; $i -lt 15; $i++) {
  Start-Sleep -Milliseconds 500
  $process.Refresh()
  if ($process.HasExited) { throw 'Review server exited; inspect review-server.out.log.' }
  try {
    $health = Invoke-RestMethod -Uri 'http://127.0.0.1:19090/healthz' -TimeoutSec 2
    $health | ConvertTo-Json -Depth 8
    exit 0
  } catch { }
}
throw 'Review server health check failed.'

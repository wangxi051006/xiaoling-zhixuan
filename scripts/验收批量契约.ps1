param([string]$EvidenceDirectory = '')
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
if (-not $env:TEST_MYSQL_DSN) { throw '请先设置 TEST_MYSQL_DSN，必须指向可清空的独立测试库。' }
if ($env:TEST_MYSQL_DSN -notmatch '\)/([^?]+)\?') { throw 'TEST_MYSQL_DSN 格式不正确。' }
$testDatabase = $matches[1]
if (-not $testDatabase.EndsWith('_test') -or $testDatabase -in @('campus_retail', 'campus_retail_test')) {
  throw '拒绝操作原演示库。请使用另建的、名称以 _test 结尾的可清空数据库。'
}
$localEnv = Join-Path $root '04_共用后端\.env.local'
if (Test-Path -LiteralPath $localEnv) {
  $localLine = Get-Content -LiteralPath $localEnv | Where-Object { $_ -match '^\s*MYSQL_DSN\s*=' } | Select-Object -First 1
  if ($localLine -match '\)/([^?]+)\?' -and $matches[1] -eq $testDatabase) { throw '测试库与本机运行库同名，拒绝清理。' }
}
$go = if (Get-Command go.exe -ErrorAction SilentlyContinue) { (Get-Command go.exe).Source } else { 'C:\Program Files\Go\bin\go.exe' }
$node = if ($env:CAMPUS_TEST_NODE) { $env:CAMPUS_TEST_NODE } elseif (Get-Command node.exe -ErrorAction SilentlyContinue) { (Get-Command node.exe).Source } else { 'C:\Program Files\nodejs\node.exe' }
$npm = Join-Path (Split-Path -Parent $node) 'npm.cmd'
if (-not (Test-Path -LiteralPath $npm)) {
  $npm = if (Get-Command npm.cmd -ErrorAction SilentlyContinue) { (Get-Command npm.cmd).Source } else { 'C:\Program Files\nodejs\npm.cmd' }
}
foreach ($binary in @($go, $node, $npm)) { if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw '缺少 Go、Node 或 npm，请检查运行时路径。' } }
if (-not $EvidenceDirectory) { $EvidenceDirectory = Join-Path $root ('verification\batch-contract-' + (Get-Date -Format 'yyyyMMdd-HHmmss')) }
$EvidenceDirectory = [IO.Path]::GetFullPath($EvidenceDirectory)
if (Test-Path -LiteralPath $EvidenceDirectory) { throw '证据目录已存在，请使用新目录，保留原日志。' }
New-Item -ItemType Directory -Path $EvidenceDirectory | Out-Null
$log = Join-Path $EvidenceDirectory 'verification.log'
$env:CAMPUS_TEST_NODE = $node
$env:PATH = (Split-Path -Parent $node) + ';' + $env:PATH
$env:GOCACHE = Join-Path $root '04_共用后端\.run\go-cache'
function Run-Step([string]$Directory, [string]$Executable, [string[]]$Arguments) {
  ('COMMAND: ' + [IO.Path]::GetFileName($Executable) + ' ' + ($Arguments -join ' ')) | Tee-Object -FilePath $log -Append
  Push-Location -LiteralPath $Directory
  $savedPreference = $ErrorActionPreference
  try {
    # Windows PowerShell classifies native stderr as errors; capture it, then
    # check the actual exit code. A failed command always terminates this script.
    $ErrorActionPreference = 'Continue'
    & $Executable @Arguments 2>&1 | ForEach-Object { "$_" } | Tee-Object -FilePath $log -Append
    $resultCode = $LASTEXITCODE
  } finally { $ErrorActionPreference = $savedPreference; Pop-Location }
  ('EXIT: ' + $resultCode) | Tee-Object -FilePath $log -Append
  if ($resultCode -ne 0) { throw "命令失败，exit=$resultCode；后续步骤未申报通过。" }
}
try {
  ('START: ' + (Get-Date -Format o) + '; disposable database=' + $testDatabase) | Tee-Object -FilePath $log
  $backend = Join-Path $root '04_共用后端\backend'
  Run-Step $backend $go @('build', './...')
  Run-Step $backend $go @('vet', './...')
  Run-Step $backend $go @('test', './...', '-count=1')
  Run-Step $backend $go @('test', '-tags=integration', './internal/app', '-run', '^TestMySQLMerchantBatch', '-count=1', '-v')
  foreach ($client in @('01_商家端', '02_用户端', '03_骑手端')) {
    $web = Join-Path $root ($client + '\web')
    ('CLIENT: ' + $client) | Tee-Object -FilePath $log -Append
    Run-Step $web $npm @('test')
    Run-Step $web $npm @('run', 'build')
  }
  ('PASS: all commands exited 0; ' + (Get-Date -Format o)) | Tee-Object -FilePath $log -Append
} catch {
  ('FAIL: ' + $_.Exception.Message) | Tee-Object -FilePath $log -Append
  throw
}

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$manifestPath = Join-Path $root '源码校验.json'
$run = Join-Path $root '.run'
New-Item -ItemType Directory -Force -Path $run | Out-Null
if (Test-Path -LiteralPath $manifestPath) {
  Copy-Item -LiteralPath $manifestPath -Destination (Join-Path $run ('源码校验_更新前_' + (Get-Date -Format 'yyyyMMdd-HHmmss-fff') + '.json'))
}
$entries = @()
foreach ($file in Get-ChildItem -LiteralPath $root -Recurse -File -Force | Sort-Object FullName) {
  $relative = $file.FullName.Substring($root.Length + 1).Replace('\', '/')
  if ($relative -eq '源码校验.json' -or $relative -match '(^|/)(node_modules|dist|data|coverage|verification|\.run[^/]*|\.git|\.tools)(/|$)' -or $relative -match '\.(log|pid|exe|zip|sql\.bak)$') { continue }
  if ($file.Name -like '.env*' -and $file.Name -notin @('.env.example', '.env.development')) { continue }
  $entries += [ordered]@{ target = $relative; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
}
[ordered]@{
  formatVersion = 2
  version = '20261114-batch-contract-0012'
  createdAt = (Get-Date -Format o)
  note = 'Current source snapshot; excludes private env, dependencies, builds, runtime data, verification evidence/logs and self. Verification history is retained separately.'
  generated = @()
  files = $entries
} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $manifestPath -Encoding UTF8
Write-Output "已更新 $($entries.Count) 个源码文件；旧清单保留在 .run。请继续执行 scripts/检查源码.ps1。"

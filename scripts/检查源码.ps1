$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$manifest = Get-Content -LiteralPath (Join-Path $root '源码校验.json') -Raw -Encoding UTF8 | ConvertFrom-Json
foreach ($entry in @($manifest.files) + @($manifest.generated)) {
  if (-not $entry.target) { continue }
  $file = Join-Path $root $entry.target
  if (-not (Test-Path -LiteralPath $file)) { throw "缺少文件：$($entry.target)" }
  if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLower() -ne $entry.sha256) { throw "文件已变化：$($entry.target)" }
}
Write-Output "PASS: $($manifest.files.Count) source files and $($manifest.generated.Count) generated configs match the snapshot."

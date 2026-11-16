param([string]$Output = '')
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
if (-not $Output) { $Output = $root + '_源码分享_' + (Get-Date -Format 'yyyyMMdd_HHmmss') }
$Output = [IO.Path]::GetFullPath($Output)
if ($Output.StartsWith($root + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or $Output -eq $root) { throw '分享目录必须在工程目录外，避免递归复制。' }
if ($Output.EndsWith('.zip', [StringComparison]::OrdinalIgnoreCase)) { throw '本脚本只生成普通文件夹，请勿指定 .zip。' }
if (Test-Path -LiteralPath $Output) { throw '输出文件夹已存在，请指定新的 -Output，避免覆盖。' }
New-Item -ItemType Directory -Path $Output | Out-Null
  foreach ($file in Get-ChildItem -LiteralPath $root -Recurse -File -Force) {
    $relative = $file.FullName.Substring($root.Length+1).Replace([IO.Path]::DirectorySeparatorChar,[char]47)
    if ($relative -match '(^|/)(node_modules|dist|data|coverage|\.run[^/]*|\.git|\.tools)(/|$)' -or $relative -match '\.(log|pid|exe|zip|sql\.bak)$') { continue }
    if ($file.Name -like '.env*' -and $file.Name -notin @('.env.example','.env.development')) { continue }
    $destination = Join-Path $Output $relative
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
    Copy-Item -LiteralPath $file.FullName -Destination $destination
  }
Write-Output "已生成普通源码文件夹：$Output"
Write-Output '未包含本机密钥、数据库数据、依赖和构建缓存。接收方需配置自己的 .env.local。'

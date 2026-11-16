$ErrorActionPreference = 'Continue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$be = 'D:\线上零食店小程序\校零智选_最新版_20261114\04_共用后端\backend'
$out = 'D:\线上零食店小程序\校零智选_最新版_20261114\verification\p1-20261114T2005'
New-Item -ItemType Directory -Force -Path $out | Out-Null
$log = Join-Path $out '01-go-check.log'
Set-Content -Path $log -Value '' -Encoding UTF8
function Line([string]$s) { Write-Host $s; Add-Content -Path $log -Value $s -Encoding UTF8 }
function Step([string]$name, [string]$cmd) {
  Line ('### ' + $name + ' | ' + $cmd)
  Push-Location $be
  $o = & cmd.exe /c $cmd 2>&1
  $code = $LASTEXITCODE
  Pop-Location
  Line ('exit=' + $code)
  foreach ($l in $o) { Line ('  ' + $l) }
}
Line ('=== P1 go check ' + (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'))
Step 'go build' 'go build ./...'
Step 'go vet' 'go vet ./...'
Step 'go test' 'go test ./... -count=1'
Line ('done ' + (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'))

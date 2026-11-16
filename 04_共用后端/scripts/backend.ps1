param(
  [ValidateSet('Start', 'Build', 'Test', 'Migrate')]
  [string]$Action = 'Start',
  [Alias('Port')][int]$LocalPortOverride = 0
)

$ErrorActionPreference = 'Stop'

function Assert-FreePort([int]$ListenPort) {
  $listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
  foreach ($listener in $listeners) {
    if ($listener.Port -eq $ListenPort) {
      throw "Port $ListenPort is already in use. Stop its owner explicitly before starting this backend."
    }
  }
}

try {
  $root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
  $backend = Join-Path $root 'backend'
  $migrations = Join-Path $root 'db\migrations'
  $envFile = Join-Path $root '.env.local'
  if (-not (Test-Path -LiteralPath (Join-Path $backend 'go.mod') -PathType Leaf)) {
    throw 'Missing backend/go.mod. Keep this script inside the delivered scripts folder.'
  }
  if (-not (Test-Path -LiteralPath $envFile -PathType Leaf)) {
    throw 'Missing .env.local. Copy .env.example to .env.local and configure your local MySQL connection first.'
  }
  if (-not (Test-Path -LiteralPath $migrations -PathType Container)) {
    throw 'Missing db/migrations. Include the complete migrations folder with this backend.'
  }
  foreach ($rawLine in (Get-Content -LiteralPath $envFile -Encoding UTF8)) {
    $line = $rawLine.Trim()
    if (-not $line -or $line.StartsWith('#')) { continue }
    $separator = $line.IndexOf('=')
    if ($separator -lt 1) { throw 'Invalid .env.local line: expected KEY=VALUE.' }
    $key = $line.Substring(0, $separator).Trim()
    if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw 'Invalid environment variable name in .env.local.' }
    $value = $line.Substring($separator + 1).Trim()
    if ($value.Length -ge 2) {
      $first = $value.Substring(0, 1)
      $last = $value.Substring($value.Length - 1, 1)
      if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
        $value = $value.Substring(1, $value.Length - 2)
      }
    }
    [Environment]::SetEnvironmentVariable($key, $value, 'Process')
  }

  $goCommand = Get-Command go.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
  $go = if ($goCommand) { $goCommand.Source } else { 'C:\Program Files\Go\bin\go.exe' }
  if (-not (Test-Path -LiteralPath $go -PathType Leaf)) {
    throw 'Go was not found. Install Go 1.22 or newer and add it to PATH.'
  }
  $env:GOCACHE = Join-Path $root '.run\go-cache'
  New-Item -ItemType Directory -Force -Path $env:GOCACHE | Out-Null
  $env:MIGRATIONS_DIR = $migrations
  if ($PSBoundParameters.ContainsKey('LocalPortOverride')) {
    if ($LocalPortOverride -lt 1 -or $LocalPortOverride -gt 65535) {
      throw 'LocalPortOverride must be between 1 and 65535.'
    }
    $env:HTTP_ADDR = '127.0.0.1:' + $LocalPortOverride
  }
  if (-not $env:HTTP_ADDR) { $env:HTTP_ADDR = '127.0.0.1:18090' }

  Push-Location -LiteralPath $backend
  try {
    switch ($Action) {
      'Start' {
        if ($env:HTTP_ADDR -notmatch ':(\d+)$') { throw 'HTTP_ADDR must end with a TCP port, for example 127.0.0.1:18090.' }
        $listenPort = [int]$matches[1]
        if ($listenPort -lt 1 -or $listenPort -gt 65535) { throw 'HTTP_ADDR has an invalid TCP port.' }
        Assert-FreePort $listenPort
        Write-Host "Starting backend on port $listenPort. Press Ctrl+C to stop."
        & $go run ./cmd/server
      }
      'Build' { & $go build ./... }
      'Test' { & $go test ./... }
      'Migrate' { & $go run ./cmd/migrate }
    }
    if ($LASTEXITCODE -ne 0) { throw "Backend $Action failed with exit code $LASTEXITCODE." }
  } finally {
    Pop-Location
  }
} catch {
  Write-Error $_ -ErrorAction Continue
  exit 1
}

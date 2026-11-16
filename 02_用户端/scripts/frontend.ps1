param(
  [ValidateSet('Start', 'Install', 'Test', 'Build')]
  [string]$Action = 'Start',
  [int]$Port = 0
)

$ErrorActionPreference = 'Stop'

function Assert-FreePort([int]$ListenPort) {
  $listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
  foreach ($listener in $listeners) {
    if ($listener.Port -eq $ListenPort) {
      throw "Port $ListenPort is already in use. Stop its owner explicitly or pass -Port with an unused port."
    }
  }
}

try {
  $root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
  $web = Join-Path $root 'web'
  # Unicode characters spell the delivered per-end configuration filename.
  $configName = '{0}{1}{2}.json' -f [char]0x7AEF, [char]0x914D, [char]0x7F6E
  $configFile = Join-Path $root $configName
  if (-not (Test-Path -LiteralPath (Join-Path $web 'package.json') -PathType Leaf)) {
    throw 'Missing web/package.json. Keep this script inside the delivered scripts folder.'
  }
  if (-not (Test-Path -LiteralPath $configFile -PathType Leaf)) { throw 'Missing per-end configuration JSON beside the web folder.' }
  $config = Get-Content -LiteralPath $configFile -Raw -Encoding UTF8 | ConvertFrom-Json
  if (-not $config.role -or -not $config.entry) { throw 'Per-end configuration must contain role, entry and port.' }
  if ($PSBoundParameters.ContainsKey('Port')) {
    $listenPort = $Port
  } else {
    $listenPort = 0
    if (-not [int]::TryParse([string]$config.port, [ref]$listenPort)) { throw 'Per-end configuration port must be an integer.' }
  }
  if ($listenPort -lt 1 -or $listenPort -gt 65535) { throw 'Port must be between 1 and 65535.' }

  $npmCommand = Get-Command npm.cmd -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
  $npm = if ($npmCommand) { $npmCommand.Source } else { 'C:\Program Files\nodejs\npm.cmd' }
  if (-not (Test-Path -LiteralPath $npm -PathType Leaf)) { throw 'npm.cmd was not found. Install Node.js 20 or newer and add it to PATH.' }
  $nodeDirectory = Split-Path -Parent $npm
  $env:PATH = $nodeDirectory + ';' + $env:PATH
  if (-not (Get-Command node.exe -CommandType Application -ErrorAction SilentlyContinue)) { throw 'node.exe was not found. Check your Node.js installation and PATH.' }
  $env:VITE_PORT = [string]$listenPort
  # Let Vite read web/.env.development unless the caller explicitly overrides
  # VITE_API_TARGET (for example, the bundled service manager's smoke port).
  $env:VITE_DEMO_MODE = 'false'
  $env:VITE_APP_ROLE = [string]$config.role
  $env:VITE_APP_ENTRY = [string]$config.entry
  if ($Action -ne 'Install' -and -not (Test-Path -LiteralPath (Join-Path $web 'node_modules') -PathType Container)) {
    throw 'Dependencies are missing. Run .\scripts\frontend.ps1 -Action Install first.'
  }

  Push-Location -LiteralPath $web
  try {
    switch ($Action) {
      'Install' {
        if (-not (Test-Path -LiteralPath (Join-Path $web 'package-lock.json') -PathType Leaf)) { throw 'Missing web/package-lock.json; npm ci needs the delivered lockfile.' }
        $npmCache = Join-Path $root '.run\npm-cache'
        & $npm ci --no-audit --no-fund --cache $npmCache
      }
      'Start' {
        Assert-FreePort $listenPort
        Write-Host ("Starting {0}: http://127.0.0.1:{1}{2}" -f $config.role, $listenPort, $config.entry)
        Write-Host 'Press Ctrl+C to stop.'
        & $npm run dev -- --host 127.0.0.1 --port $listenPort --strictPort
      }
      'Test' { & $npm test }
      'Build' { & $npm run build }
    }
    if ($LASTEXITCODE -ne 0) { throw "Frontend $Action failed with exit code $LASTEXITCODE." }
  } finally {
    Pop-Location
  }
} catch {
  Write-Error $_ -ErrorAction Continue
  exit 1
}

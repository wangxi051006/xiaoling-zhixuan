$ErrorActionPreference = 'Continue'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$root = 'D:\线上零食店小程序\校零智选_最新版_20261114'
$be = Join-Path $root '04_共用后端\backend'
$out = Join-Path $root 'verification\fix-pendingcount-20261114T2210'
New-Item -ItemType Directory -Force -Path $out | Out-Null
$log = Join-Path $out '01-verify.log'
Set-Content -Path $log -Value '' -Encoding UTF8
function Line([string]$s) { Write-Host $s; Add-Content -Path $log -Value $s -Encoding UTF8 }
function Step([string]$dir, [string]$name, [string]$cmd) {
  Line ('### ' + $name + ' | ' + $cmd)
  Line ('cwd=' + $dir)
  Push-Location $dir
  $o = & cmd.exe /c $cmd 2>&1
  $code = $LASTEXITCODE
  Pop-Location
  Line ('exit=' + $code)
  foreach ($l in $o) { Line ('  ' + $l) }
  return $code
}

$webs = @(
  (Join-Path $root '01_商家端\web'),
  (Join-Path $root '02_用户端\web'),
  (Join-Path $root '03_骑手端\web')
)

$testBody = @'
import test from 'node:test'
import assert from 'node:assert/strict'
import { batchFinished } from '../src/batch.js'

// Shape returned by POST /admin/orders/batch-actions and by /resume when DONE.
const done = {
  batchId: '12', action: 'ACCEPT', status: 'DONE',
  expectedCount: 2, completedCount: 2, pendingCount: 0,
  items: [
    { orderId: '1', result: 'SUCCESS', code: '', message: '', version: 4 },
    { orderId: '2', result: 'FAILED', code: 'VERSION_CONFLICT', message: '订单已被其他人更新，请刷新后重试', version: 3 },
  ],
}

test('DONE response with pendingCount 0 finishes the batch', () => {
  assert.equal(batchFinished(done), true)
})

test('legacy DONE response without pendingCount must NOT finish (regression for the reported bug)', () => {
  const legacy = { ...done }
  delete legacy.pendingCount
  assert.equal(Number(legacy.pendingCount), Number(undefined))
  assert.equal(batchFinished(legacy), false)
})

test('incomplete batch keeps the pending request', () => {
  const partial = { batchId: '12', status: 'RUNNING', expectedCount: 2, completedCount: 1, pendingCount: 1, items: [{ orderId: '1', result: 'SUCCESS' }] }
  assert.equal(batchFinished(partial), false)
})

test('all items terminal with zero pending finishes even when every item failed', () => {
  const allFailed = { expectedCount: 2, completedCount: 2, pendingCount: 0, items: [{ result: 'FAILED' }, { result: 'FAILED' }] }
  assert.equal(batchFinished(allFailed), true)
})
'@

foreach ($web in $webs) {
  $tests = Join-Path $web 'tests'
  New-Item -ItemType Directory -Force -Path $tests | Out-Null
  Set-Content -Path (Join-Path $tests 'batch-contract.test.mjs') -Value $testBody -Encoding UTF8
  Line ('wrote batch-contract.test.mjs -> ' + $web)
}

Line ('=== fix verification ' + (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'))
$null = Step $be 'go build' 'go build ./...'
$null = Step $be 'go vet' 'go vet ./...'
$null = Step $be 'go test' 'go test ./... -count=1'
foreach ($web in $webs) {
  $name = Split-Path (Split-Path $web -Parent) -Parent
  $null = Step $web ('npm test ' + $name) 'npm.cmd test'
  $null = Step $web ('npm run build ' + $name) 'npm.cmd run build'
}
Line ('done ' + (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'))

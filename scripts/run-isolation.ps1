param(
    [Parameter(Mandatory)][string]$Request,
    [string]$Runtime
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$work = Join-Path (Split-Path $repo -Parent) 'work'
if (-not $Runtime) { $Runtime = Join-Path $work 'isolation-runtime/runtime' }
$requestFile = (Resolve-Path -LiteralPath $Request).Path
$env:GOMODCACHE = Join-Path $work 'go/mod'
$env:GOCACHE = Join-Path $work 'go/cache'
$binary = Join-Path $repo 'backend/bin/isolation-runner.exe'
Push-Location (Join-Path $repo 'backend')
try {
    go build -o $binary ./internal/isolation/cmd/isolation
    if ($LASTEXITCODE -ne 0) { throw 'Isolation runner build failed.' }
} finally { Pop-Location }
& $binary -runtime $Runtime -request $requestFile
if ($LASTEXITCODE -ne 0) { throw "Isolated execution failed (exit $LASTEXITCODE). Inspect the bounded result or diagnostic above." }

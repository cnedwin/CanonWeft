param([string]$Runtime)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$work = Join-Path (Split-Path $repo -Parent) 'work'
if (-not $Runtime) { $Runtime = Join-Path $work 'isolation-runtime/runtime' }
if (-not (Test-Path -LiteralPath (Join-Path $Runtime 'manifest.json'))) { throw 'Required Linux runtime missing. Run scripts/install-isolation.ps1 first.' }
$env:GOMODCACHE = Join-Path $work 'go/mod'
$env:GOCACHE = Join-Path $work 'go/cache'
$previousRuntime = $env:STUDIO_ISOLATION_TEST_RUNTIME
$env:STUDIO_ISOLATION_TEST_RUNTIME = (Resolve-Path -LiteralPath $Runtime).Path
try {
    Push-Location (Join-Path $repo 'backend')
    try {
        go test ./internal/isolation/... -count=1 -v
        if ($LASTEXITCODE -ne 0) { throw 'Real Linux isolation tests failed.' }
        go vet ./internal/isolation/...
        if ($LASTEXITCODE -ne 0) { throw 'Isolation vet failed.' }
        $unformatted = gofmt -l internal/isolation
        if ($LASTEXITCODE -ne 0) { throw 'Isolation format check failed.' }
        if ($unformatted) { throw "Unformatted isolation files: $unformatted" }
    } finally { Pop-Location }
} finally {
    $env:STUDIO_ISOLATION_TEST_RUNTIME = $previousRuntime
}

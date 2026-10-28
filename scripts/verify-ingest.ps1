$ErrorActionPreference = 'Stop'
$ingestRepo = Split-Path $PSScriptRoot -Parent
$ingestBase = Split-Path $ingestRepo -Parent
. (Join-Path $PSScriptRoot 'ingest-runtime.ps1') env
& $env:STUDIO_INGEST_PYTHON -B -I (Join-Path $ingestRepo 'tools/ingest/verify_installation.py')
if ($LASTEXITCODE -ne 0) { throw 'Ingest artifact verification failed' }
& $env:STUDIO_INGEST_PYTHON -B -I (Join-Path $ingestRepo 'tools/ingest/tests/test_runtime.py')
if ($LASTEXITCODE -ne 0) { throw 'Ingest parser/resource verification failed' }
$env:GOMODCACHE = Join-Path $ingestBase 'work/go/mod'
$env:GOCACHE = Join-Path $ingestBase 'work/go/cache'
$env:STUDIO_TEST_INGEST = '1'
Push-Location (Join-Path $ingestRepo 'backend')
try {
    go test ./internal/retrieval -run TestDocument -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'Go document adapter verification failed' }
    go vet ./internal/retrieval
    if ($LASTEXITCODE -ne 0) { throw 'Go document adapter vet failed' }
} finally {
    Remove-Item Env:STUDIO_TEST_INGEST -ErrorAction SilentlyContinue
    Pop-Location
}

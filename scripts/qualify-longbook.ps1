$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') env
& (Join-Path $PSScriptRoot 'stop-test-server.ps1')
. (Join-Path $PSScriptRoot 'embedding-runtime.ps1') start
$env:STUDIO_QUALIFICATION_REPORT=Join-Path $projectRoot '.runtime/longbook-index-evidence.json'
Push-Location (Join-Path $projectRoot 'backend')
try {
    go test -tags qualification ./tests/integration -run '^TestLongBookIndexCoverageAndRetrieval$' -count=1 -timeout 6m -v
    if($LASTEXITCODE -ne 0){throw 'Long-book infrastructure qualification failed'}
} finally {
    Pop-Location
    Remove-Item Env:STUDIO_QUALIFICATION_REPORT -ErrorAction SilentlyContinue
}

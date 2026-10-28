$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') env
$env:STUDIO_PROBE_ID='restart-'+[Guid]::NewGuid().ToString('N')
Push-Location (Join-Path $projectRoot 'backend')
try {
    go test ./tests/integration -run '^TestTemporalDurableWait$' -count=1
    if($LASTEXITCODE -ne 0){throw 'Worker persistence check failed'}
    $env:STUDIO_PROBE_PHASE='start'
    go test ./tests/integration -run '^TestTemporalServerRestart$' -count=1 -v
    if($LASTEXITCODE -ne 0){throw 'Persisting restart probe failed'}
    & (Join-Path $PSScriptRoot 'runtime.ps1') stop
    & (Join-Path $PSScriptRoot 'runtime.ps1') start
    if($LASTEXITCODE -ne 0){throw 'Restart failed'}
    $env:STUDIO_PROBE_PHASE='finish'
    go test ./tests/integration -run '^TestTemporalServerRestart$' -count=1 -v
    if($LASTEXITCODE -ne 0){throw 'Recovery check failed'}
} finally {
    Remove-Item Env:STUDIO_PROBE_PHASE -ErrorAction SilentlyContinue
    Remove-Item Env:STUDIO_PROBE_ID -ErrorAction SilentlyContinue
    Pop-Location
}

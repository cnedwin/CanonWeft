param([switch]$NoBuild)
$ErrorActionPreference='Stop'
$projectRoot=$PSScriptRoot
# A fresh local launch keeps provider requests offline. Preserve a deliberate
# process setting; saving a Key and authorizing a paid call remain separate.
if (-not $env:STUDIO_PROVIDER_NETWORK) { $env:STUDIO_PROVIDER_NETWORK = 'disabled' }
. (Join-Path $projectRoot 'scripts/ingest-runtime.ps1') status
. (Join-Path $projectRoot 'scripts/runtime.ps1') start
# Provision the independent ledger before starting any API/worker. The initializer
# proves that managed services are stopped and preserves an existing baseline.
& (Join-Path $projectRoot 'scripts/initialize-authority.ps1') -Business studio
# Migrations may add protected tables. Re-audit and extend the platform's owned
# read-only collector account before launching the application or any worker.
& (Join-Path $projectRoot 'scripts/initialize-log-protection.ps1') -Business studio
. (Join-Path $projectRoot 'scripts/runtime.ps1') env
. (Join-Path $projectRoot 'scripts/embedding-runtime.ps1') start
if(-not $NoBuild) {
    Push-Location (Join-Path $projectRoot 'web')
    try {
        node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
        if($LASTEXITCODE -ne 0){throw 'Vue type check failed'}
        node node_modules/vite/bin/vite.js build
        if($LASTEXITCODE -ne 0){throw 'Vue build failed'}
    } finally {Pop-Location}
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go build -buildvcs=false -o bin/studio-prod.exe ./cmd/studio
        if($LASTEXITCODE -ne 0){throw 'Go build failed'}
    } finally {Pop-Location}
}
$env:STUDIO_ADDR='127.0.0.1:18765'
Write-Output 'Novel Studio: http://127.0.0.1:18765 . On first use, create your personal local account; there is no default login.'
& (Join-Path $projectRoot 'backend/bin/studio-prod.exe')

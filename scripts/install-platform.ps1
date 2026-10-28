$ErrorActionPreference = 'Stop'
$installProject = Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$installTools = Enable-PortableToolchain

# Install artifacts only. Database initialization, account creation and service
# startup belong to Start-Platform; this does not contact model providers.
& (Join-Path $PSScriptRoot 'install-runtime.ps1')
& (Join-Path $PSScriptRoot 'install-embedding.ps1')
& (Join-Path $PSScriptRoot 'install-ingest.ps1')
& (Join-Path $PSScriptRoot 'install-isolation.ps1') -Python $installTools.Python

& (Join-Path $PSScriptRoot 'install-web.ps1')

$env:GOMODCACHE = Join-Path (Split-Path $installProject -Parent) 'work/go/mod'
$env:GOCACHE = Join-Path (Split-Path $installProject -Parent) 'work/go/cache'
Push-Location (Join-Path $installProject 'backend')
try {
    & $installTools.Go mod download
    if ($LASTEXITCODE -ne 0) { throw 'Locked Go dependency download failed' }
    & $installTools.Go mod verify
    if ($LASTEXITCODE -ne 0) { throw 'Go dependency verification failed' }
    & $installTools.Go build -buildvcs=false -o bin/studio-prod.exe ./cmd/studio
    if ($LASTEXITCODE -ne 0) { throw 'Application build failed' }
} finally { Pop-Location }
Write-Output 'Local dependencies and application build are ready. Run Start-Platform.cmd to start. No model request was sent.'

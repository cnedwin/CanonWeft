param(
    [Parameter(Mandatory)][string]$Archive,
    [string]$SourceEnvironment='STUDIO_DSN',
    [string]$BlobRoot
)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'runtime.ps1') env
if (-not $BlobRoot) { $BlobRoot=$env:STUDIO_BLOB_ROOT }
$archivePath=[IO.Path]::GetFullPath($Archive)
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
    & go run ./cmd/backup create -archive $archivePath -source-env $SourceEnvironment -blobs $BlobRoot
    if ($LASTEXITCODE -ne 0) { throw 'Backup failed; no successful archive was produced.' }
} finally { Pop-Location }

param(
    [Parameter(Mandatory)][string]$Archive,
    [Parameter(Mandatory)][string]$BlobRoot,
    [ValidatePattern('^studio_restore_[a-f0-9]{32}$')][string]$Database=('studio_restore_'+[Guid]::NewGuid().ToString('N')),
    [string]$SourceEnvironment='STUDIO_DSN'
)
$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'runtime.ps1') env
# Local runtime root credential is used only in this process, never as a CLI
# argument or a file. The engine refuses all existing/non-isolated databases.
$restoreRootPassword=Reveal-Value $credentials.root
$previousRestoreDSN=$env:STUDIO_RESTORE_DSN
$env:STUDIO_RESTORE_DSN="root:$restoreRootPassword@tcp(127.0.0.1:13306)/$Database`?charset=utf8mb4&loc=UTC"
$archivePath=[IO.Path]::GetFullPath($Archive)
$blobPath=[IO.Path]::GetFullPath($BlobRoot)
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
    & go run ./cmd/backup restore -archive $archivePath -source-env $SourceEnvironment -blobs $blobPath
    if ($LASTEXITCODE -ne 0) { throw 'Restore refused or failed; a partial disposable target may remain for inspection.' }
} finally {
    Pop-Location
    $env:STUDIO_RESTORE_DSN=$previousRestoreDSN
    $restoreRootPassword=$null
}

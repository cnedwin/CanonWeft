$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') env
$previousBackupDSN=$env:STUDIO_BACKUP_TEST_ADMIN_DSN
try {
    # Only the test process receives this credential. Tests create and clean up
    # their own random databases; the live application database is never a target.
    $backupTestPassword=Reveal-Value $credentials.root
    $env:STUDIO_BACKUP_TEST_ADMIN_DSN="root:$backupTestPassword@tcp(127.0.0.1:13306)/?parseTime=true&charset=utf8mb4&loc=UTC"
    $backupTestPassword=$null
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go test ./internal/backup ./tests/backup ./cmd/backup -count=1 -v
        if($LASTEXITCODE -ne 0){throw 'Required real MySQL backup/restore checks failed'}
        go vet ./internal/backup ./tests/backup ./cmd/backup
        if($LASTEXITCODE -ne 0){throw 'Backup/restore static checks failed'}
    } finally {Pop-Location}
} finally {
    $env:STUDIO_BACKUP_TEST_ADMIN_DSN=$previousBackupDSN
    $backupTestPassword=$null
}

param([Parameter(Mandatory=$true)][ValidateSet('studio','studio_test')][string]$Business)
$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
# Setup must be able to resume a pending encrypted file without a READY pair.
# The ordinary runtime loader continues to reject incomplete configurations.
. (Join-Path $PSScriptRoot 'runtime.ps1') -Action env -LogSetupDatabase $Business
$previousLogAdminDSN=$env:STUDIO_LOG_ADMIN_DSN
try {
    $logSetupPassword=Reveal-Value $credentials.root
    $env:STUDIO_LOG_ADMIN_DSN="root:$logSetupPassword@tcp(127.0.0.1:13306)/?parseTime=true&charset=utf8mb4&loc=UTC"
    $logSetupPassword=$null
    $credentialFile=Join-Path $runtime "log-protection-$Business.dpapi"
    $readyFile=Join-Path $runtime "log-protection-$Business.ready.json"
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go run -buildvcs=false ./cmd/log-protection -source $Business -schema $env:STUDIO_AUTHORITY_SCHEMA -credential-file $credentialFile -ready-file $readyFile
        if($LASTEXITCODE -ne 0){throw 'Dedicated log reader setup failed; the existing account and encrypted credential were preserved for investigation or retry.'}
    } finally {Pop-Location}
} finally {
    $env:STUDIO_LOG_ADMIN_DSN=$previousLogAdminDSN
    $logSetupPassword=$null
}

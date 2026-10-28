param([Parameter(Mandatory=$true)][ValidateSet('studio','studio_test')][string]$Business)
$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') -Action env -LogSetupDatabase $Business
. (Join-Path $PSScriptRoot 'authority-preflight.ps1')
Assert-AuthorityServicesStopped -ProjectRoot $projectRoot -Business $Business -ConfiguredAddress $env:STUDIO_ADDR
$previousAuthorityDSN=$env:STUDIO_AUTHORITY_ADMIN_DSN
try {
    $authoritySetupPassword=Reveal-Value $credentials.root
    $env:STUDIO_AUTHORITY_ADMIN_DSN="root:$authoritySetupPassword@tcp(127.0.0.1:13306)/?parseTime=true&charset=utf8mb4&loc=UTC"
    $authoritySetupPassword=$null
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go run -buildvcs=false ./cmd/authority -services-stopped -source $Business -app-user $Business -schema studio_authority
        if($LASTEXITCODE -ne 0){throw 'Protected authority initialization failed'}
    } finally {Pop-Location}
} finally {
    $env:STUDIO_AUTHORITY_ADMIN_DSN=$previousAuthorityDSN
    $authoritySetupPassword=$null
}

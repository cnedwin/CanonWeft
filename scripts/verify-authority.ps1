$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') env
$previousAuthorityDSN=$env:STUDIO_AUTHORITY_TEST_ADMIN_DSN
try {
    $authorityTestPassword=Reveal-Value $credentials.root
    $env:STUDIO_AUTHORITY_TEST_ADMIN_DSN="root:$authorityTestPassword@tcp(127.0.0.1:13306)/?parseTime=true&charset=utf8mb4&loc=UTC"
    $authorityTestPassword=$null
    Push-Location (Join-Path $projectRoot 'backend')
    try {
        go test ./internal/authority ./internal/service ./cmd/authority -count=1 -v
        if($LASTEXITCODE -ne 0){throw 'Required real protected-authority checks failed'}
        go vet ./internal/authority ./internal/service ./cmd/authority
        if($LASTEXITCODE -ne 0){throw 'Protected authority static checks failed'}
    } finally {Pop-Location}
} finally {
    $env:STUDIO_AUTHORITY_TEST_ADMIN_DSN=$previousAuthorityDSN
    $authorityTestPassword=$null
}

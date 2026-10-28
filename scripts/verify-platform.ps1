$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$isolationRuntimeRoot=Join-Path (Split-Path $projectRoot -Parent) 'work/isolation-runtime/runtime'
if (-not (Test-Path -LiteralPath (Join-Path $isolationRuntimeRoot 'manifest.json'))) {
    throw 'Required Linux isolation runtime missing. Run scripts/install-isolation.ps1 before full platform verification.'
}
. (Join-Path $PSScriptRoot 'runtime.ps1') -Action env -LogSetupDatabase studio_test
& (Join-Path $PSScriptRoot 'verify-ingest.ps1')
& (Join-Path $PSScriptRoot 'stop-test-server.ps1')
& (Join-Path $PSScriptRoot 'initialize-authority.ps1') -Business studio_test
& (Join-Path $PSScriptRoot 'initialize-log-protection.ps1') -Business studio_test
& (Join-Path $PSScriptRoot 'test-log-protection-env.ps1')
. (Join-Path $PSScriptRoot 'runtime.ps1') env
& (Join-Path $PSScriptRoot 'verify-embedding-runtime.ps1')
$verificationEnvironment=@{}
foreach($name in @('STUDIO_BACKUP_TEST_ADMIN_DSN','STUDIO_AUTHORITY_TEST_ADMIN_DSN','STUDIO_QUALIFICATION_TEST_ADMIN_DSN','STUDIO_BINLOG_TEST_ADMIN_DSN','STUDIO_ISOLATION_TEST_RUNTIME','STUDIO_LOG_READER_TEST_DSN')) {
    $verificationEnvironment[$name]=[Environment]::GetEnvironmentVariable($name,'Process')
}
$verificationPassword=Reveal-Value $credentials.root
$verificationDSN="root:$verificationPassword@tcp(127.0.0.1:13306)/?parseTime=true&charset=utf8mb4&loc=UTC"
$env:STUDIO_BACKUP_TEST_ADMIN_DSN=$verificationDSN
$env:STUDIO_AUTHORITY_TEST_ADMIN_DSN=$verificationDSN
$env:STUDIO_QUALIFICATION_TEST_ADMIN_DSN=$verificationDSN
$env:STUDIO_BINLOG_TEST_ADMIN_DSN=$verificationDSN
$env:STUDIO_ISOLATION_TEST_RUNTIME=$isolationRuntimeRoot
$env:STUDIO_LOG_READER_TEST_DSN=$env:STUDIO_TEST_LOG_DSN
$verificationPassword=$null
$verificationDSN=$null
try {
. (Join-Path $PSScriptRoot 's3-test-runtime.ps1') start
try {
Push-Location (Join-Path $projectRoot 'backend')
try {
    # These packages share the reviewed VM and one guarded browser seed DB.
    # Run each required real suite once, serially, with all opt-in prerequisites.
    go test -p 1 ./... -count=1 -timeout=25m 2>&1 | Tee-Object -FilePath (Join-Path $projectRoot '.runtime/verify-platform-go-latest.log')
    if($LASTEXITCODE -ne 0){throw 'Go tests failed'}
    go vet ./...
    if($LASTEXITCODE -ne 0){throw 'Go vet failed'}
    $packageDirectories=go list -f '{{.Dir}}' ./...
    if($LASTEXITCODE -ne 0){throw 'Go package inventory failed'}
    $unformatted=gofmt -l $packageDirectories
    if($LASTEXITCODE -ne 0 -or $unformatted){throw "Go formatting verification failed: $unformatted"}
    # Integration tests may reference intentionally deleted temporary blobs.
    # This existing guarded fixture resets only studio_test for browser use.
    go test ./tests/integration -run '^TestCreateProjectIdempotent$' -count=1
    if($LASTEXITCODE -ne 0){throw 'Browser test seeding failed'}
} finally {Pop-Location}
} finally { & (Join-Path $PSScriptRoot 's3-test-runtime.ps1') stop }
} finally {
    foreach($name in $verificationEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name,$verificationEnvironment[$name],'Process')
    }
    $verificationPassword=$null
    $verificationDSN=$null
}
try {
& (Join-Path $PSScriptRoot 'start-test-server.ps1')
Push-Location (Join-Path $projectRoot 'web')
try {
    node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
    if($LASTEXITCODE -ne 0){throw 'Vue types failed'}
    node node_modules/eslint/bin/eslint.js src tests
    if($LASTEXITCODE -ne 0){throw 'Vue lint failed'}
    node node_modules/prettier/bin/prettier.cjs --check src tests *.ts *.json *.html
    if($LASTEXITCODE -ne 0){throw 'Vue formatting failed'}
    node node_modules/vite/bin/vite.js build
    if($LASTEXITCODE -ne 0){throw 'Vue build failed'}
    $env:PLAYWRIGHT_BROWSERS_PATH=Join-Path (Split-Path $projectRoot -Parent) 'work/browsers'
    Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue
    node node_modules/@playwright/test/cli.js test
    if($LASTEXITCODE -ne 0){throw 'Browser tests failed (test server on 18865 required)'}
} finally {Pop-Location}
# Imports must not rewrite immutable toolchain files (in particular Python pyc).
$null = Get-PortableToolchain
} finally { & (Join-Path $PSScriptRoot 'stop-test-server.ps1') }
# This separate tests-only executable exercises provider protocols without any
# paid network request. The ordinary production server never fabricates output.
& (Join-Path $PSScriptRoot 'verify-qualification-browser.ps1') -AllDedicated

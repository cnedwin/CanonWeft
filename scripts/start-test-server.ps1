$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
. (Join-Path $PSScriptRoot 'runtime.ps1') env
$binary=Join-Path $projectRoot 'backend/bin/studio.exe'
$pidFile=Join-Path $projectRoot '.runtime/studio-test.pid'
if(Test-Path -LiteralPath $pidFile){
    $processId=[int](Get-Content -LiteralPath $pidFile)
    $running=Get-Process -Id $processId -ErrorAction SilentlyContinue
    if($running){
        if($running.Path -ne $binary){throw 'Unexpected test server process identity'}
        Stop-Process -Id $processId
        $running.WaitForExit(10000) | Out-Null
    }
}
Push-Location (Join-Path $projectRoot 'backend')
try {
    go build -buildvcs=false -o bin/studio.exe ./cmd/studio
    if($LASTEXITCODE -ne 0){throw 'Test server build failed'}
} finally {Pop-Location}
# Match production startup ordering after an additive schema upgrade. The
# initializer preserves existing data/ledger; the collector remains read-only.
& (Join-Path $PSScriptRoot 'initialize-authority.ps1') -Business studio_test
& (Join-Path $PSScriptRoot 'initialize-log-protection.ps1') -Business studio_test
. (Join-Path $PSScriptRoot 'runtime.ps1') env
$env:STUDIO_DSN=$env:STUDIO_TEST_DSN
$env:STUDIO_TEMPORAL_NAMESPACE='studio-browser'
$env:STUDIO_ADDR='127.0.0.1:18865'
$env:STUDIO_PROVIDER_NETWORK='disabled'
$env:STUDIO_BLOB_ROOT=Join-Path $projectRoot '.runtime/browser-blobs'
$env:STUDIO_VAULT_MASTER=Join-Path $projectRoot '.runtime/browser-vault-master/key.dpapi'
$env:STUDIO_VAULT_ROOT=Join-Path $projectRoot '.runtime/browser-vault-ciphertexts'
$app=Start-Process -FilePath $binary -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $projectRoot '.runtime/studio-test.log') -RedirectStandardError (Join-Path $projectRoot '.runtime/studio-test-error.log')
$app.Id | Set-Content -LiteralPath $pidFile
for($attempt=0;$attempt -lt 30;$attempt++){
    if($app.HasExited){throw 'Test server exited before ready'}
    try { $health=Invoke-RestMethod 'http://127.0.0.1:18865/api/health' -TimeoutSec 2; if($health.database -eq 'ready'){Write-Output 'Isolated test server ready';return} } catch {Start-Sleep -Milliseconds 200}
}
throw 'Test server not ready'

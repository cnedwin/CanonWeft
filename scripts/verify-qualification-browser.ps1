param([switch]$StopOnly,[switch]$WeeklyOnly,[switch]$RecipeOnly,[switch]$AllDedicated)
$ErrorActionPreference='Stop'
$projectRoot=[IO.Path]::GetFullPath((Split-Path $PSScriptRoot -Parent))
$fixtureBinary=Join-Path $projectRoot 'backend/bin/qualification-fixture.exe'
$fixturePidFile=Join-Path $projectRoot '.runtime/qualification-fixture.pid'
function Stop-QualificationFixture {
    if(-not (Test-Path -LiteralPath $fixturePidFile)){return}
    $fixtureProcessId=[int](Get-Content -LiteralPath $fixturePidFile -Raw)
    $fixtureProcess=Get-Process -Id $fixtureProcessId -ErrorAction SilentlyContinue
    if($fixtureProcess){
        if(-not [string]::Equals($fixtureProcess.Path,$fixtureBinary,[StringComparison]::OrdinalIgnoreCase)){throw 'Fixture PID belongs to an unexpected executable; refusing to stop it'}
        Stop-Process -Id $fixtureProcessId
        $fixtureProcess.WaitForExit(10000)|Out-Null
    }
    Remove-Item -LiteralPath $fixturePidFile -ErrorAction SilentlyContinue
}
Stop-QualificationFixture
if($StopOnly){return}
. (Join-Path $PSScriptRoot 'runtime.ps1') env
# The executable also parses and rejects every DSN except the explicit
# studio_test user/database. No production schema is reset or accepted here.
& (Join-Path $PSScriptRoot 'stop-test-server.ps1')
& (Join-Path $PSScriptRoot 'initialize-authority.ps1') -Business studio_test
Push-Location (Join-Path $projectRoot 'web')
try {
    # Always test the current source, including when this entry point is invoked
    # directly after a UI change rather than by the full platform check.
    node node_modules/vite/bin/vite.js build
    if($LASTEXITCODE -ne 0){throw 'Qualification browser UI build failed'}
}finally{Pop-Location}
Push-Location (Join-Path $projectRoot 'backend')
try {
    go build -buildvcs=false -o bin/qualification-fixture.exe ./tests/fixtures/qualification
    if($LASTEXITCODE -ne 0){throw 'Qualification fixture build failed'}
}finally{Pop-Location}
$previousFixture=$env:STUDIO_QUALIFICATION_BROWSER_FIXTURE
$env:STUDIO_DSN=$env:STUDIO_TEST_DSN
$env:STUDIO_PROVIDER_NETWORK='disabled'
$env:STUDIO_BLOB_ROOT=Join-Path $projectRoot '.runtime/qualification-browser-blobs'
$env:STUDIO_VAULT_MASTER=Join-Path $projectRoot '.runtime/qualification-browser-master/key.dpapi'
$env:STUDIO_VAULT_ROOT=Join-Path $projectRoot '.runtime/qualification-browser-cipher'
$env:STUDIO_WEB_DIR=Join-Path $projectRoot 'web/dist'
$env:STUDIO_QUALIFICATION_BROWSER_FIXTURE='1'
$env:PLAYWRIGHT_BROWSERS_PATH=Join-Path (Split-Path $projectRoot -Parent) 'work/browsers'
Remove-Item Env:NO_COLOR -ErrorAction SilentlyContinue
try {
    $fixtureProcess=Start-Process -FilePath $fixtureBinary -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $projectRoot '.runtime/qualification-fixture.log') -RedirectStandardError (Join-Path $projectRoot '.runtime/qualification-fixture-error.log')
    $fixtureProcess.Id|Set-Content -LiteralPath $fixturePidFile
    $ready=$false
    for($attempt=0;$attempt -lt 100;$attempt++){
        if($fixtureProcess.HasExited){throw 'Qualification fixture exited before readiness'}
        try{$health=Invoke-RestMethod 'http://127.0.0.1:18865/api/health' -TimeoutSec 2;if($health.database -eq 'ready'){$ready=$true;break}}catch{Start-Sleep -Milliseconds 200}
    }
    if(-not $ready){throw 'Qualification fixture did not become ready'}
    Push-Location (Join-Path $projectRoot 'web')
    try {
        if($RecipeOnly){node node_modules/@playwright/test/cli.js test recipe-execution.spec.ts}
        elseif($AllDedicated){node node_modules/@playwright/test/cli.js test qualification.spec.ts qualification-weekly.spec.ts semantic-reviews.spec.ts release-origins.spec.ts recipe-execution.spec.ts}
        elseif($WeeklyOnly){node node_modules/@playwright/test/cli.js test qualification-weekly.spec.ts}
        else{node node_modules/@playwright/test/cli.js test qualification.spec.ts qualification-weekly.spec.ts}
        if($LASTEXITCODE -ne 0){throw 'Qualification browser regressions failed'}
    }finally{Pop-Location}
}finally{
    Stop-QualificationFixture
    $env:STUDIO_QUALIFICATION_BROWSER_FIXTURE=$previousFixture
}

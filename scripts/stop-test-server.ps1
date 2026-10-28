$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$binary=Join-Path $projectRoot 'backend/bin/studio.exe'
$pidFile=Join-Path $projectRoot '.runtime/studio-test.pid'
if(Test-Path -LiteralPath $pidFile){
    $processId=[int](Get-Content -LiteralPath $pidFile)
    $running=Get-Process -Id $processId -ErrorAction SilentlyContinue
    if($running){
        if($running.Path -ne $binary){throw 'Unexpected test server process identity'}
        Stop-Process -Id $processId
        if(-not $running.WaitForExit(10000)){throw 'Test server did not stop'}
    }
}

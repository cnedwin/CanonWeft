# Isolated lifecycle contracts: no real workers, ports, models or credentials.
$ErrorActionPreference='Stop'
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('canonweft-embedding-'+[guid]::NewGuid().ToString('N'))
$scripts=Join-Path $fixture 'scripts'
$runtime=Join-Path $fixture '.runtime'
$null=New-Item -ItemType Directory -Path $scripts,$runtime,(Join-Path $fixture 'env/Scripts')
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'embedding-runtime.ps1') -Destination (Join-Path $scripts 'embedding-runtime.ps1')
@'
function Get-PythonWorkerEnvironment {param($Worker); Join-Path (Split-Path $PSScriptRoot -Parent) 'env'}
'@ | Set-Content -LiteralPath (Join-Path $scripts 'python-worker-tools.ps1')
('home = '+(Join-Path $fixture 'base')) | Set-Content -LiteralPath (Join-Path $fixture 'env/pyvenv.cfg')
ConvertTo-SecureString 'isolated-test-token-not-a-provider-key' -AsPlainText -Force | ConvertFrom-SecureString | Set-Content -LiteralPath (Join-Path $runtime 'embedding-token.dpapi')
$tokenHash=(Get-FileHash -LiteralPath (Join-Path $runtime 'embedding-token.dpapi')).Hash
$oldToken=$env:STUDIO_EMBEDDING_TOKEN
$oldURL=$env:STUDIO_EMBEDDING_URL
$failures=[Collections.Generic.List[string]]::new()
try {
    foreach($case in @('stopped','already-exited','replacement','wrong-identity','wait-failed','listener-remains','control-busy')) {
        & {
            $script:fixtureCase=$case
            $script:fixturePidPath=Join-Path $runtime 'embedding.pid'
            "111`n" | Set-Content -LiteralPath $script:fixturePidPath -NoNewline
            $stamp=[DateTime]::Parse('2026-10-28T00:00:00Z').ToUniversalTime()
            $python=Join-Path $fixture 'env/Scripts/python.exe'
            $fake=[pscustomobject]@{Id=111;Path=$python;StartTime=$stamp;HasExited=$false;KillCount=0;FixtureCase=$case;PidPath=$script:fixturePidPath;SafeHandle=[pscustomobject]@{IsInvalid=$false;IsClosed=$false}}
            if($case -eq 'wrong-identity'){$fake.Path=Join-Path $fixture 'other.exe'}
            $fake | Add-Member ScriptMethod Kill {
                $this.KillCount++
                $this.HasExited=$true
                if($this.FixtureCase -eq 'replacement'){'222' | Set-Content -LiteralPath $this.PidPath}
            }
            $fake | Add-Member ScriptMethod WaitForExit {param($Milliseconds);$this.FixtureCase -ne 'wait-failed'}
            $fake | Add-Member ScriptMethod Dispose {}
            $script:fixtureProcess=$fake
            $script:fixturePython=$python
            function Get-Process {param($Id,$ErrorAction);if($script:fixtureCase -ne 'already-exited'){$script:fixtureProcess}}
            function Get-CimInstance {
                param($ClassName,$Namespace,$Filter,$OperationTimeoutSec,$ErrorAction)
                if($ClassName -eq 'MSFT_NetTCPConnection'){
                    if($Filter -ne 'State=2 AND LocalPort=18766'){throw 'Unexpected fixture listener query'}
                    if($script:fixtureCase -eq 'listener-remains'){[pscustomobject]@{OwningProcess=222}}
                    return
                }
                if($ClassName -ne 'Win32_Process'){throw 'Unexpected fixture process query'}
                [pscustomobject]@{ProcessId=111;ParentProcessId=1;ExecutablePath=$script:fixturePython;CreationDate=$script:fixtureProcess.StartTime}
            }
            $lock=$null
            if($case -eq 'control-busy'){$lock=[IO.File]::Open((Join-Path $runtime 'embedding-control.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)}
            $thrown=$false
            try { . (Join-Path $scripts 'embedding-runtime.ps1') stop } catch {$thrown=$true} finally {if($lock){$lock.Dispose()}}
            $exists=Test-Path -LiteralPath $script:fixturePidPath
            $expectedRefusal=$case -in @('wrong-identity','wait-failed','listener-remains','control-busy')
            if($thrown -ne $expectedRefusal){$failures.Add("${case}: unexpected refusal=$thrown")}
            if($case -in @('stopped','already-exited') -and $exists){$failures.Add("${case}: stale PID record retained after verified stop")}
            if($case -eq 'replacement' -and (-not $exists -or (Get-Content -LiteralPath $script:fixturePidPath -Raw).Trim() -ne '222')){$failures.Add('replacement: newer marker was removed')}
            if($expectedRefusal -and -not $exists){$failures.Add("${case}: refused operation removed marker")}
            if($case -in @('wrong-identity','control-busy') -and $fake.KillCount){$failures.Add("${case}: unverified process was stopped")}
            if((Get-FileHash -LiteralPath (Join-Path $runtime 'embedding-token.dpapi')).Hash -ne $tokenHash){$failures.Add("${case}: token changed")}
        }
    }
    # First-time startup must acquire the lifecycle lock before creating a key.
    $tokenPath=Join-Path $runtime 'embedding-token.dpapi'
    $savedToken=Join-Path $runtime 'embedding-token.saved.dpapi'
    Move-Item -LiteralPath $tokenPath -Destination $savedToken
    $firstStartLock=[IO.File]::Open((Join-Path $runtime 'embedding-control.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
    $refused=$false
    try { & (Join-Path $scripts 'embedding-runtime.ps1') start } catch {$refused=$true} finally {$firstStartLock.Dispose()}
    if(-not $refused -or (Test-Path -LiteralPath $tokenPath)){$failures.Add('concurrent-first-start: created token before owning lifecycle lock')}
    if(Test-Path -LiteralPath $tokenPath){Remove-Item -LiteralPath $tokenPath}
    Move-Item -LiteralPath $savedToken -Destination $tokenPath
} finally {
    $env:STUDIO_EMBEDDING_TOKEN=$oldToken
    $env:STUDIO_EMBEDDING_URL=$oldURL
    # Deliberately retain the small isolated fixture for failure diagnostics.
}
if($failures.Count){throw ($failures -join '; ')}
Write-Output 'PASS: eight isolated embedding stop/restart ownership contracts; no real services or models used.'

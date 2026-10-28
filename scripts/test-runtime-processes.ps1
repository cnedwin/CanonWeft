$ErrorActionPreference='Stop'
$helper=Join-Path $PSScriptRoot 'runtime-processes.ps1'
if(-not (Test-Path -LiteralPath $helper)){throw 'Runtime process ownership and graceful shutdown helpers are missing'}
. $helper
function Assert-RuntimeTest([bool]$Condition,[string]$Message){if(-not $Condition){throw $Message}}
foreach($scriptName in @('runtime.ps1','runtime-processes.ps1','test-runtime-processes.ps1')) {
    $tokens=$null;$parseErrors=$null
    [void][Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $scriptName),[ref]$tokens,[ref]$parseErrors)
    Assert-RuntimeTest ($parseErrors.Count -eq 0) ('PowerShell syntax invalid: '+$scriptName)
}
function Assert-RuntimeRefusal([scriptblock]$Action){
    $refused=$false
    try { & $Action | Out-Null } catch {
        $refused=$true
        if($_.Exception.Message -match 'secret-canary'){throw 'Runtime error exposed a secret'}
    }
    Assert-RuntimeTest $refused 'Unsafe runtime operation was accepted'
}
$exe='C:\Owned Runtime\mysql\bin\mysqld.exe'
$data='C:\Owned Runtime\repo\.runtime\mysql-data'
function New-TestMySQL($Id,$Path,$Command){[pscustomobject]@{ProcessId=$Id;ExecutablePath=$Path;CommandLine=$Command}}
$child=New-TestMySQL 20 $exe ('"'+$exe+'" --no-defaults --datadir="'+$data+'" --port=13306')
$monitor=New-TestMySQL 10 $exe $child.CommandLine
$listener=[pscustomobject]@{OwningProcess=20;LocalPort=13306;LocalAddress='127.0.0.1'}
$cases=@(
    @{Name='quoted datadir';Command=$child.CommandLine;Path=$exe;Expected=$true},
    @{Name='space separator';Command=('mysqld --datadir "'+$data+'"');Path=$exe;Expected=$true},
    @{Name='case insensitive path';Command=('mysqld --datadir="'+$data.ToUpperInvariant()+'"');Path=$exe.ToUpperInvariant();Expected=$true},
    @{Name='other executable';Command=$child.CommandLine;Path='C:\Other\mysqld.exe';Expected=$false},
    @{Name='other data';Command='mysqld --datadir=C:\Other\data';Path=$exe;Expected=$false},
    @{Name='relative data';Command='mysqld --datadir=.runtime\mysql-data';Path=$exe;Expected=$false},
    @{Name='missing command';Command=$null;Path=$exe;Expected=$false},
    @{Name='duplicate data';Command=($child.CommandLine+' --datadir=C:\Other');Path=$exe;Expected=$false},
    @{Name='option in quoted value';Command=('mysqld --comment="text --datadir='+$data+'"');Path=$exe;Expected=$false},
    @{Name='prefix path';Command=('mysqld --datadir="'+$data+'-other"');Path=$exe;Expected=$false}
)
foreach($case in $cases){
    $process=New-TestMySQL 21 $case.Path $case.Command
    $actual=Test-RuntimeProcessIdentity $process $exe '--datadir' $data
    Assert-RuntimeTest ($actual -eq $case.Expected) ('Identity case failed: '+$case.Name)
}
$state=Resolve-RuntimeMySQLState @($monitor,$child) @($listener) $exe $data 13306
Assert-RuntimeTest ($state.State -eq 'Ready' -and $state.ListenerId -eq 20 -and $state.Owned.Count -eq 2) 'Legacy monitor child was not identified'
$state=Resolve-RuntimeMySQLState @($child) @($listener) $exe $data 13306
Assert-RuntimeTest ($state.ListenerId -eq 20) 'Orphan child requires a live PID-file parent'
Assert-RuntimeRefusal { Resolve-RuntimeMySQLState @($child) @([pscustomobject]@{OwningProcess=99;LocalPort=13306;LocalAddress='127.0.0.1'}) $exe $data 13306 }
Assert-RuntimeRefusal { Resolve-RuntimeMySQLState @($child) @([pscustomobject]@{OwningProcess=20;LocalPort=13306;LocalAddress='0.0.0.0'}) $exe $data 13306 }
$state=Resolve-RuntimeMySQLState @() @() $exe $data 13306
Assert-RuntimeTest ($state.State -eq 'Stopped') 'Stopped state was not identified'
$state=Resolve-RuntimeMySQLState @($child) @() $exe $data 13306
Assert-RuntimeTest ($state.State -eq 'Starting') 'Unready process was treated as ready'
& {
    function Get-CimInstance {
        param($ClassName,$Namespace,$Filter,$OperationTimeoutSec,$ErrorAction)
        Assert-RuntimeTest ($OperationTimeoutSec -eq 3) 'CIM query has no bounded timeout'
        if($ClassName -eq 'Win32_Process'){return $child}
        Assert-RuntimeTest ($Namespace -eq 'root/StandardCimv2' -and $ClassName -eq 'MSFT_NetTCPConnection' -and $Filter -eq 'State=2 AND LocalPort=13306') 'Listener query is not bounded to the expected port'
        return $listener
    }
    $state=Get-RuntimeMySQLState $exe $data 13306
    Assert-RuntimeTest ($state.ListenerId -eq 20) 'CIM identity wrapper did not bind the listener'
    function Get-CimInstance {throw 'secret-canary'}
    Assert-RuntimeRefusal {Get-RuntimeMySQLState $exe $data 13306}
}

$previousPassword=$env:MYSQL_PWD
$env:MYSQL_PWD='parent-canary'
try {
    $info=New-RuntimeShutdownStartInfo 'C:\Owned Runtime\mysql\bin\mysqladmin.exe' 13306 'secret-canary'
    Assert-RuntimeTest ($info.Environment['MYSQL_PWD'] -eq 'secret-canary' -and $env:MYSQL_PWD -eq 'parent-canary') 'Password escaped child environment'
    Assert-RuntimeTest ($info.ArgumentList[0] -eq '--no-defaults' -and $info.ArgumentList.Contains('--no-login-paths') -and $info.ArgumentList.Contains('--protocol=TCP') -and $info.ArgumentList.Contains('--port=13306') -and $info.ArgumentList[$info.ArgumentList.Count-1] -eq 'shutdown') 'Shutdown command is not explicit'
    Assert-RuntimeTest (-not (($info.ArgumentList -join ' ') -match 'canary') -and -not $info.UseShellExecute -and $info.CreateNoWindow -and $info.RedirectStandardError -and $info.RedirectStandardOutput) 'Shutdown exposes credentials or output'
} finally {$env:MYSQL_PWD=$previousPassword}

# Exercise real ProcessStartInfo/start/wait/error cleanup using only short-lived
# PowerShell children. They never connect to a service or open runtime files.
function New-TestShutdownChild([string]$Command) {
    $info=[Diagnostics.ProcessStartInfo]::new()
    $info.FileName=[Environment]::ProcessPath
    $info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    foreach($argument in @('-NoLogo','-NoProfile','-NonInteractive','-Command',$Command)){[void]$info.ArgumentList.Add($argument)}
    $info.Environment['MYSQL_PWD']='secret-canary'
    return $info
}
$successInfo=New-TestShutdownChild 'exit 0'
Invoke-RuntimeShutdownProcess $successInfo -TimeoutMilliseconds 5000
Assert-RuntimeTest (-not $successInfo.Environment.ContainsKey('MYSQL_PWD')) 'Successful child retained password environment'
$failureInfo=New-TestShutdownChild '[Console]::Error.WriteLine("secret-canary");exit 9'
Assert-RuntimeRefusal {Invoke-RuntimeShutdownProcess $failureInfo -TimeoutMilliseconds 5000}
Assert-RuntimeTest (-not $failureInfo.Environment.ContainsKey('MYSQL_PWD')) 'Failed child retained password environment'
$timeoutInfo=New-TestShutdownChild 'Start-Sleep -Seconds 30'
$elapsed=[Diagnostics.Stopwatch]::StartNew()
Assert-RuntimeRefusal {Invoke-RuntimeShutdownProcess $timeoutInfo -TimeoutMilliseconds 100}
Assert-RuntimeTest ($elapsed.Elapsed.TotalSeconds -lt 5 -and -not $timeoutInfo.Environment.ContainsKey('MYSQL_PWD')) 'Administrative child timeout or cleanup failed'

# Replace only operating-system boundaries. The real orchestration remains in use.
$script:states=[Collections.Generic.Queue[object]]::new()
function Get-RuntimeMySQLState { if($script:states.Count -eq 0){throw 'Unexpected state query'}; $script:states.Dequeue() }
function Start-Sleep { param($Milliseconds) }
$script:shutdownCalls=0
function Invoke-RuntimeMySQLShutdown {param($Executable,$Port,$Password);$script:shutdownCalls++;Assert-RuntimeTest ($Password -eq 'secret-canary') 'Password not passed privately'}
$ready=[pscustomobject]@{State='Ready';ListenerId=20;Owned=@($child)}
$stopped=[pscustomobject]@{State='Stopped';ListenerId=0;Owned=@()}
$script:states.Enqueue($ready);$script:states.Enqueue($stopped)
Stop-RuntimeMySQL $exe $data 'C:\Owned Runtime\mysql\bin\mysqladmin.exe' 13306 'secret-canary' -Attempts 2
Assert-RuntimeTest ($script:shutdownCalls -eq 1 -and $script:states.Count -eq 0) 'Graceful stop did not wait for owned process exit'
$script:states.Enqueue($stopped)
Stop-RuntimeMySQL $exe $data 'C:\Owned Runtime\mysql\bin\mysqladmin.exe' 13306 'secret-canary' -Attempts 2
Assert-RuntimeTest ($script:shutdownCalls -eq 1) 'Stopped runtime issued shutdown'
$script:states.Enqueue($ready);$script:states.Enqueue($ready);$script:states.Enqueue($ready)
Assert-RuntimeRefusal { Stop-RuntimeMySQL $exe $data 'C:\Owned Runtime\mysql\bin\mysqladmin.exe' 13306 'secret-canary' -Attempts 2 }
$script:states.Clear()
$script:states.Enqueue($stopped)
Assert-RuntimeRefusal { Wait-RuntimeMySQL $exe $data 13306 -ExpectedProcessId 42 -Attempts 1 }
$script:states.Enqueue($ready)
Assert-RuntimeRefusal { Wait-RuntimeMySQL $exe $data 13306 -ExpectedProcessId 42 -Attempts 1 }
$script:states.Enqueue($ready)
$actual=Wait-RuntimeMySQL $exe $data 13306 -ExpectedProcessId 20 -Attempts 1
Assert-RuntimeTest ($actual.ListenerId -eq 20) 'Correct listener was not accepted'

$temporary=Join-Path ([IO.Path]::GetTempPath()) ('studio-process-test-'+[Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $script:launches=0
    function Start-Process {
        param($FilePath,$ArgumentList,$WindowStyle,[switch]$PassThru,$RedirectStandardOutput,$RedirectStandardError)
        $script:launches++
        Assert-RuntimeTest ($FilePath -eq $exe -and $ArgumentList -contains '--no-monitor' -and $WindowStyle -eq 'Hidden') 'MySQL launch did not disable the monitor'
        [pscustomobject]@{Id=20}
    }
    $pidFile=Join-Path $temporary 'mysql.pid'
    $script:states.Enqueue($ready);$script:states.Enqueue($ready)
    Start-RuntimeMySQL $exe $data 13306 @('--no-defaults') $pidFile (Join-Path $temporary 'out') (Join-Path $temporary 'err')
    Assert-RuntimeTest ($script:launches -eq 0 -and (Get-Content -LiteralPath $pidFile) -eq '20') 'Existing orphan was not adopted safely'
    Remove-Item -LiteralPath $pidFile
    $script:states.Enqueue($stopped);$script:states.Enqueue($stopped)
    Assert-RuntimeRefusal { Start-RuntimeMySQL $exe $data 13306 @('--no-defaults') $pidFile (Join-Path $temporary 'out') (Join-Path $temporary 'err') }
    Assert-RuntimeTest (-not (Test-Path -LiteralPath $pidFile)) 'Dead launch was persisted as a ready PID'
    $script:states.Enqueue($stopped);$script:states.Enqueue($ready)
    Start-RuntimeMySQL $exe $data 13306 @('--no-defaults') $pidFile (Join-Path $temporary 'out') (Join-Path $temporary 'err')
    Assert-RuntimeTest ((Get-Content -LiteralPath $pidFile) -eq '20') 'Ready listener was not persisted'
} finally {
    $full=[IO.Path]::GetFullPath($temporary)
    if(-not $full.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notmatch '^studio-process-test-[0-9a-f]{32}$'){throw 'Unexpected temporary cleanup path'}
    Remove-Item -LiteralPath $full -Recurse -Force
}
Write-Output 'Runtime process tests passed: 10 identity cases, ownership and readiness, private graceful shutdown, orphan discovery, timeout and wrong-launch refusal. No services were accessed.'

# Process ownership is independent of PID files and process names. Those are
# hints only; an executable path, explicit data/config path and listener agree.
function Split-RuntimeCommandLine {
    param([string]$CommandLine)
    if([string]::IsNullOrWhiteSpace($CommandLine) -or $CommandLine.Length -gt 32768){return}
    $tokens=[Collections.Generic.List[string]]::new()
    $token=[Text.StringBuilder]::new()
    $quoted=$false
    for($index=0;$index -lt $CommandLine.Length;$index++) {
        $character=$CommandLine[$index]
        if($character -eq '"') {
            # Runtime paths never require escaped quotes. Refuse ambiguous
            # Windows backslash/quote forms rather than guessing ownership.
            if($index -gt 0 -and $CommandLine[$index-1] -eq '\'){return}
            $quoted=-not $quoted
        } elseif([char]::IsWhiteSpace($character) -and -not $quoted) {
            if($token.Length -gt 0){$tokens.Add($token.ToString());[void]$token.Clear()}
        } else {[void]$token.Append($character)}
    }
    if($quoted){return}
    if($token.Length -gt 0){$tokens.Add($token.ToString())}
    $tokens.ToArray()
}

function Test-RuntimeProcessIdentity {
    param($Process,[string]$Executable,[string]$Option,[string]$ExpectedPath)
    try {
        if($null -eq $Process -or -not [IO.Path]::IsPathFullyQualified($Process.ExecutablePath) -or -not [IO.Path]::IsPathFullyQualified($Executable) -or -not [IO.Path]::IsPathFullyQualified($ExpectedPath)){return $false}
        if(-not [string]::Equals([IO.Path]::GetFullPath($Process.ExecutablePath),[IO.Path]::GetFullPath($Executable),[StringComparison]::OrdinalIgnoreCase)){return $false}
        $arguments=@(Split-RuntimeCommandLine $Process.CommandLine)
        $values=[Collections.Generic.List[string]]::new()
        for($index=1;$index -lt $arguments.Count;$index++) {
            if($arguments[$index] -ceq $Option) {
                if($index+1 -ge $arguments.Count){return $false}
                $index++;$values.Add($arguments[$index])
            } elseif($arguments[$index].StartsWith($Option+'=',[StringComparison]::Ordinal)) {
                $values.Add($arguments[$index].Substring($Option.Length+1))
            }
        }
        if($values.Count -ne 1 -or -not [IO.Path]::IsPathFullyQualified($values[0])){return $false}
        return [string]::Equals([IO.Path]::GetFullPath($values[0]).TrimEnd('\','/'),[IO.Path]::GetFullPath($ExpectedPath).TrimEnd('\','/'),[StringComparison]::OrdinalIgnoreCase)
    } catch {return $false}
}

function Resolve-RuntimeMySQLState {
    param([object[]]$Processes,[object[]]$Listeners,[string]$Executable,[string]$DataDirectory,[int]$Port)
    $owned=@($Processes | Where-Object {Test-RuntimeProcessIdentity $_ $Executable '--datadir' $DataDirectory})
    $listenersAtPort=@($Listeners | Where-Object {$_.LocalPort -eq $Port})
    $listenerIds=@($listenersAtPort | ForEach-Object {
        if($_.LocalAddress -ne '127.0.0.1' -or $_.OwningProcess -notin @($owned.ProcessId)){throw 'MySQL runtime listener ownership could not be verified'}
        [int]$_.OwningProcess
    } | Sort-Object -Unique)
    if($listenerIds.Count -gt 1){throw 'MySQL runtime listener ownership is ambiguous'}
    $state='Stopped';$listenerId=0
    if($owned.Count -gt 0){$state='Starting'}
    if($listenerIds.Count -eq 1){$state='Ready';$listenerId=$listenerIds[0]}
    [pscustomobject]@{State=$state;ListenerId=$listenerId;Owned=$owned}
}

function Get-RuntimeMySQLState {
    param([string]$Executable,[string]$DataDirectory,[int]$Port)
    try {
        $processes=@(Get-CimInstance Win32_Process -Filter "Name='mysqld.exe'" -OperationTimeoutSec 3 -ErrorAction Stop)
        # Query the same CIM class as Get-NetTCPConnection without depending
        # on that Windows PowerShell module being on the portable tool path.
        $listeners=@(Get-CimInstance -Namespace root/StandardCimv2 -ClassName MSFT_NetTCPConnection -Filter "State=2 AND LocalPort=$Port" -OperationTimeoutSec 3 -ErrorAction Stop)
        Resolve-RuntimeMySQLState $processes $listeners $Executable $DataDirectory $Port
    } catch {throw 'MySQL runtime process or listener identity is unavailable'}
}

function Wait-RuntimeMySQL {
    param([string]$Executable,[string]$DataDirectory,[int]$Port,[int]$ExpectedProcessId=0,[int]$Attempts=60)
    $elapsed=[Diagnostics.Stopwatch]::StartNew()
    for($attempt=0;$attempt -lt $Attempts;$attempt++) {
        if($elapsed.Elapsed.TotalSeconds -ge 30){break}
        $state=Get-RuntimeMySQLState $Executable $DataDirectory $Port
        if($state.State -eq 'Ready') {
            if($ExpectedProcessId -ne 0 -and $state.ListenerId -ne $ExpectedProcessId){throw 'MySQL listener does not belong to the launched process'}
            return $state
        }
        if($ExpectedProcessId -ne 0 -and $ExpectedProcessId -notin @($state.Owned.ProcessId)){throw 'Launched MySQL process exited before becoming ready'}
        Start-Sleep -Milliseconds 500
    }
    throw 'MySQL runtime did not become ready within the allowed time'
}

function Start-RuntimeMySQL {
    param([string]$Executable,[string]$DataDirectory,[int]$Port,[string[]]$Arguments,[string]$PIDFile,[string]$OutputFile,[string]$ErrorFile)
    $state=Get-RuntimeMySQLState $Executable $DataDirectory $Port
    $expected=0
    if($state.State -eq 'Stopped') {
        if($Arguments.Count -eq 0 -or $Arguments[0] -ne '--no-defaults'){throw 'MySQL runtime launch configuration is invalid'}
        try {
            $process=Start-Process -FilePath $Executable -ArgumentList ($Arguments+@('--no-monitor')) -WindowStyle Hidden -PassThru -RedirectStandardOutput $OutputFile -RedirectStandardError $ErrorFile
            $expected=$process.Id
        } catch {throw 'MySQL runtime could not be launched'}
    }
    $ready=Wait-RuntimeMySQL $Executable $DataDirectory $Port -ExpectedProcessId $expected
    # An old monitor or stale PID file is never the authority. Adopt only the
    # verified listener, and never publish a PID for a dead launch.
    $ready.ListenerId | Set-Content -LiteralPath $PIDFile
}

function New-RuntimeShutdownStartInfo {
    param([string]$Executable,[int]$Port,[string]$Password)
    if(-not [IO.Path]::IsPathFullyQualified($Executable) -or $Port -lt 1 -or $Port -gt 65535 -or [string]::IsNullOrEmpty($Password)){throw 'MySQL shutdown configuration is invalid'}
    $info=[Diagnostics.ProcessStartInfo]::new()
    $info.FileName=$Executable
    $info.UseShellExecute=$false
    $info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true
    $info.RedirectStandardError=$true
    foreach($argument in @('--no-defaults','--no-login-paths','--protocol=TCP','--host=127.0.0.1',"--port=$Port",'--user=root','--connect-timeout=5','shutdown')){[void]$info.ArgumentList.Add($argument)}
    # Never set the parent environment or place the password on a command line.
    $info.Environment['MYSQL_PWD']=$Password
    return $info
}

function Invoke-RuntimeShutdownProcess {
    param([Diagnostics.ProcessStartInfo]$StartInfo,[int]$TimeoutMilliseconds=30000)
    $process=[Diagnostics.Process]::new()
    try {
        $process.StartInfo=$StartInfo
        if(-not $process.Start()){throw 'Start failed'}
        $stdout=$process.StandardOutput.ReadToEndAsync()
        $stderr=$process.StandardError.ReadToEndAsync()
        if(-not $process.WaitForExit($TimeoutMilliseconds)) {
            # Only our short-lived administrative child is stopped on timeout.
            # The database is never force-killed by this helper.
            $process.Kill()
            throw 'Timeout'
        }
        if($process.ExitCode -ne 0){throw 'Shutdown rejected'}
    } catch {throw 'MySQL graceful shutdown failed; no database process was force-stopped'}
    finally {
        $StartInfo.Environment.Remove('MYSQL_PWD') | Out-Null
        $process.Dispose()
    }
}

function Invoke-RuntimeMySQLShutdown {
    param([string]$Executable,[int]$Port,[string]$Password)
    $info=New-RuntimeShutdownStartInfo $Executable $Port $Password
    Invoke-RuntimeShutdownProcess $info
}

function Stop-RuntimeMySQL {
    param([string]$Executable,[string]$DataDirectory,[string]$AdminExecutable,[int]$Port,[string]$Password,[int]$Attempts=60)
    $state=Get-RuntimeMySQLState $Executable $DataDirectory $Port
    if($state.State -eq 'Stopped'){return}
    if($state.State -ne 'Ready'){throw 'Owned MySQL is not ready for graceful shutdown; no process was stopped'}
    Invoke-RuntimeMySQLShutdown $AdminExecutable $Port $Password
    $elapsed=[Diagnostics.Stopwatch]::StartNew()
    for($attempt=0;$attempt -lt $Attempts;$attempt++) {
        if($elapsed.Elapsed.TotalSeconds -ge 30){break}
        $state=Get-RuntimeMySQLState $Executable $DataDirectory $Port
        if($state.State -eq 'Stopped'){return}
        Start-Sleep -Milliseconds 500
    }
    throw 'MySQL graceful shutdown did not finish; no database process was force-stopped'
}

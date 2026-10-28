param([ValidateSet('start','stop','env','status')][string]$EmbeddingAction='start')
$ErrorActionPreference='Stop'
$embeddingRepo=Split-Path $PSScriptRoot -Parent
$embeddingBase=Split-Path $embeddingRepo -Parent
$embeddingRuntime=Join-Path $embeddingRepo '.runtime'
. (Join-Path $PSScriptRoot 'python-worker-tools.ps1')
$embeddingPython=Join-Path (Get-PythonWorkerEnvironment embedding) 'Scripts/python.exe'
$embeddingSecretFile=Join-Path $embeddingRuntime 'embedding-token.dpapi'
$embeddingPidFile=Join-Path $embeddingRuntime 'embedding.pid'
# Serialize supported start/stop operations so a fresh worker cannot publish
# a new PID record between stop verification and removal of the old marker.
$embeddingLifecycleGuard=$null
if($EmbeddingAction -in @('start','stop')){
    $embeddingLifecycleGuard=[IO.File]::Open((Join-Path $embeddingRuntime 'embedding-control.lock'),[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
}
try {
    if(-not (Test-Path -LiteralPath $embeddingSecretFile)){
        if($EmbeddingAction -ne 'start'){throw 'Embedding runtime is not initialized'}
        $embeddingSecret=[Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
        ConvertTo-SecureString $embeddingSecret -AsPlainText -Force | ConvertFrom-SecureString | Set-Content -LiteralPath $embeddingSecretFile
    }
    $embeddingSecure=ConvertTo-SecureString (Get-Content -LiteralPath $embeddingSecretFile -Raw).Trim()
    $env:STUDIO_EMBEDDING_TOKEN=[System.Net.NetworkCredential]::new('',$embeddingSecure).Password
    $env:STUDIO_EMBEDDING_URL='http://127.0.0.1:18766'
    if($EmbeddingAction -eq 'env'){return}
    $embeddingPidSnapshot=if(Test-Path -LiteralPath $embeddingPidFile){Get-Content -LiteralPath $embeddingPidFile -Raw}else{$null}
    $embeddingProcess=if($null -ne $embeddingPidSnapshot){Get-Process -Id ([int]$embeddingPidSnapshot) -ErrorAction SilentlyContinue}else{$null}
    if($embeddingProcess -and $embeddingProcess.Path -ne $embeddingPython){throw 'Embedding process identity mismatch'}
    if($EmbeddingAction -eq 'stop'){
        if ($embeddingProcess) {
            # A Windows venv launcher has a second Python process. Stopping only the
            # launcher leaves the listener and old interpreter alive during upgrades.
            $embeddingAll = @(Get-CimInstance Win32_Process)
            $embeddingTree = @($embeddingAll | Where-Object { $_.ProcessId -eq $embeddingProcess.Id })
            if ($embeddingTree.Count -ne 1) { throw 'Cannot verify embedding process tree' }
            $embeddingIDs = [Collections.Generic.HashSet[uint32]]::new()
            [void]$embeddingIDs.Add([uint32]$embeddingProcess.Id)
            $embeddingBaseConfig = Join-Path (Split-Path (Split-Path $embeddingPython -Parent) -Parent) 'pyvenv.cfg'
            $embeddingBaseLine = @(Get-Content -LiteralPath $embeddingBaseConfig | Where-Object { $_ -like 'home = *' })
            if ($embeddingBaseLine.Count -ne 1) { throw 'Cannot verify embedding base interpreter' }
            $embeddingBaseExe = Join-Path $embeddingBaseLine[0].Substring(7) 'python.exe'
            for ($embeddingTreeIndex = 0; $embeddingTreeIndex -lt $embeddingTree.Count; $embeddingTreeIndex++) {
                $embeddingParent = $embeddingTree[$embeddingTreeIndex]
                foreach ($embeddingChild in $embeddingAll | Where-Object { $_.ParentProcessId -eq $embeddingParent.ProcessId }) {
                    if ($embeddingChild.CreationDate -lt $embeddingParent.CreationDate) { continue }
                    if (-not $embeddingIDs.Add($embeddingChild.ProcessId)) { throw 'Invalid embedding process tree' }
                    if ($embeddingChild.ExecutablePath -notin @($embeddingPython, $embeddingBaseExe, (Join-Path $env:SystemRoot 'System32/conhost.exe'))) {
                        throw 'Unexpected embedding child process; nothing stopped'
                    }
                    $embeddingTree += $embeddingChild
                }
            }
            $embeddingPinned = @()
            try {
                # Validate the entire snapshot before stopping anything. SafeHandle
                # pins each process identity; Kill must use this same Process object.
                foreach ($embeddingEntry in $embeddingTree) {
                    $embeddingStop = Get-Process -Id $embeddingEntry.ProcessId -ErrorAction SilentlyContinue
                    if (-not $embeddingStop) { continue }
                    $embeddingPinned += $embeddingStop
                    try {
                        $embeddingHandle = $embeddingStop.SafeHandle
                        if ($embeddingHandle.IsInvalid -or $embeddingHandle.IsClosed) { throw 'Invalid embedding process identity handle' }
                        if ($embeddingStop.HasExited) { continue }
                        # Win32_Process.CreationDate truncates the native 100ns time
                        # to microseconds. Compare those exact integers, not a time
                        # tolerance or floating-point conversion that loses ticks.
                        $embeddingStartTicks = $embeddingStop.StartTime.ToUniversalTime().Ticks
                        $embeddingStartMicroseconds = $embeddingStartTicks - ($embeddingStartTicks % 10)
                        if ($embeddingStop.Path -ne $embeddingEntry.ExecutablePath -or
                            $embeddingStartMicroseconds -ne $embeddingEntry.CreationDate.ToUniversalTime().Ticks) {
                            throw 'Embedding process identity changed; nothing stopped'
                        }
                    } catch {
                        if (-not $embeddingStop.HasExited) { throw }
                    }
                }
                for ($embeddingTreeIndex = $embeddingPinned.Count - 1; $embeddingTreeIndex -ge 0; $embeddingTreeIndex--) {
                    $embeddingStop = $embeddingPinned[$embeddingTreeIndex]
                    if ($embeddingStop.HasExited) { continue }
                    try { $embeddingStop.Kill() } catch {
                        # The launcher/console may naturally exit after its child.
                        # Check the pinned handle, never reacquire a possibly reused PID.
                        if (-not $embeddingStop.HasExited) { throw }
                    }
                    if (-not $embeddingStop.WaitForExit(10000)) { throw 'Embedding process did not stop' }
                }
            } finally {
                foreach ($embeddingStop in $embeddingPinned) { $embeddingStop.Dispose() }
            }
        }
        # A stopped launcher can leave an orphan listener; keep its marker until
        # the actual service is gone. A newer marker must never be cleared here.
        $embeddingListeners=@(Get-CimInstance -Namespace root/StandardCimv2 -ClassName MSFT_NetTCPConnection -Filter 'State=2 AND LocalPort=18766' -OperationTimeoutSec 3 -ErrorAction Stop)
        if($embeddingListeners.Count){throw 'Embedding listener still active; PID record retained'}
        if($null -ne $embeddingPidSnapshot -and (Test-Path -LiteralPath $embeddingPidFile)){
            if((Get-Content -LiteralPath $embeddingPidFile -Raw) -ceq $embeddingPidSnapshot){
                Remove-Item -LiteralPath $embeddingPidFile -ErrorAction Stop
            }
        }
        return
    }
    if($EmbeddingAction -eq 'start' -and -not $embeddingProcess){
        if(-not (Test-Path -LiteralPath $embeddingPython)){throw 'Run install-embedding.ps1 before starting local embeddings'}
        $embeddingArgs=@('-I','-B',(Join-Path $embeddingRepo 'tools/embedding/worker.py'),'--model',(Join-Path $embeddingBase 'work/models/bge-m3-5617a9f'),'--manifest',(Join-Path $embeddingRepo 'deploy/locks/bge-m3.json')) | ForEach-Object {'"'+$_+'"'}
        # Inference receives no database or provider environment credentials.
        $embeddingRemoved=@{}
        Get-ChildItem Env: | Where-Object { $_.Name -like 'STUDIO_*' -and $_.Name -notlike 'STUDIO_EMBEDDING_*' } | ForEach-Object { $embeddingRemoved[$_.Name]=$_.Value; [Environment]::SetEnvironmentVariable($_.Name,$null,'Process') }
        try {
            $embeddingProcess=Start-Process -FilePath $embeddingPython -ArgumentList $embeddingArgs -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $embeddingRuntime 'embedding.log') -RedirectStandardError (Join-Path $embeddingRuntime 'embedding-error.log')
        } finally { foreach($embeddingKey in $embeddingRemoved.Keys){[Environment]::SetEnvironmentVariable($embeddingKey,$embeddingRemoved[$embeddingKey],'Process')} }
        $embeddingProcess.Id|Set-Content -LiteralPath $embeddingPidFile
    }
    for($embeddingAttempt=0;$embeddingAttempt -lt 60;$embeddingAttempt++){
        if($embeddingProcess -and $embeddingProcess.HasExited){throw 'Embedding worker exited; inspect local error log'}
        try{
            $embeddingHealth=Invoke-RestMethod "$env:STUDIO_EMBEDDING_URL/health" -Headers @{Authorization="Bearer $env:STUDIO_EMBEDDING_TOKEN"} -TimeoutSec 2
            if($embeddingHealth.revision -eq '5617a9f61b028005a4858fdac845db406aefb181' -and $embeddingHealth.dimension -eq 1024){Write-Output 'Verified local BGE-M3 ready';return}
        }catch{Start-Sleep -Milliseconds 250}
    }
    throw 'Local embedding health check failed'
} finally {
    if($embeddingLifecycleGuard){$embeddingLifecycleGuard.Dispose()}
}

function Assert-AuthorityServicesStopped {
    param([string]$ProjectRoot, [string]$Business, [string]$ConfiguredAddress)
    $ErrorActionPreference='Stop'
    if ($Business -notin @('studio','studio_test')) { throw 'Unknown authority source; stopped services cannot be verified.' }
    $resolvedRoot=[IO.Path]::GetFullPath($ProjectRoot)
    $managedBinaries=@(
        [IO.Path]::GetFullPath((Join-Path $resolvedRoot 'backend/bin/studio.exe')),
        [IO.Path]::GetFullPath((Join-Path $resolvedRoot 'backend/bin/studio-prod.exe'))
    )
    $addresses=@()
    if (-not [string]::IsNullOrWhiteSpace($ConfiguredAddress)) { $addresses += $ConfiguredAddress }
    $addresses += @('127.0.0.1:18765','127.0.0.1:18865','[::1]:18765','[::1]:18865')
    foreach ($address in ($addresses | Select-Object -Unique)) {
        $endpoint=$null
        if (-not [Net.IPEndPoint]::TryParse($address,[ref]$endpoint) -or -not [Net.IPAddress]::IsLoopback($endpoint.Address) -or $endpoint.Port -lt 1) {
            throw 'Studio address is not an explicit valid loopback endpoint; stopped services cannot be verified.'
        }
        $socket=[Net.Sockets.TcpClient]::new($endpoint.AddressFamily)
        try {
            $socket.Connect($endpoint.Address,$endpoint.Port)
            throw "Studio endpoint $address is listening; stop source services before authority initialization."
        } catch [Net.Sockets.SocketException] {
            if ($_.Exception.SocketErrorCode -ne [Net.Sockets.SocketError]::ConnectionRefused) { throw }
        } finally { $socket.Dispose() }
    }
    # Workers share the studio process and may run before HTTP starts listening.
    # Inspection failure is not proof of stopped services. Never terminate here.
    $processes=@(Get-CimInstance Win32_Process -ErrorAction Stop)
    foreach ($pidName in @('studio-test.pid','studio-prod.pid','studio.pid')) {
        $pidFile=Join-Path $resolvedRoot ".runtime/$pidName"
        if (Test-Path -LiteralPath $pidFile) {
            $managedProcessId=0
            if (-not [int]::TryParse((Get-Content -LiteralPath $pidFile -Raw).Trim(),[ref]$managedProcessId) -or $managedProcessId -le 0) {
                throw 'Invalid managed Studio PID; stopped services cannot be verified.'
            }
            if ($processes | Where-Object { $_.ProcessId -eq $managedProcessId }) {
                throw 'A managed Studio PID is still active or has been reused; stop and reconcile it before authority initialization.'
            }
        }
    }
    foreach ($process in $processes) {
        if ($process.ExecutablePath -and $managedBinaries -contains [IO.Path]::GetFullPath($process.ExecutablePath)) {
            throw 'A managed Studio API/worker process is active; stop it before authority initialization.'
        }
    }
    foreach ($process in $processes) {
        if ($process.Name -in @('studio.exe','studio-prod.exe') -or ($process.Name -eq 'go.exe' -and $process.CommandLine -match '\brun\b.*cmd[\\/]studio')) {
            # An unmanaged/older binary may use a custom port or have no listener.
            # Its source DB cannot be established from public process metadata.
            throw 'An unverified Studio process is active; stopped source services cannot be established. No process was stopped.'
        }
    }

}

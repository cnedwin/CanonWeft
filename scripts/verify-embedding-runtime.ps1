$ErrorActionPreference = 'Stop'
$embeddingVerificationRepo = Split-Path $PSScriptRoot -Parent
& (Join-Path $PSScriptRoot 'embedding-runtime.ps1') start
$embeddingVerificationPID = [int](Get-Content -LiteralPath (Join-Path $embeddingVerificationRepo '.runtime/embedding.pid'))
$embeddingVerificationChildren = @(Get-CimInstance Win32_Process | Where-Object { $_.ParentProcessId -eq $embeddingVerificationPID })
& (Join-Path $PSScriptRoot 'embedding-runtime.ps1') stop
foreach ($entry in $embeddingVerificationChildren) {
    $remaining = Get-CimInstance Win32_Process -Filter "ProcessId = $($entry.ProcessId)"
    if ($remaining -and $remaining.CreationDate -eq $entry.CreationDate) {
        throw 'Embedding child survived controlled stop'
    }
}
$embeddingVerificationSocket = [Net.Sockets.TcpClient]::new()
try {
    $embeddingVerificationSocket.Connect('127.0.0.1', 18766)
    throw 'Embedding listener survived controlled stop'
} catch [Net.Sockets.SocketException] {
    Write-Output 'Embedding launcher, child process and listener stopped successfully'
} finally { $embeddingVerificationSocket.Dispose() }
& (Join-Path $PSScriptRoot 'embedding-runtime.ps1') start
Write-Output 'Real BGE service restarted successfully'

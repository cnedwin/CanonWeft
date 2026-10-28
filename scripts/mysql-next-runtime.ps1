# Independent next-version qualification instance. Never imports existing data.
[CmdletBinding()]
param([ValidateSet('start', 'stop', 'status', 'env')][string]$Action = 'status')

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'The isolated runtime requires Windows and current-user DPAPI.' }
$nextRepo = Split-Path $PSScriptRoot -Parent
$nextBase = Split-Path $nextRepo -Parent
$nextLock = Get-Content (Join-Path $nextRepo 'deploy/locks/mysql-next.json') -Raw | ConvertFrom-Json
if ($nextLock.version -ne '26.10.0' -or $nextLock.port -ne 13316 -or $nextLock.database -ne 'studio_next_test') {
    throw 'Unexpected isolated MySQL identity; review this script before changing the target.'
}
$nextRoot = Join-Path $nextBase 'work/runtime/mysql/26.10.0'
$nextPackage = Join-Path $nextRoot 'mysql-26.10.0-winx64'
$nextBin = Join-Path $nextPackage 'bin'
$nextInstance = Join-Path $nextRoot 'isolated-instance'
$nextData = Join-Path $nextInstance 'data'
$nextCredentialFile = Join-Path $nextInstance 'credentials.dpapi.json'
$nextReadyFile = Join-Path $nextInstance 'bootstrap.ready'
$nextInitFile = Join-Path $nextInstance 'bootstrap.sql'
$nextServer = Join-Path $nextBin 'mysqld.exe'
$nextPort = [int]$nextLock.port
. (Join-Path $PSScriptRoot 'runtime-processes.ps1')

function Set-NextPrivateDirectory([string]$Directory) {
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    # Change the DACL only. Set-Acl can request SACL/owner privileges absent
    # from a normal desktop token; this does not require elevated ownership.
    & icacls.exe $Directory /grant:r "*${sid}:(OI)(CI)F" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot grant current-user access to the isolated directory.' }
    & icacls.exe $Directory /inheritance:r | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot restrict isolated directory inheritance.' }
    $otherPrincipals = @((Get-Acl -LiteralPath $Directory).Access | ForEach-Object {
        $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
    } | Where-Object { $_ -ne $sid } | Sort-Object -Unique)
    foreach ($other in $otherPrincipals) {
        & icacls.exe $Directory /remove:g "*$other" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Cannot remove unrelated isolated-directory access.' }
    }
}

function New-NextProtectedSecret {
    $plain = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    try { ConvertTo-SecureString $plain -AsPlainText -Force | ConvertFrom-SecureString }
    finally { $plain = $null }
}

function Reveal-NextSecret([string]$Protected) {
    $secure = ConvertTo-SecureString $Protected
    [Net.NetworkCredential]::new('', $secure).Password
}

function Invoke-NextSQL([string]$Sql, [string]$Password) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = Join-Path $nextBin 'mysql.exe'
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in @('--no-defaults', '--no-login-paths', '--protocol=TCP', '--host=127.0.0.1', "--port=$nextPort", '--user=root', '--connect-timeout=5', '--batch', '--raw', '--skip-column-names', '--default-character-set=utf8mb4')) {
        [void]$info.ArgumentList.Add($argument)
    }
    $info.Environment['MYSQL_PWD'] = $Password
    $process = [Diagnostics.Process]::new()
    try {
        $process.StartInfo = $info
        if (-not $process.Start()) { throw 'Client launch failed.' }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.WriteLine($Sql)
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(30000)) {
            $process.Kill()
            throw 'Isolated MySQL client timed out.'
        }
        if ($process.ExitCode -ne 0) { throw 'Isolated MySQL query failed; no SQL or credential was printed.' }
        return $stdout.GetAwaiter().GetResult().Trim()
    } finally {
        [void]$info.Environment.Remove('MYSQL_PWD')
        $process.Dispose()
    }
}

if (-not (Test-Path -LiteralPath $nextServer)) { throw 'Download and verify the separate next-version package first.' }
foreach ($name in @('mysqld.exe', 'mysql.exe', 'mysqladmin.exe')) {
    $signature = Get-AuthenticodeSignature (Join-Path $nextBin $name)
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $nextLock.mysqldAuthenticode.thumbprint) {
        throw 'The next-version executable signature differs from the reviewed Oracle signer.'
    }
}
if ($Action -eq 'start') {
    # Probe DPAPI before changing directory ACLs or creating credential state.
    $protectedProbe = New-NextProtectedSecret
    $protectedProbe = $null
    $archive = Join-Path $nextRoot $nextLock.archive
    if ((Get-FileHash $archive -Algorithm MD5).Hash.ToLowerInvariant() -ne $nextLock.officialMd5 -or
        (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $nextLock.observedSha256 -or
        (Get-Item $archive).Length -ne $nextLock.bytes) { throw 'Next-version archive integrity mismatch.' }
    New-Item -ItemType Directory -Force $nextInstance | Out-Null
    Set-NextPrivateDirectory $nextInstance
    if (-not (Test-Path -LiteralPath $nextCredentialFile)) {
        if (Test-Path -LiteralPath $nextData) { throw 'Existing data without credentials; automatic initialization refused.' }
        @{ root = (New-NextProtectedSecret); test = (New-NextProtectedSecret) } |
            ConvertTo-Json | Set-Content -LiteralPath $nextCredentialFile -Encoding utf8NoBOM
    }
}
if (-not (Test-Path -LiteralPath $nextCredentialFile)) { throw 'Isolated MySQL has not been initialized.' }
$nextCredentials = Get-Content -LiteralPath $nextCredentialFile -Raw | ConvertFrom-Json
if ($Action -eq 'env') {
    $nextPassword = Reveal-NextSecret $nextCredentials.test
    $env:MYSQL_NEXT_TEST_DSN = "studio_next:$nextPassword@tcp(127.0.0.1:$nextPort)/studio_next_test?parseTime=true&charset=utf8mb4&loc=UTC&time_zone=%27%2B00%3A00%27"
    $nextPassword = $null
    return
}
$nextRootPassword = Reveal-NextSecret $nextCredentials.root
try {
    if ($Action -eq 'stop') {
        Stop-RuntimeMySQL $nextServer $nextData (Join-Path $nextBin 'mysqladmin.exe') $nextPort $nextRootPassword
        Write-Output 'Independent MySQL 26.10.0 stopped gracefully.'
        return
    }
    if ($Action -eq 'start') {
        # Identity and port ownership are checked before any initialization.
        $state = Get-RuntimeMySQLState $nextServer $nextData $nextPort
        if (-not (Test-Path -LiteralPath $nextData)) {
            if ($state.State -ne 'Stopped') { throw 'Unexpected isolated MySQL process before initialization.' }
            New-Item -ItemType Directory $nextData | Out-Null
            & $nextServer --no-defaults --initialize-insecure "--basedir=$nextPackage" "--datadir=$nextData" --console *> (Join-Path $nextInstance 'initialize.log')
            if ($LASTEXITCODE -ne 0) { throw 'Isolated initialization failed; data retained for inspection.' }
        }
        $serverArguments = @('--no-defaults', "--basedir=`"$nextPackage`"", "--datadir=`"$nextData`"", "--port=$nextPort", '--bind-address=127.0.0.1', '--mysqlx=OFF', '--local-infile=OFF', '--default-time-zone=+00:00', '--server-id=261000', "--log-bin=`"$(Join-Path $nextData 'mysql-bin')`"", '--binlog-row-metadata=FULL', '--binlog-row-image=FULL', '--console')
        if (-not (Test-Path -LiteralPath $nextReadyFile)) {
            if ($state.State -ne 'Stopped') { throw 'Bootstrap state is uncertain; inspect before restarting.' }
            $nextTestPassword = Reveal-NextSecret $nextCredentials.test
            try {
                @"
ALTER USER 'root'@'localhost' IDENTIFIED BY '$nextRootPassword';
CREATE DATABASE IF NOT EXISTS studio_next_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'studio_next'@'localhost' IDENTIFIED BY '$nextTestPassword';
GRANT ALL ON studio_next_test.* TO 'studio_next'@'localhost';
"@ | Set-Content -LiteralPath $nextInitFile -Encoding utf8NoBOM
            } finally { $nextTestPassword = $null }
            $serverArguments += "--init-file=`"$nextInitFile`""
        }
        try {
            Start-RuntimeMySQL $nextServer $nextData $nextPort $serverArguments (Join-Path $nextInstance 'mysql.pid') (Join-Path $nextInstance 'stdout.log') (Join-Path $nextInstance 'stderr.log')
            $bootstrap = Invoke-NextSQL "SELECT VERSION(), @@port, @@datadir, @@bind_address;" $nextRootPassword
            if (-not $bootstrap.StartsWith("26.10.0`t13316`t")) { throw 'Unexpected server identity after start.' }
            '26.10.0 isolated initialization complete' | Set-Content -LiteralPath $nextReadyFile -Encoding utf8NoBOM
        } finally {
            # Remove only this known transient bootstrap secret, never any data directory.
            if (Test-Path -LiteralPath $nextInitFile) { Remove-Item -LiteralPath $nextInitFile }
        }
    }
    $state = Get-RuntimeMySQLState $nextServer $nextData $nextPort
    if ($state.State -ne 'Ready') { throw 'Independent MySQL instance is not ready.' }
    $status = Invoke-NextSQL "SELECT VERSION(),@@port,@@bind_address,@@server_id,@@datadir; SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME='studio_next_test'; SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='studio_next_test'; SELECT @@binlog_row_metadata,@@binlog_row_image,@@character_set_server,@@collation_server,@@time_zone;" $nextRootPassword
    Write-Output $status
} finally { $nextRootPassword = $null }

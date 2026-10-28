# Independent LTS qualification instance. Never imports existing data.
[CmdletBinding()]
param([ValidateSet('start', 'stop', 'status', 'env')][string]$Action = 'status')

$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'The isolated runtime requires Windows and current-user DPAPI.' }
$ltsRepo = Split-Path $PSScriptRoot -Parent
$ltsBase = Split-Path $ltsRepo -Parent
$ltsLock = Get-Content (Join-Path $ltsRepo 'deploy/locks/mysql-lts.json') -Raw | ConvertFrom-Json
if ($ltsLock.version -ne '9.7.4' -or $ltsLock.port -ne 13317 -or $ltsLock.serverId -ne 90704 -or $ltsLock.database -ne 'studio_lts_test') {
    throw 'Unexpected isolated MySQL identity; review this script before changing the target.'
}
$ltsRoot = Join-Path $ltsBase 'work/runtime/mysql/9.7.4'
$ltsPackage = Join-Path $ltsRoot 'mysql-9.7.4-winx64'
$ltsBin = Join-Path $ltsPackage 'bin'
$ltsInstance = Join-Path $ltsRoot 'isolated-instance'
$ltsData = Join-Path $ltsInstance 'data'
$ltsCredentialFile = Join-Path $ltsInstance 'credentials.dpapi.json'
$ltsReadyFile = Join-Path $ltsInstance 'bootstrap.ready'
$ltsInitFile = Join-Path $ltsInstance 'bootstrap.sql'
$ltsServer = Join-Path $ltsBin 'mysqld.exe'
$ltsPort = [int]$ltsLock.port
. (Join-Path $PSScriptRoot 'runtime-processes.ps1')

function Set-LtsPrivateDirectory([string]$Directory) {
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

function New-LtsProtectedSecret {
    $plain = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    try { ConvertTo-SecureString $plain -AsPlainText -Force | ConvertFrom-SecureString }
    finally { $plain = $null }
}

function Reveal-LtsSecret([string]$Protected) {
    $secure = ConvertTo-SecureString $Protected
    [Net.NetworkCredential]::new('', $secure).Password
}

function Invoke-LtsSQL([string]$Sql, [string]$Password) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = Join-Path $ltsBin 'mysql.exe'
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in @('--no-defaults', '--no-login-paths', '--protocol=TCP', '--host=127.0.0.1', "--port=$ltsPort", '--user=root', '--connect-timeout=5', '--batch', '--raw', '--skip-column-names', '--default-character-set=utf8mb4')) {
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

if (-not (Test-Path -LiteralPath $ltsServer)) { throw 'Download and verify the separate LTS package first.' }
foreach ($name in @('mysqld.exe', 'mysql.exe', 'mysqladmin.exe')) {
    $signature = Get-AuthenticodeSignature (Join-Path $ltsBin $name)
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -ne $ltsLock.mysqldAuthenticode.thumbprint) {
        throw 'The LTS executable signature differs from the reviewed Oracle signer.'
    }
}
$ltsBinaryVersion = & $ltsServer --no-defaults --version
if ($LASTEXITCODE -ne 0 -or $ltsBinaryVersion -notmatch ' Ver 9\.7\.4 for Win64 .*\(MySQL Community Server - GPL\)$') {
    throw 'The executable is not the pinned MySQL 9.7.4 Community binary.'
}
if ($Action -eq 'start') {
    # Probe DPAPI before changing directory ACLs or creating credential state.
    $protectedProbe = New-LtsProtectedSecret
    $protectedProbe = $null
    $archive = Join-Path $ltsRoot $ltsLock.archive
    if ((Get-FileHash $archive -Algorithm MD5).Hash.ToLowerInvariant() -ne $ltsLock.officialMd5 -or
        (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $ltsLock.observedSha256 -or
        (Get-Item $archive).Length -ne $ltsLock.bytes) { throw 'Lts-version archive integrity mismatch.' }
    New-Item -ItemType Directory -Force $ltsInstance | Out-Null
    Set-LtsPrivateDirectory $ltsInstance
    if (-not (Test-Path -LiteralPath $ltsCredentialFile)) {
        if (Test-Path -LiteralPath $ltsData) { throw 'Existing data without credentials; automatic initialization refused.' }
        @{ root = (New-LtsProtectedSecret); test = (New-LtsProtectedSecret) } |
            ConvertTo-Json | Set-Content -LiteralPath $ltsCredentialFile -Encoding utf8NoBOM
    }
}
if (-not (Test-Path -LiteralPath $ltsCredentialFile)) { throw 'Isolated MySQL has not been initialized.' }
$ltsCredentials = Get-Content -LiteralPath $ltsCredentialFile -Raw | ConvertFrom-Json
if ($Action -eq 'env') {
    $ltsPassword = Reveal-LtsSecret $ltsCredentials.test
    $env:MYSQL_LTS_TEST_DSN = "studio_lts:$ltsPassword@tcp(127.0.0.1:$ltsPort)/studio_lts_test?parseTime=true&charset=utf8mb4&loc=UTC&time_zone=%27%2B00%3A00%27"
    $ltsPassword = $null
    return
}
$ltsRootPassword = Reveal-LtsSecret $ltsCredentials.root
try {
    if ($Action -eq 'stop') {
        Stop-RuntimeMySQL $ltsServer $ltsData (Join-Path $ltsBin 'mysqladmin.exe') $ltsPort $ltsRootPassword
        Write-Output 'Independent MySQL 9.7.4 stopped gracefully.'
        return
    }
    if ($Action -eq 'start') {
        # Identity and port ownership are checked before any initialization.
        $state = Get-RuntimeMySQLState $ltsServer $ltsData $ltsPort
        if (-not (Test-Path -LiteralPath $ltsData)) {
            if ($state.State -ne 'Stopped') { throw 'Unexpected isolated MySQL process before initialization.' }
            New-Item -ItemType Directory $ltsData | Out-Null
            & $ltsServer --no-defaults --initialize-insecure "--basedir=$ltsPackage" "--datadir=$ltsData" --console *> (Join-Path $ltsInstance 'initialize.log')
            if ($LASTEXITCODE -ne 0) { throw 'Isolated initialization failed; data retained for inspection.' }
        }
        $serverArguments = @('--no-defaults', "--basedir=`"$ltsPackage`"", "--datadir=`"$ltsData`"", "--port=$ltsPort", '--bind-address=127.0.0.1', '--mysqlx=OFF', '--local-infile=OFF', '--default-time-zone=+00:00', '--server-id=90704', "--log-bin=`"$(Join-Path $ltsData 'mysql-bin')`"", '--binlog-row-metadata=FULL', '--binlog-row-image=FULL', '--console')
        if (-not (Test-Path -LiteralPath $ltsReadyFile)) {
            if ($state.State -ne 'Stopped') { throw 'Bootstrap state is uncertain; inspect before restarting.' }
            $ltsTestPassword = Reveal-LtsSecret $ltsCredentials.test
            try {
                @"
ALTER USER 'root'@'localhost' IDENTIFIED BY '$ltsRootPassword';
CREATE DATABASE IF NOT EXISTS studio_lts_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS 'studio_lts'@'localhost' IDENTIFIED BY '$ltsTestPassword';
GRANT ALL ON studio_lts_test.* TO 'studio_lts'@'localhost';
"@ | Set-Content -LiteralPath $ltsInitFile -Encoding utf8NoBOM
            } finally { $ltsTestPassword = $null }
            $serverArguments += "--init-file=`"$ltsInitFile`""
        }
        try {
            Start-RuntimeMySQL $ltsServer $ltsData $ltsPort $serverArguments (Join-Path $ltsInstance 'mysql.pid') (Join-Path $ltsInstance 'stdout.log') (Join-Path $ltsInstance 'stderr.log')
            $bootstrap = Invoke-LtsSQL "SELECT VERSION(), @@port, @@datadir, @@bind_address;" $ltsRootPassword
            if (-not $bootstrap.StartsWith("9.7.4`t13317`t")) { throw 'Unexpected server identity after start.' }
            '9.7.4 isolated initialization complete' | Set-Content -LiteralPath $ltsReadyFile -Encoding utf8NoBOM
        } finally {
            # Remove only this known transient bootstrap secret, never any data directory.
            if (Test-Path -LiteralPath $ltsInitFile) { Remove-Item -LiteralPath $ltsInitFile }
        }
    }
    $state = Get-RuntimeMySQLState $ltsServer $ltsData $ltsPort
    if ($state.State -ne 'Ready') { throw 'Independent MySQL instance is not ready.' }
    $status = Invoke-LtsSQL "SELECT VERSION(),@@port,@@bind_address,@@server_id,@@datadir; SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME='studio_lts_test'; SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='studio_lts_test'; SELECT @@binlog_row_metadata,@@binlog_row_image,@@character_set_server,@@collation_server,@@time_zone;" $ltsRootPassword
    if (-not $status.StartsWith("9.7.4`t13317`t127.0.0.1`t90704`t")) { throw 'Unexpected LTS server identity.' }
    Write-Output $status
} finally { $ltsRootPassword = $null }


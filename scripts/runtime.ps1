param([ValidateSet('start','stop','env','status')][string]$Action='start',
      [ValidateSet('','studio','studio_test')][string]$LogSetupDatabase='')
$ErrorActionPreference='Stop'
if($LogSetupDatabase -ne '' -and $Action -ne 'env'){throw 'Log setup exclusion is only available while loading setup environment.'}
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$runtimeTools = Enable-PortableToolchain
$repo=Split-Path $PSScriptRoot -Parent
$base=Split-Path $repo -Parent
$runtime=Join-Path $repo '.runtime'
$packages=Join-Path $base 'work/runtime'
$runtimeLock=Get-Content (Join-Path $repo 'deploy/locks/runtime.json') -Raw | ConvertFrom-Json
. (Join-Path $PSScriptRoot 'mysql-package.ps1')
$mysqlPackage=Resolve-MySQLRuntimePackage -Packages $packages -Runtime $runtime -TargetVersion $runtimeLock.mysql.version
$mysqlBin=$mysqlPackage.Bin
$temporalBin=Join-Path $packages 'temporal'
New-Item -ItemType Directory -Force $runtime | Out-Null

function Protect-Value([string]$Value) {
    ConvertTo-SecureString $Value -AsPlainText -Force | ConvertFrom-SecureString
}
function Reveal-Value([string]$Value) {
    $secure=ConvertTo-SecureString $Value
    [System.Net.NetworkCredential]::new('', $secure).Password
}
function New-Secret { [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant() }
function Check-Exit([string]$Operation) { if ($LASTEXITCODE -ne 0) { throw "$Operation failed (exit $LASTEXITCODE)" } }

$credentialFile=Join-Path $runtime 'local-credentials.json'
if (-not (Test-Path -LiteralPath $credentialFile)) {
    if ($Action -ne 'start') { throw 'Local runtime is not initialized. Run start first.' }
    @{root=(Protect-Value (New-Secret)); app=(Protect-Value (New-Secret)); test=(Protect-Value (New-Secret)); temporal=(Protect-Value (New-Secret))} | ConvertTo-Json | Set-Content -LiteralPath $credentialFile -Encoding utf8
}
$credentials=Get-Content -LiteralPath $credentialFile -Raw | ConvertFrom-Json
$appPassword=Reveal-Value $credentials.app
$testPassword=Reveal-Value $credentials.test
$temporalPassword=Reveal-Value $credentials.temporal
$env:STUDIO_DSN="studio:$appPassword@tcp(127.0.0.1:13306)/studio?parseTime=true&charset=utf8mb4&loc=UTC&time_zone=%27%2B00%3A00%27"
$env:STUDIO_TEST_DSN="studio_test:$testPassword@tcp(127.0.0.1:13306)/studio_test?parseTime=true&charset=utf8mb4&loc=UTC&time_zone=%27%2B00%3A00%27"
$env:STUDIO_TEMPORAL='127.0.0.1:17233'
$env:STUDIO_TEMPORAL_NAMESPACE='studio'
$env:STUDIO_WEB_DIR=Join-Path $repo 'web/dist'
$env:STUDIO_BLOB_ROOT=Join-Path $repo '.runtime/blobs'
$env:STUDIO_VAULT_MASTER=Join-Path $repo '.runtime/vault-master/key.dpapi'
$env:STUDIO_VAULT_ROOT=Join-Path $repo '.runtime/vault-ciphertexts'
$env:STUDIO_ISOLATION_ROOT=Join-Path $base 'work/isolation-runtime/runtime'
if(-not $env:STUDIO_AUTHORITY_SCHEMA){$env:STUDIO_AUTHORITY_SCHEMA='studio_authority'}
$env:GOMODCACHE=Join-Path $base 'work/go/mod'
$env:GOCACHE=Join-Path $base 'work/go/cache'
. (Join-Path $PSScriptRoot 'log-protection-env.ps1')
if($Action -eq 'env' -and $LogSetupDatabase -eq '') {
    Import-LogProtectionEnvironment -RuntimeDirectory $runtime -Database studio
    Import-LogProtectionEnvironment -RuntimeDirectory $runtime -Database studio_test
}
if(Test-Path -LiteralPath (Join-Path $runtime 'embedding-token.dpapi')) { . (Join-Path $PSScriptRoot 'embedding-runtime.ps1') env }
. (Join-Path $PSScriptRoot 'ingest-runtime.ps1') env
if ($Action -eq 'env') { return }

if ($Action -eq 'stop') {
    . (Join-Path $PSScriptRoot 'runtime-processes.ps1')
    # Temporal retains its existing verified-package shutdown. MySQL requires
    # graceful shutdown of the data-owning server, including old monitor children.
    $temporalPIDPath=Join-Path $runtime 'temporal.pid'
    if (Test-Path -LiteralPath $temporalPIDPath) {
        $running=Get-Process -Id ([int](Get-Content -LiteralPath $temporalPIDPath)) -ErrorAction SilentlyContinue
        if ($running -and $running.Path -eq (Join-Path $temporalBin 'temporal-server.exe')) { Stop-Process -Id $running.Id }
    }
    $shutdownPassword=Reveal-Value $credentials.root
    try {
        Stop-RuntimeMySQL (Join-Path $mysqlBin 'mysqld.exe') (Join-Path $runtime 'mysql-data') (Join-Path $mysqlBin 'mysqladmin.exe') 13306 $shutdownPassword
        $mysqlPIDFile=Join-Path $runtime 'mysql.pid'
        if(Test-Path -LiteralPath $mysqlPIDFile){Remove-Item -LiteralPath $mysqlPIDFile}
    } finally {$shutdownPassword=$null}
    return
}
if ($Action -eq 'status') {
    Write-Output "MySQL data engine: $($mysqlPackage.Version); target: $($mysqlPackage.TargetVersion); migration pending: $($mysqlPackage.NeedsMigration)"
    foreach ($port in @(13306,17233)) {
        $socket=[Net.Sockets.TcpClient]::new()
        try { $socket.Connect('127.0.0.1',$port); Write-Output "Port $port ready" } finally { $socket.Dispose() }
    }
    return
}
if (-not (Test-Path -LiteralPath (Join-Path $mysqlBin 'mysqld.exe'))) { throw 'Verified MySQL package missing; see deploy/locks/runtime.json.' }
$mysqlData=Join-Path $runtime 'mysql-data'
$init=Join-Path $runtime 'mysql-init.sql'
if (-not (Test-Path -LiteralPath $mysqlData)) {
    New-Item -ItemType Directory $mysqlData | Out-Null
    $mysqlPackage.Version | Set-Content -LiteralPath $mysqlPackage.Marker -Encoding utf8
    & (Join-Path $mysqlBin 'mysqld.exe') --no-defaults --initialize-insecure "--basedir=$(Split-Path $mysqlBin -Parent)" "--datadir=$mysqlData" --console *> (Join-Path $runtime 'mysql-initialize.log')
    Check-Exit 'MySQL initialize'
    $rootPassword=Reveal-Value $credentials.root
    @"
ALTER USER 'root'@'localhost' IDENTIFIED BY '$rootPassword';
CREATE DATABASE studio CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE studio_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE DATABASE temporal CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE DATABASE temporal_visibility CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'studio'@'localhost' IDENTIFIED BY '$appPassword';
CREATE USER 'studio_test'@'localhost' IDENTIFIED BY '$testPassword';
CREATE USER 'temporal'@'localhost' IDENTIFIED BY '$temporalPassword';
GRANT ALL ON studio.* TO 'studio'@'localhost';
GRANT ALL ON studio_test.* TO 'studio_test'@'localhost';
GRANT ALL ON temporal.* TO 'temporal'@'localhost';
GRANT ALL ON temporal_visibility.* TO 'temporal'@'localhost';
"@ | Set-Content -LiteralPath $init -Encoding utf8NoBOM
}
$mysqlPID=Join-Path $runtime 'mysql.pid'
. (Join-Path $PSScriptRoot 'runtime-processes.ps1')
$mysqlArgs=@('--no-defaults', "--basedir=`"$(Split-Path $mysqlBin -Parent)`"", "--datadir=`"$mysqlData`"", '--port=13306','--bind-address=127.0.0.1','--mysqlx=OFF', '--local-infile=OFF', '--binlog-row-metadata=FULL', '--default-time-zone=+00:00', '--console')
if(Test-Path -LiteralPath $init){$mysqlArgs+="--init-file=`"$init`""}
Start-RuntimeMySQL (Join-Path $mysqlBin 'mysqld.exe') $mysqlData 13306 $mysqlArgs $mysqlPID (Join-Path $runtime 'mysql.log') (Join-Path $runtime 'mysql-error.log')
if(Test-Path -LiteralPath $init){Remove-Item -LiteralPath $init}
$env:SQL_HOST='127.0.0.1';$env:SQL_PORT='13306';$env:SQL_USER='temporal';$env:SQL_PASSWORD=$temporalPassword;$env:SQL_PLUGIN='mysql8'
if(-not(Test-Path -LiteralPath (Join-Path $runtime 'temporal-schema.ready'))) {
    foreach($db in @('temporal','temporal_visibility')) {
        $env:SQL_DATABASE=$db
        & (Join-Path $temporalBin 'temporal-sql-tool.exe') setup-schema -v 0.0 *> (Join-Path $runtime "$db-setup.log")
        Check-Exit 'Temporal schema setup'
        $schema=if($db -eq 'temporal'){'mysql/v8/temporal'}else{'mysql/v8/visibility'}
        & (Join-Path $temporalBin 'temporal-sql-tool.exe') update-schema --schema-name $schema *> (Join-Path $runtime "$db-update.log")
        Check-Exit 'Temporal schema update'
    }
    '1.32.1' | Set-Content -LiteralPath (Join-Path $runtime 'temporal-schema.ready')
}
Remove-Item Env:SQL_PASSWORD
$config=(Get-Content -LiteralPath (Join-Path $repo 'deploy/local/temporal.template.yaml') -Raw).Replace('__PASSWORD__',$temporalPassword)
$configFile=Join-Path $runtime 'temporal.yaml'
$config | Set-Content -LiteralPath $configFile -Encoding utf8NoBOM
# This runtime directory contains database config. Restrict it to the current Windows account and SYSTEM.
$account=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
& icacls $runtime /inheritance:r /grant:r "*${account}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' /Q | Out-Null
Check-Exit 'Runtime ACL'
$temporalPID=Join-Path $runtime 'temporal.pid'
$existing=if(Test-Path -LiteralPath $temporalPID){Get-Process -Id ([int](Get-Content -LiteralPath $temporalPID)) -ErrorAction SilentlyContinue}else{$null}
if ($existing -and $existing.Path -ne (Join-Path $temporalBin 'temporal-server.exe')) { throw 'Stored Temporal process identity mismatch' }
if(-not $existing) {
    $process=Start-Process -FilePath (Join-Path $temporalBin 'temporal-server.exe') -ArgumentList @('--config-file',"`"$configFile`"",'--allow-no-auth','start') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runtime 'temporal.log') -RedirectStandardError (Join-Path $runtime 'temporal-error.log')
    $process.Id | Set-Content -LiteralPath $temporalPID
}
# Infrastructure must be reachable before a pending collector account can be
# repaired. An invalid pair still stops the caller before starting Studio.
Import-LogProtectionEnvironment -RuntimeDirectory $runtime -Database studio
Import-LogProtectionEnvironment -RuntimeDirectory $runtime -Database studio_test
Write-Output 'MySQL and Temporal started on loopback; credentials are not printed. Use status to verify readiness.'

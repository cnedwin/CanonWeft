$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$result=[ordered]@{go=$false;node=$false;mysql=$false;temporal=$false;freeDiskGB=0;externalToolIsolation='not-qualified'}
$result.go=[bool](Get-Command go -ErrorAction SilentlyContinue)
$result.node=[bool](Get-Command node -ErrorAction SilentlyContinue)
$packages=Join-Path (Split-Path $projectRoot -Parent) 'work/runtime'
$runtimeLock=Get-Content (Join-Path $projectRoot 'deploy/locks/runtime.json') -Raw | ConvertFrom-Json
. (Join-Path $PSScriptRoot 'mysql-package.ps1')
$selected=Resolve-MySQLRuntimePackage -Packages $packages -Runtime (Join-Path $projectRoot '.runtime') -TargetVersion $runtimeLock.mysql.version
$result.mysql=Test-Path (Join-Path $selected.Bin 'mysqld.exe')
$result.mysqlDataEngine=$selected.Version
$result.mysqlTarget=$selected.TargetVersion
$result.mysqlMigrationPending=$selected.NeedsMigration
$result.temporal=Test-Path (Join-Path $packages 'temporal/temporal-server.exe')
$result.freeDiskGB=[math]::Round((Get-PSDrive -Name ([IO.Path]::GetPathRoot($projectRoot).Substring(0,1))).Free/1GB,1)
$result | ConvertTo-Json
if(-not($result.go -and $result.node -and $result.mysql -and $result.temporal)){exit 1}

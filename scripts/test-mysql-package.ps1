$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'mysql-package.ps1')
$repo=Split-Path $PSScriptRoot -Parent
$fixture=Join-Path $repo ('.test-tmp/mysql-package-'+[Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $fixture | Out-Null
$fresh=Resolve-MySQLRuntimePackage -Packages $fixture -Runtime $fixture -TargetVersion '9.7.4'
if($fresh.Version -ne '9.7.4' -or $fresh.NeedsMigration){throw 'Fresh installations must use LTS'}
New-Item -ItemType Directory (Join-Path $fixture 'mysql-data') | Out-Null
$legacy=Resolve-MySQLRuntimePackage -Packages $fixture -Runtime $fixture -TargetVersion '9.7.4'
if($legacy.Version -ne '8.4.13' -or -not $legacy.NeedsMigration){throw 'Unmarked legacy data must never auto-upgrade'}
'26.10.0' | Set-Content -LiteralPath (Join-Path $fixture 'mysql-engine-version')
$blocked=$false
try { $null=Resolve-MySQLRuntimePackage -Packages $fixture -Runtime $fixture -TargetVersion '9.7.4' } catch {$blocked=$true}
if(-not $blocked){throw 'An Innovation data directory must never be downgraded'}
'9.7.4' | Set-Content -LiteralPath (Join-Path $fixture 'mysql-engine-version')
$current=Resolve-MySQLRuntimePackage -Packages $fixture -Runtime $fixture -TargetVersion '9.7.4'
if($current.Version -ne '9.7.4' -or $current.NeedsMigration){throw 'Pinned LTS selection failed'}
Write-Output 'PASS: fresh LTS, preserved legacy, rejected downgrade, pinned LTS.'

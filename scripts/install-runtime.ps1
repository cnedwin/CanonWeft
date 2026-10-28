$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$workRoot=Join-Path (Split-Path $projectRoot -Parent) 'work/runtime'
$lock=Get-Content (Join-Path $projectRoot 'deploy/locks/runtime.json') -Raw | ConvertFrom-Json
New-Item -ItemType Directory -Force $workRoot | Out-Null
foreach($name in @('mysql','temporal','s3_test')) {
    $package=$lock.$name
    if($name -eq 'mysql') {
        # Versioned downloads cannot collide with the legacy mysql.zip/data.
        $destination=Join-Path $workRoot "mysql/$($package.version)"
        New-Item -ItemType Directory -Force $destination | Out-Null
        $archive=Join-Path $destination "mysql-$($package.version)-winx64.zip"
        if(-not(Test-Path -LiteralPath $archive)){Invoke-WebRequest $package.url -OutFile $archive}
        if((Get-FileHash -LiteralPath $archive -Algorithm MD5).Hash.ToLowerInvariant() -ne $package.md5){throw 'MySQL package checksum mismatch; no extraction performed'}
        $binary=Join-Path $destination "mysql-$($package.version)-winx64/bin/mysqld.exe"
        if(-not(Test-Path -LiteralPath $binary)){Expand-Archive -LiteralPath $archive -DestinationPath $destination}
        if((Get-AuthenticodeSignature $binary).Status -ne 'Valid'){throw 'MySQL executable signature is not valid'}
        continue
    }
    $stem=if($name -eq 's3_test'){"seaweedfs-$($package.version)"}else{$name}
    $archive=Join-Path $workRoot "$stem.zip"
    if(-not(Test-Path -LiteralPath $archive)){Invoke-WebRequest $package.url -OutFile $archive}
    $algorithm=if($name -eq 'mysql'){'MD5'}else{'SHA256'}
    $expected=if($name -eq 'mysql'){$package.md5}else{$package.sha256}
    if((Get-FileHash -LiteralPath $archive -Algorithm $algorithm).Hash.ToLowerInvariant() -ne $expected){throw "$name package checksum mismatch; no extraction performed"}
    $destination=Join-Path $workRoot $stem
    if(-not(Test-Path -LiteralPath $destination)){Expand-Archive -LiteralPath $archive -DestinationPath $destination}
}
$mysql=Join-Path $workRoot "mysql/$($lock.mysql.version)/mysql-$($lock.mysql.version)-winx64/bin/mysqld.exe"
if((Get-AuthenticodeSignature $mysql).Status -ne 'Valid'){throw 'MySQL executable signature is not valid'}
Write-Output 'Verified local MySQL, Temporal and S3 test packages are ready. No system service was installed.'

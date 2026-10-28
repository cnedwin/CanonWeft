param([ValidateSet('start','stop','env')][string]$Action='start', [switch]$StorageOnly)
$ErrorActionPreference='Stop'
$projectRoot=Split-Path $PSScriptRoot -Parent
$base=Split-Path $projectRoot -Parent
$s3RequestedAction=$Action
$package=Join-Path $base 'work/runtime/seaweedfs-4.49'
$binary=Join-Path $package 'weed.exe'
$directory=Join-Path $projectRoot '.runtime/s3-test'
New-Item -ItemType Directory -Force $directory | Out-Null
$pidFile=Join-Path $directory 'server.pid'
if($s3RequestedAction -eq 'stop'){
    if(Test-Path -LiteralPath $pidFile){
        $running=Get-Process -Id ([int](Get-Content -LiteralPath $pidFile)) -ErrorAction SilentlyContinue
        if($running){if($running.Path -ne $binary){throw 'S3 process identity mismatch'};Stop-Process -Id $running.Id}
    }
    return
}
if ($StorageOnly) {
    # A storage-only test needs neither business DB credentials nor a repeated
    # integrity walk of Python, Node, Go and PowerShell packages. Its runner
    # selects the already-qualified toolchain explicitly.
    function Protect-Value([string]$Value) { ConvertTo-SecureString $Value -AsPlainText -Force | ConvertFrom-SecureString }
    function Reveal-Value([string]$Value) { [Net.NetworkCredential]::new('', (ConvertTo-SecureString $Value)).Password }
    function New-Secret { [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant() }
} else {
    . (Join-Path $PSScriptRoot 'runtime.ps1') env
}
$credentialFile=Join-Path $directory 'credentials.json'
if(-not(Test-Path -LiteralPath $credentialFile)){
    @{writer=(Protect-Value (New-Secret));reader=(Protect-Value (New-Secret))} | ConvertTo-Json | Set-Content -LiteralPath $credentialFile -Encoding utf8
}
$credentials=Get-Content -LiteralPath $credentialFile -Raw | ConvertFrom-Json
$writer=Reveal-Value $credentials.writer
$reader=Reveal-Value $credentials.reader
$env:STUDIO_TEST_S3_ENDPOINT='http://127.0.0.1:18333'
$env:STUDIO_TEST_S3_BUCKET='studio-test'
$env:STUDIO_TEST_S3_ACCESS='studio-writer'
$env:STUDIO_TEST_S3_SECRET=$writer
$env:STUDIO_TEST_S3_READ_ACCESS='studio-reader'
$env:STUDIO_TEST_S3_READ_SECRET=$reader
if($s3RequestedAction -eq 'env'){return}
if(-not(Test-Path -LiteralPath $binary)){throw 'Verified SeaweedFS package missing'}
$config=Join-Path $directory 's3.json'
@{identities=@(
    @{name='studio-writer';credentials=@(@{accessKey='studio-writer';secretKey=$writer});actions=@('Read:studio-test','Write:studio-test','List:studio-test')},
    @{name='studio-reader';credentials=@(@{accessKey='studio-reader';secretKey=$reader});actions=@('Read:studio-test','List:studio-test')}
)} | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $config -Encoding utf8NoBOM
$running=if(Test-Path -LiteralPath $pidFile){Get-Process -Id ([int](Get-Content -LiteralPath $pidFile)) -ErrorAction SilentlyContinue}else{$null}
if($running -and $running.Path -ne $binary){throw 'S3 process identity mismatch'}
if(-not $running){
    $arguments=@('mini',"-dir=`"$directory`"",'-ip=127.0.0.1','-ip.bind=127.0.0.1','-master.port=19333','-master.telemetry=false','-volume.port=19340','-volume.max=4','-master.volumeSizeLimitMB=32','-filer.port=18888','-filer.allowedOrigins=http://127.0.0.1:18865','-filer.disableDirListing','-s3.port=18333','-s3.port.iceberg=0','-s3.port.lance=0','-s3.iam=false','-admin.ui=false','-webdav=false','-bucket=studio-test',"-s3.config=`"$config`"")
    $running=Start-Process -FilePath $binary -ArgumentList $arguments -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $directory 'server.log') -RedirectStandardError (Join-Path $directory 'server-error.log')
    $running.Id | Set-Content -LiteralPath $pidFile
}
for($attempt=0;$attempt -lt 60;$attempt++){
    if($running.HasExited){throw 'S3 test runtime exited before ready'}
    $socket=[Net.Sockets.TcpClient]::new()
    try{$socket.Connect('127.0.0.1',18333);Write-Output 'Local authenticated S3 test runtime ready';return}catch{Start-Sleep -Milliseconds 500}finally{$socket.Dispose()}
}
throw 'S3 test endpoint not ready'

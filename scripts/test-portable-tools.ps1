$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$testRoot=Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) ('work/portable-validation-' + [Guid]::NewGuid().ToString('N'))
$null=[IO.Directory]::CreateDirectory($testRoot)
$tool=@{sha256=('a'*64);version='1.0';executable='bin/tool.txt'}
$null=[IO.Directory]::CreateDirectory((Join-Path $testRoot 'bin'))
$binary=Join-Path $testRoot 'bin/tool.txt'
[IO.File]::WriteAllText($binary,'original')
$hash=(Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$receipt=@{archive_sha256=$tool.sha256;version=$tool.version;files=@(@{path='bin/tool.txt';sha256=$hash})}
$receiptPath=Join-Path $testRoot '.portable-receipt.json'
$receipt | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $receiptPath -Encoding UTF8
function Expect-Rejected([scriptblock]$Action,[string]$Label){
    $rejected=$false
    try {$null=& $Action} catch {$rejected=$true}
    if(-not $rejected){throw "Validation accepted $Label"}
}
if((Assert-PortablePackage $testRoot $tool) -ne $binary){throw 'Valid package was not resolved'}
[IO.File]::WriteAllText($binary,'modified')
Expect-Rejected {Assert-PortablePackage $testRoot $tool} 'modified content'
[IO.File]::WriteAllText($binary,'original')
Expect-Rejected {Resolve-PortableChild $testRoot '../outside'} 'parent traversal'
Expect-Rejected {Resolve-PortableChild $testRoot $binary} 'absolute path'
$receipt.files[0].path='bin/missing.txt'
$receipt | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $receiptPath -Encoding UTF8
Expect-Rejected {Assert-PortablePackage $testRoot $tool} 'missing file'
$receipt.files[0].path='bin/tool.txt'
$receipt.version='wrong'
$receipt | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $receiptPath -Encoding UTF8
Expect-Rejected {Assert-PortablePackage $testRoot $tool} 'receipt version mismatch'
$outside=Join-Path $testRoot 'real'
$null=[IO.Directory]::CreateDirectory($outside)
$junction=Join-Path $testRoot 'redirect'
$null=New-Item -ItemType Junction -Path $junction -Target $outside
Expect-Rejected {Resolve-PortableChild $testRoot 'redirect/item.txt'} 'junction ancestor'
# Keep the tiny test fixture in work for diagnosis; no recursive deletion.
Write-Output 'Portable validation passed: exact bytes, receipt, confinement and junction boundary.'

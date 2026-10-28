param([Parameter(Mandatory)][string]$FixtureRoot)
$ErrorActionPreference = 'Stop'
$repository = Split-Path $PSScriptRoot -Parent
$fixture = Join-Path ([IO.Path]::GetFullPath($FixtureRoot)) ('bootstrap-' + [guid]::NewGuid().ToString('N'))
$fixtureRepository = Join-Path $fixture 'repo'
$toolchainRoot = Join-Path $fixture 'work/runtime/toolchain'
$packageRoot = Join-Path $toolchainRoot 'locked-powershell'
$null = New-Item -ItemType Directory -Path $packageRoot, (Join-Path $fixtureRepository 'scripts'), (Join-Path $fixtureRepository 'deploy/locks') -Force
foreach ($file in @('install-portable-tools.ps1', 'portable-tools.ps1')) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $file) -Destination (Join-Path $fixtureRepository ('scripts/' + $file))
}
$lock = Get-Content -LiteralPath (Join-Path $repository 'deploy/locks/toolchain.json') -Raw | ConvertFrom-Json
$powerShellTool = $lock.tools | Where-Object name -eq 'PowerShell'
$powerShellTool.directory = 'locked-powershell'
$powerShellTool.executable = 'pwsh.cmd'
$lock | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $fixtureRepository 'deploy/locks/toolchain.json') -Encoding utf8
[IO.File]::WriteAllText((Join-Path $packageRoot 'pwsh.cmd'), "@echo off`r`n`"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`" -NoProfile -File `"%~dp0probe.ps1`" %*`r`nexit /b %ERRORLEVEL%`r`n")
@'
$ErrorActionPreference = 'Stop'
$argumentValues = @($args)
$index = [array]::IndexOf($argumentValues, '-ToolchainRoot')
if ($index -lt 0 -or $index + 1 -ge $argumentValues.Count) { throw 'Missing toolchain selector' }
$selectedRoot = [string]$argumentValues[$index + 1]
$handle = [IO.File]::Open((Join-Path $selectedRoot '.install.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try {
    @{arguments=$argumentValues;lock_acquired=$true} | ConvertTo-Json -Depth 4 | Add-Content -LiteralPath $env:PORTABLE_BOOTSTRAP_RECORD -Encoding utf8
} finally { $handle.Dispose() }
exit 23
'@ | Set-Content -LiteralPath (Join-Path $packageRoot 'probe.ps1') -Encoding utf8
$files = foreach ($name in @('pwsh.cmd', 'probe.ps1')) {
    @{path=$name;sha256=(Get-FileHash -LiteralPath (Join-Path $packageRoot $name) -Algorithm SHA256).Hash.ToLowerInvariant()}
}
$receipt = Join-Path $packageRoot '.portable-receipt.json'
@{version=$powerShellTool.version;archive_sha256=$powerShellTool.sha256;files=@($files)} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $receipt -Encoding utf8
$receiptHash = (Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash
$windowsPowerShell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$installer = Join-Path $fixtureRepository 'scripts/install-portable-tools.ps1'
$previousRecord = [Environment]::GetEnvironmentVariable('PORTABLE_BOOTSTRAP_RECORD', 'Process')
try {
    foreach ($verifyOnly in @($false, $true)) {
        $record = Join-Path $fixture ("continuation-$verifyOnly.json")
        $env:PORTABLE_BOOTSTRAP_RECORD = $record
        $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $installer, '-ToolchainRoot', $toolchainRoot, '-Launch', '-NoBuild')
        if ($verifyOnly) { $arguments += '-VerifyOnly' }
        $output = & $windowsPowerShell @arguments 2>&1
        if ($LASTEXITCODE -ne 23) { throw ('Bootstrap did not propagate the continuation failure: ' + ($output -join "`n")) }
        $calls = @(Get-Content -LiteralPath $record -Raw | ConvertFrom-Json)
        if ($calls.Count -ne 1 -or -not $calls[0].lock_acquired) { throw 'Bootstrap did not release the target install lock before handoff' }
        $forwarded = @($calls[0].arguments)
        $expected = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $installer, '-ToolchainRoot', $toolchainRoot)
        if ($verifyOnly) { $expected += '-VerifyOnly' }
        $expected += @('-Launch', '-NoBuild')
        if (($forwarded -join "`n") -ne ($expected -join "`n")) { throw 'Bootstrap changed continuation options or selected the wrong installer' }
        if ((Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash -ne $receiptHash) { throw 'Failed continuation changed the existing receipt' }
        Write-Output "PASS: bootstrap VerifyOnly=$verifyOnly preserves options, releases lock and propagates failure once"
    }

    $record = Join-Path $fixture 'lock-refusal.json'
    $env:PORTABLE_BOOTSTRAP_RECORD = $record
    $handle = [IO.File]::Open((Join-Path $toolchainRoot '.install.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try { $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $installer -ToolchainRoot $toolchainRoot 2>&1; $result = $LASTEXITCODE }
    finally { $handle.Dispose() }
    if ($result -eq 0 -or (Test-Path -LiteralPath $record)) { throw 'Concurrent bootstrap bypassed the install lock' }
    Write-Output 'PASS: concurrent installation is refused before continuation'

    $record = Join-Path $fixture 'integrity-refusal.json'
    $env:PORTABLE_BOOTSTRAP_RECORD = $record
    [IO.File]::AppendAllText((Join-Path $packageRoot 'pwsh.cmd'), "`r`nrem changed payload")
    $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $installer -ToolchainRoot $toolchainRoot -VerifyOnly 2>&1
    if ($LASTEXITCODE -eq 0 -or (Test-Path -LiteralPath $record) -or ($output -join "`n") -notmatch 'integrity mismatch') { throw 'Bootstrap executed an unverified PowerShell entry' }
    if ((Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash -ne $receiptHash) { throw 'Refused bootstrap rewrote its receipt' }
    Write-Output 'PASS: changed PowerShell entry is refused without rewriting its receipt'
} finally {
    [Environment]::SetEnvironmentVariable('PORTABLE_BOOTSTRAP_RECORD', $previousRecord, 'Process')
}

param([switch]$Integration, [string]$ToolchainRoot)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$helper = Join-Path $repo 'scripts/portable-tools.ps1'
if (-not (Test-Path -LiteralPath $helper)) { throw 'FAIL: portable toolchain resolver is missing' }
. $helper
if (-not $ToolchainRoot) { $ToolchainRoot = Get-PortableRoot }
$caseRoot = Join-Path (Split-Path $repo -Parent) ('work/portable tests/' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $caseRoot -Force | Out-Null
function Must-Fail([scriptblock]$Operation, [string]$Expected) {
    try { & $Operation } catch {
        if ($_.Exception.Message -notlike "*$Expected*") { throw }
        return
    }
    throw "FAIL: expected rejection: $Expected"
}
Must-Fail { Resolve-PortableChild $caseRoot '../outside' } 'outside'
$archive = Join-Path $caseRoot 'corrupt.zip'
[IO.File]::WriteAllText($archive, 'not the published archive')
Must-Fail { Assert-PortableArchive $archive ('0' * 64) } 'SHA256'
Must-Fail { Get-PortableToolchain -ToolchainRoot $caseRoot } 'missing'
$fakePackage = Join-Path $caseRoot 'changed package'
New-Item -ItemType Directory -Path $fakePackage | Out-Null
[IO.File]::WriteAllText((Join-Path $fakePackage 'test.exe'), 'changed payload')
@{version='1';archive_sha256=('0' * 64);files=@(@{path='test.exe';sha256=('0' * 64)})} |
    ConvertTo-Json -Depth 4 | Set-Content (Join-Path $fakePackage '.portable-receipt.json')
Must-Fail { Assert-PortablePackage $fakePackage ([pscustomobject]@{version='1';sha256=('0' * 64);executable='test.exe'}) } 'integrity mismatch'
$corruptRoot = Join-Path $caseRoot 'corrupt installer'
$corruptCache = Join-Path $corruptRoot 'downloads'
New-Item -ItemType Directory -Path $corruptCache -Force | Out-Null
Copy-Item -LiteralPath $archive -Destination (Join-Path $corruptCache 'PowerShell-7.6.5-win-x64.zip')
$windowsPowerShell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$installer = Join-Path $repo 'scripts/install-portable-tools.ps1'
$errorLog = Join-Path $caseRoot 'expected-corrupt-error.log'
$ErrorActionPreference = 'Continue'
& $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $installer -ToolchainRoot $corruptRoot *> $errorLog
$ErrorActionPreference = 'Stop'
if ($LASTEXITCODE -eq 0 -or (Get-Content -LiteralPath $errorLog -Raw) -notmatch 'SHA256 mismatch') { throw 'Installer accepted corrupt archive' }
if (@(Get-ChildItem -LiteralPath $corruptRoot -Directory | Where-Object Name -ne 'downloads').Count) { throw 'Corrupt archive was extracted' }
Write-Output 'PASS: traversal, corrupt archive and missing installation fail closed'
if ($Integration) {
    $oldPath = $env:PATH
    try {
        $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot"
        $tools = Enable-PortableToolchain -ToolchainRoot $ToolchainRoot
        $receipt = Join-Path (Split-Path $tools.PowerShell -Parent) '.portable-receipt.json'
        $receiptTime = (Get-Item -LiteralPath $receipt).LastWriteTimeUtc
        & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $installer -ToolchainRoot $ToolchainRoot
        if ($LASTEXITCODE -or (Get-Item -LiteralPath $receipt).LastWriteTimeUtc -ne $receiptTime) { throw 'Installer rerun was not idempotent' }
        $venv = Join-Path $caseRoot 'fresh venv'
        & $tools.Python -m venv $venv
        if ($LASTEXITCODE) { throw 'Fresh venv creation failed' }
        $venvPython = Join-Path $venv 'Scripts/python.exe'
        $probe = Join-Path $caseRoot 'check_venv.py'
        @'
import sys
import ssl
import sqlite3
import collections.abc
import ctypes.wintypes
import http.client
import importlib.metadata
from pathlib import Path
assert sys.version_info[:3] == (3, 12, 14)
assert sys.flags.isolated and sys.dont_write_bytecode
assert Path(sys.base_prefix).resolve() == Path(sys.argv[1]).parent.resolve()
assert sys.prefix != sys.base_prefix
print(sys.base_prefix)
'@ | Set-Content -LiteralPath $probe -Encoding UTF8
        & $venvPython -I -B $probe $tools.Python
        if ($LASTEXITCODE) { throw 'Fresh venv base check failed' }
        & $venvPython -m pip --version
        if ($LASTEXITCODE) { throw 'Fresh venv pip failed' }
        & $tools.PowerShell -NoProfile -Command '$PSVersionTable.PSVersion.ToString()'
        if ($LASTEXITCODE) { throw 'Portable pwsh failed' }
        & $tools.Node --version
        if ($LASTEXITCODE) { throw 'Portable Node failed' }
        & $tools.Go version
        if ($LASTEXITCODE) { throw 'Portable Go failed' }
        $pythonLock = (Get-PortableLock).tools | Where-Object name -eq 'Python'
        $null = Assert-PortablePackage (Join-Path $ToolchainRoot $pythonLock.directory) $pythonLock
        $oldModules = $env:PSModulePath
        try {
            # Reproduce a PS7 -> CMD -> PS5 hop, which otherwise imports the
            # wrong Microsoft.PowerShell.Utility and loses Get-FileHash.
            $env:PSModulePath = (Join-Path (Split-Path $tools.PowerShell -Parent) 'Modules') + ';' + $oldModules
            & (Join-Path $repo 'Start-Platform.cmd') -VerifyOnly -ToolchainRoot $ToolchainRoot
            if ($LASTEXITCODE) { throw 'CMD bootstrap with inherited PS7 modules failed' }
        } finally { $env:PSModulePath = $oldModules }
        Write-Output 'PASS: clean PATH, locked tools, idempotent reinstall and new venv with pip'
    } finally { $env:PATH = $oldPath }
}

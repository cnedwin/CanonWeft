$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
. (Join-Path $PSScriptRoot 'python-worker-tools.ps1')
$pythonTool = (Get-PortableLock).tools | Where-Object name -eq 'Python'
$pythonDirectory = Join-Path (Get-PortableRoot) $pythonTool.directory
$python = Assert-PortablePackage $pythonDirectory $pythonTool
$testDirectory = Join-Path (Get-PortableRoot) ('python-bootstrap-test-' + [Guid]::NewGuid().ToString('N'))
$previousBytecode = [Environment]::GetEnvironmentVariable('PYTHONDONTWRITEBYTECODE', 'Process')
Initialize-PythonWorkerEnvironment -Bootstrap $python -Environment $testDirectory
if ([Environment]::GetEnvironmentVariable('PYTHONDONTWRITEBYTECODE', 'Process') -ne $previousBytecode) {
    throw 'Bootstrap leaked caller bytecode preference'
}
$testPython = Join-Path $testDirectory 'Scripts/python.exe'
& $testPython -I -B -m pip --version
if ($LASTEXITCODE) { throw 'Fresh environment pip bootstrap failed' }
$null = Assert-PortablePackage $pythonDirectory $pythonTool
foreach ($worker in @('embedding', 'ingest')) {
    $workerPython = Join-Path (Get-PythonWorkerEnvironment $worker) 'Scripts/python.exe'
    $version = & $workerPython -I -B -c 'import platform; print(platform.python_version())'
    if ($LASTEXITCODE -or $version -ne $pythonTool.version) { throw "$worker Python mismatch" }
    Write-Output "$worker Python $version resolved to $workerPython"
}
Write-Output 'PASS: fresh pip bootstrap preserves original distribution receipt and restores caller environment.'

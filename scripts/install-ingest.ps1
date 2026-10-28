$ErrorActionPreference = 'Stop'
$ingestRepo = Split-Path $PSScriptRoot -Parent
$ingestBase = Split-Path $ingestRepo -Parent
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$ingestTools = Enable-PortableToolchain
$ingestBootstrap = $ingestTools.Python
. (Join-Path $PSScriptRoot 'python-worker-tools.ps1')
$ingestEnvironment = Get-PythonWorkerEnvironment ingest
$ingestPython = Join-Path $ingestEnvironment 'Scripts/python.exe'
$ingestWheels = Join-Path $ingestBase 'work/ingest-wheels'
$ingestLock = Join-Path $ingestRepo 'tools/ingest/requirements.lock'
if (-not (Test-Path -LiteralPath $ingestPython)) {
    Initialize-PythonWorkerEnvironment -Bootstrap $ingestBootstrap -Environment $ingestEnvironment
}
New-Item -ItemType Directory -Force $ingestWheels | Out-Null
& $ingestPython -B -m pip download --only-binary=:all: --require-hashes --dest $ingestWheels -r $ingestLock
if ($LASTEXITCODE -ne 0) { throw 'Verified ingest dependency download failed' }
& $ingestPython -B -m pip install --no-index --find-links $ingestWheels --only-binary=:all: --require-hashes -r $ingestLock
if ($LASTEXITCODE -ne 0) { throw 'Verified ingest dependency installation failed' }
& $ingestPython -B -I (Join-Path $ingestRepo 'tools/ingest/verify_installation.py')
if ($LASTEXITCODE -ne 0) { throw 'Ingest installed artifact verification failed' }
Write-Output 'Free local PDF/OCR runtime installed; no attachment upload or inference-time model download.'

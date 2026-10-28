param([ValidateSet('env', 'status')][string]$IngestAction = 'status')
$ErrorActionPreference = 'Stop'
$ingestRepo = Split-Path $PSScriptRoot -Parent
$ingestBase = Split-Path $ingestRepo -Parent
. (Join-Path $PSScriptRoot 'python-worker-tools.ps1')
$env:STUDIO_INGEST_PYTHON = Join-Path (Get-PythonWorkerEnvironment ingest) 'Scripts/python.exe'
$env:STUDIO_INGEST_SCRIPT = Join-Path $ingestRepo 'tools/ingest/worker.py'
# Enabled by default. An explicit disabled setting is preserved for text-only deployments.
if (-not $env:STUDIO_INGEST_OCR) { $env:STUDIO_INGEST_OCR = 'enabled' }
if ($IngestAction -eq 'env') { return }
& $env:STUDIO_INGEST_PYTHON -B -I (Join-Path $ingestRepo 'tools/ingest/verify_installation.py')
if ($LASTEXITCODE -ne 0) { throw 'Local ingest runtime verification failed' }

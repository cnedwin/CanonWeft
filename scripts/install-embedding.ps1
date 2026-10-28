$ErrorActionPreference='Stop'
$embeddingRepo=Split-Path $PSScriptRoot -Parent
$embeddingBase=Split-Path $embeddingRepo -Parent
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$embeddingTools = Enable-PortableToolchain
$bootstrap = $embeddingTools.Python
. (Join-Path $PSScriptRoot 'python-worker-tools.ps1')
$environment=Get-PythonWorkerEnvironment embedding
$python=Join-Path $environment 'Scripts/python.exe'
if(-not(Test-Path -LiteralPath $python)){
    Initialize-PythonWorkerEnvironment -Bootstrap $bootstrap -Environment $environment
}
& $python -B -m pip install --only-binary=:all: --require-hashes -r (Join-Path $embeddingRepo 'tools/embedding/requirements.lock')
if($LASTEXITCODE -ne 0){throw 'Verified embedding dependencies install failed'}
& $python -B (Join-Path $embeddingRepo 'tools/embedding/download_model.py') --destination (Join-Path $embeddingBase 'work/models/bge-m3-5617a9f') --manifest (Join-Path $embeddingRepo 'deploy/locks/bge-m3.json')
if($LASTEXITCODE -ne 0){throw 'Official model download or checksum failed'}
Write-Output 'Pinned local BGE-M3 dependencies and weights are installed.'

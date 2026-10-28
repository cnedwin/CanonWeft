param([string]$Python)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$cache = Join-Path (Split-Path $repo -Parent) 'work/isolation-runtime'
if (-not $Python) {
    . (Join-Path $PSScriptRoot 'portable-tools.ps1')
    $isolationTools = Enable-PortableToolchain
    $Python = $isolationTools.Python
}
if (-not (Test-Path -LiteralPath $Python)) { throw 'Python 3.12+ is required to build the pinned image. Pass -Python with its executable path.' }
& $Python -B (Join-Path $repo 'tools/isolation/install_runtime.py') $cache
if ($LASTEXITCODE -ne 0) { throw 'Verified Linux runtime installation failed.' }
Write-Output 'No Windows service, WSL, Hyper-V, host ACL, or system component was installed.'

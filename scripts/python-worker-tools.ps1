# Resolve both installer and runtime environments from the same version lock.
function Get-PythonWorkerEnvironment {
    param([Parameter(Mandatory)][ValidateSet('embedding', 'ingest')][string]$Worker)
    $workerRepo = Split-Path $PSScriptRoot -Parent
    $workerBase = Split-Path $workerRepo -Parent
    $workerLock = Get-Content -LiteralPath (Join-Path $workerRepo 'deploy/locks/python-workers.json') -Raw | ConvertFrom-Json
    if ($workerLock.schema -ne 1 -or $workerLock.python_version -notmatch '^3\.12\.\d+$') {
        throw 'Invalid Python worker version lock'
    }
    $relative = [string]$workerLock.$Worker
    if ($relative -ne "work/$Worker-venv-$($workerLock.python_version)") {
        throw 'Invalid Python worker environment path'
    }
    Join-Path $workerBase $relative
}

function Initialize-PythonWorkerEnvironment {
    param([Parameter(Mandatory)][string]$Bootstrap, [Parameter(Mandatory)][string]$Environment)
    # venv -> ensurepip -> pip does not propagate the parent's -B flag.
    # These bootstrap children are non-isolated and honor this inherited flag.
    $previousBytecode = [Environment]::GetEnvironmentVariable('PYTHONDONTWRITEBYTECODE', 'Process')
    try {
        $env:PYTHONDONTWRITEBYTECODE = '1'
        & $Bootstrap -B -m venv $Environment
        if ($LASTEXITCODE -ne 0) { throw 'Python worker environment creation failed' }
    } finally {
        if ($null -eq $previousBytecode) {
            Remove-Item Env:PYTHONDONTWRITEBYTECODE -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable('PYTHONDONTWRITEBYTECODE', $previousBytecode, 'Process')
        }
    }
}

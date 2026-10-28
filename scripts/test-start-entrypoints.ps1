param([ValidateSet('All', 'Compatibility', 'Network')][string]$Case = 'All')
$ErrorActionPreference = 'Stop'
$repository = Split-Path $PSScriptRoot -Parent
$fixtureRoot = Join-Path $repository ('.test-tmp/start-entrypoints-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $fixtureRoot
$powerShell = (Get-Process -Id $PID).Path
$failures = [Collections.Generic.List[string]]::new()

function Test-EntrypointCase([string]$Name, [scriptblock]$Check) {
    try {
        & $Check
        Write-Output "PASS: $Name"
    } catch {
        $failures.Add("${Name}: $($_.Exception.Message)")
        Write-Output "FAIL: ${Name}: $($_.Exception.Message)"
    }
}

if ($Case -in @('All', 'Compatibility')) {
    Test-EntrypointCase 'legacy PowerShell entry delegates from a directory containing spaces' {
        $fixture = Join-Path $fixtureRoot 'powershell entry'
        $null = New-Item -ItemType Directory -Path (Join-Path $fixture '.venv/Scripts') -Force
        # Keep the historical branch harmless while reproducing its wrong route.
        [IO.File]::WriteAllText((Join-Path $fixture '.venv/Scripts/python.exe'), '')
        Copy-Item -LiteralPath (Join-Path $repository 'Start.ps1') -Destination $fixture
        $record = Join-Path $fixture 'forwarded.txt'
        [IO.File]::WriteAllText((Join-Path $fixture 'Start-Platform.cmd'), "@echo off`r`necho %* > `"$record`"`r`nexit /b 23`r`n")
        $output = & $powerShell -NoProfile -File (Join-Path $fixture 'Start.ps1') -NoBrowser -NoBuild -VerifyOnly 2>&1
        $result = $LASTEXITCODE
        if (-not (Test-Path -LiteralPath $record)) { throw 'Current platform entry was not called' }
        if ((Get-Content -LiteralPath $record -Raw).Trim() -ne '-NoBuild -VerifyOnly') { throw 'Startup options were not forwarded; NoBrowser must remain compatible' }
        if ($result -ne 23) { throw "Platform exit code was lost: $result" }
    }
    Test-EntrypointCase 'legacy CMD entry reaches the current platform through Windows PowerShell' {
        $fixture = Join-Path $fixtureRoot 'cmd entry'
        $null = New-Item -ItemType Directory -Path (Join-Path $fixture '.venv/Scripts') -Force
        [IO.File]::WriteAllText((Join-Path $fixture '.venv/Scripts/python.exe'), '')
        Copy-Item -LiteralPath (Join-Path $repository 'Start.cmd') -Destination $fixture
        Copy-Item -LiteralPath (Join-Path $repository 'Start.ps1') -Destination $fixture
        $record = Join-Path $fixture 'forwarded.txt'
        [IO.File]::WriteAllText((Join-Path $fixture 'Start-Platform.cmd'), "@echo off`r`necho %* > `"$record`"`r`nexit /b 0`r`n")
        $output = & (Join-Path $fixture 'Start.cmd') -NoBrowser -NoBuild -VerifyOnly 2>&1
        if ($LASTEXITCODE -ne 0) { throw 'CMD compatibility entry failed' }
        if (-not (Test-Path -LiteralPath $record)) { throw 'CMD did not reach the current platform entry' }
        $forwarded = [IO.File]::ReadAllText($record)
        if ($forwarded.Trim() -ne '-NoBuild -VerifyOnly') { throw 'CMD startup options were lost' }
    }
}

if ($Case -in @('All', 'Network')) {
    $previousNetwork = [Environment]::GetEnvironmentVariable('STUDIO_PROVIDER_NETWORK', 'Process')
    try {
        foreach ($inputValue in @('', 'enabled', 'disabled')) {
            Test-EntrypointCase "provider network default preserves explicit '$inputValue' configuration" {
                $fixture = Join-Path $fixtureRoot ('network-' + [guid]::NewGuid().ToString('N'))
                $null = New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') -Force
                Copy-Item -LiteralPath (Join-Path $repository 'Start-Platform.ps1') -Destination $fixture
                # Stop at the first dependency boundary, before any runtime,
                # database, build, worker, download or model request can run.
                [IO.File]::WriteAllText((Join-Path $fixture 'scripts/ingest-runtime.ps1'), '[IO.File]::WriteAllText((Join-Path (Split-Path $PSScriptRoot -Parent) ''network.txt''), [string]$env:STUDIO_PROVIDER_NETWORK); throw ''ENTRYPOINT_TEST_STOP''')
                [Environment]::SetEnvironmentVariable('STUDIO_PROVIDER_NETWORK', $inputValue, 'Process')
                $output = & $powerShell -NoProfile -File (Join-Path $fixture 'Start-Platform.ps1') -NoBuild 2>&1
                if ($LASTEXITCODE -eq 0 -or ($output -join "`n") -notmatch 'ENTRYPOINT_TEST_STOP') { throw 'Fixture did not stop before real service startup' }
                $actual = Get-Content -LiteralPath (Join-Path $fixture 'network.txt') -Raw
                $expected = if ($inputValue -eq '') { 'disabled' } else { $inputValue }
                if ($actual -ne $expected) { throw "Expected provider network '$expected', received '$actual'" }
            }
        }
    } finally {
        [Environment]::SetEnvironmentVariable('STUDIO_PROVIDER_NETWORK', $previousNetwork, 'Process')
    }
}

if ($failures.Count -gt 0) { throw ($failures -join "`n") }
Write-Output 'Entrypoint checks passed without starting application services.'

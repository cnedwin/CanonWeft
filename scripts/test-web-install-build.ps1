param([Parameter(Mandatory)][string]$FixtureRoot)
$ErrorActionPreference = 'Stop'
$fixture = Join-Path ([IO.Path]::GetFullPath($FixtureRoot)) ('web-build-' + [guid]::NewGuid().ToString('N'))
$repository = Join-Path $fixture 'source'
$null = New-Item -ItemType Directory -Path (Join-Path $repository 'scripts'), (Join-Path $repository 'web'), (Join-Path $repository 'tools/package-manager'), (Join-Path $repository 'fake-tools') -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'install-web.ps1') -Destination (Join-Path $repository 'scripts/install-web.ps1')
foreach ($name in @('package.json', 'package-lock.json')) {
    '{}' | Set-Content -LiteralPath (Join-Path $repository ('tools/package-manager/' + $name)) -Encoding utf8
}
@'
function Enable-PortableToolchain {
    [pscustomobject]@{Node=(Join-Path (Split-Path $PSScriptRoot -Parent) 'fake-tools/node.ps1')}
}
'@ | Set-Content -LiteralPath (Join-Path $repository 'scripts/portable-tools.ps1') -Encoding utf8
'@exit /b 0' | Set-Content -LiteralPath (Join-Path $repository 'fake-tools/npm.cmd') -Encoding ascii
@'
if ($args[-1] -eq '--version') { $global:LASTEXITCODE=0; '11.25.0'; return }
if ($args.Count -eq 3 -and $args[1] -eq 'run' -and $args[2] -eq 'build') {
    [IO.File]::AppendAllText($env:INSTALL_BUILD_RECORD, "project-build`n")
    $global:LASTEXITCODE=[int]$env:INSTALL_BUILD_EXIT
    return
}
$global:LASTEXITCODE=0
'@ | Set-Content -LiteralPath (Join-Path $repository 'fake-tools/node.ps1') -Encoding utf8
$savedEnvironment = @{}
foreach ($name in @('INSTALL_BUILD_RECORD', 'INSTALL_BUILD_EXIT')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    foreach ($expectedExit in @(0, 23)) {
        $record = Join-Path $fixture ("build-$expectedExit.log")
        $env:INSTALL_BUILD_RECORD = $record
        $env:INSTALL_BUILD_EXIT = [string]$expectedExit
        $failed = $false
        try { & (Join-Path $repository 'scripts/install-web.ps1') }
        catch { $failed = $true }
        if (-not (Test-Path -LiteralPath $record) -or @(Get-Content -LiteralPath $record).Count -ne 1) {
            throw 'Frontend installer did not execute the project build exactly once'
        }
        if ($failed -ne ($expectedExit -ne 0)) { throw "Frontend build exit $expectedExit was not preserved" }
        Write-Output "PASS: installer delegates one project build and preserves exit $expectedExit"
    }
} finally {
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}

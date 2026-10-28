$ErrorActionPreference = 'Stop'
$webInstallRepo = Split-Path $PSScriptRoot -Parent
$webInstallWork = Join-Path (Split-Path $webInstallRepo -Parent) 'work'
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
$webInstallTools = Enable-PortableToolchain
$webPackageManager = Join-Path $webInstallWork 'runtime/package-manager'
New-Item -ItemType Directory -Force -Path $webPackageManager | Out-Null
foreach ($name in @('package.json', 'package-lock.json')) {
    Copy-Item -LiteralPath (Join-Path $webInstallRepo "tools/package-manager/$name") -Destination (Join-Path $webPackageManager $name)
}
& (Join-Path (Split-Path $webInstallTools.Node -Parent) 'npm.cmd') ci --prefix $webPackageManager --cache (Join-Path $webInstallWork 'npm-cache') --ignore-scripts --no-audit --no-fund
if ($LASTEXITCODE -ne 0) { throw 'Locked pnpm installation failed' }
$webPnpm = Join-Path $webPackageManager 'node_modules/pnpm/bin/pnpm.mjs'
$webPnpmVersion = & $webInstallTools.Node $webPnpm --version
if ($LASTEXITCODE -ne 0 -or $webPnpmVersion -ne '11.25.0') { throw 'Unexpected pnpm version' }
Push-Location (Join-Path $webInstallRepo 'web')
try {
    & $webInstallTools.Node $webPnpm install --frozen-lockfile --ignore-scripts --store-dir (Join-Path $webInstallWork 'pnpm-store')
    if ($LASTEXITCODE -ne 0) { throw 'Locked frontend dependency installation failed' }
    # Use the CLI from the frozen dependency install, and the same browser root
    # as verify-platform.ps1. Normal application startup does not need Chromium.
    $webPreviousBrowserPath = $env:PLAYWRIGHT_BROWSERS_PATH
    try {
        $env:PLAYWRIGHT_BROWSERS_PATH = Join-Path $webInstallWork 'browsers'
        & $webInstallTools.Node node_modules/@playwright/test/cli.js install chromium
        if ($LASTEXITCODE -ne 0) { throw 'Locked Playwright Chromium installation failed' }
    } finally {
        $env:PLAYWRIGHT_BROWSERS_PATH = $webPreviousBrowserPath
    }
    & $webInstallTools.Node $webPnpm run build
    if ($LASTEXITCODE -ne 0) { throw 'Frontend type check or build failed' }
} finally { Pop-Location }

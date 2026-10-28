param([Parameter(Mandatory)][ValidateSet('BGE', 'Browser', 'FreshBrowser')][string]$Case)
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$work = Join-Path (Split-Path $repo -Parent) 'work'
. (Join-Path $repo 'scripts/portable-tools.ps1')
$tools = Enable-PortableToolchain
. (Join-Path $repo 'scripts/python-worker-tools.ps1')
$embeddingPython = Join-Path (Get-PythonWorkerEnvironment embedding) 'Scripts/python.exe'

if ($Case -eq 'BGE') {
    & (Join-Path $repo 'scripts/embedding-runtime.ps1') stop
    & (Join-Path $repo 'scripts/embedding-runtime.ps1') start
    $launcherId = [int](Get-Content -LiteralPath (Join-Path $repo '.runtime/embedding.pid'))
    $processes = @(Get-CimInstance Win32_Process)
    $pythonProcesses = @($processes | Where-Object {
        ($_.ProcessId -eq $launcherId -or $_.ParentProcessId -eq $launcherId) -and
        $_.ExecutablePath -in @($embeddingPython, $tools.Python)
    })
    if ($pythonProcesses.Count -ne 2) { throw 'Expected owned venv launcher and base Python worker' }
    foreach ($process in $pythonProcesses) {
        if ($process.CommandLine -notmatch '(?:^|\s)"?-I"?(?:\s|$)' -or
            $process.CommandLine -notmatch '(?:^|\s)"?-B"?(?:\s|$)') {
            throw 'Actual BGE process is missing isolation/no-bytecode flags'
        }
    }
    Write-Output 'PASS: actual owned BGE launcher and worker both use -I -B; health is ready'
    & (Join-Path $repo 'scripts/verify-embedding-runtime.ps1')
    $null = Get-PortableToolchain
    Write-Output 'PASS: actual managed BGE stop/restart and original toolchain file integrity'
    return
}

if ($Case -eq 'Browser') {
    & (Join-Path $repo 'scripts/install-web.ps1')
    $browserRoot = Join-Path $work 'browsers'
} else {
    $browserRoot = Join-Path $work ('runtime-repair-tests/fresh-browser-' + [guid]::NewGuid().ToString('N'))
    if (Test-Path -LiteralPath $browserRoot) { throw 'Fresh browser root unexpectedly exists' }
}
$previousBrowserRoot = $env:PLAYWRIGHT_BROWSERS_PATH
Push-Location (Join-Path $repo 'web')
try {
    $env:PLAYWRIGHT_BROWSERS_PATH = $browserRoot
    if ($Case -eq 'FreshBrowser') {
        & $tools.Node node_modules/@playwright/test/cli.js install chromium
        if ($LASTEXITCODE) { throw 'Fresh locked Chromium installation failed' }
    }
    $browserProbe = @'
const fs = require('node:fs');
const { chromium } = require('@playwright/test');
(async () => {
  const executable = chromium.executablePath();
  if (!fs.existsSync(executable)) throw new Error('Installed Chromium executable missing');
  const browser = await chromium.launch({headless: true});
  try {
    const page = await browser.newPage();
    await page.setContent('<p id="qualification">workspace Chromium ready</p>');
    if (await page.locator('#qualification').textContent() !== 'workspace Chromium ready') throw new Error('Chromium page check failed');
    console.log('PASS: locked Playwright Chromium executable exists and a real headless page rendered');
    console.log('Browser version: ' + browser.version());
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
'@
    & $tools.Node -e $browserProbe
    if ($LASTEXITCODE) { throw 'Actual Chromium launch qualification failed' }
    # A second install must reuse the installed browser revision.
    & $tools.Node node_modules/@playwright/test/cli.js install chromium
    if ($LASTEXITCODE) { throw 'Idempotent Chromium installation failed' }
    Write-Output "PASS: $Case browser cache is launchable and reinstall succeeds: $browserRoot"
} finally {
    Pop-Location
    $env:PLAYWRIGHT_BROWSERS_PATH = $previousBrowserRoot
}
$null = Get-PortableToolchain
Write-Output 'PASS: original toolchain file integrity after browser qualification'

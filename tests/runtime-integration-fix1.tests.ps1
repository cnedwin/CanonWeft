param([ValidateSet('All', 'Isolation', 'Identity', 'Browser')][string]$Case = 'All')
$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$caseRoot = Join-Path (Split-Path $repo -Parent) ('work/runtime-repair-tests/' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $caseRoot | Out-Null

function Read-ScriptAst([string]$Path) {
    $parseErrors = $null
    $tokens = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count) { throw "Script parse failed: $parseErrors" }
    $ast
}

if ($Case -in @('All', 'Isolation')) {
    # Execute the production argument builder and the resulting interpreter flags.
    # Removing -I must fail even if the test runner has clean Python environment.
    $ast = Read-ScriptAst (Join-Path $repo 'scripts/embedding-runtime.ps1')
    $assignment = $ast.Find({ param($n) $n -is [Management.Automation.Language.AssignmentStatementAst] -and $n.Left.Extent.Text -eq '$embeddingArgs' }, $true)
    $embeddingRepo = $repo
    $embeddingBase = Split-Path $repo -Parent
    . ([scriptblock]::Create($assignment.Extent.Text))
    $flags = @()
    foreach ($argument in $embeddingArgs) {
        $argument = $argument.Trim('"')
        if ($argument -notlike '-*') { break }
        $flags += $argument
    }
    . (Join-Path $repo 'scripts/python-worker-tools.ps1')
    $python = Join-Path (Get-PythonWorkerEnvironment embedding) 'Scripts/python.exe'
    & $python @flags -c 'import sys; assert sys.flags.isolated == 1, "BGE launch is not isolated"; assert sys.dont_write_bytecode; print("PASS: production BGE arguments enforce isolated/no-bytecode Python")'
    if ($LASTEXITCODE) { throw 'BGE interpreter isolation regression' }
}

if ($Case -in @('All', 'Identity')) {
    # Run the real stop branch with deterministic process doubles. This avoids
    # forcing OS PID reuse or risking any real process while testing the race.
    $ast = Read-ScriptAst (Join-Path $repo 'scripts/embedding-runtime.ps1')
    foreach ($definition in $ast.FindAll({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
        . ([scriptblock]::Create($definition.Extent.Text))
    }
    $stopBranch = $ast.Find({ param($n) $n -is [Management.Automation.Language.IfStatementAst] -and $n.Clauses[0].Item1.Extent.Text -eq "$('$')EmbeddingAction -eq 'stop'" }, $true)
    if (-not $stopBranch) { throw 'Missing managed stop branch' }
    $embeddingPython = 'C:\owned\venv\Scripts\python.exe'
    $rootTime = [datetime]::new(2026, 10, 24, 10, 0, 0, [DateTimeKind]::Utc)
    function New-TestProcess([int]$ProcessId, [string]$Path, [datetime]$StartTime) {
        $process = [pscustomobject]@{ Id=$ProcessId; Path=$Path; StartTime=$StartTime; HasExited=$false; SafeHandle=[pscustomobject]@{IsInvalid=$false; IsClosed=$false} }
        $process | Add-Member ScriptMethod Kill { $script:terminated += $this.Id; $this.HasExited=$true }
        $process | Add-Member ScriptMethod WaitForExit { param($Timeout) $this.HasExited }
        $process | Add-Member ScriptMethod Dispose { }
        $process
    }
    function Get-CimInstance { param($ClassName) $script:snapshots }
    function Get-Process { param($Id, $ErrorAction) $script:processes[[int]$Id] }
    function Get-Content { param($LiteralPath) 'home = C:\owned\base' }
    function Stop-Process { param($Id) $script:terminated += [int]$Id; $script:processes[[int]$Id].HasExited=$true }

    foreach ($scenario in @('reused-child', 'reused-root', 'valid-submicrosecond', 'natural-exit', 'invalid-descendant')) {
        $script:terminated = @()
        $root = New-TestProcess 1001 $embeddingPython $rootTime.AddTicks(6)
        $child = New-TestProcess 1002 'C:\owned\base\python.exe' $rootTime.AddSeconds(1).AddTicks(9)
        $script:processes = @{1001=$root;1002=$child}
        $script:snapshots = @(
            [pscustomobject]@{ProcessId=[uint32]1001;ParentProcessId=[uint32]0;ExecutablePath=$root.Path;CreationDate=$rootTime},
            [pscustomobject]@{ProcessId=[uint32]1002;ParentProcessId=[uint32]1001;ExecutablePath=$child.Path;CreationDate=$rootTime.AddSeconds(1)}
        )
        if ($scenario -eq 'reused-child') { $child.StartTime = $child.StartTime.AddTicks(10) }
        if ($scenario -eq 'reused-root') { $root.StartTime = $root.StartTime.AddSeconds(5) }
        if ($scenario -eq 'natural-exit') { $script:processes.Remove(1002) }
        if ($scenario -eq 'invalid-descendant') { $script:snapshots[1].ExecutablePath = 'C:\unrelated\python.exe' }
        $embeddingProcess = $root
        $EmbeddingAction = 'stop'
        $rejected = $false
        try { & ([scriptblock]::Create($stopBranch.Extent.Text)) } catch {
            if ($_.Exception.Message -notmatch 'identity|Unexpected embedding child') { throw }
            $rejected = $true
        }
        if ($scenario -in @('reused-child', 'reused-root', 'invalid-descendant')) {
            if (-not $rejected -or $script:terminated.Count) { throw "FAIL: $scenario terminated snapshot/replacement process: $script:terminated" }
        } else {
            $expected = if ($scenario -eq 'natural-exit') { '1001' } else { '1002,1001' }
            if ($rejected -or ($script:terminated -join ',') -ne $expected) { throw "FAIL: $scenario did not stop only the validated tree" }
        }
        Write-Output "PASS: stop identity $scenario"
    }
    Remove-Item Function:Get-CimInstance, Function:Get-Process, Function:Get-Content, Function:Stop-Process
}

if ($Case -in @('All', 'Browser')) {
    # Run the whole installer in a miniature workspace. External package/Node
    # tools are controlled boundaries; the production flow and error checks run.
    $fixtureRepo = Join-Path $caseRoot 'fixture/repo'
    New-Item -ItemType Directory -Force -Path (Join-Path $fixtureRepo 'scripts'), (Join-Path $fixtureRepo 'web'), (Join-Path $fixtureRepo 'tools/package-manager'), (Join-Path $fixtureRepo 'fake-tools') | Out-Null
    Copy-Item -LiteralPath (Join-Path $repo 'scripts/install-web.ps1') -Destination (Join-Path $fixtureRepo 'scripts/install-web.ps1')
    foreach ($file in @('package.json', 'package-lock.json')) { '{}' | Set-Content (Join-Path $fixtureRepo "tools/package-manager/$file") }
    @'
function Enable-PortableToolchain { [pscustomobject]@{Node=(Join-Path (Split-Path $PSScriptRoot -Parent) 'fake-tools/node.ps1')} }
'@ | Set-Content (Join-Path $fixtureRepo 'scripts/portable-tools.ps1')
    '@exit /b 0' | Set-Content (Join-Path $fixtureRepo 'fake-tools/npm.cmd')
    @'
if ($args[-1] -eq '--version') { $global:LASTEXITCODE=0; '11.25.0'; return }
if ($args[0] -eq 'node_modules/@playwright/test/cli.js' -and $args[1] -eq 'install') {
    if ($args.Count -ne 3 -or $args[2] -ne 'chromium') { throw 'Wrong browser installation arguments' }
    [IO.File]::WriteAllText($env:REPAIR_TEST_BROWSER_LOG, $env:PLAYWRIGHT_BROWSERS_PATH)
    $global:LASTEXITCODE=[int]$env:REPAIR_TEST_BROWSER_EXIT
    return
}
$global:LASTEXITCODE=0
'@ | Set-Content (Join-Path $fixtureRepo 'fake-tools/node.ps1')
    $oldBrowserPath = $env:PLAYWRIGHT_BROWSERS_PATH
    try {
        $env:PLAYWRIGHT_BROWSERS_PATH = 'sentinel-external-browser-cache'
        $env:REPAIR_TEST_BROWSER_LOG = Join-Path $caseRoot 'browser-invocation.txt'
        $env:REPAIR_TEST_BROWSER_EXIT = '0'
        & (Join-Path $fixtureRepo 'scripts/install-web.ps1')
        $expectedRoot = Join-Path (Split-Path $fixtureRepo -Parent) 'work/browsers'
        if (-not (Test-Path $env:REPAIR_TEST_BROWSER_LOG) -or [IO.File]::ReadAllText($env:REPAIR_TEST_BROWSER_LOG) -ne $expectedRoot) { throw 'FAIL: installer did not provision locked Chromium into its workspace browser root' }
        if ($env:PLAYWRIGHT_BROWSERS_PATH -ne 'sentinel-external-browser-cache') { throw 'Browser installer leaked cache environment setting' }
        Write-Output 'PASS: frontend installer provisions workspace Chromium and restores environment'
        $env:REPAIR_TEST_BROWSER_EXIT = '23'
        $failed = $false
        try { & (Join-Path $fixtureRepo 'scripts/install-web.ps1') } catch {
            if ($_.Exception.Message -notmatch 'Chromium.*failed') { throw }
            $failed = $true
        }
        if (-not $failed) { throw 'FAIL: browser installation failure was ignored' }
        if ($env:PLAYWRIGHT_BROWSERS_PATH -ne 'sentinel-external-browser-cache') { throw 'Failed browser installer leaked environment setting' }
        Write-Output 'PASS: frontend installer rejects Chromium installation failure'
    } finally {
        $env:PLAYWRIGHT_BROWSERS_PATH = $oldBrowserPath
        Remove-Item Env:REPAIR_TEST_BROWSER_LOG, Env:REPAIR_TEST_BROWSER_EXIT -ErrorAction SilentlyContinue
    }
}

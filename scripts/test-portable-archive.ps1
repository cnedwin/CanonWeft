param([Parameter(Mandatory)][string]$FixtureRoot)
$ErrorActionPreference = 'Stop'
$repository = Split-Path $PSScriptRoot -Parent
$caseRoot = Join-Path ([IO.Path]::GetFullPath($FixtureRoot)) ('archive-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $caseRoot
$windowsPowerShell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$child = Join-Path $caseRoot 'expand.ps1'
@'
param($Repository, $Archive, $Destination, $Sha256, [switch]$VerifyPackage, $Member)
$ErrorActionPreference = 'Stop'
$env:PSModulePath = [IO.Path]::Combine($PSHOME, 'Modules')
. (Join-Path $Repository 'scripts/portable-tools.ps1')
if ($VerifyPackage) {
    $null = Assert-PortablePackage $Destination ([pscustomobject]@{version='1.0';sha256=$Sha256;executable=$Member})
    'PACKAGE_VALID'
    return
}
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $Repository 'scripts/install-portable-tools.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Installer parse failed' }
$definition = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Expand-PortableArchive' }, $true)
if (-not $definition) { throw 'Archive extraction function missing' }
. ([scriptblock]::Create($definition.Extent.Text))
Expand-PortableArchive $Archive $Destination ([pscustomobject]@{format='zip';sha256=$Sha256})
'EXTRACTION_COMPLETED'
'@ | Set-Content -LiteralPath $child -Encoding utf8

function New-ArchiveFixture([string]$Name, [string]$Member, [int]$Attributes = 0) {
    $path = Join-Path $caseRoot ($Name + '.zip')
    $archive = [IO.Compression.ZipFile]::Open($path, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $entry = $archive.CreateEntry($Member)
        if ($Attributes -ne 0) { $entry.ExternalAttributes = $Attributes }
        $writer = [IO.StreamWriter]::new($entry.Open())
        try { $writer.Write('archive-payload-canary') } finally { $writer.Dispose() }
    } finally { $archive.Dispose() }
    $path
}

function Invoke-ArchiveFixture([string]$Archive, [string]$Destination, [string]$Hash) {
    $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $child -Repository $repository -Archive $Archive -Destination $Destination -Sha256 $Hash 2>&1
    [pscustomobject]@{ExitCode=$LASTEXITCODE;Output=($output -join "`n")}
}

$failures = [Collections.Generic.List[string]]::new()
function Test-ArchiveCase([string]$Name, [scriptblock]$Check) {
    try { & $Check; Write-Output "PASS: $Name" }
    catch { $failures.Add("${Name}: $($_.Exception.Message)"); Write-Output "FAIL: ${Name}: $($_.Exception.Message)" }
}

Test-ArchiveCase 'Windows PowerShell bootstrap extracts ZIP members beyond MAX_PATH' {
    $member = 'tool/' + ((@('directory-with-long-name') * 10) -join '/') + '/payload.txt'
    $archive = New-ArchiveFixture 'long-path' $member
    $destination = Join-Path $caseRoot 'long extracted path'
    if ((Join-Path $destination $member).Length -le 260) { throw 'Long-path reproduction is too short' }
    $result = Invoke-ArchiveFixture $archive $destination (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($result.ExitCode -ne 0) { throw ('Valid long-path archive failed: ' + $result.Output) }
    if ([IO.File]::ReadAllText((Join-Path $destination $member)) -ne 'archive-payload-canary') { throw 'Extracted payload differs' }
}

Test-ArchiveCase 'ZIP checksum mismatch stops before extraction' {
    $archive = New-ArchiveFixture 'wrong-hash' 'tool/payload.txt'
    $destination = Join-Path $caseRoot 'hash-rejected'
    $result = Invoke-ArchiveFixture $archive $destination ('0' * 64)
    if ($result.ExitCode -eq 0 -or $result.Output -notmatch 'SHA256 mismatch') { throw 'Corrupt archive was not rejected' }
    if (Test-Path -LiteralPath $destination) { throw 'Corrupt archive created an extraction directory' }
}

Test-ArchiveCase 'ZIP path traversal is refused' {
    $archive = New-ArchiveFixture 'traversal' '../outside-canary.txt'
    $destination = Join-Path $caseRoot 'traversal-rejected'
    $result = Invoke-ArchiveFixture $archive $destination (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($result.ExitCode -eq 0 -or $result.Output -notmatch 'outside owned directory') { throw 'Traversal archive was not rejected' }
    if (Test-Path -LiteralPath (Join-Path $caseRoot 'outside-canary.txt')) { throw 'Traversal wrote outside its destination' }
}

Test-ArchiveCase 'ZIP symlink metadata is refused before native extraction' {
    $archive = New-ArchiveFixture 'symlink' 'tool/link' ([int](0xA000 -shl 16))
    $destination = Join-Path $caseRoot 'symlink-rejected'
    $result = Invoke-ArchiveFixture $archive $destination (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($result.ExitCode -eq 0 -or $result.Output -notmatch 'Unsupported ZIP entry type') { throw 'Symbolic-link archive was not rejected' }
    if (Test-Path -LiteralPath (Join-Path $destination 'tool/link')) { throw 'Unsupported ZIP member was extracted' }
}

Test-ArchiveCase 'Windows bootstrap verifies every long-path package file and rejects changed bytes' {
    $member = 'tool/' + ((@('long-verified-directory') * 10) -join '/') + '/payload.txt'
    $package = Join-Path $caseRoot 'verified package'
    $payload = Join-Path $package $member
    $null = New-Item -ItemType Directory -Path (Split-Path $payload -Parent) -Force
    [IO.File]::WriteAllText($payload, 'verified-original-canary')
    $packageHash = 'a' * 64
    @{version='1.0';archive_sha256=$packageHash;files=@(@{path=$member;sha256=(Get-FileHash -LiteralPath $payload -Algorithm SHA256).Hash.ToLowerInvariant()})} |
        ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $package '.portable-receipt.json') -Encoding utf8
    $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $child -Repository $repository -Destination $package -Sha256 $packageHash -VerifyPackage -Member $member 2>&1
    if ($LASTEXITCODE -ne 0 -or ($output -join "`n") -notmatch 'PACKAGE_VALID') { throw ('Valid package with a long member path failed verification: ' + ($output -join "`n")) }
    [IO.File]::WriteAllText($payload, 'changed-canary')
    $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $child -Repository $repository -Destination $package -Sha256 $packageHash -VerifyPackage -Member $member 2>&1
    if ($LASTEXITCODE -eq 0 -or ($output -join "`n") -notmatch 'integrity mismatch') { throw 'Changed long-path package file was not rejected' }
}

Test-ArchiveCase 'Windows bootstrap refuses a reparse ancestor beyond MAX_PATH' {
    $package = Join-Path $caseRoot 'junction package'
    $relativeDirectory = (@('long-junction-directory') * 10) -join '/'
    $directory = Join-Path $package $relativeDirectory
    $target = Join-Path $caseRoot 'junction-target'
    $null = New-Item -ItemType Directory -Path $directory, $target -Force
    [IO.File]::WriteAllText((Join-Path $target 'payload.txt'), 'junction-target-canary')
    $null = New-Item -ItemType Junction -Path (Join-Path $directory 'redirect') -Target $target
    $member = $relativeDirectory + '/redirect/payload.txt'
    $packageHash = 'b' * 64
    @{version='1.0';archive_sha256=$packageHash;files=@(@{path=$member;sha256=(Get-FileHash -LiteralPath (Join-Path $target 'payload.txt') -Algorithm SHA256).Hash.ToLowerInvariant()})} |
        ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $package '.portable-receipt.json') -Encoding utf8
    $output = & $windowsPowerShell -NoProfile -ExecutionPolicy Bypass -File $child -Repository $repository -Destination $package -Sha256 $packageHash -VerifyPackage -Member $member 2>&1
    if ($LASTEXITCODE -eq 0 -or ($output -join "`n") -notmatch 'Reparse point is not allowed') { throw 'Long-path reparse ancestor was not refused' }
}

if ($failures.Count) { throw ($failures -join "`n") }
Write-Output 'Portable archive cases passed without installing tools or starting services.'

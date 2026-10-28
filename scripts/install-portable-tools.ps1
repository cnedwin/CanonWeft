param(
    [string]$ToolchainRoot,
    [switch]$VerifyOnly,
    [switch]$Launch,
    [switch]$NoBuild
)
$ErrorActionPreference = 'Stop'
# A CMD hop does not remove PS7's inherited module search paths when entering
# Windows PowerShell 5.1. Use this engine's own built-in modules during bootstrap.
$env:PSModulePath = [IO.Path]::Combine($PSHOME, 'Modules')
. (Join-Path $PSScriptRoot 'portable-tools.ps1')
if (-not $ToolchainRoot) { $ToolchainRoot = Get-PortableRoot }
$ownedWork = [IO.Path]::GetFullPath((Join-Path (Split-Path $script:PortableRepository -Parent) 'work')).TrimEnd('\')
$ToolchainRoot = [IO.Path]::GetFullPath($ToolchainRoot).TrimEnd('\')
if (-not $ToolchainRoot.StartsWith($ownedWork + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Toolchain installation must stay inside this workspace work directory.'
}
if (-not [Environment]::Is64BitProcess) { throw 'Run 64-bit Windows PowerShell; toolchain is windows-x64.' }

function Expand-PortableArchive([string]$Archive, [string]$Destination, $Tool) {
    Assert-PortableArchive $Archive $Tool.sha256
    New-Item -ItemType Directory -Path $Destination | Out-Null
    $checkedPaths = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    if ($Tool.format -eq 'zip') {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
        try {
            foreach ($entry in $zip.Entries) {
                $null = Resolve-PortableChild $Destination $entry.FullName $checkedPaths
                # Native ZIP extraction understands Unix links and Windows
                # reparse attributes. Refuse those before it can create one.
                $unixType = ($entry.ExternalAttributes -shr 16) -band 0xF000
                if ($unixType -notin @(0, 0x4000, 0x8000) -or ($entry.ExternalAttributes -band 0x400)) {
                    throw "Unsupported ZIP entry type: $($entry.FullName)"
                }
            }
        } finally { $zip.Dispose() }
        # .NET Framework's ZIP writer retains MAX_PATH under Windows
        # PowerShell 5.1. Windows tar supports long destination/member paths.
        $tar = Join-Path $env:SystemRoot 'System32/tar.exe'
        if (-not (Test-Path -LiteralPath $tar)) { throw 'Windows tar.exe missing; Windows 10 1809 or newer is required.' }
        & $tar -xf $Archive -C $Destination
        if ($LASTEXITCODE) { throw 'Cannot extract verified ZIP archive' }
    } elseif ($Tool.format -eq 'tar.gz') {
        $tar = Join-Path $env:SystemRoot 'System32/tar.exe'
        if (-not (Test-Path -LiteralPath $tar)) { throw 'Windows tar.exe missing; Windows 10 1809 or newer is required.' }
        $entries = & $tar -tzf $Archive
        if ($LASTEXITCODE) { throw 'Cannot list verified Python archive' }
        foreach ($entry in $entries) { $null = Resolve-PortableChild $Destination $entry $checkedPaths }
        & $tar -xzf $Archive -C $Destination
        if ($LASTEXITCODE) { throw 'Cannot extract verified Python archive' }
    } else { throw "Unsupported archive format: $($Tool.format)" }
}

function Install-PortablePackage($Tool) {
    $destination = Resolve-PortableChild $ToolchainRoot $Tool.directory
    if (Test-Path -LiteralPath $destination) {
        # Enable checks existing packages before application use. The PS5
        # bootstrap separately verifies its pinned PS7 before handing over.
        Write-Host "Found existing $($Tool.name) $($Tool.version); verifying before use"
        return
    }
    $cache = Resolve-PortableChild $ToolchainRoot 'downloads'
    New-Item -ItemType Directory -Path $cache -Force | Out-Null
    $archive = Resolve-PortableChild $cache $Tool.archive
    if (-not (Test-Path -LiteralPath $archive)) {
        $uri = [uri]$Tool.url
        if ($uri.Scheme -ne 'https' -or $uri.Host -notin @('github.com', 'nodejs.org', 'dl.google.com')) { throw 'Unexpected download origin' }
        $partial = Resolve-PortableChild $cache ($Tool.archive + '.' + [guid]::NewGuid().ToString('N') + '.partial')
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Write-Host "Downloading $($Tool.name) $($Tool.version) from $($uri.Host)"
        $request = [Net.HttpWebRequest]::Create($uri)
        $request.Timeout = 30000
        $request.ReadWriteTimeout = 30000
        $response = $null
        $output = $null
        try {
            $response = $request.GetResponse()
            $output = [IO.File]::Open($partial, [IO.FileMode]::CreateNew)
            $response.GetResponseStream().CopyTo($output)
        } finally {
            if ($output) { $output.Dispose() }
            if ($response) { $response.Dispose() }
        }
        Assert-PortableArchive $partial $Tool.sha256
        Move-Item -LiteralPath $partial -Destination $archive
    }
    Assert-PortableArchive $archive $Tool.sha256
    # Keep staging paths short for downstream consumers; verified system tar
    # handles the deeply nested ZIP members without Framework ZIP limits.
    $staging = Resolve-PortableChild $ToolchainRoot ('s-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
    Expand-PortableArchive $archive $staging $Tool
    $files = @(Get-ChildItem -LiteralPath $staging -Recurse -File | ForEach-Object {
        if ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Archive contains reparse point' }
        [ordered]@{ path = $_.FullName.Substring($staging.Length + 1); sha256 = (Get-FileHash -LiteralPath (ConvertTo-PortableFileSystemPath $_.FullName) -Algorithm SHA256).Hash.ToLowerInvariant() }
    })
    [ordered]@{ version = $Tool.version; archive_sha256 = $Tool.sha256; files = $files } |
        ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $staging '.portable-receipt.json') -Encoding UTF8
    $null = Assert-PortablePackage $staging $Tool
    # Same-volume directory rename publishes only a complete verified package.
    Move-Item -LiteralPath $staging -Destination $destination
    Write-Host "Installed $($Tool.name) $($Tool.version)"
}

$lockHandle = $null
if ($PSVersionTable.PSVersion.Major -lt 7) {
    # The inbox engine is the bootstrap, not the long-path file verifier.
    # Hash the first PowerShell package before executing it, then continue in
    # that exact engine so Framework path limits cannot omit package files.
    try {
        $powerShellTool = (Get-PortableLock).tools | Where-Object name -eq 'PowerShell'
        if (-not $VerifyOnly) {
            New-Item -ItemType Directory -Path $ToolchainRoot -Force | Out-Null
            $bootstrapLock = Resolve-PortableChild $ToolchainRoot '.install.lock'
            $lockHandle = [IO.File]::Open($bootstrapLock, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
            Install-PortablePackage $powerShellTool
        }
        $powerShellDirectory = Resolve-PortableChild $ToolchainRoot $powerShellTool.directory
        $bootstrapPowerShell = Assert-PortablePackage $powerShellDirectory $powerShellTool
    } finally {
        if ($lockHandle) { $lockHandle.Dispose(); $lockHandle = $null }
    }
    $continuationArguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath, '-ToolchainRoot', $ToolchainRoot)
    if ($VerifyOnly) { $continuationArguments += '-VerifyOnly' }
    if ($Launch) { $continuationArguments += '-Launch' }
    if ($NoBuild) { $continuationArguments += '-NoBuild' }
    & $bootstrapPowerShell @continuationArguments
    exit $LASTEXITCODE
}
try {
    $lockPath = Resolve-PortableChild $ToolchainRoot '.install.lock'
    if (-not $VerifyOnly) {
        New-Item -ItemType Directory -Path $ToolchainRoot -Force | Out-Null
        $lockHandle = [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        foreach ($tool in (Get-PortableLock).tools) { Install-PortablePackage $tool }
    }
    $tools = Enable-PortableToolchain -ToolchainRoot $ToolchainRoot
    Assert-PortableVersions $tools
    Write-Host 'Portable toolchain verified. Existing virtual environments were not modified.'
} finally {
    if ($lockHandle) { $lockHandle.Dispose() }
}
if ($Launch -and -not $VerifyOnly) {
    $launchArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $script:PortableRepository 'Start-Platform.ps1'))
    if ($NoBuild) { $launchArgs += '-NoBuild' }
    & $tools.PowerShell @launchArgs
    exit $LASTEXITCODE
}

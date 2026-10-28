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
            }
        } finally { $zip.Dispose() }
        [IO.Compression.ZipFile]::ExtractToDirectory($Archive, $Destination)
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
        # The single final Enable pass checks every existing package before
        # any executable runs; do not hash it twice on an ordinary restart.
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
    # Keep staging shorter than final paths: Windows PowerShell 5.1's ZIP API
    # still applies MAX_PATH, and npm contains deeply nested package files.
    $staging = Resolve-PortableChild $ToolchainRoot ('s-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
    Expand-PortableArchive $archive $staging $Tool
    $files = @(Get-ChildItem -LiteralPath $staging -Recurse -File | ForEach-Object {
        if ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Archive contains reparse point' }
        [ordered]@{ path = $_.FullName.Substring($staging.Length + 1); sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    })
    [ordered]@{ version = $Tool.version; archive_sha256 = $Tool.sha256; files = $files } |
        ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $staging '.portable-receipt.json') -Encoding UTF8
    $null = Assert-PortablePackage $staging $Tool
    # Same-volume directory rename publishes only a complete verified package.
    Move-Item -LiteralPath $staging -Destination $destination
    Write-Host "Installed $($Tool.name) $($Tool.version)"
}

$lockHandle = $null
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

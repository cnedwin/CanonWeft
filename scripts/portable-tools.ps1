# Compatible with built-in Windows PowerShell 5.1. Dot-sourcing has no side effects.
$script:PortableRepository = Split-Path $PSScriptRoot -Parent

function Get-PortableRoot {
    Join-Path (Split-Path $script:PortableRepository -Parent) 'work/runtime/toolchain'
}

function Resolve-PortableChild([string]$Root, [string]$Relative, [Collections.Generic.HashSet[string]]$CheckedPaths = $null) {
    $fullRoot = [IO.Path]::GetFullPath($Root).TrimEnd('\', '/')
    $child = [IO.Path]::GetFullPath((Join-Path $fullRoot $Relative))
    if ([IO.Path]::IsPathRooted($Relative) -or -not $child.StartsWith($fullRoot + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "Path outside owned directory: $Relative"
    }
    # Do not follow an existing junction/symlink into an unrelated directory.
    $cursor = $child
    while ($cursor) {
        if ($null -ne $CheckedPaths -and $CheckedPaths.Contains($cursor)) { break }
        if (Test-Path -LiteralPath $cursor) {
            if ((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Reparse point is not allowed in toolchain path: $cursor"
            }
        }
        if ($null -ne $CheckedPaths) { $null = $CheckedPaths.Add($cursor) }
        $cursor = Split-Path $cursor -Parent
    }
    $child
}

function Get-PortableLock {
    $lock = Get-Content -LiteralPath (Join-Path $script:PortableRepository 'deploy/locks/toolchain.json') -Raw | ConvertFrom-Json
    if ($lock.schema -ne 1 -or $lock.platform -ne 'windows-x64' -or $lock.tools.Count -ne 4) { throw 'Invalid toolchain lock' }
    $lock
}

function Assert-PortableArchive([string]$Path, [string]$Sha256) {
    if ($Sha256 -notmatch '^[a-f0-9]{64}$' -or (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash -ne $Sha256) {
        throw "SHA256 mismatch; archive was not extracted: $Path"
    }
}

function Assert-PortablePackage([string]$Directory, $Tool) {
    # Reuse ancestor checks only within this one read-only validation pass.
    $checkedPaths = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $receiptPath = Resolve-PortableChild $Directory '.portable-receipt.json'
    if (-not (Test-Path -LiteralPath $receiptPath)) { throw "Toolchain receipt missing: $Directory" }
    $receipt = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json
    if ($receipt.archive_sha256 -ne $Tool.sha256 -or $receipt.version -ne $Tool.version -or @($receipt.files).Count -lt 1) {
        throw "Toolchain receipt mismatch: $Directory"
    }
    foreach ($file in $receipt.files) {
        $path = Resolve-PortableChild $Directory $file.path $checkedPaths
        if (-not (Test-Path -LiteralPath $path) -or (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $file.sha256) {
            throw "Toolchain file integrity mismatch: $path"
        }
    }
    $exe = Resolve-PortableChild $Directory $Tool.executable
    if (-not (Test-Path -LiteralPath $exe)) { throw "Toolchain executable missing: $exe" }
    $exe
}

function Get-PortableToolchain([string]$ToolchainRoot = (Get-PortableRoot)) {
    $result = [ordered]@{}
    foreach ($tool in (Get-PortableLock).tools) {
        $directory = Resolve-PortableChild $ToolchainRoot $tool.directory
        $result[$tool.name] = Assert-PortablePackage $directory $tool
    }
    [pscustomobject]$result
}

function Enable-PortableToolchain([string]$ToolchainRoot = (Get-PortableRoot)) {
    $tools = Get-PortableToolchain -ToolchainRoot $ToolchainRoot
    $bins = @($tools.PowerShell, $tools.Node, $tools.Python, $tools.Go) | ForEach-Object { Split-Path $_ -Parent }
    $env:PATH = (($bins + @($env:PATH -split ';')) | Select-Object -Unique) -join ';'
    $env:STUDIO_PYTHON = $tools.Python
    $env:GOROOT = Split-Path (Split-Path $tools.Go -Parent) -Parent
    $env:GOTOOLCHAIN = 'local'
    $env:PYTHONNOUSERSITE = '1'
    $env:PYTHONDONTWRITEBYTECODE = '1'
    Remove-Item Env:PYTHONHOME -ErrorAction SilentlyContinue
    Remove-Item Env:PYTHONPATH -ErrorAction SilentlyContinue
    $tools
}

function Assert-PortableVersions($Tools) {
    $actual = @{
        PowerShell = (& $Tools.PowerShell -NoProfile -Command '$PSVersionTable.PSVersion.ToString()')
        Node = (& $Tools.Node --version)
        Python = (& $Tools.Python -I -B -c 'import platform; print(platform.python_version())')
        Go = (& $Tools.Go version)
    }
    foreach ($tool in (Get-PortableLock).tools) {
        $expected = $tool.version
        if ($tool.name -eq 'Node') { $expected = 'v' + $expected }
        if ($tool.name -eq 'Go') { $expected = 'go version go' + $expected + ' windows/amd64' }
        if ([string]$actual[$tool.name] -ne $expected) { throw "Unexpected $($tool.name) version: $($actual[$tool.name])" }
    }
}

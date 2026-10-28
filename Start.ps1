param([switch]$NoBrowser, [switch]$NoBuild, [switch]$VerifyOnly)
$ErrorActionPreference = 'Stop'
# Compatibility entry for the current Go/Vue platform. The platform leaves
# browser navigation to the user, so the historical NoBrowser option is safe.
$launchArguments = @()
if ($NoBuild) { $launchArguments += '-NoBuild' }
if ($VerifyOnly) { $launchArguments += '-VerifyOnly' }
& (Join-Path $PSScriptRoot 'Start-Platform.cmd') @launchArguments
exit $LASTEXITCODE

# Read-only selection: a dependency update must never upgrade an existing data directory.
function Resolve-MySQLRuntimePackage {
    param([Parameter(Mandatory)][string]$Packages,
          [Parameter(Mandatory)][string]$Runtime,
          [Parameter(Mandatory)][string]$TargetVersion)
    if ($TargetVersion -ne '9.7.4') { throw 'Review the MySQL LTS baseline before changing the target version.' }
    $data = Join-Path $Runtime 'mysql-data'
    $marker = Join-Path $Runtime 'mysql-engine-version'
    $version = $TargetVersion
    if (Test-Path -LiteralPath $data) {
        if (Test-Path -LiteralPath $marker) {
            $version = (Get-Content -LiteralPath $marker -Raw).Trim()
        } else {
            # This repository's original runtime created only 8.4.13 data.
            # Keep its old behavior until a verified migration writes a marker.
            $version = '8.4.13'
        }
    }
    if ($version -notin @('8.4.13','9.7.4')) { throw 'Unknown data engine version; refusing to select a different server.' }
    $relative = if ($version -eq '8.4.13') { 'mysql/mysql-8.4.13-winx64/bin' } else { 'mysql/9.7.4/mysql-9.7.4-winx64/bin' }
    [pscustomobject]@{Version=$version;TargetVersion=$TargetVersion;Bin=(Join-Path $Packages $relative);Data=$data;Marker=$marker;NeedsMigration=($version -ne $TargetVersion)}
}

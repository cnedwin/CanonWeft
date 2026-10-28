# Loading a prepared collector account does not create users or grant privileges.
function Import-LogProtectionEnvironment {
    param([Parameter(Mandatory=$true)][string]$RuntimeDirectory,
          [Parameter(Mandatory=$true)][ValidateSet('studio','studio_test')][string]$Database)
    $credentialName="log-protection-$Database.dpapi"
    $credentialPath=Join-Path $RuntimeDirectory $credentialName
    $readyPath=Join-Path $RuntimeDirectory "log-protection-$Database.ready.json"
    $hasCredential=Test-Path -LiteralPath $credentialPath -PathType Leaf
    $hasReady=Test-Path -LiteralPath $readyPath -PathType Leaf
    if(-not $hasCredential -and -not $hasReady){return}
    $plainBytes=$null
    try {
        if(-not $hasCredential -or -not $hasReady){throw 'Incomplete pair'}
        $credentialItem=Get-Item -LiteralPath $credentialPath
        $readyItem=Get-Item -LiteralPath $readyPath
        if($credentialItem.Length -gt 32768 -or $readyItem.Length -gt 16384 -or ($credentialItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -or ($readyItem.Attributes -band [IO.FileAttributes]::ReparsePoint)){throw 'Invalid file'}
        $ready=Get-Content -LiteralPath $readyPath -Raw | ConvertFrom-Json
        $cipher=[IO.File]::ReadAllBytes($credentialPath)
        $cipherHash=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($cipher)).ToLowerInvariant()
        if($ready.format -ne 1 -or $ready.status -ne 'READY' -or $ready.database -cne $Database -or $ready.host -cne '127.0.0.1' -or $ready.grant_profile -cne 'log-reader-v1' -or $ready.credential_file -cne $credentialName -or $ready.credential_sha256 -cne $cipherHash -or $ready.server_uuid -cnotmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' -or $ready.user -cnotmatch '^backup_[0-9a-f]{20}$' -or $ready.table_fingerprint -cnotmatch '^[0-9a-f]{64}$'){throw 'Invalid pairing'}
        $plainBytes=[Security.Cryptography.ProtectedData]::Unprotect($cipher,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser)
        $credential=[Text.Encoding]::UTF8.GetString($plainBytes) | ConvertFrom-Json
        foreach($field in @('format','database','server_uuid','user','host','authority_schema','grant_profile')) {
            if($credential.$field -cne $ready.$field){throw 'Identity mismatch'}
        }
        if([string]::IsNullOrWhiteSpace($credential.dsn) -or $credential.dsn.Length -gt 8192){throw 'Invalid connection'}
        # The Go startup path independently verifies the live server identity
        # and exact current grants. A READY file is not a coverage claim.
        $variable=if($Database -eq 'studio_test'){'STUDIO_TEST_LOG_DSN'}else{'STUDIO_LOG_DSN'}
        [Environment]::SetEnvironmentVariable($variable,[string]$credential.dsn,'Process')
    } catch {
        # Do not expose JSON parsing exceptions that may include plaintext.
        throw "Log protection credentials for $Database could not be verified. Re-run the local log protection setup."
    } finally {
        if($null -ne $plainBytes){[Array]::Clear($plainBytes,0,$plainBytes.Length)}
        $credential=$null
    }
}

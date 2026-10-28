$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot 'log-protection-env.ps1')
$root=Join-Path ([IO.Path]::GetTempPath()) ('studio-log-env-'+[Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$previous=$env:STUDIO_TEST_LOG_DSN
try {
    $setupBlocked=$false
    try { & (Join-Path $PSScriptRoot 'runtime.ps1') -Action start -LogSetupDatabase studio_test } catch { $setupBlocked=$_.Exception.Message -match 'only available while loading setup environment' }
    if(-not $setupBlocked){throw 'Setup exclusion was accepted by normal startup'}
    $env:STUDIO_TEST_LOG_DSN='unchanged'
    Import-LogProtectionEnvironment -RuntimeDirectory $root -Database studio_test
    if($env:STUDIO_TEST_LOG_DSN -ne 'unchanged'){throw 'Missing optional setup changed environment'}
    $credential=@{format=1;database='studio_test';server_uuid='11111111-1111-1111-1111-111111111111';user=('backup_'+('a'*20));host='127.0.0.1';authority_schema='studio_authority';grant_profile='log-reader-v1';dsn='synthetic-connection-canary'}
    $plain=[Text.Encoding]::UTF8.GetBytes(($credential|ConvertTo-Json -Compress))
    $cipher=[Security.Cryptography.ProtectedData]::Protect($plain,$null,[Security.Cryptography.DataProtectionScope]::CurrentUser)
    [Array]::Clear($plain,0,$plain.Length)
    $credentialPath=Join-Path $root 'log-protection-studio_test.dpapi'
    $readyPath=Join-Path $root 'log-protection-studio_test.ready.json'
    [IO.File]::WriteAllBytes($credentialPath,$cipher)
    $ready=@{format=1;status='READY';database='studio_test';server_uuid=$credential.server_uuid;user=$credential.user;host='127.0.0.1';authority_schema='studio_authority';grant_profile='log-reader-v1';schema_version=31;table_fingerprint=('b'*64);credential_file='log-protection-studio_test.dpapi';credential_sha256=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($cipher)).ToLowerInvariant();verified_at=[DateTime]::UtcNow.ToString('O')}
    $ready|ConvertTo-Json|Set-Content -LiteralPath $readyPath -Encoding utf8
    Import-LogProtectionEnvironment -RuntimeDirectory $root -Database studio_test
    if($env:STUDIO_TEST_LOG_DSN -ne 'synthetic-connection-canary'){throw 'Verified credential was not loaded'}
    foreach($mutation in @('hash','identity','missing','malformed')) {
        $env:STUDIO_TEST_LOG_DSN='unchanged'
        $changed=$ready.Clone()
        if($mutation -eq 'hash'){$changed.credential_sha256='c'*64}
        if($mutation -eq 'identity'){$changed.server_uuid='22222222-2222-2222-2222-222222222222'}
        $changed|ConvertTo-Json|Set-Content -LiteralPath $readyPath -Encoding utf8
        if($mutation -eq 'missing'){Remove-Item -LiteralPath $readyPath}
        if($mutation -eq 'malformed'){'synthetic-secret-canary invalid json'|Set-Content -LiteralPath $readyPath}
        $failed=$false
        try {Import-LogProtectionEnvironment -RuntimeDirectory $root -Database studio_test} catch {$failed=$true;if($_.Exception.Message -match 'canary'){throw 'Secret exception leaked'}}
        if(-not $failed -or $env:STUDIO_TEST_LOG_DSN -ne 'unchanged'){throw "Invalid pair did not fail closed: $mutation"}
    }
    Write-Output 'Log protection environment: startup exclusion guard, optional absence, DPAPI pair, hash, identity, missing file and redacted error checks passed.'
} finally {
    $env:STUDIO_TEST_LOG_DSN=$previous
    # Only this newly created GUID directory may be removed.
    $full=[IO.Path]::GetFullPath($root)
    $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if(-not $full.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notmatch '^studio-log-env-[0-9a-f]{32}$'){throw 'Unexpected test cleanup path'}
    Remove-Item -LiteralPath $full -Recurse -Force
}

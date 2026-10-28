# Dedicated local log-reader setup

Documentation checked: 2026-10-28. Current implementation, installation and qualification states are maintained in [Implementation status](IMPLEMENTATION_STATUS.md).

The log collector uses a separate MySQL account. Setup creates or completes only that account; it does not initialize, migrate or change the source database or protected authority ledger. The source must already have the current checked migration history and protected provenance.

Run the local setup in the Windows account that runs Studio:

```powershell
./scripts/initialize-log-protection.ps1 -Business studio
```

`Start-Platform.ps1` initializes/re-audits the `studio` collector after the business migrations and protected authority setup, before launching the application. Historical setup verification used random test schemas and `studio_test`; that test scope is not a statement that the actual application has never been started. Current service installation and log-protection state are recorded in [Implementation status](IMPLEMENTATION_STATUS.md). Use `-Business studio_test` only when intentionally configuring that separate test database. A pending or invalid pair must be repaired with the explicit setup command before ordinary launch can proceed.

The account name is `backup_` plus the first 20 lowercase SHA256 digits of `server_uuid + NUL + database`. The account is restricted to host `127.0.0.1`. Its only global grants are `FLUSH_TABLES`, `REPLICATION CLIENT` and `REPLICATION SLAVE`; table SELECT covers the fixed backup/log source catalog, schema version/history and exactly the configured authority's `sources` table. No database wildcard SELECT, business/ledger writes, DDL, grant option, roles or proxy are accepted. MySQL permits the short global snapshot boundary with `FLUSH_TABLES`; broad `RELOAD` is unnecessary. See [MySQL FLUSH](https://dev.mysql.com/doc/refman/8.4/en/flush.html) and [MySQL privilege definitions](https://dev.mysql.com/doc/refman/8.4/en/privileges-provided.html).

MySQL replication privileges necessarily apply to the server's log stream; table SELECT grants do not restrict the replication protocol to one database. The collector independently filters and validates the fixed source contract. Use the dedicated local runtime instance and protect the reader credential accordingly.

The existing account is never reset or taken over. Repeating setup requires the matching protected password and an existing privilege subset without any excess grant. An administrator must investigate and explicitly remediate extra privileges before retrying; setup does not silently revoke them. A matching account at any other host is rejected as ambiguous.

Two files are stored outside business data and backups:

- `.runtime/log-protection-<database>.dpapi`: raw CurrentUser DPAPI bytes, no outer base64 and no optional entropy. The UTF8 JSON plaintext has `format:1`, `database`, lowercase `server_uuid`, `user`, `host:"127.0.0.1"`, `authority_schema`, `grant_profile:"log-reader-v1"` and the private `dsn`. The file is limited to 32 KiB and restricted to the current Windows SID and SYSTEM.
- `.runtime/log-protection-<database>.ready.json`: public `format:1`, `status:"READY"`, the same identity fields, `schema_version`, `table_fingerprint` (SHA256 of the fixed table/column contract plus authority sources scope), credential basename in `credential_file`, encrypted-file SHA256 in `credential_sha256`, and UTC `verified_at`. It contains no password or DSN and is limited to 16 KiB.

The encrypted file is durably created before CREATE USER. Account creation and grants implicitly commit in MySQL; if a later grant fails, the pending encrypted file is retained and no READY is published. The explicit setup-only environment load can resume that state; ordinary startup rejects incomplete pairs. Repetition authenticates with the same password, checks current grants and publishes READY last. Runtime startup independently checks live source identity and self-grants, so later privilege expansion is rejected.

READY means that dedicated account setup was verified. Continuous coverage, gaps, retained log integrity and recovery points are separate states reported by the log protection system. CurrentUser DPAPI also means that another Windows account or machine cannot use the copied credential file.

The command reads administrator credentials only from the temporary process variable `STUDIO_LOG_ADMIN_DSN`. The wrapper loads this from the existing encrypted local runtime credentials and restores the previous environment value afterward. Credentials are never command-line arguments or public output; errors contain a safe stage and, where available, only a MySQL numeric code.

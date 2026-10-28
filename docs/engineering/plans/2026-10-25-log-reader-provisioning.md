# Minimum privilege log-reader provisioning

> 历史工程方案：保留技术决策、接口与验证设计；文中的迁移版本和计划状态仅适用于对应历史节点。当前实现状态见[实现状态](../../IMPLEMENTATION_STATUS.md)，当前版本见[版本基线](../../COMMUNITY_STACK_BASELINE.md)。

Goal: provision a dedicated local MySQL reader without changing business data or taking over an existing account.

Architecture: a new setup command audits a fixed table-level privilege profile, protects the generated connection with CurrentUser DPAPI before account creation, and publishes a paired public READY file only after authenticating and checking the final grants. The runtime separately audits current self-grants; READY is setup evidence, not continuous protection evidence.

Tech stack: Go, database/sql, MySQL 8.4, Windows DPAPI, PowerShell. Spec: the parent-approved F08 contract; exact payload fields are documented in LOG_READER_SETUP.md.

Global constraints: no paid calls, production studio changes, credential logging, existing password resets, source migrations, business/authority writes, broad SELECT, roles, proxy or GRANT OPTION. Only global FLUSH_TABLES / REPLICATION CLIENT / REPLICATION SLAVE and SELECT on the fixed source catalog plus authority.sources. Preserve other agents' files.

## Tasks and tests

1. RED/GREEN fixed self-grant parser and exported backup.VerifyLogReaderScope(ctx, db, authoritySchema). Reject extra globals, wildcard/schema grants, column grants, roles, proxy, wrong identity, mandatory roles and missing required grants. Derive table fingerprint from the same fixed backup catalog.
2. RED/GREEN readercredentials protected single-file storage. Verify actual Windows DPAPI roundtrip, tamper rejection, redacted formatting, no plaintext, binding mismatch, exclusive creation and size/path bounds.
3. RED/GREEN backup.ProvisionLogReader and cmd/log-protection. Acquire a source/account advisory lock, validate source schema without migration, refuse unowned accounts, persist pending credentials before CREATE USER, audit subset before supplementing grants, authenticate and audit complete profile, publish READY last. Repetition preserves the same password/account.
4. Actual random database tests: source reads and log baseline succeed with the reader; writes/DDL/authority mutations fail. Existing extra privileges or missing credential refuse without changes. Induce failure after implicit account creation then resume from the pending encrypted file. Run only with the coordinated MySQL DDL lease.
5. Add initialize-log-protection.ps1, run explicitly authorized studio_test setup twice and validate runtime loader compatibility. Never run production studio setup. Format, unit/integration tests, vet and independent review before exact-file commit.

## Review focus

- No credential in errors/arguments/public metadata; raw SQL errors become safe setup errors.
- Existing account ownership requires the protected matching password; no ALTER/DROP USER.
- DCL implicit commits are recoverable and no incomplete setup publishes READY.
- Self-grants reject broader or role-derived privileges even after setup.
- Schema/table catalog changes fail closed until explicit re-provisioning; no claim that successful setup proves log coverage.

Self-review: source identity, privilege parsing, filesystem pairing and interrupted DCL are separate test boundaries; actual reader execution is required in addition to parser tests. Runtime loading and production entry wiring are integration boundaries outside this component scope.

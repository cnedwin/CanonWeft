# Consistent backup and isolated restore engine

Documentation checked: 2026-10-28. Current implementation, installation and qualification states are maintained in [Implementation status](IMPLEMENTATION_STATUS.md).

This document defines the F08 consistent-snapshot and isolated-restore component. Schema 28 introduced capability evaluation/qualification records alongside protected authority provenance, private streaming previews and actor-scoped working indexes; schemas 24–27 retain explicit trusted upgrades. Later schema support follows the compiled catalog and its version-specific conversions. It restores application content, metadata and accounting into a new disposable database and a new blob directory. It never starts the restored application or any worker.

## Automatic checkpoint protection

The real `cmd/studio` process now owns a backup runner for its entire lifetime. Its default repository is `backups/<database>` beside the configured blob directory, so production and test databases cannot share a repository. `STUDIO_BACKUP_DIRECTORY` may select a dedicated absolute directory. It must not be an unrelated nonempty directory, source blob directory, vault or database directory. Initialization binds the exact MySQL server UUID/database and takes an OS-released single-writer lock; a crashed process does not leave a live lock lease behind.

The runner checks every minute, creates a daily baseline and supplementary ten-minute data-only checkpoints, and validates the actual durable copy before committing an immutable recovery-point record. The copy includes all referenced objects, with the same exclusions and current-authority boundaries as manual backups. A failed or cancelled attempt never advances the successful snapshot timestamp. A restart reuses a fresh committed point. The Vue workspace's owner-only “备份保护” page shows the actual recovery-point time separately from attempt time and failure state. Polling this page does not reset unsaved project fields.

Committed archives expire at snapshot time plus 35 days, including the last remaining archive; retaining it forever would break the deletion deadline. The resulting missing/stale protection is visible. Interrupted imports/database captures have durable ownership intents and a 24-hour cleanup grace. Cleanup removes exact owned filenames only, never unrelated files or recursive paths. Atomic status writes contain no credentials or source content. Archive integrity validation is bounded and in memory; the directory is not an encrypted store.

**This scheduled snapshot fallback is not MySQL binary-log protection.** Continuous log capture, exact baseline/log coordinates, isolated replay, recovery activation, long-lived current-authority protection, deleted-project handling and online content/object retention are separate F08 components; their current integration is recorded in [Implementation status](IMPLEMENTATION_STATUS.md) and [Log recovery](LOG_RECOVERY.md). Snapshot frequency alone establishes neither continuous-log coverage nor independent-media qualification. The protection page must report those states separately. Closing the application stops this runner; no hidden operating-system scheduled task is installed.

## Run locally

Use PowerShell 7 with the existing local runtime initialized. Paths below are examples; the parent directories must already exist. Never place backups in the vault, database or source blob directories.

```powershell
./scripts/create-backup.ps1 -Archive 'D:\Backups\studio-20261025.zip'
./scripts/restore-backup.ps1 -Archive 'D:\Backups\studio-20261025.zip' -BlobRoot 'D:\Restore\studio-20261025-blobs'
```

The restore wrapper generates `studio_restore_<32 lowercase hex>` and uses the local runtime root credential in process memory to create that isolated database. It never prints that credential or passes it as a process argument. All pre-existing databases, including empty ones, are refused. A pre-existing blob destination is also refused. `studio`, `studio_test`, Temporal databases and arbitrary names cannot be restore targets.

The executable can also be used directly from `backend`:

```powershell
go run ./cmd/backup create -archive 'D:\Backups\studio.zip' -blobs 'D:\StudioBlobs'
go run ./cmd/backup verify -archive 'D:\Backups\studio.zip'
go run ./cmd/backup restore -archive 'D:\Backups\studio.zip' -blobs 'D:\Restore\new-blobs'
```

`create` reads the source connection from `STUDIO_DSN`; `restore` reads its destination connection from `STUDIO_RESTORE_DSN` and its protected authority connection from `STUDIO_AUTHORITY_DSN` (falling back to the source environment). For restore, the authority connection's business database name is cleared before opening, so a deleted original business schema is not required. Optional `-source-env` / `-target-env` / `-authority-env` select environment-variable names, never literal credentials. The target account needs permission to create a new isolated schema and run the bundled migrations. `verify` is offline integrity validation only; it does not verify current revocations, archival authenticity or recovery eligibility.

## Consistency and format

- A pinned connection obtains the same database advisory schema lock used by `storage.RunMigrations`. It then explicitly starts a read-only REPEATABLE READ transaction with `WITH CONSISTENT SNAPSHOT`. All application-table reads use that transaction. Normal application writes can continue. Unmanaged concurrent DDL is not supported and must not run.
- The fixed compiled table/column catalog for the supported source schema is checked against the source; schema 28 is one explicitly cataloged historical version. Every application table must be InnoDB. Unknown tables/columns, missing tables, newer versions and any migration checksum mismatch fail closed. Legacy archives cannot inject new working-index or qualification tables; old index jobs are explicitly converted to published jobs.
- `manifest.json` records the source server UUID and database, observed snapshot time, schema, every embedded migration's normalized SHA-256 and original `EXECUTED` / `LEGACY_BASELINE` provenance, explicit exclusions and every payload file's hash and byte length. A legacy baseline is historical provenance, not proof that its early migrations were executed by the current migration runner.
- `data.json` stores rows using fixed column order and base64 byte cells, with SQL NULL distinct from empty bytes. No archive SQL or restoration hook is executed. The restore engine creates tables only from its bundled trusted migrations and inserts rows with parameterized values.
- Required immutable objects are enumerated from actual IP, release manifest, resource, imported-source, skill package/source archive, generation input/output, original output hashes in model-call receipts (including cancelled late and conflicting results), tool trial and knowledge references. Each object is read by project and content hash and independently verified. An object written after the snapshot that is not referenced by the snapshot is omitted. A missing/corrupt snapshot-referenced object fails the entire backup.
- ZIP paths are restricted to `manifest.json`, `data.json` and `blobs/<project-id>/<sha256>`. Traversal, backslashes, absolute paths, links, special entries, executable-mode entries, duplicates, unknown entries, missing/unreferenced objects and hash/size mismatches are rejected. Nothing is extracted by archive filename.
- Current bounded implementation: archive and total uncompressed bytes at most 256 MiB, row JSON at most 128 MiB, each blob at most 32 MiB, manifest at most 1 MiB, at most 10,000 entries, and bounded row collection. It validates in memory. Large-media streaming and larger-capacity qualification are pending. SHA-256 detects corruption; the archive is not signed or encrypted.

MySQL reference: [START TRANSACTION / consistent snapshot](https://dev.mysql.com/doc/refman/8.4/en/commit.html) and [consistent nonlocking reads and DDL limitations](https://dev.mysql.com/doc/refman/8.4/en/innodb-consistent-read.html). A REPEATABLE READ snapshot applies to all consistent reads in one transaction; `WITH CONSISTENT SNAPSHOT` does not itself change isolation level.

## Current authority and deletion boundary

`CurrentAuthority` is a trusted infrastructure interface, never loaded from the archive. The CLI uses `ProtectedAuthority`: a separately provisioned, append-only ledger excluded from every business archive. It binds the original server UUID/database to an immutable ledger ID/protocol and complete initial baseline. Restore checks current project deletions and Skill/capability revocations before creating the destination, then reads and validates them again immediately before committing restored rows. See `PROTECTED_AUTHORITY.md` for provisioning and permission boundaries.

Current skill and capability revocations are merged into matching restored versions; historical revocations already in the archive remain. Skill state becomes REVOKED, and existing parent/upstream identifiers remain so lineage checks can still reject descendants. Revocations of newer versions absent from the snapshot need no restored row.

If any snapshot project is now absent from the current source, restore refuses the whole archive. This component-level refusal prevents deletion resurrection; any separate project-subset recovery must satisfy the current protected recovery policy. No arbitrary JSON file with `complete=true` is accepted as proof of current authority. Failure to connect, identity mismatch, incomplete/invalid history or missing evidence refuses restoration.

Recovery after loss of the original business database is supported while its protected ledger remains available, including a tested actual DROP of a generated temporary source database. The default ledger shares the MySQL instance and disk; this does not establish whole-server/disk disaster protection. Deleted-project handling and recovery activation follow the current F08 recovery workflow; this component does not grant reactivation authority. The check describes the time recorded in `authority_checked_at`; it is not an ongoing synchronization subscription. All restored access remains disabled, and reopening the target requires a fresh authority check and a separate recovery procedure.

## Secrets, permissions and side effects

The archive does not read the vault, master-key directory, runtime configuration, environment secrets or Temporal databases. It clears database credential references and password hashes, and excludes sessions, project memberships and role-to-model bindings. Schemas through35 also exclude outbox. Schema36 exports preserve event identities and exact payloads because their retention source history must remain verifiable; the manifest explicitly excludes pending delivery instead. In the archive only, an unprocessed event receives processed_at=created_at as a deterministic delivery-suppression marker, never as evidence that its business action executed. Already processed timestamps are retained. Snapshot and binlog normalization use the same rule; restore rejects pending events and does not alter the source queue. Historical36 archives with the old exclusions remain readable; continued capture cannot introduce event history under that old manifest and requires a fresh baseline through existing capture-gap recovery. All restored users, spending budgets and discovery sources are disabled. The system-managed key/password/session exclusion is not a detector for secrets a user may have pasted into their own documents or uploaded files; application content remains private data.

Pending generation/review/index/discovery/tool work is stopped; cancellation/paused flags are set where available and tool execution leases are cleared. Nonterminal RESERVED/DISPATCHED/RUNNING model calls become UNKNOWN, preserving reserved amounts, receipts and accounting for reconciliation. No unknown paid call is automatically resubmitted or refunded. Existing final results remain content evidence, not permission to dispatch again. Credentials and model bindings must be recreated separately.

All actor-scoped working indexes restore with `enabled=0` and every working-index job restores as CANCELLED, including formerly READY jobs. Private chunks and vectors remain content evidence tied to their original disabled actor, never an active permission grant. Private source hashes address SQL content, not blob objects. A new authorized build is required after separate identity reactivation; see `PRIVATE_WORKING_INDEX.md`.

Schema 28 explicitly catalogs `qualification_runs`, `qualification_steps`, `capability_qualifications`, `capability_bindings`, `capability_binding_heads`, `qualification_approvals` and `qualification_schedules`. Qualification `report_hash` is a referenced object root. Restored evaluation runs are CANCELLED and qualification/binding/schedule enablement is cleared. Older formats receive explicit empty qualification tables. Current protected capability revocations are still rechecked; an archived report or binding head never authorizes automatic reactivation.

Temporal history, schedules, visibility and task queues are intentionally not backed up or restored by this engine. It does not offer workflow-resume or exactly-once external-provider recovery. A recovery operator must reconcile outstanding charges/results and establish an isolated runtime/Temporal configuration before enabling any new work. A successful destination contains `RESTORED_ISOLATED_REBIND_REQUIRED`; incomplete attempts retain `RESTORE_INCOMPLETE` when the blob phase was reached. This marker is advisory, not a substitute for the database restrictions above. Failed disposable targets are retained for inspection, never silently retried in place.

## Validation and measurements

Run pure/format tests without MySQL:

```powershell
cd backend
go test ./internal/backup ./tests/backup ./cmd/backup -count=1
go vet ./internal/backup ./tests/backup ./cmd/backup
```

Real integration tests require `STUDIO_BACKUP_TEST_ADMIN_DSN` supplied in process memory. They create only uniquely generated `studio_backup_test_<32hex>` source schemas and `studio_restore_<32hex>` destinations, run real migrations, and clean up only those names. They never reset `studio_test` or touch the live application schema. Without that environment variable, MySQL cases explicitly skip.

Covered behavior includes a real round trip with post-backup skill/capability revocations, content-hash verification, absent credentials and grants, preserved unknown-call reservation, existing/live target refusal, missing/wrong authority, current project deletion refusal before destination writes, missing/corrupt objects, migration drift and concurrent multi-table commits. ZIP attack cases start from a valid archive, mutate one property and assert the corresponding guard reason; they cover path traversal, absolute/backslash paths, links, executable hooks, duplicate entries and inflation limits. Receipt regressions call the real settlement service to produce cancelled late and conflicting outputs, read back all three restored objects and reject missing/corrupt receipt-only objects. Two additional real-database tests add revocations after the first authority read and require the final read to replay them, or fail that final read and verify all restored business rows roll back.

A local synthetic run on 2026-10-25 Asia/Kuala_Lumpur measured a ten-object / 21-row snapshot at 65 ms and isolated restore at 931 ms. The snapshot was observed at `2026-10-24T16:52:06.4375136Z`. These are fixture measurements, not a production benchmark. JSON output includes observed snapshot time, elapsed milliseconds, object count/bytes and row count; restore adds destination identity and authority check time. The snapshot timestamp is the application-observed time immediately after the consistent-snapshot statement, not a binlog position or commit timestamp.

The RPO <= 15 minutes / RTO <= 8 hours targets are **not established** by these measurements or by snapshot frequency. Automatic checkpoint scheduling, owner status UI, 35-day snapshot expiry and interrupted-capture cleanup have engineering verification recorded in [Implementation status](IMPLEMENTATION_STATUS.md). Continuous logs, deletion/limited recovery, retention and recovery/rebinding are assessed through their complete current workflows, rather than through this historical snapshot measurement. Independent backup media, large-data capacity and actual recovery targets require separate qualification. Protected deletion/revocation authority and recovery without the original business database are implemented and separately tested.

The log capture and replay contract follows MySQL's [binary-log backup interface](https://dev.mysql.com/doc/refman/8.4/en/mysqlbinlog.html) and [consistent baseline coordinates](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html). These references define protocol requirements; actual log coverage, replay checks and qualification are recorded separately in [Log recovery](LOG_RECOVERY.md) and [Implementation status](IMPLEMENTATION_STATUS.md).

SET @qualification_origin_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='qualification_calls' AND column_name='origin') = 0,
 'ALTER TABLE qualification_calls ADD COLUMN origin VARCHAR(24) NOT NULL DEFAULT ''NATIVE_29''',
 'SELECT 1'
);
PREPARE qualification_origin_migration FROM @qualification_origin_ddl;
EXECUTE qualification_origin_migration;
DEALLOCATE PREPARE qualification_origin_migration;
SET @qualification_legacy_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='qualification_calls' AND column_name='legacy_source') = 0,
 'ALTER TABLE qualification_calls ADD COLUMN legacy_source JSON NULL',
 'SELECT 1'
);
PREPARE qualification_legacy_migration FROM @qualification_legacy_ddl;
EXECUTE qualification_legacy_migration;
DEALLOCATE PREPARE qualification_legacy_migration;

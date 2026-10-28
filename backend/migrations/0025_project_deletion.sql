SET @project_deletion_ddl = IF(
  (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='projects' AND column_name='deleted_at') = 0,
  'ALTER TABLE projects ADD COLUMN deleted_at DATETIME(6) NULL',
  'SELECT 1'
);
PREPARE project_deletion_migration FROM @project_deletion_ddl;
EXECUTE project_deletion_migration;
DEALLOCATE PREPARE project_deletion_migration;

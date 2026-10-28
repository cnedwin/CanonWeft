SET @private_index_ddl = IF(
 (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='index_jobs' AND column_name='kind') = 0,
 'ALTER TABLE index_jobs ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT ''published''',
 'SELECT 1'
);
PREPARE private_index_migration FROM @private_index_ddl;
EXECUTE private_index_migration;
DEALLOCATE PREPARE private_index_migration;
CREATE TABLE IF NOT EXISTS working_indexes (
 id CHAR(64) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, manifest JSON NOT NULL, model_revision CHAR(40) NOT NULL,
 chunks INT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(actor_id) REFERENCES users(id), INDEX(project_id,branch_id,actor_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS working_chunks (
 id CHAR(64) PRIMARY KEY, index_id CHAR(64) NOT NULL, source_key CHAR(64) NOT NULL,
 source_name VARCHAR(160) NOT NULL, ref_kind VARCHAR(32) NOT NULL, ref_id CHAR(32) NOT NULL,
 ref_version VARCHAR(128) NOT NULL, part_number INT NOT NULL, source_hash CHAR(64) NOT NULL,
 start_rune INT NOT NULL, end_rune INT NOT NULL, content TEXT NOT NULL,
 vector BLOB NOT NULL, vector_hash CHAR(64) NOT NULL,
 FOREIGN KEY(index_id) REFERENCES working_indexes(id), INDEX(index_id,id),
 FULLTEXT INDEX working_text(content) WITH PARSER ngram
) ENGINE=InnoDB;

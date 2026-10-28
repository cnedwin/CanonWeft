CREATE TABLE IF NOT EXISTS source_imports (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, filename VARCHAR(160) NOT NULL, source_hash CHAR(64) NOT NULL,
 source_size BIGINT NOT NULL, report JSON NOT NULL, request_hash CHAR(64) NOT NULL,
 operation_id VARCHAR(120) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE(actor_id,operation_id), FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS source_import_candidates (
 import_id CHAR(32) NOT NULL, resource_id CHAR(32) NOT NULL, release_id CHAR(32) NOT NULL,
 PRIMARY KEY(import_id,release_id), FOREIGN KEY(import_id) REFERENCES source_imports(id),
 FOREIGN KEY(resource_id) REFERENCES resources(id), FOREIGN KEY(release_id) REFERENCES artifact_releases(id)
) ENGINE=InnoDB;

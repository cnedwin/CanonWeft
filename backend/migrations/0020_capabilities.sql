CREATE TABLE IF NOT EXISTS capability_versions (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL,
 capability_key VARCHAR(80) NOT NULL, kind VARCHAR(16) NOT NULL,
 parent_id CHAR(32) NOT NULL DEFAULT '', content_hash CHAR(64) NOT NULL,
 definition JSON NOT NULL, created_by CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(created_by) REFERENCES users(id),
 INDEX(project_id,capability_key,created_at), INDEX(parent_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capability_heads (
 project_id CHAR(32) NOT NULL, capability_key VARCHAR(80) NOT NULL,
 version_id CHAR(32) NOT NULL,
 PRIMARY KEY(project_id,capability_key),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(version_id) REFERENCES capability_versions(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capability_revocations (
 version_id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, reason VARCHAR(1000) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(version_id) REFERENCES capability_versions(id), FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS skill_packages (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL,
 parent_id CHAR(32) NOT NULL DEFAULT '', upstream_id CHAR(32) NOT NULL DEFAULT '',
 bundle_hash CHAR(64) NOT NULL, report JSON NOT NULL, source JSON NOT NULL,
 state VARCHAR(24) NOT NULL DEFAULT 'CANDIDATE', created_by CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(created_by) REFERENCES users(id),
 INDEX(project_id,created_at), INDEX(parent_id), INDEX(upstream_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS skill_revocations (
 package_id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, reason VARCHAR(1000) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(package_id) REFERENCES skill_packages(id), FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;

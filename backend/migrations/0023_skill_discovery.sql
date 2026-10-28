CREATE TABLE IF NOT EXISTS skill_discovery_sources (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 repository VARCHAR(201) NOT NULL, source_ref VARCHAR(120) NOT NULL, source_path VARCHAR(240) NOT NULL,
 keyword VARCHAR(120) NOT NULL DEFAULT '', enabled BOOLEAN NOT NULL DEFAULT FALSE,
 candidate_limit INT NOT NULL DEFAULT 3, revision BIGINT NOT NULL DEFAULT 1,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS skill_discovery_runs (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, source_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, source_revision BIGINT NOT NULL, source_snapshot JSON NOT NULL,
 state VARCHAR(24) NOT NULL DEFAULT 'QUEUED', reason VARCHAR(600) NOT NULL DEFAULT '',
 inventory JSON NULL, done_count INT NOT NULL DEFAULT 0, total_count INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(source_id) REFERENCES skill_discovery_sources(id), FOREIGN KEY(project_id) REFERENCES projects(id),
 INDEX discovery_daily(project_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS skill_discovery_candidates (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, source_id CHAR(32) NOT NULL, run_id CHAR(32) NOT NULL,
 source_path VARCHAR(240) NOT NULL, source_commit CHAR(40) NOT NULL, package_id CHAR(32) NOT NULL,
 previous_id CHAR(32) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY discovery_version(source_id,source_path,source_commit),
 FOREIGN KEY(source_id) REFERENCES skill_discovery_sources(id), FOREIGN KEY(run_id) REFERENCES skill_discovery_runs(id),
 FOREIGN KEY(package_id) REFERENCES skill_packages(id), FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;

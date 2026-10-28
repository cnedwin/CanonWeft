CREATE TABLE IF NOT EXISTS tool_trials (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 capability_id CHAR(32) NOT NULL, frozen JSON NOT NULL,
 state VARCHAR(24) NOT NULL DEFAULT 'QUEUED', reason VARCHAR(600) NOT NULL DEFAULT '',
 lease_id CHAR(32) NOT NULL DEFAULT '', output_hash CHAR(64) NOT NULL DEFAULT '',
 elapsed_ms BIGINT NOT NULL DEFAULT 0, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(actor_id) REFERENCES users(id),
 FOREIGN KEY(capability_id) REFERENCES capability_versions(id), INDEX(project_id,created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS index_jobs (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, index_id CHAR(64) NOT NULL,
 release_id CHAR(32) NOT NULL DEFAULT '', candidate_id CHAR(32) NOT NULL DEFAULT '',
 state VARCHAR(20) NOT NULL DEFAULT 'QUEUED', reason VARCHAR(300) NOT NULL DEFAULT '',
 total_sources INT NOT NULL, done_sources INT NOT NULL DEFAULT 0, chunks INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(actor_id) REFERENCES users(id), INDEX(project_id,branch_id,created_at)
) ENGINE=InnoDB;

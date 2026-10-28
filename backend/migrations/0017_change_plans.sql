CREATE TABLE IF NOT EXISTS change_plans (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 candidate_id CHAR(32) NOT NULL, payload JSON NOT NULL, content_hash CHAR(64) NOT NULL,
 created_by CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(candidate_id) REFERENCES ip_revisions(id), FOREIGN KEY(created_by) REFERENCES users(id),
 INDEX(project_id,created_at)
) ENGINE=InnoDB;

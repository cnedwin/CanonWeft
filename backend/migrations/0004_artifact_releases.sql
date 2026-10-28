CREATE TABLE IF NOT EXISTS resources (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 name VARCHAR(160) NOT NULL, kind VARCHAR(40) NOT NULL, head_revision CHAR(32) NOT NULL DEFAULT '',
 content_hash CHAR(64) NOT NULL DEFAULT '', revision BIGINT NOT NULL DEFAULT 1,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id), INDEX(project_id,branch_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS artifact_releases (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 manifest JSON NOT NULL, content_hash CHAR(64) NOT NULL,
 status ENUM('PREPARED','READY','PUBLISHED') NOT NULL DEFAULT 'PREPARED',
 created_by CHAR(32) NOT NULL, adopted_by CHAR(32), adopted_hash CHAR(64),
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id), INDEX(project_id,created_at)
) ENGINE=InnoDB;

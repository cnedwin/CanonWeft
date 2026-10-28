CREATE TABLE IF NOT EXISTS branches (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, name VARCHAR(120) NOT NULL,
 parent_id CHAR(32) NOT NULL DEFAULT '', base_revision CHAR(32) NOT NULL DEFAULT '',
 head_revision CHAR(32) NOT NULL DEFAULT '', revision BIGINT NOT NULL DEFAULT 1,
 FOREIGN KEY(project_id) REFERENCES projects(id), UNIQUE(project_id,name)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ip_revisions (
 id CHAR(32) PRIMARY KEY, branch_id CHAR(32) NOT NULL, base_head CHAR(32) NOT NULL,
 payload JSON NOT NULL, content_hash CHAR(64) NOT NULL,
 status ENUM('PREPARED','READY','PUBLISHED') NOT NULL DEFAULT 'PREPARED',
 created_by CHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(branch_id) REFERENCES branches(id), INDEX(branch_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS adoptions (
 candidate_id CHAR(32) PRIMARY KEY, actor_id CHAR(32) NOT NULL, content_hash CHAR(64) NOT NULL,
 adopted_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(candidate_id) REFERENCES ip_revisions(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS outbox (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, kind VARCHAR(80) NOT NULL,
 resource_id CHAR(32) NOT NULL, payload JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), processed_at DATETIME(6),
 UNIQUE(kind,resource_id), FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;

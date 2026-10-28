-- Native novel entry. Private setup drafts exist before a project is created.
CREATE TABLE IF NOT EXISTS novel_setup_drafts (
 id CHAR(32) NOT NULL PRIMARY KEY,
 workspace_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL,
 step INT NOT NULL,
 payload MEDIUMTEXT NOT NULL,
 content_hash CHAR(64) NOT NULL,
 confirmed_novel_id CHAR(32) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id),
 FOREIGN KEY(actor_id) REFERENCES users(id),
 INDEX(workspace_id,actor_id,updated_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS novels (
 id CHAR(32) NOT NULL PRIMARY KEY,
 project_id CHAR(32) NOT NULL,
 branch_id CHAR(32) NOT NULL,
 created_by CHAR(32) NOT NULL,
 setup_id CHAR(32) NOT NULL UNIQUE,
 settings_revision BIGINT NOT NULL,
 stage VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(created_by) REFERENCES users(id),
 FOREIGN KEY(setup_id) REFERENCES novel_setup_drafts(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS novel_settings_versions (
 novel_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL,
 payload MEDIUMTEXT NOT NULL,
 content_hash CHAR(64) NOT NULL,
 created_by CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(novel_id,revision),
 FOREIGN KEY(novel_id) REFERENCES novels(id),
 FOREIGN KEY(created_by) REFERENCES users(id)
) ENGINE=InnoDB;

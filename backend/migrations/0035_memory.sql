CREATE TABLE IF NOT EXISTS memory_sessions (
 id CHAR(32) NOT NULL PRIMARY KEY,
 project_id CHAR(32) NOT NULL,
 branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,
 document JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX(project_id,branch_id,actor_id,updated_at),
 CONSTRAINT memory_session_revision_positive CHECK (revision > 0),
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS memory_items (
 id CHAR(32) NOT NULL PRIMARY KEY,
 project_id CHAR(32) NOT NULL,
 branch_id CHAR(32) NOT NULL,
 head_revision CHAR(32) NOT NULL,
 kind VARCHAR(32) NOT NULL,
 state VARCHAR(24) NOT NULL,
 actor_id CHAR(32) NULL,
 session_id CHAR(32) NULL,
 document JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX(project_id,branch_id,state,kind),
 INDEX(session_id,actor_id),
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(actor_id) REFERENCES users(id),
 FOREIGN KEY(session_id) REFERENCES memory_sessions(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS memory_revisions (
 id CHAR(32) NOT NULL PRIMARY KEY,
 project_id CHAR(32) NOT NULL,
 item_id CHAR(32) NOT NULL,
 parent_id CHAR(32) NULL,
 document JSON NOT NULL,
 actor_id CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX(project_id,item_id,created_at),
 FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(item_id) REFERENCES memory_items(id),
 FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS model_prices (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, connection_id CHAR(32) NOT NULL,
 connection_revision BIGINT NOT NULL, payload JSON NOT NULL, content_hash CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id), FOREIGN KEY(connection_id,connection_revision) REFERENCES model_connection_versions(connection_id,revision)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS generation_tasks (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, project_id CHAR(32) NOT NULL,
 branch_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL, operation_id VARCHAR(120) NOT NULL,
 request_hash CHAR(64) NOT NULL, input_hash CHAR(64) NOT NULL, frozen JSON NOT NULL,
 credential_ref CHAR(64) NOT NULL, state VARCHAR(30) NOT NULL, call_id CHAR(32) NOT NULL DEFAULT '',
 paused BOOLEAN NOT NULL DEFAULT FALSE, cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE(actor_id,operation_id), INDEX(project_id,created_at),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS model_connections (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, revision BIGINT NOT NULL,
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS model_connection_versions (
 connection_id CHAR(32) NOT NULL, revision BIGINT NOT NULL, config JSON NOT NULL,
 credential_ref CHAR(64) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(connection_id,revision), FOREIGN KEY(connection_id) REFERENCES model_connections(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS model_bindings (
 workspace_id CHAR(32) NOT NULL, role VARCHAR(40) NOT NULL, connection_id CHAR(32) NOT NULL,
 connection_revision BIGINT NOT NULL, revision BIGINT NOT NULL,
 PRIMARY KEY(workspace_id,role), FOREIGN KEY(workspace_id) REFERENCES workspaces(id),
 FOREIGN KEY(connection_id,connection_revision) REFERENCES model_connection_versions(connection_id,revision)
) ENGINE=InnoDB;

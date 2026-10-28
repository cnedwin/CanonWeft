CREATE TABLE IF NOT EXISTS workspaces (
 id CHAR(32) PRIMARY KEY, name VARCHAR(120) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS users (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, username VARCHAR(80) NOT NULL UNIQUE,
 password_hash VARCHAR(128) NOT NULL, role ENUM('owner','editor','publisher','reader') NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sessions (
 token_hash CHAR(64) PRIMARY KEY, user_id CHAR(32) NOT NULL, expires_at DATETIME(6) NOT NULL,
 FOREIGN KEY(user_id) REFERENCES users(id), INDEX(user_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS projects (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, name VARCHAR(120) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id), INDEX(workspace_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS project_members (
 project_id CHAR(32) NOT NULL, user_id CHAR(32) NOT NULL,
 role ENUM('owner','editor','publisher','reader') NOT NULL,
 PRIMARY KEY(project_id,user_id), FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(user_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS operations (
 actor_id CHAR(32) NOT NULL, operation_id VARCHAR(120) COLLATE utf8mb4_bin NOT NULL,
 kind VARCHAR(40) NOT NULL, request_hash CHAR(64) NOT NULL, result_id CHAR(32) NOT NULL,
 PRIMARY KEY(actor_id,operation_id), FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS audit_events (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, workspace_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 action VARCHAR(60) NOT NULL, resource_id CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX(workspace_id,created_at)
) ENGINE=InnoDB;

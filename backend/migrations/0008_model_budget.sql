CREATE TABLE IF NOT EXISTS model_budgets (
 workspace_id CHAR(32) PRIMARY KEY, currency CHAR(3) NOT NULL, limit_micros BIGINT NOT NULL,
 per_call_micros BIGINT NOT NULL, spent_micros BIGINT NOT NULL DEFAULT 0,
 reserved_micros BIGINT NOT NULL DEFAULT 0, enabled BOOLEAN NOT NULL DEFAULT FALSE, revision BIGINT NOT NULL,
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS model_calls (
 id CHAR(32) PRIMARY KEY, workspace_id CHAR(32) NOT NULL, project_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 operation_id VARCHAR(120) COLLATE utf8mb4_bin NOT NULL, input_hash CHAR(64) NOT NULL,
 reserved_micros BIGINT NOT NULL, charged_micros BIGINT NOT NULL DEFAULT 0,
 state VARCHAR(30) NOT NULL, cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
 provider_receipt VARCHAR(200) NOT NULL DEFAULT '', output_hash VARCHAR(64) NOT NULL DEFAULT '', settlement_hash VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE(actor_id,operation_id), INDEX(workspace_id,state), FOREIGN KEY(workspace_id) REFERENCES workspaces(id),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;

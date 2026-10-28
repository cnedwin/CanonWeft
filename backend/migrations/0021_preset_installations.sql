CREATE TABLE IF NOT EXISTS capability_preset_steps (
 project_id CHAR(32) NOT NULL, step_key VARCHAR(120) NOT NULL,
 kind VARCHAR(24) NOT NULL, request_hash CHAR(64) NOT NULL,
 result_id CHAR(32) NOT NULL, created_by CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(project_id,step_key), FOREIGN KEY(project_id) REFERENCES projects(id)
) ENGINE=InnoDB;

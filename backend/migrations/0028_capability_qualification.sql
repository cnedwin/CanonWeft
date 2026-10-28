CREATE TABLE IF NOT EXISTS qualification_runs (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, frozen LONGTEXT NOT NULL,
 state VARCHAR(24) NOT NULL DEFAULT 'QUEUED', reason VARCHAR(1000) NOT NULL DEFAULT '',
 paused BOOLEAN NOT NULL DEFAULT FALSE, cancelled BOOLEAN NOT NULL DEFAULT FALSE,
 report_hash CHAR(64) NOT NULL DEFAULT '', qualification_id CHAR(32) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id),
 FOREIGN KEY(actor_id) REFERENCES users(id), INDEX(project_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_steps (
 id CHAR(32) PRIMARY KEY, run_id CHAR(32) NOT NULL, project_id CHAR(32) NOT NULL,
 ordinal INT NOT NULL, arm VARCHAR(16) NOT NULL, case_key VARCHAR(80) NOT NULL, repeat_index INT NOT NULL,
 child_id CHAR(32) NOT NULL DEFAULT '', child_kind VARCHAR(16) NOT NULL DEFAULT '',
 state VARCHAR(24) NOT NULL DEFAULT 'NOT_RUN', metrics LONGTEXT NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(run_id) REFERENCES qualification_runs(id), UNIQUE KEY(run_id,ordinal)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capability_qualifications (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, run_id CHAR(32) NOT NULL,
 payload LONGTEXT NOT NULL, report_hash CHAR(64) NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE,
 expires_at DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(run_id) REFERENCES qualification_runs(id), UNIQUE KEY(run_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capability_bindings (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, task_kind VARCHAR(100) NOT NULL,
 qualification_id CHAR(32) NOT NULL, previous_id CHAR(32) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE, created_by CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(qualification_id) REFERENCES capability_qualifications(id), INDEX(project_id,task_kind)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS capability_binding_heads (
 project_id CHAR(32) NOT NULL, task_kind VARCHAR(100) NOT NULL, binding_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1, PRIMARY KEY(project_id,task_kind),
 FOREIGN KEY(binding_id) REFERENCES capability_bindings(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_approvals (
 run_id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 payload LONGTEXT NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(run_id) REFERENCES qualification_runs(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_schedules (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 config LONGTEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 revision BIGINT NOT NULL DEFAULT 1, last_week VARCHAR(20) NOT NULL DEFAULT '',
 reason VARCHAR(1000) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), UNIQUE KEY(project_id)
) ENGINE=InnoDB;

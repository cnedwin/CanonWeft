CREATE TABLE IF NOT EXISTS qualification_week_grants (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, week_key VARCHAR(12) NOT NULL,
 schedule_id CHAR(32) NOT NULL, schedule_revision BIGINT NOT NULL,
 currency VARCHAR(3) NOT NULL DEFAULT '', cap_micros BIGINT NOT NULL DEFAULT 0,
 reserved_micros BIGINT NOT NULL DEFAULT 0, spent_micros BIGINT NOT NULL DEFAULT 0,
 enabled BOOLEAN NOT NULL DEFAULT TRUE, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY(project_id,week_key), FOREIGN KEY(project_id) REFERENCES projects(id),
 FOREIGN KEY(schedule_id) REFERENCES qualification_schedules(id),
 CHECK(cap_micros>=0), CHECK(reserved_micros>=0), CHECK(spent_micros>=0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS qualification_calls (
 call_id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, run_id CHAR(32) NOT NULL,
 step_id CHAR(32) NOT NULL, grant_id CHAR(32) NOT NULL DEFAULT '',
 reserved_micros BIGINT NOT NULL, held_micros BIGINT NOT NULL,
 charged_micros BIGINT NOT NULL DEFAULT 0, state VARCHAR(24) NOT NULL DEFAULT 'RESERVED',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(call_id) REFERENCES model_calls(id), FOREIGN KEY(run_id) REFERENCES qualification_runs(id),
 FOREIGN KEY(step_id) REFERENCES qualification_steps(id), UNIQUE KEY(step_id),
 CHECK(reserved_micros>=0), CHECK(held_micros>=0), CHECK(charged_micros>=0)
) ENGINE=InnoDB;

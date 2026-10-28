CREATE TABLE IF NOT EXISTS model_call_accounting (
 call_id CHAR(32) PRIMARY KEY, currency CHAR(3) NOT NULL, revision BIGINT NOT NULL DEFAULT 0,
 FOREIGN KEY(call_id) REFERENCES model_calls(id)
) ENGINE=InnoDB;
INSERT IGNORE INTO model_call_accounting(call_id,currency)
 SELECT c.id,COALESCE((SELECT JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.price.currency')) FROM generation_tasks g WHERE g.call_id=c.id LIMIT 1),b.currency)
 FROM model_calls c JOIN model_budgets b ON b.workspace_id=c.workspace_id;
CREATE TABLE IF NOT EXISTS model_call_receipts (
 id CHAR(64) PRIMARY KEY, call_id CHAR(32) NOT NULL, payload JSON NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(call_id) REFERENCES model_calls(id), INDEX(call_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS model_reconciliations (
 id CHAR(32) PRIMARY KEY, call_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL, payload JSON NOT NULL, content_hash CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(call_id) REFERENCES model_calls(id), FOREIGN KEY(actor_id) REFERENCES users(id),
 UNIQUE(call_id,revision)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS ip_drafts (
 branch_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL, revision BIGINT NOT NULL,
 base_head CHAR(32) NOT NULL, payload JSON NOT NULL,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(branch_id,actor_id), FOREIGN KEY(branch_id) REFERENCES branches(id), FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ip_merge_candidates (
 candidate_id CHAR(32) PRIMARY KEY, parent_head CHAR(32) NOT NULL, old_base CHAR(32) NOT NULL,
 FOREIGN KEY(candidate_id) REFERENCES ip_revisions(id)
) ENGINE=InnoDB;

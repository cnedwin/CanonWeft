CREATE TABLE IF NOT EXISTS ip_promotions (
 candidate_id CHAR(32) PRIMARY KEY, payload JSON NOT NULL,
 FOREIGN KEY(candidate_id) REFERENCES ip_revisions(id)
) ENGINE=InnoDB;

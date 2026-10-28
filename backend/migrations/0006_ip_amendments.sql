CREATE TABLE IF NOT EXISTS ip_amendments (
 candidate_id CHAR(32) PRIMARY KEY, reason TEXT NOT NULL,
 approved_by CHAR(32), approved_hash CHAR(64),
 FOREIGN KEY(candidate_id) REFERENCES ip_revisions(id)
) ENGINE=InnoDB;

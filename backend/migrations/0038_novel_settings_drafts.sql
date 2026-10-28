CREATE TABLE IF NOT EXISTS novel_settings_drafts (
 novel_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL,
 base_revision BIGINT NOT NULL,
 payload MEDIUMTEXT NOT NULL,
 content_hash CHAR(64) NOT NULL,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(novel_id,actor_id),
 FOREIGN KEY(novel_id) REFERENCES novels(id),
 FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;

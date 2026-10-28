CREATE TABLE IF NOT EXISTS resource_drafts (
 resource_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL, revision BIGINT NOT NULL,
 base_head CHAR(32) NOT NULL, body MEDIUMTEXT NOT NULL,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(resource_id,actor_id), FOREIGN KEY(resource_id) REFERENCES resources(id), FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;

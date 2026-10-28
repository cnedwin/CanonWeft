CREATE TABLE IF NOT EXISTS resource_policy_drafts (
 resource_id CHAR(32) NOT NULL, actor_id CHAR(32) NOT NULL,
 revision BIGINT NOT NULL, payload JSON NOT NULL,
 PRIMARY KEY(resource_id,actor_id),
 FOREIGN KEY(resource_id) REFERENCES resources(id), FOREIGN KEY(actor_id) REFERENCES users(id)
) ENGINE=InnoDB;

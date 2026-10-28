CREATE TABLE IF NOT EXISTS patch_drafts (
  branch_id CHAR(32) NOT NULL,
  actor_id CHAR(32) NOT NULL,
  project_id CHAR(32) NOT NULL,
  revision BIGINT NOT NULL,
  payload MEDIUMTEXT NOT NULL,
  content_hash CHAR(64) NOT NULL,
  updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (branch_id, actor_id),
  FOREIGN KEY (branch_id) REFERENCES branches(id),
  FOREIGN KEY (actor_id) REFERENCES users(id),
  FOREIGN KEY (project_id) REFERENCES projects(id)
);

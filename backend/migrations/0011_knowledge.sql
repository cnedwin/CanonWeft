CREATE TABLE IF NOT EXISTS knowledge_indexes (
 id CHAR(64) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 ip_head CHAR(32) NOT NULL, manifest JSON NOT NULL, model_revision CHAR(40) NOT NULL,
 chunks INT NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(branch_id) REFERENCES branches(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS knowledge_chunks (
 id CHAR(64) PRIMARY KEY, index_id CHAR(64) NOT NULL, resource_id CHAR(32) NOT NULL,
 resource_name VARCHAR(160) NOT NULL, resource_revision CHAR(32) NOT NULL,
 source_hash CHAR(64) NOT NULL, start_rune INT NOT NULL, end_rune INT NOT NULL,
 content TEXT NOT NULL, vector BLOB NOT NULL, vector_hash CHAR(64) NOT NULL,
 FOREIGN KEY(index_id) REFERENCES knowledge_indexes(id), INDEX(index_id,id),
 FULLTEXT INDEX knowledge_text(content) WITH PARSER ngram
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS context_packages (
 id CHAR(32) PRIMARY KEY, project_id CHAR(32) NOT NULL, branch_id CHAR(32) NOT NULL,
 actor_id CHAR(32) NOT NULL, index_id CHAR(64) NOT NULL, content_hash CHAR(64) NOT NULL,
 payload JSON NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 FOREIGN KEY(project_id) REFERENCES projects(id), FOREIGN KEY(index_id) REFERENCES knowledge_indexes(id)
) ENGINE=InnoDB;

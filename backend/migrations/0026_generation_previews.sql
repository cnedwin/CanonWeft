CREATE TABLE IF NOT EXISTS generation_previews (
 generation_id CHAR(32) PRIMARY KEY,
 content MEDIUMTEXT NOT NULL,
 sequence BIGINT NOT NULL,
 content_hash CHAR(64) NOT NULL,
 FOREIGN KEY(generation_id) REFERENCES generation_tasks(id) ON DELETE CASCADE,
 CHECK(sequence > 0), CHECK(OCTET_LENGTH(content) <= 4194304)
) ENGINE=InnoDB;

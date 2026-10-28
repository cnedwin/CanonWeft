ALTER TABLE change_plans MODIFY COLUMN candidate_id CHAR(32) NULL;
CREATE TABLE IF NOT EXISTS change_plan_releases (
 plan_id CHAR(32) PRIMARY KEY, release_id CHAR(32) NOT NULL,
 FOREIGN KEY(plan_id) REFERENCES change_plans(id),
 FOREIGN KEY(release_id) REFERENCES artifact_releases(id)
) ENGINE=InnoDB;

-- Preserve exact JSON numeric literals in user-authored contracts. MySQL's
-- native JSON storage may coerce decimal/exponent literals to binary floating point.
-- Existing JSON objects are converted to equivalent text. Application validation
-- and content hashes remain mandatory on every read and write.
ALTER TABLE capability_versions MODIFY COLUMN definition MEDIUMTEXT NOT NULL;

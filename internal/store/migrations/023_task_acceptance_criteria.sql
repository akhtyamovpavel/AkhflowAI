ALTER TABLE tasks ADD COLUMN acceptance_criteria TEXT NOT NULL DEFAULT '' CHECK (octet_length(acceptance_criteria) <= 16384);

-- Store uploaded bytes in PostgreSQL so attachments survive API container replacement.
-- Existing metadata-only attachments remain unchanged.
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS content BYTEA;

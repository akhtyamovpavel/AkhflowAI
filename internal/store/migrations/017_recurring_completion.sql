ALTER TABLE tasks ADD COLUMN IF NOT EXISTS recurrence_spawned BOOLEAN NOT NULL DEFAULT FALSE;

-- Preserve earlier completion chains, including already deleted occurrences.
UPDATE tasks AS source
SET recurrence_spawned = TRUE
WHERE EXISTS (
    SELECT 1 FROM audit_log AS entry
    WHERE entry.action = 'recur_task'
      AND entry.entity = 'task'
      AND entry.payload ->> 'from' = source.id::text
);

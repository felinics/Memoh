-- 0165_normalize_reasoning_effort (down)
-- The up migration is lossy by design: it maps two retired spellings onto the
-- surviving one, and nothing records which rows carried which. Restoring 'none'
-- or 'adaptive' would be guesswork, and both readers accept 'disable' anyway, so
-- the down migration leaves the data alone. The schema is unchanged either way,
-- which is what makes this reversible in the sense that matters: no column, table
-- or constraint is created or dropped.
SELECT 1;

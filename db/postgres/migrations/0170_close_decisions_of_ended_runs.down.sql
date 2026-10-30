-- 0170_close_decisions_of_ended_runs (down)
-- The up migration only closes requests whose run had already ended; reopening
-- them would make answerable again a question nothing can apply an answer to.
-- No column, table or constraint was created or dropped, so the data is left
-- as it is.
SELECT 1;

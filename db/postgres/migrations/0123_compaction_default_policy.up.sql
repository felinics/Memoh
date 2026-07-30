-- 0123_compaction_default_policy
-- New bots default to compaction on with the model-relative policy: a zero
-- threshold derives soft/hard/target levels from the chat model's context
-- window. Existing bots keep their stored values (legacy behavior).

ALTER TABLE bots ALTER COLUMN compaction_threshold SET DEFAULT 0;
ALTER TABLE bots ALTER COLUMN compaction_enabled SET DEFAULT true;

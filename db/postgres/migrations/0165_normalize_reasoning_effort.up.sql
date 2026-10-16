-- 0165_normalize_reasoning_effort
-- "off" has one spelling now ("disable"), but two older spellings are still in
-- stored rows. 0014 and 0088 both allowed 'none' as a *tier*, and the API and
-- command paths accepted 'adaptive' — a thinking mode that was never an effort
-- level. Both still read as what they meant, so an unnormalized row keeps working
-- and hides how many there are. Normalizing them lets the readers that exist only
-- for these two spellings be deleted.
--
-- 'none' becomes 'disable': the same state, under the name that survives. That is
-- the mapping 0128 already applied to reasoning_enabled = false, and 0128's own
-- comment explains why 'disable' is the survivor — 'none' is OpenAI's wire word
-- and was replaced once already (minimal -> none in gpt-5.1).
--
-- 'adaptive' becomes 'disable' on a model that cannot be turned off, and is
-- cleared to the column default otherwise (ReconcileStored's rule: a stored value
-- the model does not offer lands on the model's default tier). "Cannot be turned
-- off" is a capability the model catalog owns, so it is read from
-- models.config->>'thinking_mode' = 'always' rather than guessed from the value.
-- The model a bot runs is chat_model_id; there is no bots.model_id column.
--
-- The effort columns other than bots.reasoning_effort are free-form and tolerate
-- any string, but they carry the same two spellings and are normalized the same
-- way so one vocabulary reaches the wire. They take the plain mapping only: the
-- always-on rule needs the model, and a row that cannot name one keeps the value
-- it has.

-- Every table this migration reads or writes is under FORCE row level security
-- keyed on memoh.team_id, which a migration connection never sets: a bare
-- statement raises "memoh.team_id is not set" from memoh_current_team_id() and
-- aborts the whole migration. models is in the list because the always-on rule
-- reads it from a subquery, not just because it is written to. Lift the policy on
-- all of them for the data steps and restore it after, the same way 0125, 0128 and
-- 0130 do.
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule DISABLE ROW LEVEL SECURITY;
ALTER TABLE models NO FORCE ROW LEVEL SECURITY;
ALTER TABLE models DISABLE ROW LEVEL SECURITY;

UPDATE bots
   SET reasoning_effort = 'disable'
 WHERE reasoning_effort = 'none';

UPDATE bots
   SET reasoning_effort = 'disable'
 WHERE reasoning_effort = 'adaptive'
   AND EXISTS (
     SELECT 1 FROM models
      WHERE models.id = bots.chat_model_id
        AND models.config->>'thinking_mode' = 'always'
   );

UPDATE bots
   SET reasoning_effort = 'medium'
 WHERE reasoning_effort = 'adaptive';

UPDATE bot_sessions
   SET preferred_reasoning_effort = 'disable'
 WHERE preferred_reasoning_effort = 'none';

UPDATE bot_sessions
   SET preferred_reasoning_effort = 'medium'
 WHERE preferred_reasoning_effort = 'adaptive';

UPDATE schedule
   SET reasoning_effort = 'disable'
 WHERE reasoning_effort = 'none';

UPDATE schedule
   SET reasoning_effort = 'medium'
 WHERE reasoning_effort = 'adaptive';

-- Restore every policy in the reverse order of lifting, so the schema's security
-- posture is exactly what it was before this file ran.
ALTER TABLE models ENABLE ROW LEVEL SECURITY;
ALTER TABLE models FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;

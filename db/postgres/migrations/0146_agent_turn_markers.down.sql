-- 0146_agent_turn_markers
-- Restore the ACP-era spellings of the reconciliation marker keys.

ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;

UPDATE public.bot_history_messages
SET metadata = (
      metadata
      - 'agent_turn_outcome'
      - 'agent_decision_projection'
      - 'agent_decision_tool_call_id'
    ) || (
      SELECT jsonb_object_agg('acp_' || substring(key FROM 7), value)
      FROM jsonb_each(metadata)
      WHERE key IN (
        'agent_turn_outcome',
        'agent_decision_projection',
        'agent_decision_tool_call_id'
      )
    )
WHERE metadata ?| ARRAY[
  'agent_turn_outcome',
  'agent_decision_projection',
  'agent_decision_tool_call_id'
];

ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;

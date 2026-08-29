-- name: GetSettingsByBotID :one
SELECT
  bots.id AS bot_id,
  bots.language,
  bots.reasoning_effort,
  bots.compaction_enabled,
  bots.compaction_threshold,
  bots.compaction_target_percent,
  bots.timezone,
  chat_models.id AS chat_model_id,
  bots.default_bot_agent_id,
  bots.chat_runtime,
  bots.chat_acp_agent_id,
  bots.chat_acp_project_path,
  bots.chat_acp_project_mode,
  compaction_models.id AS compaction_model_id,
  search_providers.id AS search_provider_id,
  fetch_providers.id AS fetch_provider_id,
  memory_providers.id AS memory_provider_id,
  image_models.id AS image_model_id,
  tts_models.id AS tts_model_id,
  transcription_models.id AS transcription_model_id,
  video_models.id AS video_model_id,
  bots.persist_full_tool_results,
  bots.show_tool_calls_in_im,
  bots.tool_approval_config,
  bots.display_enabled,
  bots.overlay_provider,
  bots.overlay_enabled,
  bots.overlay_config,
  bots.command_ui_language
FROM bots
LEFT JOIN models AS chat_models ON chat_models.id = bots.chat_model_id AND chat_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS compaction_models ON compaction_models.id = bots.compaction_model_id AND compaction_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS image_models ON image_models.id = bots.image_model_id AND image_models.team_id = public.memoh_current_team_id()
LEFT JOIN search_providers ON search_providers.id = bots.search_provider_id AND search_providers.team_id = public.memoh_current_team_id()
LEFT JOIN fetch_providers ON fetch_providers.id = bots.fetch_provider_id AND fetch_providers.team_id = public.memoh_current_team_id()
LEFT JOIN memory_providers ON memory_providers.id = bots.memory_provider_id AND memory_providers.team_id = public.memoh_current_team_id()
LEFT JOIN models AS tts_models ON tts_models.id = bots.tts_model_id AND tts_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS transcription_models ON transcription_models.id = bots.transcription_model_id AND transcription_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS video_models ON video_models.id = bots.video_model_id AND video_models.team_id = public.memoh_current_team_id()
WHERE bots.team_id = public.memoh_current_team_id() AND bots.id = $1;

-- name: UpsertBotSettings :one
WITH updated AS (
  UPDATE bots
  SET language = sqlc.arg(language),
      reasoning_effort = sqlc.arg(reasoning_effort),
      compaction_enabled = sqlc.arg(compaction_enabled),
      compaction_threshold = sqlc.arg(compaction_threshold),
      compaction_target_percent = CASE
        WHEN sqlc.arg(compaction_target_percent_set)::boolean
          THEN sqlc.narg(compaction_target_percent)::integer
        ELSE bots.compaction_target_percent
      END,
      timezone = COALESCE(sqlc.narg(timezone)::text, bots.timezone),
      chat_model_id = CASE
        WHEN sqlc.arg(chat_model_id_set)::boolean THEN sqlc.narg(chat_model_id)::uuid
        ELSE bots.chat_model_id
      END,
      default_bot_agent_id = CASE
        WHEN sqlc.arg(default_bot_agent_id_set)::boolean THEN sqlc.narg(default_bot_agent_id)::uuid
        ELSE bots.default_bot_agent_id
      END,
      chat_runtime = sqlc.arg(chat_runtime),
      chat_acp_agent_id = sqlc.narg(chat_acp_agent_id)::text,
      chat_acp_project_path = sqlc.arg(chat_acp_project_path),
      chat_acp_project_mode = sqlc.arg(chat_acp_project_mode),
      compaction_model_id = CASE
        WHEN sqlc.arg(compaction_model_id_set)::boolean THEN sqlc.narg(compaction_model_id)::uuid
        ELSE bots.compaction_model_id
      END,
      search_provider_id = CASE
        WHEN sqlc.arg(search_provider_id_set)::boolean THEN sqlc.narg(search_provider_id)::uuid
        ELSE bots.search_provider_id
      END,
      fetch_provider_id = CASE
        WHEN sqlc.arg(fetch_provider_id_set)::boolean THEN sqlc.narg(fetch_provider_id)::uuid
        ELSE bots.fetch_provider_id
      END,
      memory_provider_id = CASE
        WHEN sqlc.arg(memory_provider_id_set)::boolean THEN sqlc.narg(memory_provider_id)::uuid
        ELSE bots.memory_provider_id
      END,
      image_model_id = CASE
        WHEN sqlc.arg(image_model_id_set)::boolean THEN sqlc.narg(image_model_id)::uuid
        ELSE bots.image_model_id
      END,
      tts_model_id = CASE
        WHEN sqlc.arg(tts_model_id_set)::boolean THEN sqlc.narg(tts_model_id)::uuid
        ELSE bots.tts_model_id
      END,
      transcription_model_id = CASE
        WHEN sqlc.arg(transcription_model_id_set)::boolean THEN sqlc.narg(transcription_model_id)::uuid
        ELSE bots.transcription_model_id
      END,
      video_model_id = CASE
        WHEN sqlc.arg(video_model_id_set)::boolean THEN sqlc.narg(video_model_id)::uuid
        ELSE bots.video_model_id
      END,
      persist_full_tool_results = sqlc.arg(persist_full_tool_results),
      show_tool_calls_in_im = sqlc.arg(show_tool_calls_in_im),
      tool_approval_config = sqlc.arg(tool_approval_config),
      display_enabled = sqlc.arg(display_enabled),
      overlay_provider = sqlc.arg(overlay_provider),
      overlay_enabled = sqlc.arg(overlay_enabled),
      overlay_config = sqlc.arg(overlay_config),
      command_ui_language = sqlc.arg(command_ui_language),
      updated_at = now()
  WHERE bots.team_id = public.memoh_current_team_id() AND bots.id = sqlc.arg(id)
  RETURNING bots.id, bots.language, bots.reasoning_effort, bots.compaction_enabled, bots.compaction_threshold, bots.compaction_target_percent, bots.timezone, bots.chat_model_id, bots.default_bot_agent_id, bots.chat_runtime, bots.chat_acp_agent_id, bots.chat_acp_project_path, bots.chat_acp_project_mode, bots.compaction_model_id, bots.image_model_id, bots.search_provider_id, bots.fetch_provider_id, bots.memory_provider_id, bots.tts_model_id, bots.transcription_model_id, bots.video_model_id, bots.persist_full_tool_results, bots.show_tool_calls_in_im, bots.tool_approval_config, bots.display_enabled, bots.overlay_provider, bots.overlay_enabled, bots.overlay_config, bots.command_ui_language
)
SELECT
  updated.id AS bot_id,
  updated.language,
  updated.reasoning_effort,
  updated.compaction_enabled,
  updated.compaction_threshold,
  updated.compaction_target_percent,
  updated.timezone,
  chat_models.id AS chat_model_id,
  updated.default_bot_agent_id,
  updated.chat_runtime,
  updated.chat_acp_agent_id,
  updated.chat_acp_project_path,
  updated.chat_acp_project_mode,
  compaction_models.id AS compaction_model_id,
  search_providers.id AS search_provider_id,
  fetch_providers.id AS fetch_provider_id,
  memory_providers.id AS memory_provider_id,
  image_models.id AS image_model_id,
  tts_models.id AS tts_model_id,
  transcription_models.id AS transcription_model_id,
  video_models.id AS video_model_id,
  updated.persist_full_tool_results,
  updated.show_tool_calls_in_im,
  updated.tool_approval_config,
  updated.display_enabled,
  updated.overlay_provider,
  updated.overlay_enabled,
  updated.overlay_config,
  updated.command_ui_language
FROM updated
LEFT JOIN models AS chat_models ON chat_models.id = updated.chat_model_id AND chat_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS compaction_models ON compaction_models.id = updated.compaction_model_id AND compaction_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS image_models ON image_models.id = updated.image_model_id AND image_models.team_id = public.memoh_current_team_id()
LEFT JOIN search_providers ON search_providers.id = updated.search_provider_id AND search_providers.team_id = public.memoh_current_team_id()
LEFT JOIN fetch_providers ON fetch_providers.id = updated.fetch_provider_id AND fetch_providers.team_id = public.memoh_current_team_id()
LEFT JOIN memory_providers ON memory_providers.id = updated.memory_provider_id AND memory_providers.team_id = public.memoh_current_team_id()
LEFT JOIN models AS tts_models ON tts_models.id = updated.tts_model_id AND tts_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS transcription_models ON transcription_models.id = updated.transcription_model_id AND transcription_models.team_id = public.memoh_current_team_id()
LEFT JOIN models AS video_models ON video_models.id = updated.video_model_id AND video_models.team_id = public.memoh_current_team_id();

-- name: DeleteSettingsByBotID :exec
UPDATE bots
SET language = 'auto',
    command_ui_language = 'auto',
    reasoning_effort = 'medium',
    compaction_enabled = true,
    compaction_threshold = 0,
    compaction_target_percent = NULL,
    chat_model_id = NULL,
    default_bot_agent_id = NULL,
    chat_runtime = 'model',
    chat_acp_agent_id = NULL,
    chat_acp_project_path = '/data',
    chat_acp_project_mode = 'project',
    compaction_model_id = NULL,
    image_model_id = NULL,
    search_provider_id = NULL,
    fetch_provider_id = NULL,
    memory_provider_id = NULL,
    tts_model_id = NULL,
    transcription_model_id = NULL,
    video_model_id = NULL,
    persist_full_tool_results = false,
    show_tool_calls_in_im = false,
    tool_approval_config = '{"enabled":false,"read":{"require_approval":false,"bypass_globs":[],"force_review_globs":[]},"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb,
    display_enabled = true,
    overlay_provider = '',
    overlay_enabled = false,
    overlay_config = '{}'::jsonb,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: UpsertContextFragmentTexts :exec
INSERT INTO context_fragment_texts (bot_id, content_hash, kind, label, text, text_bytes, truncated)
SELECT
  sqlc.arg(bot_id)::uuid,
  unnest(sqlc.arg(content_hashes)::text[]),
  unnest(sqlc.arg(kinds)::text[]),
  unnest(sqlc.arg(labels)::text[]),
  unnest(sqlc.arg(texts)::text[]),
  unnest(sqlc.arg(text_bytes)::int[]),
  unnest(sqlc.arg(truncated)::boolean[])
ON CONFLICT (team_id, bot_id, content_hash) DO NOTHING;

-- name: ListContextFragmentPreviews :many
SELECT content_hash, kind, label, left(text, sqlc.arg(preview_chars)::int) AS preview, text_bytes, truncated
FROM context_fragment_texts
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND content_hash = ANY(sqlc.arg(content_hashes)::text[]);

-- name: ListContextFragmentTexts :many
SELECT content_hash, kind, label, text, text_bytes, truncated
FROM context_fragment_texts
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND content_hash = ANY(sqlc.arg(content_hashes)::text[]);

-- name: UpsertMemoryNode :one
INSERT INTO memory.memory_nodes (
  id, bot_id, body, hash, layer, fact_type, subject, confidence,
  metadata, source_message_ids, profile_ref, topic, captured_at, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (team_id, id) DO UPDATE SET
  body = EXCLUDED.body,
  hash = EXCLUDED.hash,
  layer = EXCLUDED.layer,
  fact_type = EXCLUDED.fact_type,
  subject = EXCLUDED.subject,
  confidence = EXCLUDED.confidence,
  metadata = EXCLUDED.metadata,
  source_message_ids = EXCLUDED.source_message_ids,
  profile_ref = EXCLUDED.profile_ref,
  topic = EXCLUDED.topic,
  expires_at = EXCLUDED.expires_at,
  updated_at = now()
RETURNING *;

-- name: GetMemoryNode :one
SELECT * FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: ListMemoryNodesByBot :many
SELECT * FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1
ORDER BY captured_at ASC;

-- name: ListMemoryNodesByBotLayer :many
SELECT * FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND layer = $2
ORDER BY captured_at ASC;

-- name: ListMemoryNodesByBotProfile :many
SELECT * FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND profile_ref = $2
ORDER BY captured_at ASC;

-- name: DeleteMemoryNode :exec
DELETE FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: DeleteAllMemoryNodesByBot :exec
DELETE FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: CountMemoryNodesByBot :one
SELECT COUNT(*) FROM memory.memory_nodes
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: InsertMemoryEdge :exec
INSERT INTO memory.memory_edges (bot_id, src_node, dst_node, rel, weight, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (team_id, bot_id, src_node, dst_node, rel) DO UPDATE SET
  weight = EXCLUDED.weight,
  metadata = EXCLUDED.metadata;

-- name: ListMemoryEdgesFromNode :many
SELECT * FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND src_node = $2
ORDER BY weight DESC;

-- name: ListMemoryEdgesByBot :many
SELECT * FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: ListMemoryEdgesByRel :many
SELECT * FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND rel = $2;

-- name: DeleteMemoryEdgesForNode :exec
DELETE FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND (src_node = $2 OR dst_node = $2);

-- name: DeleteAllMemoryEdgesByBot :exec
DELETE FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: CountMemoryEdgesByBot :one
SELECT COUNT(*) FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1;

-- name: DeleteMemoryEdgesByRelForBot :exec
DELETE FROM memory.memory_edges
WHERE team_id = iam.memoh_current_team_id() AND bot_id = $1 AND rel = $2;

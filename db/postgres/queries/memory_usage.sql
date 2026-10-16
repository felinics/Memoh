-- name: CreateMemoryUsage :exec
INSERT INTO bot_memory_usage (bot_id, model_id, operation, usage)
VALUES (sqlc.arg(bot_id), sqlc.narg(model_id)::uuid, sqlc.arg(operation), sqlc.arg(usage));

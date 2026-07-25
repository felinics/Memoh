-- name: InsertLifecycleEvent :exec
INSERT INTO runtime.lifecycle_events (id, container_id, event_type, payload)
VALUES (
  sqlc.arg(id),
  sqlc.arg(container_id),
  sqlc.arg(event_type),
  sqlc.arg(payload)
);

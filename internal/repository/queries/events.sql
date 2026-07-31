-- name: IsEventProcessed :one
SELECT EXISTS (
    SELECT 1 FROM processed_events WHERE event_id = $1
);

-- name: MarkEventProcessed :exec
INSERT INTO processed_events (event_id, event_type)
VALUES ($1, $2)
ON CONFLICT (event_id) DO NOTHING;
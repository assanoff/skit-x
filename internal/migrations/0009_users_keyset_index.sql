-- +goose Up
-- Composite index backing keyset (cursor) pagination over users: the
-- QueryByCursor predicate (created_at, id) < (:after_ts, :after_id) ORDER BY
-- created_at DESC, id DESC is an index range scan on this index, so paging stays
-- O(limit) regardless of how deep the cursor has advanced (unlike OFFSET).
CREATE INDEX IF NOT EXISTS users_created_at_id_desc_idx
    ON users (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS users_created_at_id_desc_idx;

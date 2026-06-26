-- +goose Up
-- Composite index backing keyset (cursor) pagination over widgets: the
-- QueryByCursor predicate (created_at, id) < (:after_ts, :after_id) ORDER BY
-- created_at DESC, id DESC is an index range scan on this index, so paging stays
-- O(limit) regardless of how deep the cursor has advanced (unlike OFFSET).
CREATE INDEX IF NOT EXISTS widgets_created_at_id_desc_idx
    ON widgets (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS widgets_created_at_id_desc_idx;

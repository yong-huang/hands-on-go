-- schema.sql：sqlc 的类型推断来源（与实验 05 迁移 001 的 PG 方言保持一致）。
-- sqlc 从这里读表结构，为每个查询生成强类型的参数与返回值。
CREATE TABLE bookmarks (
    id         BIGSERIAL PRIMARY KEY,
    url        TEXT NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_bookmarks_url ON bookmarks(url);

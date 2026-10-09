-- bookmarks.sql：SQL 即源码。每个带名字的语句生成一个强类型 Go 函数。
-- sqlc 从 schema.sql 推断列类型，参数与返回值在编译期就定型。

-- name: CreateBookmark :one
INSERT INTO bookmarks (url, title) VALUES ($1, $2)
RETURNING id, url, title, created_at;

-- name: GetBookmark :one
SELECT id, url, title, created_at FROM bookmarks WHERE id = $1;

-- name: ListBookmarks :many
SELECT id, url, title, created_at FROM bookmarks ORDER BY id DESC;

-- name: UpdateBookmarkTitle :execrows
UPDATE bookmarks SET title = $2 WHERE id = $1;

-- name: DeleteBookmark :execrows
DELETE FROM bookmarks WHERE id = $1;

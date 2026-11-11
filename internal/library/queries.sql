-- The Library (spec 25).

-- name: UpsertBook :one
-- updated_at moves only when something changed. Answers whether the row is new.
INSERT INTO library_books AS b (id, title, author, subject, description, pages, pdf_key, pdf_sha256, pdf_bytes,
                                cover_key, cover_type, cover_sha256, listed, position)
VALUES (@id, @title, @author, @subject, @description, @pages, @pdf_key, @pdf_sha256, @pdf_bytes,
        sqlc.narg('cover_key'), sqlc.narg('cover_type'), sqlc.narg('cover_sha256'), true, @position)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title, author = EXCLUDED.author, subject = EXCLUDED.subject, description = EXCLUDED.description,
    pages = EXCLUDED.pages, pdf_key = EXCLUDED.pdf_key, pdf_sha256 = EXCLUDED.pdf_sha256, pdf_bytes = EXCLUDED.pdf_bytes,
    cover_key = EXCLUDED.cover_key, cover_type = EXCLUDED.cover_type, cover_sha256 = EXCLUDED.cover_sha256,
    listed = true, position = EXCLUDED.position,
    updated_at = CASE WHEN (b.title, b.author, b.subject, b.description, b.pages, b.pdf_sha256, b.cover_sha256, b.listed, b.position)
                           IS DISTINCT FROM
                           (EXCLUDED.title, EXCLUDED.author, EXCLUDED.subject, EXCLUDED.description, EXCLUDED.pages,
                            EXCLUDED.pdf_sha256, EXCLUDED.cover_sha256, true, EXCLUDED.position)
                      THEN now() ELSE b.updated_at END
RETURNING (xmax = 0)::boolean AS inserted, updated_at;

-- name: UnlistOtherBooks :one
WITH gone AS (
    UPDATE library_books SET listed = false, updated_at = now()
    WHERE listed AND NOT (id = ANY (@ids::text[]))
    RETURNING 1
)
SELECT count(*)::int FROM gone;

-- name: StoredFiles :many
SELECT id, pdf_sha256, cover_sha256 FROM library_books;

-- The listed books in order, each with how many people have opened it and
-- where this reader is in it.
-- name: ListBooks :many
SELECT b.id, b.title, b.author, b.subject, b.description, b.pages, b.pdf_bytes, b.cover_sha256,
       (SELECT count(*) FROM library_progress v WHERE v.book_id = b.id)::int AS views,
       p.page AS my_page, p.updated_at AS my_updated_at
FROM library_books b
LEFT JOIN library_progress p ON p.book_id = b.id AND p.person_kind = @person_kind AND p.person_id = @person_id
WHERE b.listed
ORDER BY b.position, b.id;

-- name: GetBook :one
SELECT * FROM library_books WHERE id = @id AND listed;

-- name: BookCover :one
SELECT cover_key, cover_type, cover_sha256 FROM library_books WHERE id = @id AND listed AND cover_key IS NOT NULL;

-- name: SetProgress :one
INSERT INTO library_progress (person_kind, person_id, book_id, page)
VALUES (@person_kind, @person_id, @book_id, @page)
ON CONFLICT (person_kind, person_id, book_id) DO UPDATE SET page = EXCLUDED.page, updated_at = now()
RETURNING page, updated_at;

-- Universities (spec 24).

-- name: UpsertUniversity :one
-- updated_at moves only when something changed, so the list's version (and
-- the apps' caches) change only then. Answers whether the row is new or changed.
INSERT INTO universities AS u (id, name, short_name, country, city, lat, lng, qs_rank, qs_display, list_reason,
                               sat, details, logo_key, logo_type, logo_sha256, listed, position)
VALUES (@id, @name, sqlc.narg('short_name'), @country, sqlc.narg('city'), sqlc.narg('lat'), sqlc.narg('lng'),
        sqlc.narg('qs_rank'), sqlc.narg('qs_display'), @list_reason, sqlc.narg('sat'), sqlc.narg('details'),
        sqlc.narg('logo_key'), sqlc.narg('logo_type'), sqlc.narg('logo_sha256'), true, @position)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, short_name = EXCLUDED.short_name, country = EXCLUDED.country, city = EXCLUDED.city,
    lat = EXCLUDED.lat, lng = EXCLUDED.lng, qs_rank = EXCLUDED.qs_rank, qs_display = EXCLUDED.qs_display,
    list_reason = EXCLUDED.list_reason, sat = EXCLUDED.sat, details = EXCLUDED.details,
    logo_key = EXCLUDED.logo_key, logo_type = EXCLUDED.logo_type, logo_sha256 = EXCLUDED.logo_sha256,
    listed = true, position = EXCLUDED.position,
    updated_at = CASE WHEN (u.name, u.short_name, u.country, u.city, u.lat, u.lng, u.qs_rank, u.qs_display, u.list_reason,
                            u.sat, u.details, u.logo_sha256, u.listed, u.position)
                           IS DISTINCT FROM
                           (EXCLUDED.name, EXCLUDED.short_name, EXCLUDED.country, EXCLUDED.city, EXCLUDED.lat, EXCLUDED.lng,
                            EXCLUDED.qs_rank, EXCLUDED.qs_display, EXCLUDED.list_reason, EXCLUDED.sat, EXCLUDED.details,
                            EXCLUDED.logo_sha256, true, EXCLUDED.position)
                      THEN now() ELSE u.updated_at END
RETURNING (xmax = 0)::boolean AS inserted, updated_at;

-- Universities gone from the data stay for the students who chose them, unlisted.
-- name: UnlistOthers :one
WITH gone AS (
    UPDATE universities SET listed = false, updated_at = now()
    WHERE listed AND NOT (id = ANY (@ids::text[]))
    RETURNING 1
)
SELECT count(*)::int FROM gone;

-- name: StoredLogos :many
SELECT id, logo_sha256 FROM universities WHERE logo_sha256 IS NOT NULL;

-- name: UpsertCountry :exec
INSERT INTO university_countries (code, details) VALUES (@code, @details)
ON CONFLICT (code) DO UPDATE SET details = EXCLUDED.details,
    updated_at = CASE WHEN university_countries.details IS DISTINCT FROM EXCLUDED.details THEN now()
                      ELSE university_countries.updated_at END;

-- name: ListedUniversities :many
SELECT id, name, short_name, country, city, lat, lng, qs_rank, qs_display, sat, logo_sha256, (details IS NOT NULL)::boolean AS has_details
FROM universities WHERE listed ORDER BY position, id;

-- name: ListVersion :one
SELECT count(*)::int AS n, coalesce(max(updated_at), 'epoch')::timestamptz AS latest FROM universities WHERE listed;

-- name: GetUniversity :one
SELECT * FROM universities WHERE id = @id;

-- name: GetCountry :one
SELECT details FROM university_countries WHERE code = @code;

-- name: UniversityLogo :one
SELECT logo_key, logo_type, logo_sha256 FROM universities WHERE id = @id AND logo_key IS NOT NULL;

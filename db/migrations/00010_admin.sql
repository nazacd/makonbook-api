-- +goose Up
-- Admin panel access (spec 13). Admins sign in with their HolliHop login,
-- checked by HolliHop; this list says who among HolliHop's staff may use the
-- panel and for which branches. HolliHop exposes no roles, hence our own list.
CREATE TABLE admin_access (
    member_type text NOT NULL CHECK (member_type IN ('Employee', 'Teacher')),  -- as HolliHop reports it
    member_id   integer NOT NULL,                  -- HolliHop employee or teacher Id
    name        text NOT NULL DEFAULT '',          -- refreshed at each sign-in
    super       boolean NOT NULL DEFAULT false,    -- all branches, manages this list
    office_ids  integer[] NOT NULL DEFAULT '{}',   -- branches, for non-super admins
    granted_at  timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    PRIMARY KEY (member_type, member_id),
    CHECK (super OR cardinality(office_ids) > 0)
);

CREATE TABLE admin_sessions (
    id           bigserial PRIMARY KEY,
    token_hash   bytea NOT NULL UNIQUE,            -- sha256 of the cookie value
    member_type  text NOT NULL,
    member_id    integer NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,             -- fixed: no sliding renewal
    revoked_at   timestamptz,
    FOREIGN KEY (member_type, member_id) REFERENCES admin_access (member_type, member_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE admin_sessions;
DROP TABLE admin_access;

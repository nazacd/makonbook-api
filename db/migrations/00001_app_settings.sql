-- +goose Up
-- Small runtime settings that admins can change without a redeploy.
CREATE TABLE app_settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Oldest app version allowed to talk to the API, per platform (spec D15).
INSERT INTO app_settings (key, value)
VALUES ('min_app_version', '{"android": "1.0.0", "ios": "1.0.0"}');

-- +goose Down
DROP TABLE app_settings;

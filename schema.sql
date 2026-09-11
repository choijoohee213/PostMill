CREATE TABLE IF NOT EXISTS posts (
    id               bigserial PRIMARY KEY,
    affiliate        text        NOT NULL,
    product_url      text        NOT NULL DEFAULT '',
    affiliate_link   text        NOT NULL DEFAULT '',
    memo             text        NOT NULL DEFAULT '',
    body             text        NOT NULL DEFAULT '',
    status           text        NOT NULL,
    error_msg        text        NOT NULL DEFAULT '',
    thread_permalink text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    published_at     timestamptz
);

CREATE INDEX IF NOT EXISTS posts_status_created_at_idx
    ON posts (status, created_at DESC);

CREATE TABLE IF NOT EXISTS app_state (
    key        text PRIMARY KEY,
    value      text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 본문에 이어 답글로 올릴 디테일. 본문을 짧게 두고 나머지를 여기 담는다.
ALTER TABLE posts ADD COLUMN IF NOT EXISTS detail text NOT NULL DEFAULT '';

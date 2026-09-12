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

-- 스레드 계정별 토큰. 사용자마다 자기 계정으로 발행한다.
CREATE TABLE IF NOT EXISTS threads_users (
    user_id      text PRIMARY KEY,
    username     text        NOT NULL DEFAULT '',
    access_token text        NOT NULL,
    expires_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- 게시물의 주인. 조회와 수정은 모두 이 값으로 걸러진다.
ALTER TABLE posts ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS posts_user_status_idx
    ON posts (user_id, status, created_at DESC);

-- AI가 고른 상품 이름. 사용자가 이 이름으로 상품을 찾아 제휴 링크를 만든다.
ALTER TABLE posts ADD COLUMN IF NOT EXISTS product_name text NOT NULL DEFAULT '';

-- 두 번째 답글. 게시물 → 답글1 → 답글2 → 링크 순으로 이어진다.
ALTER TABLE posts ADD COLUMN IF NOT EXISTS detail2 text NOT NULL DEFAULT '';

-- 게시 진행 상태. 요청이 중간에 끊겨도 이어서 마칠 수 있게 남긴다.
-- 이게 없으면 어디까지 올라갔는지 알 수 없어 같은 글을 다시 올리게 된다.
ALTER TABLE posts ADD COLUMN IF NOT EXISTS thread_post_id text NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN IF NOT EXISTS replies_done int NOT NULL DEFAULT 0;
ALTER TABLE posts ADD COLUMN IF NOT EXISTS last_reply_id text NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN IF NOT EXISTS publish_started_at timestamptz;

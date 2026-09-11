package main

import (
	"context"
	_ "embed"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// 게시물 상태.
const (
	StatusGenerating = "generating"
	StatusPending    = "pending"
	StatusApproved   = "approved"
	StatusPublishing = "publishing"
	StatusPublished  = "published"
	StatusFailed     = "failed"
	StatusHeld       = "held"
)

// 제휴사.
const (
	AffiliateCoupang = "coupang"
	AffiliateToss    = "toss"
)

type Post struct {
	ID              int64
	Affiliate       string
	ProductURL      string
	AffiliateLink   string
	Memo            string
	Body            string
	Detail          string
	Status          string
	ErrorMsg        string
	ThreadPermalink string
	CreatedAt       time.Time
	PublishedAt     *time.Time
}

type DB struct {
	pool *pgxpool.Pool
}

// Open은 커넥션 풀을 열고 schema.sql을 실행한다.
func Open(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() { db.pool.Close() }

const postColumns = `id, affiliate, product_url, affiliate_link, memo, body, detail,
	status, error_msg, thread_permalink, created_at, published_at`

func scanPost(row pgx.Row) (*Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.Affiliate, &p.ProductURL, &p.AffiliateLink, &p.Memo,
		&p.Body, &p.Detail, &p.Status, &p.ErrorMsg, &p.ThreadPermalink, &p.CreatedAt, &p.PublishedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateDraft는 generating 상태의 row를 만들고 id를 반환한다.
func (db *DB) CreateDraft(ctx context.Context, affiliate, productURL, affiliateLink, memo string) (int64, error) {
	var id int64
	err := db.pool.QueryRow(ctx,
		`INSERT INTO posts (affiliate, product_url, affiliate_link, memo, status)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		affiliate, productURL, affiliateLink, memo, StatusGenerating).Scan(&id)
	return id, err
}

func (db *DB) GetPost(ctx context.Context, id int64) (*Post, error) {
	return scanPost(db.pool.QueryRow(ctx,
		`SELECT `+postColumns+` FROM posts WHERE id = $1`, id))
}

// ListByStatus는 주어진 상태들의 게시물을 최신순으로 반환한다.
func (db *DB) ListByStatus(ctx context.Context, statuses ...string) ([]*Post, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT `+postColumns+` FROM posts WHERE status = ANY($1) ORDER BY created_at DESC`,
		statuses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// SetGenerated는 초안 생성이 끝난 row를 pending으로 전환한다.
func (db *DB) SetGenerated(ctx context.Context, id int64, body, detail string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE posts SET body = $2, detail = $3, status = $4, error_msg = '' WHERE id = $1`,
		id, body, detail, StatusPending)
	return err
}

// UpdateBody는 편집 화면에서 수정한 본문과 디테일을 저장한다.
func (db *DB) UpdateBody(ctx context.Context, id int64, body, detail string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE posts SET body = $2, detail = $3 WHERE id = $1`, id, body, detail)
	return err
}

// SetStatus는 상태만 바꾼다 (보류, 재생성 등).
func (db *DB) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := db.pool.Exec(ctx, `UPDATE posts SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (db *DB) DeletePost(ctx context.Context, id int64) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
	return err
}

// publishableStatuses는 발행을 시작할 수 있는 상태다.
// publishing과 published가 빠져 있는 것이 중복 발행을 막는 핵심이다.
var publishableStatuses = []string{StatusPending, StatusApproved, StatusFailed, StatusHeld}

// ClaimForPublish는 한 번의 조건부 UPDATE로 발행 권한을 선점한다.
// 이미 다른 요청이 선점했으면 false를 반환한다 (중복 발행 방지, SPEC 5-2).
//
// 상태 전환을 여러 UPDATE로 쪼개면 안 된다. 예를 들어 approved로 바꾼 뒤
// 선점하면, 두 번째 요청의 approved 전환이 첫 요청의 publishing을 덮어써서
// 둘 다 통과한다. 반드시 이 한 번의 UPDATE로 끝내야 한다.
func (db *DB) ClaimForPublish(ctx context.Context, id int64) (bool, error) {
	tag, err := db.pool.Exec(ctx,
		`UPDATE posts SET status = $2 WHERE id = $1 AND status = ANY($3)`,
		id, StatusPublishing, publishableStatuses)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (db *DB) MarkPublished(ctx context.Context, id int64, permalink string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE posts SET status = $2, thread_permalink = $3, published_at = now(), error_msg = ''
		 WHERE id = $1`,
		id, StatusPublished, permalink)
	return err
}

func (db *DB) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE posts SET status = $2, error_msg = $3 WHERE id = $1`,
		id, StatusFailed, errMsg)
	return err
}

// GetState는 app_state 값을 반환한다. 키가 없으면 빈 문자열과 false.
func (db *DB) GetState(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := db.pool.QueryRow(ctx, `SELECT value FROM app_state WHERE key = $1`, key).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (db *DB) SetState(ctx context.Context, key, value string) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO app_state (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value)
	return err
}

// SetGenerateError는 초안 생성이 실패했을 때 사유만 기록한다.
// 상태는 generating으로 남겨두고, 목록에서 재시도 버튼을 노출한다.
func (db *DB) SetGenerateError(ctx context.Context, id int64, errMsg string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE posts SET error_msg = $2 WHERE id = $1 AND status = $3`,
		id, errMsg, StatusGenerating)
	return err
}

// SetPublishNote는 발행은 됐지만 사용자가 알아야 할 일이 있을 때 남긴다.
// 상태는 published 그대로 둔다.
func (db *DB) SetPublishNote(ctx context.Context, id int64, note string) error {
	_, err := db.pool.Exec(ctx, `UPDATE posts SET error_msg = $2 WHERE id = $1`, id, note)
	return err
}

// ListPublished는 발행 이력을 최신 발행순으로 반환한다.
func (db *DB) ListPublished(ctx context.Context) ([]*Post, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT `+postColumns+` FROM posts WHERE status = $1 ORDER BY published_at DESC NULLS LAST`,
		StatusPublished)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

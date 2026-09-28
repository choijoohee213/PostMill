package main

import (
	"context"
	"strings"
	"testing"
)

func TestSecretBox_잠그고_연다(t *testing.T) {
	b, err := newSecretBox("열쇠")
	if err != nil {
		t.Fatal(err)
	}

	const plain = "sk_live_비밀키"
	sealed, err := b.seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, plain) {
		t.Fatalf("잠근 값에 원문이 그대로 있다: %q", sealed)
	}
	got, err := b.open(sealed)
	if err != nil || got != plain {
		t.Fatalf("연 값=%q err=%v", got, err)
	}

	// 같은 값을 두 번 잠가도 다르게 나온다. 같은 키를 쓰는지 대조하지 못한다.
	again, _ := b.seal(plain)
	if again == sealed {
		t.Error("두 번 잠근 값이 같다")
	}

	// 열쇠가 다르면 열 수 없다.
	other, _ := newSecretBox("다른 열쇠")
	if _, err := other.open(sealed); err == nil {
		t.Error("다른 열쇠로 열렸다")
	}
	// 값이 망가져도 열 수 없다.
	if _, err := b.open(sealed[:len(sealed)-4] + "AAAA"); err == nil {
		t.Error("망가진 값이 열렸다")
	}
	if _, err := newSecretBox(""); err == nil {
		t.Error("빈 열쇠를 받아들였다")
	}
}

// DB에 들어간 제휴 키는 평문이 아니어야 한다.
func TestTossKey_DB에는_잠긴_채로_들어간다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "sealed-key"

	k := &TossKey{UserID: user, AccessKey: "ak_평문", SecretKey: "sk_평문", PublisherID: "pub-1"}
	if err := db.SetTossKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DeleteTossKey(ctx, user) })

	var access, secret string
	if err := db.pool.QueryRow(ctx,
		`SELECT access_key, secret_key FROM toss_keys WHERE user_id = $1`, user).
		Scan(&access, &secret); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(access, "평문") || strings.Contains(secret, "평문") {
		t.Fatalf("평문이 그대로 있다: access=%q secret=%q", access, secret)
	}

	got, ok, err := db.GetTossKey(ctx, user)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.AccessKey != k.AccessKey || got.SecretKey != k.SecretKey || got.PublisherID != k.PublisherID {
		t.Fatalf("읽어 온 키=%+v", got)
	}
}

// 액세스 토큰도 잠근다. 이것만 있어도 남의 계정으로 API를 부를 수 있다.
func TestTossToken_DB에는_잠긴_채로_들어간다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const pub = "pub-sealed"

	exp := nowPlusDays(300)
	if err := db.SetTossToken(ctx, pub, "tok_평문", exp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ClearTossToken(ctx, pub) })

	var stored string
	if err := db.pool.QueryRow(ctx,
		`SELECT access_token FROM toss_tokens WHERE publisher_id = $1`, pub).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "평문") {
		t.Fatalf("평문이 그대로 있다: %q", stored)
	}

	got, _, ok, err := db.GetTossToken(ctx, pub)
	if err != nil || !ok || got != "tok_평문" {
		t.Fatalf("읽어 온 토큰=%q ok=%v err=%v", got, ok, err)
	}
}

// Threads 토큰도 잠가서 넣는다. 암호화 전에 저장된 평문은 그대로 읽는다.
func TestThreadsToken_잠가서_넣고_옛_평문도_읽는다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "sealed-threads"
	t.Cleanup(func() { db.DeleteThreadsUser(ctx, user) })

	if err := db.SaveThreadsUser(ctx, ThreadsUser{
		UserID: user, Username: "me", AccessToken: "TH_평문", ExpiresAt: nowPlusDays(30),
	}); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := db.pool.QueryRow(ctx,
		`SELECT access_token FROM threads_users WHERE user_id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "평문") {
		t.Fatalf("평문이 그대로 있다: %q", stored)
	}
	u, ok, err := db.GetThreadsUser(ctx, user)
	if err != nil || !ok || u.AccessToken != "TH_평문" {
		t.Fatalf("읽어 온 토큰=%q ok=%v err=%v", u.AccessToken, ok, err)
	}

	// 암호화를 붙이기 전에 저장된 행을 흉내낸다.
	if _, err := db.pool.Exec(ctx,
		`UPDATE threads_users SET access_token = $2 WHERE user_id = $1`, user, "TH_옛평문"); err != nil {
		t.Fatal(err)
	}
	u, ok, err = db.GetThreadsUser(ctx, user)
	if err != nil || !ok || u.AccessToken != "TH_옛평문" {
		t.Fatalf("옛 평문 토큰=%q ok=%v err=%v", u.AccessToken, ok, err)
	}
}

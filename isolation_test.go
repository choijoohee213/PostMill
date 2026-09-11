package main

import (
	"context"
	"testing"
)

// 사용자 두 명이 서로의 글에 손댈 수 없어야 한다.
// 여기가 뚫리면 남의 글이 내 목록에 보이거나, 더 나쁘게는
// 내 글이 남의 스레드 계정으로 발행된다.
func TestIsolation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	const me, sister = "user-me", "user-sister"

	myID, err := db.CreateDraft(ctx, me, AffiliateCoupang, "", "https://l/me", "내 메모")
	if err != nil {
		t.Fatal(err)
	}
	herID, err := db.CreateDraft(ctx, sister, AffiliateToss, "", "https://l/her", "언니 메모")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.DeletePost(ctx, me, myID)
		db.DeletePost(ctx, sister, herID)
	})

	db.SetGenerated(ctx, myID, "내 본문", "")
	db.SetGenerated(ctx, herID, "언니 본문", "")

	t.Run("남의 글은 조회되지 않는다", func(t *testing.T) {
		if _, err := db.GetPost(ctx, me, herID); err == nil {
			t.Fatal("언니 글이 내게 조회됐다")
		}
		if _, err := db.GetPost(ctx, me, myID); err != nil {
			t.Fatalf("내 글이 조회되지 않는다: %v", err)
		}
	})

	t.Run("목록에 남의 글이 섞이지 않는다", func(t *testing.T) {
		posts, err := db.ListByStatus(ctx, me, StatusPending)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range posts {
			if p.UserID != me {
				t.Fatalf("남의 글이 목록에 있다: id=%d owner=%s", p.ID, p.UserID)
			}
		}
	})

	t.Run("남의 글은 수정되지 않는다", func(t *testing.T) {
		if err := db.UpdateBody(ctx, me, herID, "해킹된 본문", ""); err != nil {
			t.Fatal(err)
		}
		her, err := db.GetPost(ctx, sister, herID)
		if err != nil {
			t.Fatal(err)
		}
		if her.Body != "언니 본문" {
			t.Fatalf("언니 글이 수정됐다: %q", her.Body)
		}
	})

	t.Run("남의 글은 상태가 바뀌지 않는다", func(t *testing.T) {
		if err := db.SetStatus(ctx, me, herID, StatusHeld); err != nil {
			t.Fatal(err)
		}
		her, _ := db.GetPost(ctx, sister, herID)
		if her.Status != StatusPending {
			t.Fatalf("언니 글 상태가 바뀌었다: %s", her.Status)
		}
	})

	t.Run("남의 글은 발행 선점이 되지 않는다", func(t *testing.T) {
		ok, err := db.ClaimForPublish(ctx, me, herID)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("언니 글을 내가 발행하려 선점했다")
		}
		her, _ := db.GetPost(ctx, sister, herID)
		if her.Status != StatusPending {
			t.Fatalf("언니 글 상태가 바뀌었다: %s", her.Status)
		}
	})

	t.Run("남의 글은 삭제되지 않는다", func(t *testing.T) {
		if err := db.DeletePost(ctx, me, herID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetPost(ctx, sister, herID); err != nil {
			t.Fatal("언니 글이 삭제됐다")
		}
	})

	t.Run("발행 이력에 남의 글이 섞이지 않는다", func(t *testing.T) {
		db.SetStatus(ctx, sister, herID, StatusApproved)
		db.ClaimForPublish(ctx, sister, herID)
		db.MarkPublished(ctx, herID, "https://threads/her")

		posts, err := db.ListPublished(ctx, me)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range posts {
			if p.UserID != me {
				t.Fatalf("남의 발행 글이 이력에 있다: id=%d owner=%s", p.ID, p.UserID)
			}
		}
	})
}

func TestThreadsUser_사용자별로_토큰이_분리된다(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	a := ThreadsUser{UserID: "u-a", Username: "alice", AccessToken: "token-a"}
	b := ThreadsUser{UserID: "u-b", Username: "bob", AccessToken: "token-b"}
	a.ExpiresAt, b.ExpiresAt = nowPlusDays(60), nowPlusDays(60)

	if err := db.SaveThreadsUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveThreadsUser(ctx, b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.DeleteThreadsUser(ctx, "u-a")
		db.DeleteThreadsUser(ctx, "u-b")
	})

	got, ok, err := db.GetThreadsUser(ctx, "u-a")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.AccessToken != "token-a" {
		t.Fatalf("토큰이 섞였다: %q", got.AccessToken)
	}

	// 한 명을 지워도 다른 사람은 남아야 한다.
	if err := db.DeleteThreadsUser(ctx, "u-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GetThreadsUser(ctx, "u-a"); ok {
		t.Fatal("지운 사용자가 남아 있다")
	}
	if _, ok, _ := db.GetThreadsUser(ctx, "u-b"); !ok {
		t.Fatal("다른 사용자까지 지워졌다")
	}
}

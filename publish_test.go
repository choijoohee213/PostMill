package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// publishRecorder는 올라간 글을 순서대로 기록하는 가짜 Threads다.
type publishRecorder struct {
	mu     sync.Mutex
	posted []capturedCall
	failAt int // 몇 번째 발행에서 실패할지 (0이면 실패 없음)
	n      int
}

func (rec *publishRecorder) server(t *testing.T) *Threads {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/me/threads":
			rec.mu.Lock()
			rec.n++
			n := rec.n
			if rec.failAt > 0 && n == rec.failAt {
				rec.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"nope"}}`)
				return
			}
			rec.posted = append(rec.posted, capturedCall{
				Text: r.FormValue("text"), ReplyTo: r.FormValue("reply_to_id"),
			})
			rec.mu.Unlock()
			fmt.Fprintf(w, `{"id":"c%d"}`, n)
		case "/me/threads_publish":
			fmt.Fprintf(w, `{"id":"%s"}`, "p"+strings.TrimPrefix(r.FormValue("creation_id"), "c"))
		default:
			if r.URL.Query().Get("fields") == "status,error_message" {
				fmt.Fprint(w, `{"status":"FINISHED"}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"permalink": "https://threads/p"})
		}
	}))
	t.Cleanup(srv.Close)

	th := NewThreads()
	th.HTTP = srv.Client()
	th.BaseURL = srv.URL + "/"
	return th
}

func (rec *publishRecorder) calls() []capturedCall {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]capturedCall(nil), rec.posted...)
}

func publishApp(t *testing.T, th *Threads) *app {
	t.Helper()
	return &app{db: openTestDB(t), threads: th}
}

func newPublishable(t *testing.T, db *DB, user string) *Post {
	t.Helper()
	ctx := context.Background()
	id, err := db.CreateDraft(ctx, user, AffiliateCoupang, "", "https://link/x", "메모")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DeletePost(ctx, user, id) })

	if err := db.SetGenerated(ctx, id, "본문", "답글하나", "답글둘"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimForPublish(ctx, user, id); err != nil || !ok {
		t.Fatalf("선점 실패: ok=%v err=%v", ok, err)
	}
	p, err := db.GetPost(ctx, user, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunPublish_게시물과_답글_세개를_사슬로_잇는다(t *testing.T) {
	rec := &publishRecorder{}
	a := publishApp(t, rec.server(t))
	p := newPublishable(t, a.db, "pub-chain")

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	got := rec.calls()
	want := []capturedCall{
		{Text: "조립된 본문", ReplyTo: ""},
		{Text: "답글하나", ReplyTo: "p1"},
		{Text: "답글둘", ReplyTo: "p2"},
		{Text: "https://link/x", ReplyTo: "p3"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d개 올라감: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d번째: %+v, 기대 %+v", i, got[i], want[i])
		}
	}

	after, _ := a.db.GetPost(context.Background(), p.UserID, p.ID)
	if after.Status != StatusPublished {
		t.Fatalf("상태=%s", after.Status)
	}
	if after.RepliesDone != 3 {
		t.Fatalf("답글 진행=%d, 3이어야 한다", after.RepliesDone)
	}
}

// 게시가 중간에 끊긴 뒤 이어받을 때 본문을 다시 올리면 같은 글이 두 번 올라간다.
func TestRunPublish_이어받을_때_본문을_다시_올리지_않는다(t *testing.T) {
	// 3번째 발행(답글2)에서 끊긴 상황을 만든다.
	first := &publishRecorder{failAt: 3}
	a := publishApp(t, first.server(t))
	p := newPublishable(t, a.db, "pub-resume")

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	mid, _ := a.db.GetPost(context.Background(), p.UserID, p.ID)
	if mid.Status != StatusPublishing {
		t.Fatalf("끊긴 뒤 상태=%s, publishing이어야 한다", mid.Status)
	}
	if mid.ThreadPostID == "" {
		t.Fatal("본문 id가 저장되지 않았다. 이어받을 때 본문을 또 올린다")
	}
	if mid.RepliesDone != 1 {
		t.Fatalf("답글 진행=%d, 1이어야 한다", mid.RepliesDone)
	}

	// 새 가짜 서버로 이어받는다.
	second := &publishRecorder{}
	a.threads = second.server(t)
	a.runPublish(mid, "tok", "조립된 본문", "https://link/x")

	got := second.calls()
	for _, c := range got {
		if c.Text == "조립된 본문" {
			t.Fatal("본문을 다시 올렸다")
		}
	}
	want := []capturedCall{
		{Text: "답글둘", ReplyTo: mid.LastReplyID},
		{Text: "https://link/x", ReplyTo: "p1"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d개 올라감: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d번째: %+v, 기대 %+v", i, got[i], want[i])
		}
	}

	after, _ := a.db.GetPost(context.Background(), p.UserID, p.ID)
	if after.Status != StatusPublished {
		t.Fatalf("상태=%s", after.Status)
	}
}

// 본문조차 못 올렸으면 되돌려도 안전하다. 다시 누를 수 있어야 한다.
func TestRunPublish_본문이_실패하면_failed로_되돌린다(t *testing.T) {
	rec := &publishRecorder{failAt: 1}
	a := publishApp(t, rec.server(t))
	p := newPublishable(t, a.db, "pub-bodyfail")

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	after, _ := a.db.GetPost(context.Background(), p.UserID, p.ID)
	if after.Status != StatusFailed {
		t.Fatalf("상태=%s, failed여야 한다", after.Status)
	}
	if !strings.Contains(after.ErrorMsg, "게시 실패") {
		t.Fatalf("사유=%q", after.ErrorMsg)
	}
}

func TestRunPublish_빈_답글은_건너뛰고_진행으로_기록한다(t *testing.T) {
	rec := &publishRecorder{}
	a := publishApp(t, rec.server(t))
	ctx := context.Background()

	const user = "pub-empty"
	id, _ := a.db.CreateDraft(ctx, user, AffiliateCoupang, "", "https://link/x", "메모")
	t.Cleanup(func() { a.db.DeletePost(ctx, user, id) })
	a.db.SetGenerated(ctx, id, "본문", "", "")
	a.db.ClaimForPublish(ctx, user, id)
	p, _ := a.db.GetPost(ctx, user, id)

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	got := rec.calls()
	// 본문 + 링크만 올라가야 한다.
	if len(got) != 2 {
		t.Fatalf("%d개 올라감: %+v", len(got), got)
	}
	if got[1].Text != "https://link/x" || got[1].ReplyTo != "p1" {
		t.Errorf("링크가 본문에 달리지 않았다: %+v", got[1])
	}
}

func TestCanResume_시간이_지나야_이어받을_수_있다(t *testing.T) {
	old := time.Now().Add(-publishStaleAfter - time.Minute)
	fresh := time.Now()

	if canResume(&Post{Status: StatusPublishing, PublishStartedAt: &fresh}) {
		t.Error("방금 시작한 게시를 이어받으려 한다")
	}
	if !canResume(&Post{Status: StatusPublishing, PublishStartedAt: &old}) {
		t.Error("멈춘 게시를 이어받지 못한다")
	}
	if canResume(&Post{Status: StatusPublished, PublishStartedAt: &old}) {
		t.Error("이미 끝난 게시를 이어받으려 한다")
	}
	if canResume(&Post{Status: StatusPublishing}) {
		t.Error("시작 시각이 없는데 이어받으려 한다")
	}
}

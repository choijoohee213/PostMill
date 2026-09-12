package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 발행 핸들러 전체를 실제 DB + 가짜 Threads로 돌린다.
func newTestApp(t *testing.T, threadsSrv *httptest.Server) *app {
	t.Helper()
	db := openTestDB(t)

	th := NewThreads()
	th.HTTP = threadsSrv.Client()
	th.BaseURL = threadsSrv.URL + "/"

	// 편집 화면 렌더링만 필요하므로 최소 템플릿으로 대체한다.
	tpl := template.Must(template.New("edit.html").Parse(`{{.Error}}`))

	return &app{db: db, tpl: nil, threads: th, testTpl: tpl,
		session: &session{secret: []byte("test-secret")}}
}

func TestPublishHandler_중복_발행을_막는다(t *testing.T) {
	var mu sync.Mutex
	published := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/me/threads":
			// 답글이 아닌 컨테이너, 즉 본문 발행 시도만 센다.
			if r.FormValue("reply_to_id") == "" {
				mu.Lock()
				published++
				mu.Unlock()
			}
			fmt.Fprint(w, `{"id":"c1"}`)
		case "/me/threads_publish":
			// 느린 네트워크를 흉내낸다. 이 사이에 두 번째 요청이 들어온다.
			time.Sleep(300 * time.Millisecond)
			fmt.Fprint(w, `{"id":"post-1"}`)
		default:
			if r.URL.Query().Get("fields") == "status,error_message" {
				fmt.Fprint(w, `{"status":"FINISHED"}`)
				return
			}
			fmt.Fprint(w, `{"permalink":"https://threads.net/p/1"}`)
		}
	}))
	defer srv.Close()

	a := newTestApp(t, srv)
	ctx := context.Background()

	const testUser = "publish-test-user"
	a.db.SaveThreadsUser(ctx, ThreadsUser{
		UserID: testUser, Username: "tester",
		AccessToken: "test-token", ExpiresAt: nowPlusDays(60),
	})
	t.Cleanup(func() { a.db.DeleteThreadsUser(ctx, testUser) })

	id, _ := a.db.CreateDraft(ctx, testUser, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "발행할 본문이다", "", "")
	t.Cleanup(func() { a.db.DeletePost(ctx, testUser, id) })

	// 버튼을 두 번 누른 상황을 흉내낸다.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := a.signedRequest(t, testUser, fmt.Sprintf("/drafts/%d/publish", id), id)
			a.handlePublish(httptest.NewRecorder(), req)
		}()
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()

	// 게시는 백그라운드로 도므로 끝날 때까지 기다린다.
	var p *Post
	deadline := time.Now().Add(30 * time.Second)
	for {
		p, _ = a.db.GetPost(ctx, testUser, id)
		if p != nil && p.Status == StatusPublished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("게시가 끝나지 않았다. 상태=%s", p.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}

	mu.Lock()
	n := published
	mu.Unlock()
	// 버튼을 두 번 눌렀어도 본문은 한 번만 올라가야 한다.
	if n != 1 {
		t.Fatalf("본문 발행이 %d회 일어났다. 1회여야 한다", n)
	}
	if p.ThreadPermalink == "" {
		t.Fatal("퍼머링크가 비었다")
	}
	if p.RepliesDone != 3 {
		t.Fatalf("답글 진행=%d, 3이어야 한다", p.RepliesDone)
	}
}

func TestPublishHandler_토큰이_없으면_발행하지_않는다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("토큰이 없는데 Threads를 호출했다")
	}))
	defer srv.Close()

	a := newTestApp(t, srv)
	ctx := context.Background()

	const testUser = "publish-test-notoken"
	a.db.DeleteThreadsUser(ctx, testUser)
	id, _ := a.db.CreateDraft(ctx, testUser, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "본문", "", "")
	t.Cleanup(func() { a.db.DeletePost(ctx, testUser, id) })

	rec := httptest.NewRecorder()
	a.handlePublish(rec, a.signedRequest(t, testUser, "/publish", id))

	if !strings.Contains(rec.Body.String(), "토큰으로 로그인") {
		t.Fatalf("안내 문구가 없다: %q", rec.Body.String())
	}
	p, _ := a.db.GetPost(ctx, testUser, id)
	if p.Status != StatusPending {
		t.Fatalf("상태=%s, pending 그대로여야 한다", p.Status)
	}
}

func TestPublishHandler_본문이_비면_발행하지_않는다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("본문이 비었는데 Threads를 호출했다")
	}))
	defer srv.Close()

	a := newTestApp(t, srv)
	ctx := context.Background()

	const testUser = "publish-test-user"
	a.db.SaveThreadsUser(ctx, ThreadsUser{
		UserID: testUser, Username: "tester",
		AccessToken: "test-token", ExpiresAt: nowPlusDays(60),
	})
	t.Cleanup(func() { a.db.DeleteThreadsUser(ctx, testUser) })

	id, _ := a.db.CreateDraft(ctx, testUser, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "", "", "")
	t.Cleanup(func() { a.db.DeletePost(ctx, testUser, id) })

	rec := httptest.NewRecorder()
	a.handlePublish(rec, a.signedRequest(t, testUser, "/publish", id))

	if !strings.Contains(rec.Body.String(), "게시할 수 없어요") {
		t.Fatalf("가드가 동작하지 않았다: %q", rec.Body.String())
	}
}

// signedRequest는 그 사용자로 로그인한 상태의 요청을 만든다.
// 핸들러가 세션에서 사용자를 읽으므로 쿠키가 없으면 404가 된다.
func (a *app) signedRequest(t *testing.T, userID, path string, id int64) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	a.session.issue(rec, userID, false)

	req := httptest.NewRequest(http.MethodPost, path, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	req.SetPathValue("id", fmt.Sprint(id))
	return req
}

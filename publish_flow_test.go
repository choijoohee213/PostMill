package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// 발행 핸들러 전체를 실제 DB + 가짜 Threads로 돌린다.
func newTestApp(t *testing.T, threadsSrv *httptest.Server) *app {
	t.Helper()
	db, err := Open(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Skipf("DATABASE_URL 없음: %v", err)
	}
	t.Cleanup(db.Close)

	th := NewThreads()
	th.HTTP = threadsSrv.Client()
	th.BaseURL = threadsSrv.URL + "/"

	// 편집 화면 렌더링만 필요하므로 최소 템플릿으로 대체한다.
	tpl := template.Must(template.New("edit.html").Parse(`{{.Error}}`))

	return &app{db: db, tpl: nil, threads: th, testTpl: tpl}
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
			fmt.Fprint(w, `{"permalink":"https://threads.net/p/1"}`)
		}
	}))
	defer srv.Close()

	a := newTestApp(t, srv)
	ctx := context.Background()

	a.db.SetState(ctx, stateAccessToken, "test-token")
	id, _ := a.db.CreateDraft(ctx, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "발행할 본문이다")
	t.Cleanup(func() { a.db.DeletePost(ctx, id) })

	// 버튼을 두 번 누른 상황을 흉내낸다.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/drafts/%d/publish", id), nil)
			req.SetPathValue("id", fmt.Sprint(id))
			a.handlePublish(httptest.NewRecorder(), req)
		}()
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()

	mu.Lock()
	n := published
	mu.Unlock()
	if n != 1 {
		t.Fatalf("본문 발행이 %d회 일어났다. 1회여야 한다", n)
	}

	p, _ := a.db.GetPost(ctx, id)
	if p.Status != StatusPublished {
		t.Fatalf("상태=%s, published여야 한다", p.Status)
	}
	if p.ThreadPermalink == "" {
		t.Fatal("퍼머링크가 비었다")
	}
}

func TestPublishHandler_토큰이_없으면_발행하지_않는다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("토큰이 없는데 Threads를 호출했다")
	}))
	defer srv.Close()

	a := newTestApp(t, srv)
	ctx := context.Background()

	a.db.pool.Exec(ctx, `DELETE FROM app_state WHERE key = $1`, stateAccessToken)
	id, _ := a.db.CreateDraft(ctx, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "본문")
	t.Cleanup(func() { a.db.DeletePost(ctx, id) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/publish", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	a.handlePublish(rec, req)

	if !strings.Contains(rec.Body.String(), "연결되지 않았습니다") {
		t.Fatalf("안내 문구가 없다: %q", rec.Body.String())
	}
	p, _ := a.db.GetPost(ctx, id)
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

	a.db.SetState(ctx, stateAccessToken, "test-token")
	id, _ := a.db.CreateDraft(ctx, AffiliateCoupang, "", "https://link.example/x", "메모")
	a.db.SetGenerated(ctx, id, "")
	t.Cleanup(func() { a.db.DeletePost(ctx, id) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/publish", nil)
	req.SetPathValue("id", fmt.Sprint(id))
	a.handlePublish(rec, req)

	if !strings.Contains(rec.Body.String(), "발행할 수 없습니다") {
		t.Fatalf("가드가 동작하지 않았다: %q", rec.Body.String())
	}
}

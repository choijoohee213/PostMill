package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type capturedCall struct {
	Path    string
	Text    string
	ReplyTo string
}

// fakeThreads는 컨테이너 생성과 발행을 흉내내고 호출 내용을 기록한다.
func fakeThreads(t *testing.T, opts fakeOpts) (*Threads, *[]capturedCall) {
	t.Helper()
	var calls []capturedCall

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		path := r.URL.Path
		calls = append(calls, capturedCall{
			Path:    path,
			Text:    r.FormValue("text"),
			ReplyTo: r.FormValue("reply_to_id"),
		})

		switch {
		case path == "/me/threads":
			isReply := r.FormValue("reply_to_id") != ""
			if isReply && opts.replyContainerFails {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"reply not allowed"}}`)
				return
			}
			if !isReply && opts.bodyFails {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"invalid token"}}`)
				return
			}
			fmt.Fprint(w, `{"id":"container-1"}`)

		case path == "/me/threads_publish":
			fmt.Fprint(w, `{"id":"post-99"}`)

		default: // 퍼머링크 조회
			if opts.permalinkFails {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{
				"permalink": "https://www.threads.net/@me/post/abc",
			})
		}
	}))
	t.Cleanup(srv.Close)

	th := NewThreads()
	th.HTTP = srv.Client()
	th.BaseURL = srv.URL + "/"
	return th, &calls
}

type fakeOpts struct {
	bodyFails           bool
	replyContainerFails bool
	permalinkFails      bool
}

func TestPublish_본문과_답글을_순서대로_올린다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	res, err := th.Publish(context.Background(), "tok", "본문 텍스트", "https://link")
	if err != nil {
		t.Fatalf("실패: %v", err)
	}
	if res.PostID != "post-99" {
		t.Fatalf("PostID=%q", res.PostID)
	}
	if res.Permalink != "https://www.threads.net/@me/post/abc" {
		t.Fatalf("Permalink=%q", res.Permalink)
	}
	if res.ReplyErr != nil {
		t.Fatalf("ReplyErr=%v", res.ReplyErr)
	}

	// 본문 컨테이너 → 본문 발행 → 답글 컨테이너 → 답글 발행 → 퍼머링크
	if len(*calls) != 5 {
		t.Fatalf("호출 %d회: %+v", len(*calls), *calls)
	}
	c := *calls
	if c[0].Text != "본문 텍스트" || c[0].ReplyTo != "" {
		t.Errorf("본문 컨테이너가 이상함: %+v", c[0])
	}
	if c[2].Text != "https://link" {
		t.Errorf("답글 본문이 이상함: %+v", c[2])
	}
	// 답글은 발행된 글 ID를 가리켜야 한다. 컨테이너 ID가 아니다.
	if c[2].ReplyTo != "post-99" {
		t.Errorf("답글 대상이 %q, post-99여야 한다", c[2].ReplyTo)
	}
}

func TestPublish_본문이_실패하면_에러다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{bodyFails: true})

	if _, err := th.Publish(context.Background(), "tok", "본문", "https://link"); err == nil {
		t.Fatal("에러여야 한다")
	}
	// 본문이 실패했으면 답글을 시도하면 안 된다.
	for _, c := range *calls {
		if c.ReplyTo != "" {
			t.Fatalf("본문 실패인데 답글을 시도함: %+v", c)
		}
	}
}

func TestPublish_답글만_실패하면_발행은_성공이다(t *testing.T) {
	// 본문이 이미 스레드에 올라갔으므로 실패로 되돌리면 안 된다.
	// 되돌리면 사용자가 다시 눌러 같은 글이 두 번 올라간다.
	th, _ := fakeThreads(t, fakeOpts{replyContainerFails: true})

	res, err := th.Publish(context.Background(), "tok", "본문", "https://link")
	if err != nil {
		t.Fatalf("본문은 성공했으므로 에러가 아니어야 한다: %v", err)
	}
	if res.PostID != "post-99" {
		t.Fatalf("PostID=%q", res.PostID)
	}
	if res.ReplyErr == nil {
		t.Fatal("ReplyErr에 답글 실패가 담겨야 한다")
	}
}

func TestPublish_퍼머링크_실패는_발행을_막지_않는다(t *testing.T) {
	th, _ := fakeThreads(t, fakeOpts{permalinkFails: true})

	res, err := th.Publish(context.Background(), "tok", "본문", "https://link")
	if err != nil {
		t.Fatalf("에러가 아니어야 한다: %v", err)
	}
	if res.PostID != "post-99" {
		t.Fatalf("PostID=%q", res.PostID)
	}
	if res.Permalink != "" {
		t.Fatalf("퍼머링크는 비어야 한다: %q", res.Permalink)
	}
}

func TestRefreshToken_새_토큰과_만료를_돌려준다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("grant_type") != "th_refresh_token" {
			t.Errorf("grant_type=%q", r.URL.Query().Get("grant_type"))
		}
		fmt.Fprint(w, `{"access_token":"new-token","token_type":"bearer","expires_in":5184000}`)
	}))
	defer srv.Close()

	th := NewThreads()
	th.HTTP = srv.Client()
	// 갱신 엔드포인트는 BaseURL과 다른 호스트를 쓰므로 직접 호출을 흉내낸다.
	got, expiry, err := th.refreshAt(context.Background(), srv.URL+"/", "old-token")
	if err != nil {
		t.Fatalf("실패: %v", err)
	}
	if got != "new-token" {
		t.Fatalf("토큰=%q", got)
	}
	// 60일짜리여야 한다.
	if d := time.Until(expiry); d < 59*24*time.Hour || d > 61*24*time.Hour {
		t.Fatalf("만료까지 %v, 60일 근처여야 한다", d)
	}
}

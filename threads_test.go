package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	statusCalls := 0
	publishCount := 0

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
			// 발행마다 다른 id를 준다. 같은 id면 사슬이 이어졌는지 구분할 수 없다.
			publishCount++
			fmt.Fprintf(w, `{"id":"post-%d"}`, publishCount)

		default:
			// 컨테이너 상태 조회와 퍼머링크 조회가 같은 경로 모양이다.
			if r.URL.Query().Get("fields") == "status,error_message" {
				st := "FINISHED"
				if opts.containerStatuses != nil {
					i := statusCalls
					if i >= len(opts.containerStatuses) {
						i = len(opts.containerStatuses) - 1
					}
					st = opts.containerStatuses[i]
					statusCalls++
				}
				json.NewEncoder(w).Encode(map[string]string{"status": st, "error_message": opts.containerError})
				return
			}
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
	containerStatuses   []string // 상태 조회에 순서대로 돌려줄 값
	containerError      string
}

func TestPublishText_컨테이너를_만들고_발행한다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	id, err := th.PublishText(context.Background(), "tok", "본문 텍스트", "")
	if err != nil {
		t.Fatalf("실패: %v", err)
	}
	if id != "post-1" {
		t.Fatalf("id=%q", id)
	}

	c := *calls
	if c[0].Path != "/me/threads" || c[0].Text != "본문 텍스트" || c[0].ReplyTo != "" {
		t.Errorf("컨테이너 생성이 이상함: %+v", c[0])
	}
	// 발행 전에 반드시 상태를 확인해야 한다. 준비되기 전에 발행하면 실패한다.
	if c[1].Path == "/me/threads_publish" {
		t.Error("상태를 확인하지 않고 발행했다")
	}
}

func TestPublishText_답글은_부모를_가리킨다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	if _, err := th.PublishText(context.Background(), "tok", "답글", "parent-9"); err != nil {
		t.Fatalf("실패: %v", err)
	}
	if c := (*calls)[0]; c.ReplyTo != "parent-9" {
		t.Fatalf("ReplyTo=%q", c.ReplyTo)
	}
}

func TestPublishText_컨테이너_생성이_실패하면_발행하지_않는다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{bodyFails: true})

	if _, err := th.PublishText(context.Background(), "tok", "본문", ""); err == nil {
		t.Fatal("에러여야 한다")
	}
	for _, c := range *calls {
		if c.Path == "/me/threads_publish" {
			t.Fatal("컨테이너 생성이 실패했는데 발행을 시도했다")
		}
	}
}

func TestPublishText_컨테이너가_준비될_때까지_기다린다(t *testing.T) {
	// 만든 직후에는 IN_PROGRESS다. 이때 발행하면 실패한다.
	orig := containerPollInterval
	containerPollInterval = time.Millisecond
	defer func() { containerPollInterval = orig }()

	th, calls := fakeThreads(t, fakeOpts{
		containerStatuses: []string{"IN_PROGRESS", "IN_PROGRESS", "FINISHED"},
	})

	if _, err := th.PublishText(context.Background(), "tok", "본문", ""); err != nil {
		t.Fatalf("기다린 뒤 성공했어야 한다: %v", err)
	}
	publishes := 0
	for _, c := range *calls {
		if c.Path == "/me/threads_publish" {
			publishes++
		}
	}
	if publishes != 1 {
		t.Fatalf("발행 %d회, 1회여야 한다", publishes)
	}
}

func TestPublishText_컨테이너가_ERROR면_사유를_알린다(t *testing.T) {
	orig := containerPollInterval
	containerPollInterval = time.Millisecond
	defer func() { containerPollInterval = orig }()

	th, _ := fakeThreads(t, fakeOpts{
		containerStatuses: []string{"ERROR"},
		containerError:    "text too long",
	})

	_, err := th.PublishText(context.Background(), "tok", "본문", "")
	if err == nil || !strings.Contains(err.Error(), "text too long") {
		t.Fatalf("사유가 담기지 않았다: %v", err)
	}
}

func TestPublishText_준비되지_않으면_시간초과로_멈춘다(t *testing.T) {
	origI, origT := containerPollInterval, containerPollTimeout
	containerPollInterval, containerPollTimeout = time.Millisecond, 5*time.Millisecond
	defer func() { containerPollInterval, containerPollTimeout = origI, origT }()

	th, _ := fakeThreads(t, fakeOpts{containerStatuses: []string{"IN_PROGRESS"}})

	if _, err := th.PublishText(context.Background(), "tok", "본문", ""); err == nil {
		t.Fatal("시간 초과로 에러여야 한다")
	}
}

func TestPermalink_실패하면_에러를_돌려준다(t *testing.T) {
	th, _ := fakeThreads(t, fakeOpts{permalinkFails: true})

	if _, err := th.Permalink(context.Background(), "tok", "post-1"); err == nil {
		t.Fatal("에러여야 한다")
	}
}

func TestAuthorizeHost_사라진_상수를_쓰지_않는다(t *testing.T) {
	// OAuth 승인 흐름을 걷어냈으므로 권한 목록만 남아 있어야 한다.
	if !strings.Contains(threadsScopes, "threads_manage_replies") {
		t.Fatalf("scope에 threads_manage_replies가 없다: %q", threadsScopes)
	}
}

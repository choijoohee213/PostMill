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

func TestPublish_본문과_답글을_순서대로_올린다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	res, err := th.Publish(context.Background(), "tok", "본문 텍스트", "", "https://link")
	if err != nil {
		t.Fatalf("실패: %v", err)
	}
	if res.PostID != "post-1" {
		t.Fatalf("PostID=%q", res.PostID)
	}
	if res.Permalink != "https://www.threads.net/@me/post/abc" {
		t.Fatalf("Permalink=%q", res.Permalink)
	}
	if res.ReplyErr != nil {
		t.Fatalf("ReplyErr=%v", res.ReplyErr)
	}

	// 본문 컨테이너 → 상태 확인 → 발행 → 답글 컨테이너 → 상태 확인 → 발행 → 퍼머링크
	if len(*calls) != 7 {
		t.Fatalf("호출 %d회: %+v", len(*calls), *calls)
	}
	c := *calls
	if c[0].Text != "본문 텍스트" || c[0].ReplyTo != "" {
		t.Errorf("본문 컨테이너가 이상함: %+v", c[0])
	}
	// 발행 전에 반드시 상태를 확인해야 한다. 준비되기 전에 발행하면 실패한다.
	if c[1].Path == "/me/threads_publish" {
		t.Error("상태를 확인하지 않고 발행했다")
	}
	if c[3].Text != "https://link" {
		t.Errorf("답글 본문이 이상함: %+v", c[3])
	}
	// 답글은 발행된 글 ID를 가리켜야 한다. 컨테이너 ID가 아니다.
	if c[3].ReplyTo != "post-1" {
		t.Errorf("답글 대상이 %q, post-1여야 한다", c[3].ReplyTo)
	}
}

func TestPublish_본문이_실패하면_에러다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{bodyFails: true})

	if _, err := th.Publish(context.Background(), "tok", "본문", "", "https://link"); err == nil {
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

	res, err := th.Publish(context.Background(), "tok", "본문", "", "https://link")
	if err != nil {
		t.Fatalf("본문은 성공했으므로 에러가 아니어야 한다: %v", err)
	}
	if res.PostID != "post-1" {
		t.Fatalf("PostID=%q", res.PostID)
	}
	if res.ReplyErr == nil {
		t.Fatal("ReplyErr에 답글 실패가 담겨야 한다")
	}
}

func TestPublish_퍼머링크_실패는_발행을_막지_않는다(t *testing.T) {
	th, _ := fakeThreads(t, fakeOpts{permalinkFails: true})

	res, err := th.Publish(context.Background(), "tok", "본문", "", "https://link")
	if err != nil {
		t.Fatalf("에러가 아니어야 한다: %v", err)
	}
	if res.PostID != "post-1" {
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

func TestPublish_컨테이너가_준비될_때까지_기다린다(t *testing.T) {
	// 만든 직후에는 IN_PROGRESS다. 이때 발행하면 실패한다.
	orig := containerPollInterval
	containerPollInterval = time.Millisecond
	defer func() { containerPollInterval = orig }()

	th, calls := fakeThreads(t, fakeOpts{
		containerStatuses: []string{"IN_PROGRESS", "IN_PROGRESS", "FINISHED"},
	})

	if _, err := th.Publish(context.Background(), "tok", "본문", "", "https://link"); err != nil {
		t.Fatalf("기다린 뒤 성공했어야 한다: %v", err)
	}

	// 본문과 답글 각각 컨테이너를 만들고 발행하므로 발행 호출은 2회다.
	publishes := 0
	for _, c := range *calls {
		if c.Path == "/me/threads_publish" {
			publishes++
		}
	}
	if publishes != 2 {
		t.Fatalf("발행 호출 %d회, 2회여야 한다", publishes)
	}
}

func TestPublish_컨테이너가_ERROR면_사유를_알린다(t *testing.T) {
	orig := containerPollInterval
	containerPollInterval = time.Millisecond
	defer func() { containerPollInterval = orig }()

	th, _ := fakeThreads(t, fakeOpts{
		containerStatuses: []string{"ERROR"},
		containerError:    "text too long",
	})

	_, err := th.Publish(context.Background(), "tok", "본문", "", "https://link")
	if err == nil {
		t.Fatal("에러여야 한다")
	}
	if !strings.Contains(err.Error(), "text too long") {
		t.Fatalf("사유가 담기지 않았다: %v", err)
	}
}

func TestPublish_준비되지_않으면_시간초과로_멈춘다(t *testing.T) {
	origI, origT := containerPollInterval, containerPollTimeout
	containerPollInterval, containerPollTimeout = time.Millisecond, 5*time.Millisecond
	defer func() { containerPollInterval, containerPollTimeout = origI, origT }()

	th, _ := fakeThreads(t, fakeOpts{containerStatuses: []string{"IN_PROGRESS"}})

	if _, err := th.Publish(context.Background(), "tok", "본문", "", "https://link"); err == nil {
		t.Fatal("시간 초과로 에러여야 한다")
	}
}

func TestPublish_디테일과_링크를_사슬로_잇는다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	res, err := th.Publish(context.Background(), "tok", "본문", "디테일 내용", "https://link")
	if err != nil {
		t.Fatalf("실패: %v", err)
	}
	if res.ReplyErr != nil {
		t.Fatalf("ReplyErr=%v", res.ReplyErr)
	}

	// 컨테이너 생성 호출만 추려서 순서와 부모를 확인한다.
	var containers []capturedCall
	for _, c := range *calls {
		if c.Path == "/me/threads" {
			containers = append(containers, c)
		}
	}
	if len(containers) != 3 {
		t.Fatalf("컨테이너 %d개: %+v", len(containers), containers)
	}
	if containers[0].Text != "본문" || containers[0].ReplyTo != "" {
		t.Errorf("본문: %+v", containers[0])
	}
	// 디테일은 본문(post-1)에 달린다.
	if containers[1].Text != "디테일 내용" || containers[1].ReplyTo != "post-1" {
		t.Errorf("디테일: %+v", containers[1])
	}
	// 링크는 디테일(post-2)에 달려 사슬이 이어진다.
	if containers[2].Text != "https://link" || containers[2].ReplyTo != "post-2" {
		t.Errorf("링크: %+v", containers[2])
	}
}

func TestPublish_디테일이_없으면_링크를_본문에_단다(t *testing.T) {
	th, calls := fakeThreads(t, fakeOpts{})

	if _, err := th.Publish(context.Background(), "tok", "본문", "", "https://link"); err != nil {
		t.Fatalf("실패: %v", err)
	}

	var containers []capturedCall
	for _, c := range *calls {
		if c.Path == "/me/threads" {
			containers = append(containers, c)
		}
	}
	if len(containers) != 2 {
		t.Fatalf("컨테이너 %d개여야 한다: %+v", len(containers), containers)
	}
	if containers[1].ReplyTo != "post-1" {
		t.Errorf("링크가 본문에 달리지 않았다: %+v", containers[1])
	}
}

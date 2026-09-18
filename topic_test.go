package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeTopic_다듬고_규칙에_맞는지_본다(t *testing.T) {
	cases := []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "  ", want: ""},
		{in: "생활용품", want: "생활용품"},
		{in: " #생활용품 ", want: "생활용품"}, // 해시는 붙여도 되고 안 붙여도 된다
		{in: "가성비.추천", bad: true},     // 마침표 불가
		{in: "책&영화", bad: true},       // & 불가
		{in: strings.Repeat("가", TopicMaxChars), want: strings.Repeat("가", TopicMaxChars)},
		{in: strings.Repeat("가", TopicMaxChars+1), bad: true},
	}
	for _, c := range cases {
		got, err := NormalizeTopic(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q는 거절해야 한다", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q → %q, 원하는 값 %q", c.in, got, c.want)
		}
	}
}

// TestPublishText_주제는_본문에만_붙는다는 Threads 규칙을 지키는지 본다.
// 답글에 topic_tag를 보내면 API가 거절한다.
func TestPublishText_주제는_본문에만_붙는다(t *testing.T) {
	var topics []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/me/threads":
			topics = append(topics, r.FormValue("topic_tag"))
			fmt.Fprint(w, `{"id":"container-1"}`)
		case "/me/threads_publish":
			fmt.Fprint(w, `{"id":"post-1"}`)
		default:
			json.NewEncoder(w).Encode(map[string]string{"status": "FINISHED"})
		}
	}))
	defer srv.Close()
	th := &Threads{HTTP: srv.Client(), BaseURL: srv.URL + "/"}

	if _, err := th.PublishText(context.Background(), "tok", "본문", "", "생활용품"); err != nil {
		t.Fatalf("본문 게시 실패: %v", err)
	}
	if _, err := th.PublishText(context.Background(), "tok", "답글", "post-1", "생활용품"); err != nil {
		t.Fatalf("답글 게시 실패: %v", err)
	}
	if _, err := th.PublishImages(context.Background(), "tok", "본문", "생활용품",
		[]string{srv.URL + "/media/a"}); err != nil {
		t.Fatalf("사진 게시 실패: %v", err)
	}

	want := []string{"생활용품", "", "생활용품"} // 본문, 답글, 사진 본문
	if strings.Join(topics, "|") != strings.Join(want, "|") {
		t.Fatalf("주제가 %q로 갔다, 원하는 값 %q", topics, want)
	}
}

func TestDraftSave_주제를_저장하고_잘못된_주제는_막는다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	user := "topic-user"
	id, err := db.CreateManualDraft(ctx, user, ManualDraft{Affiliate: AffiliateToss, ProductName: "밥솥"})
	if err != nil {
		t.Fatalf("초안 생성 실패: %v", err)
	}
	t.Cleanup(func() { db.DeletePost(ctx, user, id) })
	if err := db.SetGenerated(ctx, id, "본문", "답글", ""); err != nil {
		t.Fatalf("초안 저장 실패: %v", err)
	}

	// 편집 화면 렌더링만 필요하므로 최소 템플릿으로 대체한다.
	a := &app{db: db, testTpl: template.Must(template.New("edit.html").Parse(`{{.Error}}`)),
		session: &session{secret: []byte("test-secret")}}

	save := func(topic string) *httptest.ResponseRecorder {
		form := url.Values{"body": {"본문"}, "detail": {"답글"}, "topic": {topic}}
		req := a.signedRequest(t, user, fmt.Sprintf("/drafts/%d", id), id)
		req.Body = io.NopCloser(strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.handleDraftSave(w, req)
		return w
	}

	if w := save(" #생활용품 "); w.Code != http.StatusSeeOther {
		t.Fatalf("저장이 %d로 끝났다", w.Code)
	}
	p, err := db.GetPost(ctx, user, id)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if p.Topic != "생활용품" {
		t.Fatalf("주제가 %q로 저장됐다", p.Topic)
	}

	if w := save("가성비.추천"); w.Code != http.StatusBadRequest {
		t.Fatalf("마침표가 든 주제인데 %d로 끝났다", w.Code)
	}
	p, _ = db.GetPost(ctx, user, id)
	if p.Topic != "생활용품" {
		t.Fatalf("거절된 주제가 저장됐다: %q", p.Topic)
	}

	topics, err := db.RecentTopics(ctx, user, 8)
	if err != nil {
		t.Fatalf("최근 주제 조회 실패: %v", err)
	}
	if len(topics) != 1 || topics[0] != "생활용품" {
		t.Fatalf("최근 주제가 %q다", topics)
	}
}

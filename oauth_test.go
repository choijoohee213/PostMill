package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// renderConnect가 연결 상태를 읽으므로 실제 DB가 필요하다.
func oauthApp(t *testing.T) *app {
	t.Helper()
	return &app{
		db:        openTestDB(t),
		appID:     "test-app-id",
		appSecret: "test-secret",
		threads:   NewThreads(),
		testTpl:   template.Must(template.New("connect.html").Parse(`{{.Error}}`)),
	}
}

func TestAuthorizeURL_필수_파라미터를_담는다(t *testing.T) {
	raw := AuthorizeURL("app-1", "https://example.com/connect/callback", "st4te")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, threadsAuthorizeURL) {
		t.Fatalf("승인 URL이 아니다: %s", raw)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":     "app-1",
		"redirect_uri":  "https://example.com/connect/callback",
		"response_type": "code",
		"state":         "st4te",
	} {
		if q.Get(k) != want {
			t.Errorf("%s=%q, %q여야 한다", k, q.Get(k), want)
		}
	}
	// 답글로 링크를 올리므로 이 권한이 빠지면 안 된다.
	if !strings.Contains(q.Get("scope"), "threads_manage_replies") {
		t.Errorf("scope에 threads_manage_replies가 없다: %q", q.Get("scope"))
	}
}

func TestConnectStart_state_쿠키를_심고_리다이렉트한다(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "https://app.example/connect/start", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	oauthApp(t).handleConnectStart(rec, r)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, threadsAuthorizeURL) {
		t.Fatalf("Location=%q", loc)
	}

	var state string
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthStateCookie {
			state = c.Value
			if !c.HttpOnly {
				t.Error("state 쿠키가 HttpOnly가 아니다")
			}
		}
	}
	if state == "" {
		t.Fatal("state 쿠키가 없다")
	}
	// 쿠키의 state와 URL의 state가 같아야 대조가 의미 있다.
	u, _ := url.Parse(loc)
	if u.Query().Get("state") != state {
		t.Fatalf("URL state=%q, 쿠키 state=%q", u.Query().Get("state"), state)
	}
	// 리디렉션 URI는 요청 호스트에서 만들어져야 한다.
	if got := u.Query().Get("redirect_uri"); got != "https://app.example/connect/callback" {
		t.Fatalf("redirect_uri=%q", got)
	}
}

// 콜백에 state 없이 code만 들고 오는 요청은 남이 만든 것이다.
func TestCallback_state가_없으면_거부한다(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/connect/callback?code=abc", nil)
	oauthApp(t).handleConnectCallback(rec, r)

	if !strings.Contains(rec.Body.String(), "유효하지 않습니다") {
		t.Fatalf("거부되지 않았다: %q", rec.Body.String())
	}
}

func TestCallback_state가_다르면_거부한다(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/connect/callback?code=abc&state=attacker", nil)
	r.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "mine"})
	oauthApp(t).handleConnectCallback(rec, r)

	if !strings.Contains(rec.Body.String(), "유효하지 않습니다") {
		t.Fatalf("거부되지 않았다: %q", rec.Body.String())
	}
}

func TestCallback_승인_거부를_알린다(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/connect/callback?error=access_denied&error_description=user+denied", nil)
	oauthApp(t).handleConnectCallback(rec, r)

	if !strings.Contains(rec.Body.String(), "승인이 취소") {
		t.Fatalf("안내가 없다: %q", rec.Body.String())
	}
}

func TestCallback_code가_없으면_거부한다(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/connect/callback?state=ok", nil)
	r.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "ok"})
	oauthApp(t).handleConnectCallback(rec, r)

	if !strings.Contains(rec.Body.String(), "인증 코드를 받지 못했습니다") {
		t.Fatalf("거부되지 않았다: %q", rec.Body.String())
	}
}

func TestPublicBase_프록시_뒤에서_https를_알아본다(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://postmill.onrender.com/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := publicBase(r); got != "https://postmill.onrender.com" {
		t.Fatalf("got=%q", got)
	}

	plain := httptest.NewRequest(http.MethodGet, "http://localhost:8123/x", nil)
	if got := publicBase(plain); got != "http://localhost:8123" {
		t.Fatalf("got=%q", got)
	}
}

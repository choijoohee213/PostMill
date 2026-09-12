package main

// 토큰 로그인은 지금 화면과 라우트에서 빠져 있다. 되살릴 때를 위해
// 핸들러 자체의 동작은 그대로 검증해 둔다.
import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func tokenLoginApp(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	th := NewThreads()
	th.HTTP = srv.Client()
	th.BaseURL = srv.URL + "/"
	th.TokenURL = srv.URL + "/"
	return &app{
		db:        openTestDB(t),
		threads:   th,
		appSecret: "test-secret",
		session:   &session{secret: []byte("test-secret")},
		testTpl:   template.Must(template.New("login.html").Parse(`{{.Error}}`)),
	}
}

func TestTokenLogin_빈_토큰은_거부한다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("빈 토큰인데 API를 호출했다")
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	tokenLoginApp(t, srv).handleTokenLogin(rec,
		httptest.NewRequest(http.MethodPost, "/login/token", nil))

	assertLoginError(t, rec, "토큰을 붙여넣어")
}

func TestTokenLogin_유효하지_않은_토큰은_거부한다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"Invalid OAuth access token"}}`)
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login/token",
		strings.NewReader("token=bogus"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenLoginApp(t, srv).handleTokenLogin(rec, r)

	assertLoginError(t, rec, "토큰이 유효하지 않습니다")
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("세션이 발급됐다")
		}
	}
}

// 대시보드가 주는 토큰으로 그 계정의 세션이 만들어져야 한다.
// 브라우저에 어떤 계정이 물려 있든 상관없어야 한다는 것이 핵심이다.
func TestTokenLogin_토큰의_주인으로_로그인된다(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "access_token") {
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "long-token", "expires_in": 5184000,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"id": "user-from-token", "username": "tokenowner",
		})
	}))
	defer srv.Close()

	a := tokenLoginApp(t, srv)
	t.Cleanup(func() {
		a.db.DeleteThreadsUser(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "user-from-token")
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login/token",
		strings.NewReader("token="+url.QueryEscape("TH-short")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.handleTokenLogin(rec, r)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("code=%d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	var got string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(c)
			got = a.session.userID(req)
		}
	}
	if got != "user-from-token" {
		t.Fatalf("세션 사용자=%q", got)
	}

	u, ok, err := a.db.GetThreadsUser(r.Context(), "user-from-token")
	if err != nil || !ok {
		t.Fatalf("사용자가 저장되지 않았다: ok=%v err=%v", ok, err)
	}
	if u.AccessToken != "long-token" {
		t.Fatalf("장기 토큰으로 바뀌지 않았다: %q", u.AccessToken)
	}
	if u.Username != "tokenowner" {
		t.Fatalf("username=%q", u.Username)
	}
}

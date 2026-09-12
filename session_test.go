package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testSession() *session { return &session{secret: []byte("test-secret-key")} }

func cookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("세션 쿠키가 없다")
	return nil
}

func requestWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func TestSession_발급한_쿠키에서_사용자를_읽는다(t *testing.T) {
	s := testSession()
	rec := httptest.NewRecorder()
	s.issue(rec, "user-42", false)

	if got := s.userID(requestWith(cookieFrom(t, rec))); got != "user-42" {
		t.Fatalf("userID=%q", got)
	}
}

func TestSession_쿠키가_없으면_빈_사용자(t *testing.T) {
	if got := testSession().userID(requestWith(nil)); got != "" {
		t.Fatalf("userID=%q", got)
	}
}

func TestSession_서명이_다르면_거부한다(t *testing.T) {
	// 다른 비밀키로 만든 쿠키는 통과하면 안 된다.
	other := &session{secret: []byte("attacker-key")}
	rec := httptest.NewRecorder()
	other.issue(rec, "user-42", false)

	if got := testSession().userID(requestWith(cookieFrom(t, rec))); got != "" {
		t.Fatalf("다른 키로 서명한 쿠키가 통과했다: %q", got)
	}
}

// 공격자가 쿠키의 사용자 id만 남의 것으로 바꾸는 경우.
// 이게 통과하면 남의 계정으로 글을 쓸 수 있다.
func TestSession_사용자를_바꿔치기하면_거부한다(t *testing.T) {
	s := testSession()
	rec := httptest.NewRecorder()
	s.issue(rec, "user-me", false)
	c := cookieFrom(t, rec)

	payload, sig, _ := strings.Cut(c.Value, ".")
	_, expiry, _ := strings.Cut(payload, "|")
	c.Value = "user-victim|" + expiry + "." + sig

	if got := s.userID(requestWith(c)); got != "" {
		t.Fatalf("바꿔치기한 사용자가 통과했다: %q", got)
	}
}

func TestSession_만료를_늘려도_거부한다(t *testing.T) {
	s := testSession()
	rec := httptest.NewRecorder()
	s.issue(rec, "user-42", false)
	c := cookieFrom(t, rec)

	payload, sig, _ := strings.Cut(c.Value, ".")
	userID, _, _ := strings.Cut(payload, "|")
	forged := userID + "|" + strconv.FormatInt(time.Now().Add(100*365*24*time.Hour).Unix(), 10)
	c.Value = forged + "." + sig

	if got := s.userID(requestWith(c)); got != "" {
		t.Fatalf("만료를 조작한 쿠키가 통과했다: %q", got)
	}
}

func TestSession_만료된_쿠키는_거부한다(t *testing.T) {
	s := testSession()
	payload := "user-42|" + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	c := &http.Cookie{Name: sessionCookie, Value: payload + "." + s.sign(payload)}

	if got := s.userID(requestWith(c)); got != "" {
		t.Fatalf("만료된 쿠키가 통과했다: %q", got)
	}
}

func TestSession_형식이_깨진_쿠키는_거부한다(t *testing.T) {
	s := testSession()
	for _, v := range []string{"", "abc", "abc.def", ".", "9999999999", "|123.sig", "u|abc.sig"} {
		c := &http.Cookie{Name: sessionCookie, Value: v}
		if got := s.userID(requestWith(c)); got != "" {
			t.Errorf("%q 가 통과했다 (userID=%q)", v, got)
		}
	}
}

func TestSession_쿠키는_HttpOnly다(t *testing.T) {
	rec := httptest.NewRecorder()
	testSession().issue(rec, "user-42", false)
	if c := cookieFrom(t, rec); !c.HttpOnly {
		t.Fatal("HttpOnly가 아니다")
	}
}

func TestRequireAuth_로그인_전에는_로그인_화면으로(t *testing.T) {
	a := &app{session: testSession()}
	handler := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("보호된 핸들러가 실행됐다: %s", r.URL.Path)
	}))

	for _, path := range []string{"/", "/new", "/drafts/1", "/settings", "/history"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("%s: code=%d location=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestRequireAuth_로그인_경로와_정적파일은_열려있다(t *testing.T) {
	a := &app{session: testSession()}
	reached := 0
	handler := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))

	open := []string{"/login", "/login/start", "/login/callback", "/static/style.css"}
	for _, path := range open {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if reached != len(open) {
		t.Fatalf("%d개만 통과했다. %d개여야 한다", reached, len(open))
	}
}

func TestRequireAuth_토큰_로그인_경로는_열려있다(t *testing.T) {
	// 로그인 수단이므로 로그인 전에도 닿을 수 있어야 한다.
	a := &app{session: testSession()}
	reached := false
	a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/login/token", nil))

	if !reached {
		t.Fatal("로그인 경로가 막혀 있다")
	}
}

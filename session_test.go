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

func TestSession_발급한_쿠키는_유효하다(t *testing.T) {
	s := testSession()
	rec := httptest.NewRecorder()
	s.issue(rec, false)

	if !s.valid(requestWith(cookieFrom(t, rec))) {
		t.Fatal("방금 발급한 쿠키가 무효하다")
	}
}

func TestSession_쿠키가_없으면_무효(t *testing.T) {
	if testSession().valid(requestWith(nil)) {
		t.Fatal("쿠키가 없는데 유효하다고 한다")
	}
}

func TestSession_서명이_다르면_무효(t *testing.T) {
	// 다른 비밀키로 만든 쿠키는 통과하면 안 된다.
	other := &session{secret: []byte("attacker-key")}
	rec := httptest.NewRecorder()
	other.issue(rec, false)

	if testSession().valid(requestWith(cookieFrom(t, rec))) {
		t.Fatal("다른 키로 서명한 쿠키가 통과했다")
	}
}

func TestSession_만료를_늘려도_서명이_깨진다(t *testing.T) {
	// 공격자가 만료 시각만 바꿔치기하는 경우.
	s := testSession()
	rec := httptest.NewRecorder()
	s.issue(rec, false)
	c := cookieFrom(t, rec)

	_, sig, _ := strings.Cut(c.Value, ".")
	forged := strconv.FormatInt(time.Now().Add(100*365*24*time.Hour).Unix(), 10) + "." + sig
	c.Value = forged

	if s.valid(requestWith(c)) {
		t.Fatal("만료 시각을 조작한 쿠키가 통과했다")
	}
}

func TestSession_만료된_쿠키는_무효(t *testing.T) {
	s := testSession()
	payload := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	c := &http.Cookie{Name: sessionCookie, Value: payload + "." + s.sign(payload)}

	if s.valid(requestWith(c)) {
		t.Fatal("만료된 쿠키가 통과했다")
	}
}

func TestSession_형식이_깨진_쿠키는_무효(t *testing.T) {
	s := testSession()
	for _, v := range []string{"", "abc", "abc.def", ".", "9999999999"} {
		c := &http.Cookie{Name: sessionCookie, Value: v}
		if s.valid(requestWith(c)) {
			t.Errorf("%q 가 통과했다", v)
		}
	}
}

func TestSession_쿠키는_HttpOnly다(t *testing.T) {
	// 자바스크립트로 세션을 훔칠 수 없어야 한다.
	rec := httptest.NewRecorder()
	testSession().issue(rec, false)
	if c := cookieFrom(t, rec); !c.HttpOnly {
		t.Fatal("HttpOnly가 아니다")
	}
}

func TestRequireAuth_로그인_전에는_로그인_화면으로(t *testing.T) {
	a := &app{session: testSession()}
	handler := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("보호된 핸들러가 실행됐다: %s", r.URL.Path)
	}))

	for _, path := range []string{"/", "/new", "/drafts/1", "/connect", "/history"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("%s: code=%d location=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestRequireAuth_로그인_화면과_정적파일은_열려있다(t *testing.T) {
	a := &app{session: testSession()}
	reached := 0
	handler := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))

	for _, path := range []string{"/login", "/static/style.css", "/static/app.js"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if reached != 3 {
		t.Fatalf("%d개만 통과했다. 3개여야 한다", reached)
	}
}

func TestCheckPassword(t *testing.T) {
	if !checkPassword("hunter2", "hunter2") {
		t.Error("같은 비밀번호가 거부됨")
	}
	for _, given := range []string{"", "hunter", "hunter22", "HUNTER2"} {
		if checkPassword(given, "hunter2") {
			t.Errorf("%q 가 통과했다", given)
		}
	}
}

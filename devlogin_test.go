package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLocalHost(t *testing.T) {
	local := []string{"localhost", "localhost:8123", "127.0.0.1", "127.0.0.1:8123", "[::1]:8123"}
	for _, h := range local {
		if !isLocalHost(h) {
			t.Errorf("%q 가 로컬로 인식되지 않았다", h)
		}
	}
	remote := []string{
		"postmill.onrender.com",
		"postmill.onrender.com:443",
		"evil.com",
		"localhost.evil.com", // 접두사만 같은 도메인
		"notlocalhost",
		"192.168.219.100:8123",
	}
	for _, h := range remote {
		if isLocalHost(h) {
			t.Errorf("%q 가 로컬로 인식됐다", h)
		}
	}
}

// 배포 환경에서는 환경변수가 있더라도 열리면 안 된다.
func TestDevLogin_배포_호스트에서는_닫혀있다(t *testing.T) {
	a := &app{devUserID: "user-1", session: testSession()}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "https://postmill.onrender.com/login/dev", nil)
	a.handleDevLogin(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, 404여야 한다", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Fatal("세션이 발급됐다")
		}
	}
}

// 환경변수가 없으면 로컬에서도 열리면 안 된다.
func TestDevLogin_환경변수가_없으면_닫혀있다(t *testing.T) {
	a := &app{devUserID: "", session: testSession()}

	rec := httptest.NewRecorder()
	a.handleDevLogin(rec, httptest.NewRequest(http.MethodPost, "http://localhost:8123/login/dev", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, 404여야 한다", rec.Code)
	}
}

func TestDevLogin_로컬에서_환경변수가_있으면_로그인된다(t *testing.T) {
	a := &app{devUserID: "user-dev", session: testSession()}

	rec := httptest.NewRecorder()
	a.handleDevLogin(rec, httptest.NewRequest(http.MethodPost, "http://localhost:8123/login/dev", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	var got string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			got = a.session.userID(r)
		}
	}
	if got != "user-dev" {
		t.Fatalf("세션 사용자=%q", got)
	}
}

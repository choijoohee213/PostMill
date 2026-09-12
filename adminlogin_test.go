package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func adminApp(pw string) *app {
	return &app{
		adminPassword: pw,
		session:       testSession(),
		testTpl:       template.Must(template.New("login.html").Parse(`{{.Error}}`)),
	}
}

func postPassword(pw string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login/admin",
		strings.NewReader("password="+pw))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func sessionUserFrom(a *app, rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(c)
			return a.session.userID(r)
		}
	}
	return ""
}

// ADMIN_PASSWORD가 없으면 관리자 문이 아예 없어야 한다.
func TestAdminLogin_비밀번호가_설정되지_않으면_닫혀있다(t *testing.T) {
	a := adminApp("")
	rec := httptest.NewRecorder()
	a.handleAdminLogin(rec, postPassword(""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, 404여야 한다", rec.Code)
	}
	if u := sessionUserFrom(a, rec); u != "" {
		t.Fatalf("세션이 발급됐다: %q", u)
	}
}

func TestAdminLogin_틀린_비밀번호는_거부한다(t *testing.T) {
	a := adminApp("correct-horse")
	for _, given := range []string{"", "wrong", "correct-hors", "correct-horsee", "CORRECT-HORSE"} {
		rec := httptest.NewRecorder()
		a.handleAdminLogin(rec, postPassword(given))

		if u := sessionUserFrom(a, rec); u != "" {
			t.Errorf("%q 로 로그인됐다", given)
		}
	}
}

func TestAdminLogin_맞으면_관리자_세션을_준다(t *testing.T) {
	a := adminApp("correct-horse")
	rec := httptest.NewRecorder()
	a.handleAdminLogin(rec, postPassword("correct-horse"))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if u := sessionUserFrom(a, rec); u != adminUserID {
		t.Fatalf("세션 사용자=%q", u)
	}
}

// 관리자 id가 Threads 사용자 id와 겹치면 남의 초안이 보인다.
func TestAdminUserID_숫자가_아니라_스레드_id와_겹치지_않는다(t *testing.T) {
	for _, r := range adminUserID {
		if r >= '0' && r <= '9' {
			t.Fatalf("관리자 id에 숫자가 섞여 있다: %q", adminUserID)
		}
	}
}

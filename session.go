package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "postmill_session"
const sessionTTL = 365 * 24 * time.Hour

// session은 표준 라이브러리만으로 서명된 쿠키를 다룬다.
// 담는 것은 스레드 사용자 id와 만료 시각뿐이라 서버 측 저장소가 필요 없다.
type session struct {
	secret []byte
}

// issue는 사용자 id와 만료 시각을 담아 서명한 쿠키를 심는다.
func (s *session) issue(w http.ResponseWriter, userID string, secure bool) {
	expiry := time.Now().Add(sessionTTL).Unix()
	payload := userID + "|" + strconv.FormatInt(expiry, 10)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    payload + "." + s.sign(payload),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(expiry, 0),
	})
}

func (s *session) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// userID는 쿠키에서 사용자 id를 꺼낸다. 서명이나 만료가 어긋나면 빈 문자열.
//
// 서명을 먼저 검사해야 한다. 먼저 값을 읽고 나중에 검사하면, 검사를
// 빠뜨린 경로에서 남의 id로 행세할 수 있다.
func (s *session) userID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	payload, sig, found := strings.Cut(c.Value, ".")
	if !found {
		return ""
	}
	// 서명 비교는 반드시 상수 시간으로 한다.
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return ""
	}

	userID, rawExpiry, found := strings.Cut(payload, "|")
	if !found || userID == "" {
		return ""
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil || time.Now().Unix() >= expiry {
		return ""
	}
	return userID
}

func (s *session) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *app) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if a.session.userID(r) != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, "login.html", map[string]any{
		"Error":    r.URL.Query().Get("error"),
		"DevLogin": a.devLoginEnabled(r),
		"AppID":    a.appID,
	})
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.session.clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// requireAuth는 로그인하지 않은 요청을 로그인 화면으로 보낸다.
// 이 앱은 사용자의 스레드 계정으로 글을 쓰므로 인증은 필수다.
func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) || a.session.userID(r) != "" {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func isPublicPath(path string) bool {
	return path == "/login" ||
		path == "/login/dev" ||
		path == "/login/token" ||
		strings.HasPrefix(path, "/static/")
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "postmill_session"
const sessionTTL = 30 * 24 * time.Hour

// session은 표준 라이브러리만으로 서명된 쿠키를 다룬다.
// 저장할 상태가 "로그인했다" 하나뿐이라 서버 측 세션 저장소가 필요 없다.
type session struct {
	secret []byte
}

// issue는 만료 시각을 담아 서명한 쿠키 값을 만든다.
func (s *session) issue(w http.ResponseWriter, secure bool) {
	expiry := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(expiry, 10)
	value := payload + "." + s.sign(payload)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
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

// valid는 서명과 만료를 확인한다.
func (s *session) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	payload, sig, found := strings.Cut(c.Value, ".")
	if !found {
		return false
	}
	// 서명 비교는 반드시 상수 시간으로 한다.
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return false
	}
	expiry, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry
}

func (s *session) sign(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// checkPassword는 타이밍 공격을 피하려고 상수 시간으로 비교한다.
func checkPassword(given, want string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

func (a *app) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if a.session.valid(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, "login.html", map[string]string{})
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !checkPassword(r.FormValue("password"), a.password) {
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, "login.html", map[string]string{"Error": "비밀번호가 맞지 않습니다."})
		return
	}
	// 배포 환경(HTTPS)에서만 Secure를 켠다. 로컬 http에서는 쿠키가 저장되지 않는다.
	a.session.issue(w, r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.session.clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// requireAuth는 로그인하지 않은 요청을 로그인 화면으로 보낸다.
// 이 앱은 사용자의 스레드 계정으로 글을 쓸 수 있으므로 인증은 필수다.
func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) || a.session.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func isPublicPath(path string) bool {
	return path == "/login" || strings.HasPrefix(path, "/static/")
}

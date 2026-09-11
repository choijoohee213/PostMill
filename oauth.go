package main

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const oauthStateCookie = "postmill_oauth_state"

// callbackPath는 Meta 대시보드에 등록해야 하는 리디렉션 경로다.
const callbackPath = "/login/callback"

// publicBase는 지금 요청이 들어온 주소를 그대로 돌려준다.
// 리디렉션 URI는 승인 요청과 코드 교환에서 글자 그대로 같아야 하므로
// 두 곳 모두 이 함수를 쓴다.
func publicBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func loginFailed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

// handleLoginStart는 사용자를 스레드 승인 화면으로 보낸다.
func (a *app) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	if a.appID == "" || a.appSecret == "" {
		loginFailed(w, r, "THREADS_APP_ID 또는 THREADS_APP_SECRET이 설정되지 않았습니다.")
		return
	}

	state, err := randomState()
	if err != nil {
		log.Printf("state 생성 실패: %v", err)
		loginFailed(w, r, "로그인을 시작하지 못했습니다.")
		return
	}

	// state를 쿠키에 담아 돌아왔을 때 대조한다. 남이 만든 콜백 요청을 막는다.
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(10 * time.Minute),
	})

	http.Redirect(w, r, AuthorizeURL(a.appID, publicBase(r)+callbackPath, state), http.StatusSeeOther)
}

// handleLoginCallback은 승인 화면에서 돌아온 요청을 처리한다.
// 여기서 로그인과 토큰 저장이 한 번에 끝난다.
func (a *app) handleLoginCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if reason := q.Get("error_description"); reason != "" {
		loginFailed(w, r, "승인이 취소되었습니다: "+reason)
		return
	}

	// state 대조. 쿠키가 없거나 값이 다르면 우리가 시작한 흐름이 아니다.
	c, err := r.Cookie(oauthStateCookie)
	if err != nil || c.Value == "" || c.Value != q.Get("state") {
		loginFailed(w, r, "로그인 요청이 유효하지 않습니다. 다시 시도해주세요.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1})

	// 문서상 code 끝에 "#_"가 붙어 올 수 있다.
	code := strings.TrimSuffix(q.Get("code"), "#_")
	if code == "" {
		loginFailed(w, r, "인증 코드를 받지 못했습니다.")
		return
	}

	short, err := a.threads.CodeToToken(r.Context(), a.appID, a.appSecret, publicBase(r)+callbackPath, code)
	if err != nil {
		log.Printf("코드 교환 실패: %v", err)
		loginFailed(w, r, "인증 코드를 토큰으로 바꾸지 못했습니다: "+err.Error())
		return
	}

	long, expiresAt, err := a.threads.ExchangeToken(r.Context(), a.appSecret, short)
	if err != nil {
		log.Printf("장기 토큰 교환 실패: %v", err)
		loginFailed(w, r, "장기 토큰으로 바꾸지 못했습니다: "+err.Error())
		return
	}

	// 누구인지 확인한다. 이 id가 세션의 신원이자 게시물의 주인이 된다.
	me, err := a.threads.Me(r.Context(), long)
	if err != nil {
		log.Printf("계정 조회 실패: %v", err)
		loginFailed(w, r, "스레드 계정 정보를 읽지 못했습니다: "+err.Error())
		return
	}

	if err := a.db.SaveThreadsUser(r.Context(), ThreadsUser{
		UserID:      me.ID,
		Username:    me.Username,
		AccessToken: long,
		ExpiresAt:   expiresAt,
	}); err != nil {
		log.Printf("사용자 저장 실패: %v", err)
		loginFailed(w, r, "로그인 정보를 저장하지 못했습니다.")
		return
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	a.session.issue(w, me.ID, secure)
	log.Printf("로그인: @%s (%s), 토큰 만료 %s", me.Username, me.ID, expiresAt.Format(time.RFC3339))

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

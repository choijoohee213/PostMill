package main

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"strings"
	"time"
)

const oauthStateCookie = "postmill_oauth_state"

// callbackPath는 Meta 대시보드에 등록해야 하는 리디렉션 경로다.
const callbackPath = "/connect/callback"

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

// handleConnectStart는 사용자를 Threads 승인 화면으로 보낸다.
func (a *app) handleConnectStart(w http.ResponseWriter, r *http.Request) {
	if a.appID == "" || a.appSecret == "" {
		a.renderConnect(w, r, "THREADS_APP_ID 또는 THREADS_APP_SECRET이 설정되지 않았습니다.", false)
		return
	}

	state, err := randomState()
	if err != nil {
		log.Printf("state 생성 실패: %v", err)
		a.renderConnect(w, r, "연결을 시작하지 못했습니다.", false)
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

// handleConnectCallback은 승인 화면에서 돌아온 요청을 처리한다.
func (a *app) handleConnectCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if reason := q.Get("error_description"); reason != "" {
		a.renderConnect(w, r, "승인이 취소되었습니다: "+reason, false)
		return
	}

	// state 대조. 쿠키가 없거나 값이 다르면 우리가 시작한 흐름이 아니다.
	c, err := r.Cookie(oauthStateCookie)
	if err != nil || c.Value == "" || c.Value != q.Get("state") {
		a.renderConnect(w, r, "연결 요청이 유효하지 않습니다. 다시 시도해주세요.", false)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/", MaxAge: -1})

	// 문서상 code 끝에 "#_"가 붙어 올 수 있다.
	code := strings.TrimSuffix(q.Get("code"), "#_")
	if code == "" {
		a.renderConnect(w, r, "인증 코드를 받지 못했습니다.", false)
		return
	}

	short, err := a.threads.CodeToToken(r.Context(), a.appID, a.appSecret, publicBase(r)+callbackPath, code)
	if err != nil {
		log.Printf("코드 교환 실패: %v", err)
		a.renderConnect(w, r, "인증 코드를 토큰으로 바꾸지 못했습니다: "+err.Error(), false)
		return
	}

	long, expiresAt, err := a.threads.ExchangeToken(r.Context(), a.appSecret, short)
	if err != nil {
		log.Printf("장기 토큰 교환 실패: %v", err)
		a.renderConnect(w, r, "장기 토큰으로 바꾸지 못했습니다: "+err.Error(), false)
		return
	}

	if err := a.saveToken(r.Context(), long, expiresAt); err != nil {
		log.Printf("토큰 저장 실패: %v", err)
		a.renderConnect(w, r, "토큰을 저장하지 못했습니다.", false)
		return
	}
	log.Printf("Threads 계정을 연결했다 (OAuth). 만료: %s", expiresAt.Format(time.RFC3339))

	http.Redirect(w, r, "/connect?done=1", http.StatusSeeOther)
}

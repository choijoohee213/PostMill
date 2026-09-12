package main

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// 토큰으로 로그인. 지금은 화면과 라우트에서 빠져 있다.
//
// 되살리려면 main.go에 POST /login/token을 등록하고 session.go의
// isPublicPath에 같은 경로를 넣은 뒤, login.html에 입력칸을 붙이면 된다.
//
// 브라우저에 어떤 Meta 계정이 물려 있든 상관없이 들어올 수 있는 길이다.
// 계정 센터에 여러 계정이 묶여 있으면 승인 화면이 대표 계정으로 넘어가
// 원하는 계정으로 로그인할 수 없는 경우가 있다. 그때 쓴다.
//
// Threads 연동이 막혔을 때 앱에 들어가 고칠 수단이기도 하다.
func (a *app) handleTokenLogin(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		loginFailed(w, r, "토큰을 붙여넣어 주세요.")
		return
	}
	if a.appSecret == "" {
		loginFailed(w, r, "THREADS_APP_SECRET이 설정되지 않았습니다.")
		return
	}

	// 대시보드가 주는 토큰은 1시간짜리다. 60일짜리로 바꿔 저장한다.
	// 이미 장기 토큰이면 교환이 실패하므로, 그때는 받은 토큰을 그대로 쓴다.
	long, expiresAt, err := a.threads.ExchangeToken(r.Context(), a.appSecret, token)
	if err != nil {
		log.Printf("토큰 교환 실패(그대로 써본다): %v", err)
		long, expiresAt = token, time.Now().Add(24*time.Hour)
	}

	me, err := a.threads.Me(r.Context(), long)
	if err != nil {
		log.Printf("토큰으로 계정 조회 실패: %v", err)
		loginFailed(w, r, "토큰이 유효하지 않습니다: "+err.Error())
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
	log.Printf("토큰 로그인: @%s (%s)", me.Username, me.ID)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

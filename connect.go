package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// currentUser는 지금 요청을 보낸 사용자의 스레드 계정과 토큰이다.
// 발행에 쓸 토큰을 읽는 곳은 여기 하나뿐이다.
func (a *app) currentUser(r *http.Request) (*ThreadsUser, bool) {
	userID := a.session.userID(r)
	if userID == "" {
		return nil, false
	}
	u, ok, err := a.db.GetThreadsUser(r.Context(), userID)
	if err != nil {
		log.Printf("사용자 조회 실패 (%s): %v", userID, err)
		return nil, false
	}
	return u, ok
}

type settingsData struct {
	Username  string
	ExpiresAt string
	Error     string
}

func (a *app) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := settingsData{Error: r.URL.Query().Get("error")}
	if u, ok := a.currentUser(r); ok {
		data.Username = u.Username
		data.ExpiresAt = u.ExpiresAt.Local().Format("2006년 1월 2일")
	}
	a.render(w, "settings.html", data)
}

// handleDisconnect는 저장된 토큰을 지우고 로그아웃한다.
// 토큰이 없으면 로그인 상태를 유지할 이유도 없다.
func (a *app) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if userID := a.session.userID(r); userID != "" {
		if err := a.db.DeleteThreadsUser(r.Context(), userID); err != nil {
			log.Printf("연결 해제 실패 (%s): %v", userID, err)
		}
	}
	a.session.clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *app) handleHistory(w http.ResponseWriter, r *http.Request) {
	posts, err := a.db.ListPublished(r.Context(), a.session.userID(r))
	if err != nil {
		log.Printf("이력 조회 실패: %v", err)
		http.Error(w, "이력을 불러오지 못했습니다", http.StatusInternalServerError)
		return
	}
	a.render(w, "history.html", posts)
}

// refreshTokenIfNeeded는 만료가 임박한 토큰을 그 자리에서 갱신한다.
// 크론이 없으므로 요청 앞단에서 처리한다 (SPEC 5-3).
func (a *app) refreshTokenIfNeeded(ctx context.Context, u *ThreadsUser) {
	if time.Until(u.ExpiresAt) > refreshWindow {
		return
	}

	newToken, newExpiry, err := a.threads.RefreshToken(ctx, u.AccessToken)
	if err != nil {
		log.Printf("토큰 갱신 실패 (%s): %v", u.UserID, err)
		return
	}
	u.AccessToken, u.ExpiresAt = newToken, newExpiry
	if err := a.db.SaveThreadsUser(ctx, *u); err != nil {
		log.Printf("갱신된 토큰 저장 실패 (%s): %v", u.UserID, err)
		return
	}
	log.Printf("토큰을 갱신했다 (%s). 새 만료: %s", u.UserID, newExpiry.Format(time.RFC3339))
}

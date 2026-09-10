package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

// currentToken은 발행에 쓸 Threads 토큰을 돌려준다.
//
// 지금은 앱 전체가 계정 하나를 쓰므로 app_state에서 읽는다. 나중에 사용자가
// 늘어나면 이 함수 안에서 "로그인한 사용자의 토큰"을 읽도록 바꾸면 되고,
// 발행 코드는 손대지 않아도 된다. 토큰을 읽는 곳을 여기 하나로 모아둔 이유다.
func (a *app) currentToken(ctx context.Context) (string, bool) {
	token, ok, err := a.db.GetState(ctx, stateAccessToken)
	if err != nil {
		log.Printf("토큰 조회 실패: %v", err)
		return "", false
	}
	return token, ok && token != ""
}

func (a *app) saveToken(ctx context.Context, token string, expiresAt time.Time) error {
	if err := a.db.SetState(ctx, stateAccessToken, token); err != nil {
		return err
	}
	return a.db.SetState(ctx, stateExpiresAt, expiresAt.Format(time.RFC3339))
}

type connectData struct {
	Connected   bool
	ExpiresAt   string
	AppID       string
	Error       string
	Done        bool
	CallbackURL string // 대시보드에 등록해야 하는 리디렉션 URI
}

func (a *app) handleConnectForm(w http.ResponseWriter, r *http.Request) {
	a.renderConnect(w, r, "", r.URL.Query().Has("done"))
}

func (a *app) renderConnect(w http.ResponseWriter, r *http.Request, errMsg string, done bool) {
	data := connectData{
		AppID:       a.appID,
		Error:       errMsg,
		Done:        done,
		CallbackURL: publicBase(r) + callbackPath,
	}

	if _, ok := a.currentToken(r.Context()); ok {
		data.Connected = true
		if raw, found, _ := a.db.GetState(r.Context(), stateExpiresAt); found {
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				data.ExpiresAt = t.Local().Format("2006년 1월 2일")
			}
		}
	}
	a.render(w, "connect.html", data)
}

// handleConnect는 대시보드에서 받은 단기 토큰을 장기 토큰으로 바꿔 저장한다.
func (a *app) handleConnect(w http.ResponseWriter, r *http.Request) {
	short := strings.TrimSpace(r.FormValue("token"))
	if short == "" {
		a.renderConnect(w, r, "토큰을 붙여넣어 주세요.", false)
		return
	}
	if a.appSecret == "" {
		a.renderConnect(w, r, "THREADS_APP_SECRET이 설정되지 않았습니다.", false)
		return
	}

	long, expiresAt, err := a.threads.ExchangeToken(r.Context(), a.appSecret, short)
	if err != nil {
		log.Printf("토큰 교환 실패: %v", err)
		a.renderConnect(w, r, "토큰을 교환하지 못했습니다: "+err.Error(), false)
		return
	}

	if err := a.saveToken(r.Context(), long, expiresAt); err != nil {
		log.Printf("토큰 저장 실패: %v", err)
		a.renderConnect(w, r, "토큰을 저장하지 못했습니다.", false)
		return
	}
	log.Printf("Threads 계정을 연결했다. 만료: %s", expiresAt.Format(time.RFC3339))

	http.Redirect(w, r, "/connect?done=1", http.StatusSeeOther)
}

func (a *app) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.db.SetState(r.Context(), stateAccessToken, ""); err != nil {
		log.Printf("연결 해제 실패: %v", err)
	}
	http.Redirect(w, r, "/connect", http.StatusSeeOther)
}

func (a *app) handleHistory(w http.ResponseWriter, r *http.Request) {
	posts, err := a.db.ListPublished(r.Context())
	if err != nil {
		log.Printf("이력 조회 실패: %v", err)
		http.Error(w, "이력을 불러오지 못했습니다", http.StatusInternalServerError)
		return
	}
	a.render(w, "history.html", posts)
}

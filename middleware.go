package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// tokenKeeper는 매 요청 앞단에서 Threads 토큰 만료를 확인하고
// 임박했으면 그 자리에서 갱신한다. 크론이 없으므로 이 방식을 쓴다 (SPEC 5-3).
//
// 갱신에 실패해도 요청은 그대로 진행시킨다. 토큰이 아직 유효할 수 있고,
// 목록을 보는 것처럼 토큰이 필요 없는 작업까지 막을 이유가 없다.
func (a *app) tokenKeeper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 정적 파일과 로그인 화면에서는 DB를 건드리지 않는다.
		if !isPublicPath(r.URL.Path) {
			a.refreshTokenIfNeeded(r.Context())
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) refreshTokenIfNeeded(ctx context.Context) {
	token, ok, err := a.db.GetState(ctx, stateAccessToken)
	if err != nil || !ok || token == "" {
		return // 아직 연결되지 않았다
	}

	expiresAt, ok, err := a.db.GetState(ctx, stateExpiresAt)
	if err != nil || !ok {
		return
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		log.Printf("토큰 만료 시각을 읽지 못했다: %v", err)
		return
	}
	if time.Until(t) > refreshWindow {
		return
	}

	newToken, newExpiry, err := a.threads.RefreshToken(ctx, token)
	if err != nil {
		log.Printf("토큰 갱신 실패: %v", err)
		return
	}
	if err := a.db.SetState(ctx, stateAccessToken, newToken); err != nil {
		log.Printf("갱신된 토큰 저장 실패: %v", err)
		return
	}
	if err := a.db.SetState(ctx, stateExpiresAt, newExpiry.Format(time.RFC3339)); err != nil {
		log.Printf("만료 시각 저장 실패: %v", err)
	}
	log.Printf("Threads 토큰을 갱신했다. 새 만료: %s", newExpiry.Format(time.RFC3339))
}

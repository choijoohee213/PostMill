package main

import (
	"net/http"
)

// tokenKeeper는 로그인한 사용자의 토큰 만료가 임박했으면
// 요청 앞단에서 갱신한다. 크론이 없으므로 이 방식을 쓴다 (SPEC 5-3).
//
// 갱신에 실패해도 요청은 그대로 진행시킨다. 토큰이 아직 유효할 수 있고,
// 목록을 보는 것처럼 토큰이 필요 없는 작업까지 막을 이유가 없다.
func (a *app) tokenKeeper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isPublicPath(r.URL.Path) {
			if u, ok := a.currentUser(r); ok {
				a.refreshTokenIfNeeded(r.Context(), u)
			}
		}
		next.ServeHTTP(w, r)
	})
}

package main

import (
	"log"
	"net"
	"net/http"
	"strings"
)

// 로컬 개발용 로그인.
//
// Meta는 http:// 페이지에서 시작한 로그인을 차단하므로(오류 1349187)
// 로컬에서는 스레드 로그인을 쓸 수 없다. 그래서 우회로를 둔다.
//
// 두 조건을 모두 만족할 때만 열린다:
//  1. DEV_LOGIN_USER_ID 환경변수가 있다
//  2. 요청이 localhost로 들어왔다
//
// 배포 환경에는 환경변수가 없고 호스트도 localhost가 아니므로
// 둘 중 어느 하나만으로는 열리지 않는다.
func (a *app) devLoginEnabled(r *http.Request) bool {
	return a.devUserID != "" && isLocalHost(r.Host)
}

func isLocalHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(h)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (a *app) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !a.devLoginEnabled(r) {
		http.NotFound(w, r)
		return
	}
	a.session.issue(w, a.devUserID, false)
	log.Printf("개발용 로그인: %s", a.devUserID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

package main

import (
	"crypto/subtle"
	"log"
	"net/http"
)

// adminUserID는 관리자 세션의 사용자 id다.
//
// Threads 사용자 id는 숫자 문자열이므로 이 값과 겹치지 않는다.
// 관리자는 threads_users에 행이 없어서 게시가 자동으로 막힌다.
// 별도 권한 검사를 두지 않은 이유다.
const adminUserID = "admin"

// adminEnabled는 ADMIN_PASSWORD가 설정됐을 때만 관리자 로그인을 연다.
func (a *app) adminEnabled() bool { return a.adminPassword != "" }

func (a *app) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !a.adminEnabled() {
		http.NotFound(w, r)
		return
	}

	given := r.FormValue("password")
	// 비밀번호 비교는 반드시 상수 시간으로 한다.
	if subtle.ConstantTimeCompare([]byte(given), []byte(a.adminPassword)) != 1 {
		loginFailed(w, r, "비밀번호가 맞지 않습니다.")
		return
	}

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	a.session.issue(w, adminUserID, secure)
	log.Print("관리자 로그인")

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

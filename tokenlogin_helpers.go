package main

import (
	"net/http"
	"net/url"
)

// loginFailed는 사유를 담아 로그인 화면으로 되돌려보낸다.
func loginFailed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

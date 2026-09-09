package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

// 목록 화면의 탭. 각 탭이 어떤 상태를 모아 보여주는지 여기서만 정한다.
type tab struct {
	Key      string
	Label    string
	Statuses []string
}

var tabs = []tab{
	{"review", "검수대기", []string{StatusGenerating, StatusPending, StatusApproved, StatusPublishing, StatusFailed}},
	{"published", "발행완료", []string{StatusPublished}},
	{"held", "보류", []string{StatusHeld}},
}

var templateFuncs = template.FuncMap{
	"preview":     preview,
	"formatTime":  formatTime,
	"affiliateKo": affiliateKo,
}

// preview는 카드에 보여줄 본문 앞 2줄을 뽑는다.
func preview(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 2 {
		lines = lines[:2]
	}
	return strings.Join(lines, "\n")
}

func formatTime(t time.Time) string {
	return t.Local().Format("1월 2일 15:04")
}

func affiliateKo(a string) string {
	switch a {
	case AffiliateCoupang:
		return "쿠팡"
	case AffiliateToss:
		return "토스"
	default:
		return a
	}
}

type listData struct {
	Tabs       []tab
	ActiveTab  string
	Posts      []*Post
	Generating bool // 생성 중인 카드가 있을 때만 폴링한다
}

func (a *app) handleList(w http.ResponseWriter, r *http.Request) {
	active := r.URL.Query().Get("tab")
	current := tabs[0]
	for _, t := range tabs {
		if t.Key == active {
			current = t
			break
		}
	}

	posts, err := a.db.ListByStatus(r.Context(), current.Statuses...)
	if err != nil {
		log.Printf("목록 조회 실패: %v", err)
		http.Error(w, "목록을 불러오지 못했습니다", http.StatusInternalServerError)
		return
	}

	generating := false
	for _, p := range posts {
		if p.Status == StatusGenerating {
			generating = true
			break
		}
	}

	a.render(w, "list.html", listData{
		Tabs:       tabs,
		ActiveTab:  current.Key,
		Posts:      posts,
		Generating: generating,
	})
}

func (a *app) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("템플릿 렌더 실패 (%s): %v", name, err)
	}
}

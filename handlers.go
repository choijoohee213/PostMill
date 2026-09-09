package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strconv"
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
	"canRetry":    canRetry,
}

// staleAfter가 지나도록 generating에 머문 초안은 goroutine이 죽은 것으로 본다.
// Render 무료 티어는 요청 타임아웃과 sleep이 있어 실제로 일어난다.
const staleAfter = 2 * time.Minute

// canRetry는 목록에 재시도 버튼을 노출할지 정한다.
func canRetry(p *Post) bool {
	if p.Status != StatusGenerating {
		return false
	}
	return p.ErrorMsg != "" || time.Since(p.CreatedAt) > staleAfter
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

type newData struct {
	Affiliates []affiliateOption
	Form       newForm
	Error      string
}

type affiliateOption struct {
	Key   string
	Label string
}

type newForm struct {
	Affiliate     string
	AffiliateLink string
	ProductURL    string
	Memo          string
}

var affiliateOptions = []affiliateOption{
	{AffiliateCoupang, "쿠팡"},
	{AffiliateToss, "토스"},
}

func (a *app) handleNewForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, "new.html", newData{
		Affiliates: affiliateOptions,
		Form:       newForm{Affiliate: AffiliateCoupang},
	})
}

func (a *app) handleNewSubmit(w http.ResponseWriter, r *http.Request) {
	form := newForm{
		Affiliate:     r.FormValue("affiliate"),
		AffiliateLink: strings.TrimSpace(r.FormValue("affiliate_link")),
		ProductURL:    strings.TrimSpace(r.FormValue("product_url")),
		Memo:          strings.TrimSpace(r.FormValue("memo")),
	}

	if msg := validateNewForm(form); msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		a.render(w, "new.html", newData{Affiliates: affiliateOptions, Form: form, Error: msg})
		return
	}

	id, err := a.db.CreateDraft(r.Context(), form.Affiliate, form.ProductURL, form.AffiliateLink, form.Memo)
	if err != nil {
		log.Printf("초안 생성 실패: %v", err)
		http.Error(w, "저장하지 못했습니다", http.StatusInternalServerError)
		return
	}

	// 생성은 백그라운드로 넘기고 즉시 목록으로 보낸다.
	go a.generate(id, form.Affiliate, form.AffiliateLink, form.Memo)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func validateNewForm(f newForm) string {
	if _, err := disclosureFor(f.Affiliate); err != nil {
		return "제휴사를 선택해주세요."
	}
	if f.AffiliateLink == "" {
		return "제휴 링크를 입력해주세요."
	}
	room, _ := BodyRoom(f.Affiliate, f.AffiliateLink)
	if room <= 0 {
		return "제휴 링크가 너무 길어 본문을 쓸 여유가 없습니다."
	}
	if f.Memo == "" {
		return "상품 메모를 입력해주세요."
	}
	return ""
}

func (a *app) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	p, err := a.db.GetPost(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if p.Status != StatusGenerating {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := a.db.SetGenerateError(r.Context(), id, ""); err != nil {
		log.Printf("재시도 준비 실패: %v", err)
	}
	go a.generate(id, p.Affiliate, p.AffiliateLink, p.Memo)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// generate는 요청과 무관하게 도는 백그라운드 작업이다.
// 요청 컨텍스트를 쓰면 리다이렉트와 동시에 취소되므로 쓰지 않는다.
func (a *app) generate(id int64, affiliate, affiliateLink, memo string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 상세한 원인은 로그에만 남긴다. 화면에는 짧은 문장만 보여준다.
	fail := func(reason string, err error) {
		log.Printf("초안 생성 실패 (id=%d): %v", id, err)
		if dbErr := a.db.SetGenerateError(ctx, id, reason); dbErr != nil {
			log.Printf("실패 기록도 실패 (id=%d): %v", id, dbErr)
		}
	}

	room, err := BodyRoom(affiliate, affiliateLink)
	if err != nil {
		fail("제휴사 정보가 잘못되었습니다.", err)
		return
	}

	body, err := a.gemini.GenerateDraft(ctx, affiliate, memo, room)
	if err != nil {
		fail("초안을 만들지 못했습니다.", err)
		return
	}
	if err := a.db.SetGenerated(ctx, id, body); err != nil {
		fail("저장하지 못했습니다.", err)
	}
}

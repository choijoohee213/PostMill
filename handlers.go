package main

import (
	"context"
	"fmt"
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
// 실측상 생성에 60~100초가 걸리므로, 아직 도는 중인 초안에 재시도 버튼이
// 뜨지 않도록 넉넉히 잡는다. 두 번 생성하면 무료 할당량만 낭비된다.
const staleAfter = 5 * time.Minute

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

// formatTime은 time.Time과 *time.Time을 모두 받는다.
// published_at은 미발행 상태를 구분하려고 nullable이라 포인터로 온다.
func formatTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Local().Format("1월 2일 15:04")
	case *time.Time:
		if t == nil {
			return "-"
		}
		return t.Local().Format("1월 2일 15:04")
	default:
		return "-"
	}
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
	tpl := a.tpl
	if tpl == nil {
		tpl = a.testTpl
	}
	if err := tpl.ExecuteTemplate(w, name, data); err != nil {
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
	go a.generate(id, form.Affiliate, form.Memo)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func validateNewForm(f newForm) string {
	if _, err := disclosureFor(f.Affiliate); err != nil {
		return "제휴사를 선택해주세요."
	}
	if f.AffiliateLink == "" {
		return "제휴 링크를 입력해주세요."
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
	go a.generate(id, p.Affiliate, p.Memo)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// generate는 요청과 무관하게 도는 백그라운드 작업이다.
// 요청 컨텍스트를 쓰면 리다이렉트와 동시에 취소되므로 쓰지 않는다.
func (a *app) generate(id int64, affiliate, memo string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// 상세한 원인은 로그에만 남긴다. 화면에는 짧은 문장만 보여준다.
	fail := func(reason string, err error) {
		log.Printf("초안 생성 실패 (id=%d): %v", id, err)
		if dbErr := a.db.SetGenerateError(ctx, id, reason); dbErr != nil {
			log.Printf("실패 기록도 실패 (id=%d): %v", id, dbErr)
		}
	}

	room, err := BodyRoom(affiliate)
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

type editData struct {
	Post       *Post
	Disclosure string
	Preview    string
	Reply      string
	Room       int
	Used       int
	Error      string
}

// draftFor는 편집 가능한 초안을 읽는다. 생성 중이거나 없는 글은 목록으로 돌려보낸다.
func (a *app) draftFor(w http.ResponseWriter, r *http.Request) (*Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	p, err := a.db.GetPost(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	if p.Status == StatusGenerating {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return nil, false
	}
	return p, true
}

func (a *app) handleDraftEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	a.renderEdit(w, p, "")
}

func (a *app) renderEdit(w http.ResponseWriter, p *Post, errMsg string) {
	disclosure, _ := disclosureFor(p.Affiliate)
	room, _ := BodyRoom(p.Affiliate)
	reply, _ := ComposeReply(p.AffiliateLink)

	// 미리보기는 발행과 같은 함수로 만든다. 화면과 실제 발행물이 달라질 수 없다.
	preview, err := Compose(p.Affiliate, p.Body)
	if err != nil && errMsg == "" {
		errMsg = "지금 상태로는 발행할 수 없습니다: " + err.Error()
	}

	a.render(w, "edit.html", editData{
		Post:       p,
		Disclosure: disclosure,
		Preview:    preview,
		Reply:      reply,
		Room:       room,
		Used:       CharCount(strings.TrimSpace(p.Body)),
		Error:      errMsg,
	})
}

func (a *app) handleDraftSave(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	// 저장 자체는 막지 않는다. 발행 가능한지는 미리보기와 카운터가 알려준다.
	if err := a.db.UpdateBody(r.Context(), p.ID, body); err != nil {
		log.Printf("본문 저장 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, p, "저장하지 못했습니다.")
		return
	}
	p.Body = body

	http.Redirect(w, r, fmt.Sprintf("/drafts/%d", p.ID), http.StatusSeeOther)
}

func (a *app) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if err := a.db.SetStatus(r.Context(), p.ID, StatusGenerating); err != nil {
		log.Printf("재생성 준비 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, p, "재생성을 시작하지 못했습니다.")
		return
	}
	go a.generate(p.ID, p.Affiliate, p.Memo)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) handleHold(w http.ResponseWriter, r *http.Request) {
	a.changeStatus(w, r, StatusHeld, "/?tab=held")
}

func (a *app) handleUnhold(w http.ResponseWriter, r *http.Request) {
	a.changeStatus(w, r, StatusPending, "/")
}

func (a *app) changeStatus(w http.ResponseWriter, r *http.Request, status, redirect string) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if err := a.db.SetStatus(r.Context(), p.ID, status); err != nil {
		log.Printf("상태 변경 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, p, "상태를 바꾸지 못했습니다.")
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if err := a.db.DeletePost(r.Context(), p.ID); err != nil {
		log.Printf("삭제 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, p, "삭제하지 못했습니다.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handlePublish는 검수를 마친 초안을 스레드에 올린다.
func (a *app) handlePublish(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}

	// 발행 직전 가드. 조립에 실패하면 여기서 멈춘다 (SPEC 5-1).
	text, err := Compose(p.Affiliate, p.Body)
	if err != nil {
		a.renderEdit(w, p, "발행할 수 없습니다: "+err.Error())
		return
	}
	reply, err := ComposeReply(p.AffiliateLink)
	if err != nil {
		a.renderEdit(w, p, "발행할 수 없습니다: "+err.Error())
		return
	}

	token, ok := a.currentToken(r.Context())
	if !ok {
		a.renderEdit(w, p, "스레드 계정이 아직 연결되지 않았습니다. 설정에서 연결해주세요.")
		return
	}

	// 한 번의 조건부 UPDATE로 선점한다. 두 번째 요청은 여기서 걸린다.
	claimed, err := a.db.ClaimForPublish(r.Context(), p.ID)
	if err != nil {
		log.Printf("발행 선점 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, p, "발행을 시작하지 못했습니다.")
		return
	}
	if !claimed {
		// 이미 다른 요청이 발행 중이다. 조용히 목록으로 보낸다.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	res, err := a.threads.Publish(r.Context(), token, text, reply)
	if err != nil {
		log.Printf("발행 실패 (id=%d): %v", p.ID, err)
		if dbErr := a.db.MarkFailed(r.Context(), p.ID, "스레드에 올리지 못했습니다."); dbErr != nil {
			log.Printf("실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := a.db.MarkPublished(r.Context(), p.ID, res.Permalink); err != nil {
		log.Printf("발행 기록 실패 (id=%d): %v", p.ID, err)
	}
	// 본문은 올라갔는데 링크 답글만 실패한 경우. 발행을 되돌리지 않고 알리기만 한다.
	if res.ReplyErr != nil {
		log.Printf("링크 답글 실패 (id=%d): %v", p.ID, res.ReplyErr)
		if dbErr := a.db.SetPublishNote(r.Context(), p.ID,
			"글은 올라갔지만 링크 답글에 실패했습니다. 스레드에서 직접 링크를 답글로 달아주세요."); dbErr != nil {
			log.Printf("답글 실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
		}
	}

	http.Redirect(w, r, "/?tab=published", http.StatusSeeOther)
}

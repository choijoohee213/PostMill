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
	{"published", "게시완료", []string{StatusPublished}},
	{"held", "보류", []string{StatusHeld}},
}

var templateFuncs = template.FuncMap{
	"preview":       preview,
	"formatTime":    formatTime,
	"affiliateKo":   affiliateKo,
	"affiliateOf":   affiliateOf,
	"canRetry":      canRetry,
	"canResume":     canResume,
	"canEditImages": canEditImages,
	"won":           won,
}

// canResume은 게시가 끊긴 채 멈춘 글인지 본다.
func canResume(p *Post) bool {
	return p.Status == StatusPublishing &&
		p.PublishStartedAt != nil &&
		time.Since(*p.PublishStartedAt) > publishStaleAfter
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

// affiliateOf는 제휴사 키로 선택지를 찾는다. 편집 화면처럼 목록 전체가
// 아니라 글 하나의 제휴사만 아는 곳에서 바로가기를 꺼내 쓴다.
func affiliateOf(key string) *affiliateOption {
	for i := range affiliateOptions {
		if affiliateOptions[i].Key == key {
			return &affiliateOptions[i]
		}
	}
	return nil
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
	Affiliates []affiliateOption
	BatchSize  int
	Missing    int    // 검수대기가 세 장에 못 미치는 만큼
	LastAff    string // 추가 버튼이 쓸 제휴사
	Query      string // 게시완료 검색어

	// 만들기 칸
	Error      string
	Form       manualForm
	TossAPI    bool            // 토스 API가 연결돼 있는지
	Areas      []coupangArea   // 쿠팡 AI 초안 분야
	Categories []categoryGroup // 토스 카테고리 선택지
}

func (a *app) handleList(w http.ResponseWriter, r *http.Request) {
	a.renderList(w, r, listExtra{})
}

// listExtra는 목록 위 만들기 칸에 되돌려 보여줄 것이다 (직접 만들기 실패).
type listExtra struct {
	Error string
	Form  manualForm
}

func (a *app) renderList(w http.ResponseWriter, r *http.Request, extra listExtra) {
	a.expireImages(r)

	active := r.URL.Query().Get("tab")
	current := tabs[0]
	for _, t := range tabs {
		if t.Key == active {
			current = t
			break
		}
	}

	// 검색은 게시완료에만 둔다. 검수대기는 빈 자리 계산이 목록 개수에 달려 있다.
	query := ""
	if current.Key == "published" {
		query = strings.TrimSpace(r.URL.Query().Get("q"))
	}

	posts, err := a.db.ListByStatus(r.Context(), a.session.userID(r), query, current.Statuses...)
	if err != nil {
		log.Printf("목록 조회 실패: %v", err)
		http.Error(w, "목록을 불러오지 못했습니다", http.StatusInternalServerError)
		return
	}

	// 생성 중이거나 올리는 중인 카드가 있을 때만 폴링한다.
	generating := false
	for _, p := range posts {
		if p.Status == StatusGenerating || (p.Status == StatusPublishing && !canResume(p)) {
			generating = true
			break
		}
	}

	// 검수대기가 세 장에 못 미치면 채울 자리를 알려준다.
	missing := 0
	lastAff := AffiliateCoupang
	if current.Key == "review" {
		if n := autoBatchSize - len(posts); n > 0 {
			missing = n
		}
		if len(posts) > 0 {
			lastAff = posts[0].Affiliate
		}
	}

	a.render(w, "list.html", listData{
		Tabs:       tabs,
		ActiveTab:  current.Key,
		Posts:      posts,
		Generating: generating,
		Affiliates: affiliateOptions,
		BatchSize:  autoBatchSize,
		Missing:    missing,
		LastAff:    lastAff,
		Query:      query,
		Error:      extra.Error,
		Form:       extra.Form,
		TossAPI:    a.toss != nil,
		Areas:      coupangAreas,
		Categories: a.categoriesFor(r, current.Key),
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

type affiliateOption struct {
	Key   string
	Label string

	// 제휴 링크를 만들러 가는 곳. PostMill이 링크를 대신 발급하지 못하므로
	// 최소한 만들러 가는 길은 폼 안에서 열어준다.
	HelpURL   string
	HelpLabel string
}

var affiliateOptions = []affiliateOption{
	{
		Key:       AffiliateCoupang,
		Label:     "쿠팡",
		HelpURL:   "https://partners.coupang.com/",
		HelpLabel: "쿠팡 파트너스 열기",
	},
	{
		Key:       AffiliateToss,
		Label:     "토스",
		HelpURL:   "https://sharelink.toss.im/",
		HelpLabel: "토스 쉐어링크 열기",
	},
}

func (a *app) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	p, err := a.db.GetPost(r.Context(), a.session.userID(r), id)
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
	if !p.IsManual() {
		go a.suggest(id, p.UserID, p.Affiliate, a.recentProducts(r.Context(), p.UserID), defaultPick(p.Affiliate), -1, randomHook())
	} else {
		go a.generate(id, p.Affiliate, p.ProductName, p.Memo)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// generate는 요청과 무관하게 도는 백그라운드 작업이다.
// 요청 컨텍스트를 쓰면 리다이렉트와 동시에 취소되므로 쓰지 않는다.
// 상품은 사용자가 골랐으므로 글만 쓴다. 메모가 없으면 상품 이름으로만 쓴다.
func (a *app) generate(id int64, affiliate, productName, memo string) {
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

	body, detail, err := a.gemini.GenerateDraft(ctx, affiliate, manualMemo(productName, memo), room, randomHook())
	if err != nil {
		fail("초안을 만들지 못했습니다.", err)
		return
	}
	if err := a.db.SetGenerated(ctx, id, body, detail, ""); err != nil {
		fail("저장하지 못했습니다.", err)
	}
}

type editData struct {
	Post        *Post
	Disclosure  string
	Preview     string
	Reply       string
	Room        int
	BodyTarget  int
	DetailLimit int
	Used        int
	DetailUsed  int
	Detail2Used int
	Handle      string
	Error       string
	Images      []PostImage
	MaxImages   int
}

// draftFor는 편집 가능한 초안을 읽는다. 생성 중이거나 없는 글은 목록으로 돌려보낸다.
func (a *app) draftFor(w http.ResponseWriter, r *http.Request) (*Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	// 남의 글은 조회되지 않으므로 여기서 404가 된다.
	p, err := a.db.GetPost(r.Context(), a.session.userID(r), id)
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
	a.expireImages(r)
	a.renderEdit(w, r, p, "")
}

func (a *app) renderEdit(w http.ResponseWriter, r *http.Request, p *Post, errMsg string) {
	handle := "@나"
	if u, ok := a.currentUser(r); ok && u.Username != "" {
		handle = "@" + u.Username
	}

	disclosure, _ := disclosureFor(p.Affiliate)
	room, _ := BodyRoom(p.Affiliate)
	reply, _ := ComposeReply(p.AffiliateLink)

	// 미리보기는 발행과 같은 함수로 만든다. 화면과 실제 발행물이 달라질 수 없다.
	preview, err := Compose(p.Affiliate, p.Body)
	if err != nil && errMsg == "" {
		errMsg = "지금 상태로는 게시할 수 없어요: " + err.Error()
	}

	images, err := a.db.ListImages(r.Context(), p.ID)
	if err != nil {
		log.Printf("사진 목록 조회 실패 (id=%d): %v", p.ID, err)
	}

	a.render(w, "edit.html", editData{
		Post:        p,
		Images:      images,
		MaxImages:   maxImages,
		Disclosure:  disclosure,
		Preview:     preview,
		Reply:       reply,
		Room:        room,
		BodyTarget:  bodyTargetChars,
		DetailLimit: detailMaxChars,
		Used:        CharCount(strings.TrimSpace(p.Body)),
		DetailUsed:  CharCount(strings.TrimSpace(p.Detail)),
		Detail2Used: CharCount(strings.TrimSpace(p.Detail2)),
		Handle:      handle,
		Error:       errMsg,
	})
}

func (a *app) handleDraftSave(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	detail := strings.TrimSpace(r.FormValue("detail"))
	detail2 := strings.TrimSpace(r.FormValue("detail2"))
	// 저장 자체는 막지 않는다. 게시 가능한지는 미리보기와 카운터가 알려준다.
	if err := a.db.UpdateBody(r.Context(), p.UserID, p.ID, body, detail, detail2); err != nil {
		log.Printf("본문 저장 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "저장하지 못했습니다.")
		return
	}
	p.Body, p.Detail, p.Detail2 = body, detail, detail2

	http.Redirect(w, r, fmt.Sprintf("/drafts/%d", p.ID), http.StatusSeeOther)
}

func (a *app) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if err := a.db.SetStatus(r.Context(), p.UserID, p.ID, StatusGenerating); err != nil {
		log.Printf("재생성 준비 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "재생성을 시작하지 못했습니다.")
		return
	}
	if err := a.db.DeleteImages(r.Context(), p.UserID, p.ID); err != nil {
		log.Printf("재생성 사진 삭제 실패 (id=%d): %v", p.ID, err)
	}
	go a.generate(p.ID, p.Affiliate, p.ProductName, p.Memo)

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
	if err := a.db.SetStatus(r.Context(), p.UserID, p.ID, status); err != nil {
		log.Printf("상태 변경 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "상태를 바꾸지 못했습니다.")
		return
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (a *app) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if err := a.db.DeletePost(r.Context(), p.UserID, p.ID); err != nil {
		log.Printf("삭제 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "삭제하지 못했습니다.")
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
		a.renderEdit(w, r, p, "게시할 수 없어요: "+err.Error())
		return
	}
	reply, err := ComposeReply(p.AffiliateLink)
	if err != nil {
		a.renderEdit(w, r, p, "게시할 수 없어요: "+err.Error())
		return
	}

	u, ok := a.currentUser(r)
	if !ok {
		a.renderEdit(w, r, p,
			"Threads 계정이 연결되지 않아 게시할 수 없어요. 토큰으로 로그인해주세요.")
		return
	}

	if reason := a.tossUnavailableReason(r.Context(), p); reason != "" {
		a.renderEdit(w, r, p, reason)
		return
	}

	// 한 번의 조건부 UPDATE로 선점한다. 두 번째 요청은 여기서 걸린다.
	claimed, err := a.db.ClaimForPublish(r.Context(), p.UserID, p.ID)
	if err != nil {
		log.Printf("발행 선점 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "게시를 시작하지 못했어요.")
		return
	}
	if !claimed {
		// 이미 다른 요청이 발행 중이다. 조용히 목록으로 보낸다.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// 게시는 백그라운드로 넘기고 바로 목록으로 보낸다.
	// 게시 중인 글은 검수대기 탭에 있고, 거기서만 폴링이 돈다.
	a.startPublish(p, u.AccessToken, text, reply)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// categoriesFor는 만들기 칸이 보이는 검수대기 탭에서만 토스 카테고리를 준비한다.
func (a *app) categoriesFor(r *http.Request, tab string) []categoryGroup {
	if tab != "review" {
		return nil
	}
	return a.tossCategoryGroups(r.Context())
}

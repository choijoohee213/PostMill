package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTossServer는 토큰 발급, 목록 조회, 링크 발급을 흉내내고 호출을 센다.
type fakeTossServer struct {
	mu         sync.Mutex
	tokenCalls int
	listCalls  int
	linkBodies []map[string]any
	authSeen   []string

	best     []TossProduct
	deals    []TossProduct
	linkFail string // 비어 있지 않으면 링크 발급을 이 코드로 실패시킨다
}

func (f *fakeTossServer) client(t *testing.T) *Toss {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if r.URL.Path == "/token" {
			r.ParseForm()
			f.tokenCalls++
			if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "ak" ||
				r.FormValue("client_secret") != "sk" || r.FormValue("scope") != "sharelink:read sharelink:write" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"access_token":"tok%d","token_type":"Bearer","expires_in":31535999}`, f.tokenCalls)
			return
		}

		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		success := func(v any) {
			b, _ := json.Marshal(map[string]any{"resultType": "SUCCESS", "success": v})
			w.Write(b)
		}
		switch r.URL.Path {
		case "/products/best-selling":
			f.listCalls++
			success(map[string]any{"items": f.best, "hasNext": false})
		case "/products/today-deals":
			f.listCalls++
			success(map[string]any{"items": f.deals, "hasNext": false})
		case "/links":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.linkBodies = append(f.linkBodies, body)
			if f.linkFail != "" {
				fmt.Fprintf(w, `{"resultType":"FAIL","error":{"errorCode":%q,"reason":"no"}}`, f.linkFail)
				return
			}
			success(map[string]any{
				"tacaItemId": body["tacaItemId"],
				"shortUrl":   fmt.Sprintf("https://toss.im/_m/%v", body["tacaItemId"]),
				"originUrl":  "https://toss.shopping/t/1?k=x",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewToss("ak", "sk", "pub-uuid")
	c.HTTP = srv.Client()
	c.BaseURL = srv.URL + "/"
	c.TokenURL = srv.URL + "/token"
	return c
}

func TestToss_토큰을_받고_목록과_링크를_부른다(t *testing.T) {
	f := &fakeTossServer{best: []TossProduct{{TacaItemID: 7, DisplayName: "청소기", DisplayPrice: 19900, DiscountRate: 33.5}}}
	c := f.client(t)
	ctx := context.Background()

	token, exp, err := c.IssueToken(ctx)
	if err != nil || token != "tok1" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	if time.Until(exp) < 300*24*time.Hour {
		t.Fatalf("만료=%v, 1년 가까이여야 한다", exp)
	}

	best, err := c.BestSelling(ctx, token, 50)
	if err != nil || len(best) != 1 || best[0].TacaItemID != 7 || best[0].DiscountRate != 33.5 {
		t.Fatalf("best=%+v err=%v", best, err)
	}

	link, err := c.CreateLink(ctx, token, 7)
	if err != nil || link != "https://toss.im/_m/7" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	body := f.linkBodies[0]
	if body["publisherId"] != "pub-uuid" || body["tacaItemId"] != float64(7) {
		t.Fatalf("발급 요청=%+v", body)
	}
	for _, a := range f.authSeen {
		if a != "Bearer tok1" {
			t.Fatalf("Authorization=%q", a)
		}
	}
}

// HTTP 200이어도 resultType이 FAIL이면 실패다. 200만 보고 성공으로 치면 안 된다.
func TestToss_HTTP_200이어도_FAIL이면_실패다(t *testing.T) {
	f := &fakeTossServer{linkFail: tossAccessDenied}
	c := f.client(t)

	_, err := c.CreateLink(context.Background(), "tok", 1)
	var te *tossError
	if !errors.As(err, &te) || te.Code != tossAccessDenied || te.HTTPStatus != 200 {
		t.Fatalf("err=%v", err)
	}
	if msg := draftFailMessage(err); !strings.Contains(msg, "IP") {
		t.Fatalf("안내 문구=%q", msg)
	}
}

func TestTossCandidates(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.FixedZone("KST", 9*3600))
	later := now.Add(10 * time.Hour).Format(time.RFC3339)
	soon := now.Add(time.Hour).Format(time.RFC3339)

	deals := []TossProduct{
		{TacaItemID: 1, DisplayName: "특가 넉넉", EndAt: later},
		{TacaItemID: 2, DisplayName: "특가 곧 끝남", EndAt: soon},
	}
	best := []TossProduct{
		{TacaItemID: 1, DisplayName: "특가 넉넉"}, // 특가와 겹친다
		{TacaItemID: 3, DisplayName: "품절", IsSoldOut: true},
		{TacaItemID: 4, DisplayName: "최근에 다룸"},
		{TacaItemID: 5, DisplayName: "베스트 A"},
		{TacaItemID: 6, DisplayName: "베스트 B"},
		{TacaItemID: 7, DisplayName: "베스트 C"},
		{TacaItemID: 8, DisplayName: "베스트 D"},
	}

	all := tossCandidates(best, deals, []string{"최근에 다룸"}, now, -1, 3)
	var ids []int64
	for _, p := range all {
		ids = append(ids, p.TacaItemID)
	}
	if fmt.Sprint(ids) != "[1 5 6 7 8]" {
		t.Fatalf("후보=%v, 특가 먼저·품절/곧끝남/최근/중복 제외여야 한다", ids)
	}
	if all[0].EndAt == "" {
		t.Error("특가 표시가 사라졌다")
	}

	// 동시에 만드는 초안끼리 후보가 겹치지 않는다.
	seen := map[int64]int{}
	for slot := 0; slot < 3; slot++ {
		for _, p := range tossCandidates(best, deals, []string{"최근에 다룸"}, now, slot, 3) {
			seen[p.TacaItemID]++
		}
	}
	if len(seen) != 5 {
		t.Fatalf("나눈 후보 합=%v, 전체와 같아야 한다", seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("상품 %d이 %d개 몫에 들어갔다", id, n)
		}
	}
}

func TestSuggestFromToss_번호로_상품을_고른다(t *testing.T) {
	shortBackoff(t)
	replies := []string{
		"맨 위에 거\n---\n본문\n---\n답1\n---\n답2", // 번호가 아니면 다시 뽑는다
		"2번\n---\n본문이야\n---\n답글하나\n---\n답글둘",
	}
	n := 0
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody(replies[n]))
		n++
	})
	products := []TossProduct{{DisplayName: "첫째"}, {DisplayName: "둘째"}}

	i, d, err := g.SuggestFromToss(context.Background(), products)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("호출 %d번, 번호를 못 읽으면 다시 뽑아야 한다", *calls)
	}
	if i != 1 || d.ProductName != "둘째" || d.Body != "본문이야" || d.Detail2 != "답글둘" {
		t.Fatalf("i=%d d=%+v", i, d)
	}
}

func tossGemini(t *testing.T) *Gemini {
	t.Helper()
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody("1\n---\n본문\n---\n답글하나\n---\n답글둘"))
	})
	return g
}

func TestSuggest_토스는_고른_상품의_쉐어링크까지_넣는다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "toss-suggest"

	f := &fakeTossServer{best: []TossProduct{
		{TacaItemID: 42, DisplayName: "토스테스트 무선 청소기", ProductURL: "https://toss.shopping/t/42"},
	}}
	a := &app{db: db, gemini: tossGemini(t), toss: f.client(t)}
	// 다른 테스트가 남긴 토큰을 쓰지 않게 비운다.
	db.SetState(ctx, stateTossToken, "")
	db.SetState(ctx, stateTossExpiresAt, "")

	id, _ := db.CreateDraft(ctx, user, AffiliateToss, "", "", "")
	t.Cleanup(func() { db.DeletePost(ctx, user, id) })

	a.suggest(id, AffiliateToss, nil, "", -1)

	p, _ := db.GetPost(ctx, user, id)
	if p.Status != StatusPending || p.ErrorMsg != "" {
		t.Fatalf("상태=%s 사유=%q", p.Status, p.ErrorMsg)
	}
	if p.AffiliateLink != "https://toss.im/_m/42" {
		t.Fatalf("제휴 링크=%q, 발급한 쉐어링크여야 한다", p.AffiliateLink)
	}
	if p.ProductURL != "https://toss.shopping/t/42" || p.ProductName != "토스테스트 무선 청소기" {
		t.Fatalf("상품=%q %q", p.ProductName, p.ProductURL)
	}

	// 토큰과 목록은 다시 받지 않는다.
	id2, _ := db.CreateDraft(ctx, user, AffiliateToss, "", "", "")
	t.Cleanup(func() { db.DeletePost(ctx, user, id2) })
	a.suggest(id2, AffiliateToss, nil, "", -1)
	if f.tokenCalls != 1 || f.listCalls != 2 {
		t.Fatalf("토큰 %d번, 목록 %d번. 토큰 1번·목록 2번(베스트+특가)이어야 한다", f.tokenCalls, f.listCalls)
	}
}

func TestSuggest_토스_API가_거부하면_이유를_남긴다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "toss-denied"

	f := &fakeTossServer{
		best:     []TossProduct{{TacaItemID: 1, DisplayName: "토스테스트 거부"}},
		linkFail: tossAccessDenied,
	}
	a := &app{db: db, gemini: tossGemini(t), toss: f.client(t)}
	id, _ := db.CreateDraft(ctx, user, AffiliateToss, "", "", "")
	t.Cleanup(func() { db.DeletePost(ctx, user, id) })

	a.suggest(id, AffiliateToss, nil, "", -1)

	p, _ := db.GetPost(ctx, user, id)
	if p.Status != StatusGenerating || !strings.Contains(p.ErrorMsg, "IP") {
		t.Fatalf("상태=%s 사유=%q", p.Status, p.ErrorMsg)
	}
}

// 자동으로 넣은 링크는 사용자가 손댄 것이 아니다. 새로 만들 때 버려야 쌓이지 않는다.
func TestAutoLink_자동_링크는_버리고_직접_넣은_링크는_남긴다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "toss-autolink"

	mk := func(link string) int64 {
		id, _ := db.CreateDraft(ctx, user, AffiliateToss, "", "", "")
		t.Cleanup(func() { db.DeletePost(ctx, user, id) })
		db.SetAutoGenerated(ctx, id, AutoDraft{ProductName: "상품", Body: "본문", AffiliateLink: link})
		return id
	}
	auto := mk("https://toss.im/_m/auto")
	edited := mk("https://toss.im/_m/auto2")
	db.UpdateLink(ctx, user, edited, "https://toss.im/_m/mine")
	resaved := mk("https://toss.im/_m/same")
	db.UpdateLink(ctx, user, resaved, "https://toss.im/_m/same") // 같은 값을 다시 저장해도 자동 그대로

	if err := db.ClearAutoDrafts(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetPost(ctx, user, auto); err == nil {
		t.Error("자동 링크 초안이 남았다")
	}
	if _, err := db.GetPost(ctx, user, resaved); err == nil {
		t.Error("같은 링크를 다시 저장한 자동 초안이 남았다")
	}
	if _, err := db.GetPost(ctx, user, edited); err != nil {
		t.Error("직접 링크를 넣은 초안을 지웠다")
	}

	// 재생성하면 이전 상품의 자동 링크를 비운다. 직접 넣은 링크는 둔다.
	regen := mk("https://toss.im/_m/old")
	db.ResetForRegenerate(ctx, user, regen)
	db.ResetForRegenerate(ctx, user, edited)
	if p, _ := db.GetPost(ctx, user, regen); p.AffiliateLink != "" {
		t.Errorf("재생성 후 자동 링크=%q", p.AffiliateLink)
	}
	if p, _ := db.GetPost(ctx, user, edited); p.AffiliateLink != "https://toss.im/_m/mine" {
		t.Errorf("재생성 후 직접 링크=%q", p.AffiliateLink)
	}
}

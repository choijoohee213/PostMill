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

	subTagBodies []map[string]any
	subTagStatus string // 비면 CREATED

	soldOut     map[int64]bool
	gone        map[int64]bool // 판매 종료로 조회되지 않는 상품
	detailFail  bool
	detailCalls int

	perfQueries []string
	perfPages   []string // 쪽마다 돌려줄 success JSON
	settleMonth string

	categories   []TossCategory
	catBest      map[string][]TossProduct // 카테고리 ID → 베스트
	detailsByID  map[int64]ProductDetail  // 상세 조회에 돌려줄 상품 (tacaItemId 또는 tacaId 키)
	detailParams []string

	subTagList   []string
	perfBySubTag map[string]string // perfPages가 없을 때 subTagId별로 돌려줄 success JSON ("" = 전체)
	settleBySub  map[string]int64
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
		case "/products/detail":
			f.detailCalls++
			if f.detailFail {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			var items []any
			var notFound []int64
			ids := r.URL.Query().Get("tacaItemIds")
			if ids == "" {
				ids = r.URL.Query().Get("tacaIds")
			}
			f.detailParams = append(f.detailParams, r.URL.RawQuery)
			for _, s := range strings.Split(ids, ",") {
				var id int64
				fmt.Sscan(s, &id)
				if f.gone[id] {
					notFound = append(notFound, id)
					continue
				}
				if d, ok := f.detailsByID[id]; ok {
					items = append(items, d)
					continue
				}
				items = append(items, map[string]any{"tacaItemId": id, "isSoldOut": f.soldOut[id]})
			}
			success(map[string]any{"items": items, "notFoundIds": notFound})
		case "/sub-tags/create":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.subTagBodies = append(f.subTagBodies, body)
			st := f.subTagStatus
			if st == "" {
				st = "CREATED"
			}
			success(map[string]any{"results": []map[string]any{{"status": st}}})
		case "/performance":
			f.perfQueries = append(f.perfQueries, r.URL.RawQuery)
			if f.perfPages == nil {
				fmt.Fprintf(w, `{"resultType":"SUCCESS","success":%s}`, f.perfBySubTag[r.URL.Query().Get("subTagId")])
				return
			}
			page := f.perfPages[len(f.perfQueries)-1]
			fmt.Fprintf(w, `{"resultType":"SUCCESS","success":%s}`, page)
		case "/categories":
			success(map[string]any{"categories": f.categories})
		case "/sub-tags":
			var list []map[string]string
			for _, id := range f.subTagList {
				list = append(list, map[string]string{"subTagId": id})
			}
			success(map[string]any{"subTags": list, "hasNext": false})
		default:
			if strings.HasPrefix(r.URL.Path, "/products/best-categories/") {
				f.listCalls++
				success(map[string]any{"items": f.catBest[strings.TrimPrefix(r.URL.Path, "/products/best-categories/")]})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/settlements/") {
				f.settleMonth = strings.TrimPrefix(r.URL.Path, "/settlements/")
				amount := int64(41200)
				if f.settleBySub != nil {
					amount = f.settleBySub[r.URL.Query().Get("subTagId")]
				}
				success(map[string]any{"summary": map[string]any{"commissionAmount": amount}, "items": []any{}})
				return
			}
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

	link, err := c.CreateLink(ctx, token, 7, "u-1")
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

	_, err := c.CreateLink(context.Background(), "tok", 1, "")
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

	all := tossCandidates(append(deals, best...), []string{"최근에 다룸"}, now, -1, 3)
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
		for _, p := range tossCandidates(append(deals, best...), []string{"최근에 다룸"}, now, slot, 3) {
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
		"맨 위에 거\n---\n본문\n---\n답1", // 번호가 아니면 다시 뽑는다
		"2번\n---\n본문이야\n---\n답글하나",
	}
	n := 0
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody(replies[n]))
		n++
	})
	products := []TossProduct{{DisplayName: "첫째"}, {DisplayName: "둘째"}}

	i, d, err := g.SuggestFromToss(context.Background(), products, hookTypes[0])
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("호출 %d번, 번호를 못 읽으면 다시 뽑아야 한다", *calls)
	}
	if i != 1 || d.ProductName != "둘째" || d.Body != "본문이야" || d.Detail != "답글하나" {
		t.Fatalf("i=%d d=%+v", i, d)
	}
}

func tossGemini(t *testing.T) *Gemini {
	t.Helper()
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody("1\n---\n본문\n---\n답글하나"))
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

	a.suggest(id, user, AffiliateToss, nil, "", -1, hookTypes[0])

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
	a.suggest(id2, user, AffiliateToss, nil, "", -1, hookTypes[0])
	if f.tokenCalls != 1 || f.listCalls != 1 {
		t.Fatalf("토큰 %d번, 목록 %d번. 토큰 1번·목록 1번(베스트)이어야 한다", f.tokenCalls, f.listCalls)
	}

	// 링크는 계정 subTag로 발급하고, subTag 등록은 한 번만 한다.
	if len(f.subTagBodies) != 1 {
		t.Fatalf("subTag 등록 %d번", len(f.subTagBodies))
	}
	for _, body := range f.linkBodies {
		if body["subTagId"] != "u-"+user {
			t.Fatalf("발급 요청=%+v, subTagId가 계정 것이어야 한다", body)
		}
	}
	if p.TacaItemID != 42 || !p.LinkAuto {
		t.Fatalf("taca_item_id=%d link_auto=%v", p.TacaItemID, p.LinkAuto)
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

	a.suggest(id, user, AffiliateToss, nil, "", -1, hookTypes[0])

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

func TestTossSubTag_허용_문자만_쓴다(t *testing.T) {
	for _, id := range []string{"17841400000000000", "admin"} {
		tag := tossSubTag(id)
		if len(tag) > 64 {
			t.Fatalf("%q는 64자를 넘는다", tag)
		}
		for _, c := range tag {
			ok := c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
			if !ok {
				t.Fatalf("%q에 허용되지 않는 문자 %q", tag, c)
			}
		}
	}
}

func TestToss_subTag_등록이_형식_오류면_실패다(t *testing.T) {
	f := &fakeTossServer{subTagStatus: "INVALID_FORMAT"}
	if err := f.client(t).EnsureSubTag(context.Background(), "tok", "u 1", ""); err == nil {
		t.Fatal("형식 오류를 성공으로 봤다")
	}
	f2 := &fakeTossServer{subTagStatus: "ALREADY_EXISTS"}
	if err := f2.client(t).EnsureSubTag(context.Background(), "tok", "u-1", "@me"); err != nil {
		t.Fatalf("이미 있는 subTag는 성공이어야 한다: %v", err)
	}
	if f2.subTagBodies[0]["subTags"].([]any)[0].(map[string]any)["label"] != "@me" {
		t.Fatalf("등록 요청=%+v", f2.subTagBodies[0])
	}
}

func TestToss_실적은_여러_쪽을_이어_받는다(t *testing.T) {
	f := &fakeTossServer{perfPages: []string{
		`{"summary":{"clickCount":10,"expectedCommissionAmount":900},"items":[{"productId":1,"expectedCommissionAmount":500}],"hasNext":true,"nextCursor":"c2"}`,
		`{"summary":{"clickCount":10,"expectedCommissionAmount":900},"items":[{"productId":2,"expectedCommissionAmount":400}],"hasNext":false,"nextCursor":null}`,
	}}
	c := f.client(t)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, kst)
	perf, err := c.Performance(context.Background(), "tok", from, from.AddDate(0, 0, 13), "u-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(perf.Items) != 2 || perf.Summary.ClickCount != 10 {
		t.Fatalf("perf=%+v", perf)
	}
	if !strings.Contains(f.perfQueries[0], "fromDate=2026-09-01") || !strings.Contains(f.perfQueries[0], "toDate=2026-09-14") ||
		!strings.Contains(f.perfQueries[0], "subTagId=u-1") || !strings.Contains(f.perfQueries[1], "cursor=c2") {
		t.Fatalf("요청=%v", f.perfQueries)
	}

	settled, err := c.SettledCommission(context.Background(), "tok", "2026-08", "")
	if err != nil || settled != 41200 || f.settleMonth != "2026-08" {
		t.Fatalf("settled=%d month=%q err=%v", settled, f.settleMonth, err)
	}
}

func TestMergeStatsRows_상품별로_합치고_수익순으로(t *testing.T) {
	rows := mergeStatsRows([]PerformanceItem{
		{ProductID: 1, ProductName: "A", Attribution: "DIRECT", SoldQuantity: 1, ExpectedCommissionAmount: 100},
		{ProductID: 2, ProductName: "B", Attribution: "DIRECT", SoldQuantity: 2, ExpectedCommissionAmount: 300},
		{ProductID: 1, ProductName: "A", Attribution: "INDIRECT", SoldQuantity: 3, ExpectedCommissionAmount: 250},
	})
	if len(rows) != 2 || rows[0].ProductID != 1 || rows[0].Sold != 4 || rows[0].Expected != 350 || !rows[0].Indirect {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[1].Indirect {
		t.Error("직접만 있는 상품에 간접 표시")
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -41200: "-41,200"} {
		if got := won(in); got != want {
			t.Errorf("won(%d)=%q, %q여야 한다", in, got, want)
		}
	}
}

func TestTossUnavailableReason(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	f := &fakeTossServer{soldOut: map[int64]bool{11: true}, gone: map[int64]bool{12: true}}
	a := &app{db: db, toss: f.client(t)}

	post := func(item int64, auto bool) *Post {
		return &Post{Affiliate: AffiliateToss, LinkAuto: auto, TacaItemID: item}
	}
	if r := a.tossUnavailableReason(ctx, post(11, true)); !strings.Contains(r, "품절") {
		t.Errorf("품절 상품: %q", r)
	}
	if r := a.tossUnavailableReason(ctx, post(12, true)); !strings.Contains(r, "판매가 끝났") {
		t.Errorf("판매 종료 상품: %q", r)
	}
	if r := a.tossUnavailableReason(ctx, post(13, true)); r != "" {
		t.Errorf("살 수 있는 상품을 막았다: %q", r)
	}

	calls := f.detailCalls
	if r := a.tossUnavailableReason(ctx, post(11, false)); r != "" || f.detailCalls != calls {
		t.Errorf("직접 넣은 링크까지 확인했다: %q", r)
	}
	if r := a.tossUnavailableReason(ctx, &Post{Affiliate: AffiliateCoupang}); r != "" || f.detailCalls != calls {
		t.Errorf("쿠팡 글을 확인했다: %q", r)
	}

	// 토스가 응답하지 않으면 게시를 막지 않는다.
	f.detailFail = true
	if r := a.tossUnavailableReason(ctx, post(11, true)); r != "" {
		t.Errorf("토스 장애로 게시를 막았다: %q", r)
	}
}

func TestPublishHandler_품절된_토스_상품은_게시하지_않는다(t *testing.T) {
	threads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Threads를 불렀다: %s", r.URL.Path)
	}))
	defer threads.Close()
	a := newTestApp(t, threads)
	f := &fakeTossServer{soldOut: map[int64]bool{77: true}}
	a.toss = f.client(t)
	ctx := context.Background()

	const user = "toss-soldout"
	a.db.SaveThreadsUser(ctx, ThreadsUser{UserID: user, AccessToken: "tok", ExpiresAt: nowPlusDays(30)})
	id, _ := a.db.CreateDraft(ctx, user, AffiliateToss, "", "", "")
	t.Cleanup(func() { a.db.DeletePost(ctx, user, id) })
	a.db.SetAutoGenerated(ctx, id, AutoDraft{ProductName: "상품", Body: "본문", AffiliateLink: "https://toss.im/_m/77", TacaItemID: 77})

	login := httptest.NewRecorder()
	a.session.issue(login, user, false)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /drafts/{id}/publish", a.handlePublish)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/drafts/%d/publish", id), nil)
	req.AddCookie(cookieFrom(t, login))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "품절") {
		t.Fatalf("응답=%d %q", w.Code, w.Body.String())
	}
	if p, _ := a.db.GetPost(ctx, user, id); p.Status != StatusPending {
		t.Fatalf("상태=%s, 게시를 시작하면 안 된다", p.Status)
	}
}

// 주인 계정은 subTag 없이 발급한 옛 링크까지 제 몫이다. 전체에서 다른 계정 몫을 뺀다.
func TestAccountStats_주인은_전체에서_다른_계정을_뺀다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	orig, _, _ := db.GetState(ctx, stateTossOwner)
	db.SetState(ctx, stateTossOwner, "owner")
	t.Cleanup(func() { db.SetState(ctx, stateTossOwner, orig) })

	f := &fakeTossServer{
		subTagList: []string{"u-owner", "u-sister"},
		perfBySubTag: map[string]string{
			"": `{"summary":{"clickCount":100,"soldQuantity":10,"expectedCommissionAmount":1000},"items":[
				{"productId":1,"attribution":"DIRECT","soldQuantity":6,"expectedCommissionAmount":600},
				{"productId":2,"attribution":"DIRECT","soldQuantity":4,"expectedCommissionAmount":400}]}`,
			"u-sister": `{"summary":{"clickCount":30,"soldQuantity":4,"expectedCommissionAmount":400},"items":[
				{"productId":2,"attribution":"DIRECT","soldQuantity":4,"expectedCommissionAmount":400}]}`,
		},
		settleBySub: map[string]int64{"": 5000, "u-sister": 1200},
	}
	a := &app{db: db, toss: f.client(t)}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, kst)

	perf, settled, err := a.accountStats(ctx, "tok", "owner", from, from, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if perf.Summary.ClickCount != 70 || perf.Summary.SoldQuantity != 6 || perf.Summary.ExpectedCommissionAmount != 600 || settled != 3800 {
		t.Fatalf("주인 실적=%+v 정산=%d", perf.Summary, settled)
	}
	if len(perf.Items) != 1 || perf.Items[0].ProductID != 1 {
		t.Fatalf("주인 상품=%+v, 동생 몫 상품은 빠져야 한다", perf.Items)
	}

	sister, settled, err := a.accountStats(ctx, "tok", "sister", from, from, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if sister.Summary.SoldQuantity != 4 || settled != 1200 {
		t.Fatalf("다른 계정 실적=%+v 정산=%d", sister.Summary, settled)
	}

	// subTag를 아직 등록하지 않은 계정은 토스에 묻지 않고 0이다 (묻으면 거절된다).
	before := len(f.perfQueries)
	none, settled, err := a.accountStats(ctx, "tok", "newbie", from, from, "2026-09")
	if err != nil || none.Summary.SoldQuantity != 0 || settled != 0 || len(f.perfQueries) != before {
		t.Fatalf("미등록 계정 실적=%+v 정산=%d err=%v 조회=%d번", none.Summary, settled, err, len(f.perfQueries)-before)
	}
}

func TestTossOwner_처음_토스_글을_쓴_계정을_기억한다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	orig, _, _ := db.GetState(ctx, stateTossOwner)
	db.SetState(ctx, stateTossOwner, "")
	t.Cleanup(func() { db.SetState(ctx, stateTossOwner, orig) })

	first, _ := db.CreateDraft(ctx, "owner-first", AffiliateToss, "", "", "")
	t.Cleanup(func() { db.DeletePost(ctx, "owner-first", first) })
	second, _ := db.CreateDraft(ctx, "owner-second", AffiliateToss, "", "", "")
	t.Cleanup(func() { db.DeletePost(ctx, "owner-second", second) })

	a := &app{db: db}
	owner, err := a.tossOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 테스트 DB에 더 오래된 토스 글이 없다면 처음 만든 계정이 주인이다.
	if want, _ := db.FirstTossAuthor(ctx); owner != want {
		t.Fatalf("주인=%q, %q여야 한다", owner, want)
	}

	// 옛 글을 지워도 주인은 바뀌지 않는다.
	db.DeletePost(ctx, "owner-first", first)
	if again, _ := a.tossOwner(ctx); again != owner {
		t.Fatalf("글을 지우자 주인이 %q에서 %q로 바뀌었다", owner, again)
	}
}

func TestCountPublishedToss_이_달과_누적을_센다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "count-toss"

	publish := func(affiliate string, publishedAt time.Time) {
		id, _ := db.CreateDraft(ctx, user, affiliate, "", "https://link/x", "메모")
		t.Cleanup(func() { db.DeletePost(ctx, user, id) })
		db.ClaimForPublish(ctx, user, id)
		db.MarkPublished(ctx, id, "")
		db.pool.Exec(ctx, `UPDATE posts SET published_at = $2 WHERE id = $1`, id, publishedAt)
	}
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, kst)
	publish(AffiliateToss, sep.Add(time.Hour))
	publish(AffiliateToss, sep.AddDate(0, 0, 29))
	publish(AffiliateToss, sep.Add(-time.Hour)) // 8월 마지막 날
	publish(AffiliateCoupang, sep.Add(time.Hour))

	draft, _ := db.CreateDraft(ctx, user, AffiliateToss, "", "", "메모") // 게시 안 한 글
	t.Cleanup(func() { db.DeletePost(ctx, user, draft) })

	inRange, total, err := db.CountPublishedToss(ctx, user, sep, sep.AddDate(0, 1, 0))
	if err != nil || inRange != 2 || total != 3 {
		t.Fatalf("이 달=%d 누적=%d err=%v, 2와 3이어야 한다", inRange, total, err)
	}
}

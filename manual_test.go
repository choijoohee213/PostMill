package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTossProducts_고른_곳에서_상품을_가져온다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	orig, _, _ := db.GetState(ctx, stateTossOwner)
	db.SetState(ctx, stateTossOwner, "src-owner")
	t.Cleanup(func() { db.SetState(ctx, stateTossOwner, orig) })

	f := &fakeTossServer{
		best:  []TossProduct{{TacaItemID: 1, DisplayName: "베스트"}},
		deals: []TossProduct{{TacaItemID: 2, DisplayName: "특가", EndAt: "2099-01-01T00:00:00+09:00"}},
		catBest: map[string][]TossProduct{
			"10": {{TacaItemID: 10, DisplayName: "주방1"}, {TacaItemID: 11, DisplayName: "주방2"}},
			"20": {{TacaItemID: 20, DisplayName: "청소1"}},
		},
		subTagList: []string{"u-src-owner"},
		perfBySubTag: map[string]string{"": `{"summary":{},"items":[
			{"productId":501,"attribution":"DIRECT","soldQuantity":1},
			{"productId":502,"attribution":"DIRECT","soldQuantity":5}]}`},
		detailsByID: map[int64]ProductDetail{
			501: {TacaItemID: 501, CategoryIDs: []int64{1, 20}},
			502: {TacaItemID: 502, CategoryIDs: []int64{1, 10}},
		},
	}
	a := &app{db: db, toss: f.client(t)}

	names := func(source string) string {
		t.Helper()
		_, items, err := a.tossProducts(ctx, "src-owner", source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		var out []string
		for _, p := range items {
			out = append(out, p.DisplayName)
		}
		return strings.Join(out, ",")
	}
	if got := names(TossSourceBest); got != "베스트" {
		t.Errorf("베스트=%q", got)
	}
	if got := names(""); got != "베스트" {
		t.Errorf("기본=%q, 베스트여야 한다", got)
	}
	if got := names(TossSourceDeal); got != "특가" {
		t.Errorf("하루특가=%q", got)
	}
	if got := names("cat:20"); got != "청소1" {
		t.Errorf("카테고리=%q", got)
	}
	// 많이 팔린 카테고리(10: 5개)가 앞에 오고 번갈아 섞인다.
	if got := names(TossSourceMine); got != "주방1,청소1,주방2" {
		t.Errorf("내 실적 기준=%q", got)
	}

	// 판매가 없으면 베스트에서 고른다.
	f.perfBySubTag[""] = `{"summary":{},"items":[]}`
	a.tossState.lists = nil
	if got := names(TossSourceMine); got != "베스트" {
		t.Errorf("판매 없을 때=%q", got)
	}
}

func TestTossCategoryTree_모든_단계를_넘긴다(t *testing.T) {
	db := openTestDB(t)
	f := &fakeTossServer{categories: []TossCategory{
		{CategoryID: 1, DisplayName: "생활", Children: []TossCategory{
			{CategoryID: 10, DisplayName: "주방", Children: []TossCategory{{CategoryID: 100, DisplayName: "밀폐용기"}}},
		}},
		{CategoryID: 2, DisplayName: "도서"},
	}}
	a := &app{db: db, toss: f.client(t)}
	tree := a.tossCategoryTree(context.Background())
	if len(tree) != 2 || tree[0].Children[0].Children[0].ID != 100 || tree[0].Children[0].Children[0].Name != "밀폐용기" {
		t.Fatalf("tree=%+v", tree)
	}
	if (&app{}).tossCategoryTree(context.Background()) != nil {
		t.Error("토스 API가 없으면 비어야 한다")
	}
}

// 랭킹이 없는 카테고리를 고르면 상품이 0개가 된다. 한 단계씩 올라가 찾는다.
func TestTossProducts_랭킹이_빈_카테고리는_상위에서_고른다(t *testing.T) {
	db := openTestDB(t)
	f := &fakeTossServer{
		best: []TossProduct{{TacaItemID: 1, DisplayName: "베스트"}},
		categories: []TossCategory{
			{CategoryID: 1, DisplayName: "생활", Children: []TossCategory{
				{CategoryID: 10, DisplayName: "주방", Children: []TossCategory{{CategoryID: 100, DisplayName: "밀폐용기"}}},
			}},
			{CategoryID: 2, DisplayName: "도서", Children: []TossCategory{{CategoryID: 20, DisplayName: "만화"}}},
		},
		catBest: map[string][]TossProduct{"10": {{TacaItemID: 10, DisplayName: "주방 베스트"}}},
	}
	a := &app{db: db, toss: f.client(t)}
	ctx := context.Background()

	_, items, err := a.tossProducts(ctx, "u", "cat:100")
	if err != nil || len(items) != 1 || items[0].DisplayName != "주방 베스트" {
		t.Fatalf("소분류가 비면 중분류에서: items=%+v err=%v", items, err)
	}
	_, items, err = a.tossProducts(ctx, "u", "cat:20")
	if err != nil || len(items) != 1 || items[0].DisplayName != "베스트" {
		t.Fatalf("끝까지 비면 베스트에서: items=%+v err=%v", items, err)
	}
}

// redirectTransport는 토스 짧은 링크를 흉내내 상품 주소로 보낸다.
type redirectTransport struct{ calls []string }

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls = append(rt.calls, req.URL.String())
	h := http.Header{}
	switch req.URL.Host {
	case "toss.im":
		h.Set("Location", "https://service.toss.im/shopping?next=1")
	case "service.toss.im":
		h.Set("Location", "https://toss.shopping/t/777?k=x")
	default:
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: h, Request: req}, nil
	}
	return &http.Response{StatusCode: 302, Body: http.NoBody, Header: h, Request: req}, nil
}

func TestTossProductID(t *testing.T) {
	rt := &redirectTransport{}
	a := &app{tossLinkClient: &http.Client{Transport: rt}}
	ctx := context.Background()

	if id, err := a.tossProductID(ctx, "https://toss.shopping/t/56322313?utm=a"); err != nil || id != 56322313 {
		t.Errorf("상품 주소: id=%d err=%v", id, err)
	}
	if len(rt.calls) != 0 {
		t.Error("상품 주소는 따라가지 않아도 된다")
	}
	if id, err := a.tossProductID(ctx, "https://toss.im/_m/abcDE"); err != nil || id != 777 {
		t.Errorf("짧은 링크: id=%d err=%v calls=%v", id, err, rt.calls)
	}
	for _, bad := range []string{"https://evil.example/t/1", "https://toss.im.evil.example/t/1", "ftp://toss.shopping/t/1", "그냥 글자"} {
		before := len(rt.calls)
		if _, err := a.tossProductID(ctx, bad); err == nil {
			t.Errorf("%q를 받아들였다", bad)
		}
		if len(rt.calls) != before {
			t.Errorf("%q로 요청을 보냈다", bad)
		}
	}
}

func TestManualMemo(t *testing.T) {
	if got := manualMemo("무선 청소기", "흡입력 좋음"); got != "상품: 무선 청소기\n흡입력 좋음" {
		t.Errorf("메모 있음=%q", got)
	}
	if got := manualMemo("무선 청소기", ""); !strings.Contains(got, "상품: 무선 청소기") || !strings.Contains(got, "메모 없음") {
		t.Errorf("메모 없음=%q", got)
	}
}

func manualApp(t *testing.T, f *fakeTossServer) *app {
	t.Helper()
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody("본문이야\n---\n디테일"))
	})
	a := &app{db: openTestDB(t), gemini: g, session: testSession(),
		tpl: template.Must(template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html"))}
	if f != nil {
		a.toss = f.client(t)
	}
	return a
}

func postManual(t *testing.T, a *app, user string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	login := httptest.NewRecorder()
	a.session.issue(login, user, false)
	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookieFrom(t, login))
	w := httptest.NewRecorder()
	a.handleManualSubmit(w, req)
	return w
}

func waitGenerated(t *testing.T, db *DB, user string, id int64) *Post {
	t.Helper()
	for i := 0; i < 100; i++ {
		p, _ := db.GetPost(context.Background(), user, id)
		if p != nil && p.Status != StatusGenerating {
			return p
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("생성이 끝나지 않았다")
	return nil
}

func latestPost(t *testing.T, db *DB, user string) *Post {
	t.Helper()
	posts, err := db.ListByStatus(context.Background(), user, "", StatusGenerating, StatusPending)
	if err != nil || len(posts) == 0 {
		t.Fatalf("초안이 없다: %v", err)
	}
	p := posts[0]
	t.Cleanup(func() { db.DeletePost(context.Background(), user, p.ID) })
	return p
}

func TestManualSubmit_토스는_링크만으로_상품과_쉐어링크를_채운다(t *testing.T) {
	f := &fakeTossServer{detailsByID: map[int64]ProductDetail{
		9876: {TacaItemID: 12345, TacaID: 9876, DisplayName: "토스 무선 청소기", ProductURL: "https://toss.shopping/t/9876"},
	}}
	a := manualApp(t, f)
	const user = "manual-toss"

	w := postManual(t, a, user, url.Values{"affiliate": {"toss"}, "product_link": {"https://toss.shopping/t/9876"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("응답=%d %s", w.Code, w.Body)
	}
	p := waitGenerated(t, a.db, user, latestPost(t, a.db, user).ID)
	if p.ProductName != "토스 무선 청소기" || p.AffiliateLink != "https://toss.im/_m/12345" || !p.LinkAuto ||
		p.TacaItemID != 12345 || !p.Manual || p.Body != "본문이야" {
		t.Fatalf("초안=%+v", p)
	}
	if !strings.Contains(f.detailParams[0], "tacaIds=9876") {
		t.Errorf("상세 조회=%v, 주소의 tacaId로 조회해야 한다", f.detailParams)
	}
	if f.linkBodies[0]["subTagId"] != "u-"+user {
		t.Errorf("발급 요청=%+v", f.linkBodies[0])
	}

	// 직접 고른 초안은 자동 생성 버튼이 지우지 않는다(메모가 없어도).
	a.db.ClearAutoDrafts(context.Background(), user)
	if _, err := a.db.GetPost(context.Background(), user, p.ID); err != nil {
		t.Fatal("직접 고른 초안이 지워졌다")
	}
}

func TestManualSubmit_잘못된_입력은_목록에_이유를_보여준다(t *testing.T) {
	f := &fakeTossServer{gone: map[int64]bool{1: true}}
	a := manualApp(t, f)

	cases := []struct {
		form url.Values
		want string
	}{
		{url.Values{"affiliate": {"toss"}, "product_link": {"https://example.com/t/1"}}, "알아보지 못했어요"},
		{url.Values{"affiliate": {"toss"}, "product_link": {"https://toss.shopping/t/1"}}, "판매가 끝났거나"},
		{url.Values{"affiliate": {"coupang"}, "affiliate_link": {"https://link.coupang.com/a"}}, "상품 이름"},
		{url.Values{"affiliate": {"coupang"}, "product_name": {"청소기"}}, "제휴 링크"},
	}
	for _, c := range cases {
		w := postManual(t, a, "manual-bad", c.form)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%v: 응답=%d, %q가 보여야 한다", c.form, w.Code, c.want)
		}
		// 입력한 값이 되돌아와 다시 적지 않아도 된다.
		if v := c.form.Get("product_name"); v != "" && !strings.Contains(w.Body.String(), v) {
			t.Errorf("%v: 입력값이 사라졌다", c.form)
		}
	}
}

func TestManualSubmit_쿠팡은_이름과_링크로_만든다(t *testing.T) {
	a := manualApp(t, nil)
	const user = "manual-coupang"
	w := postManual(t, a, user, url.Values{"affiliate": {"coupang"}, "affiliate_link": {"https://link.coupang.com/a"},
		"product_name": {"접이식 설거지통"}, "memo": {"물기 잘 빠짐"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("응답=%d %s", w.Code, w.Body)
	}
	p := waitGenerated(t, a.db, user, latestPost(t, a.db, user).ID)
	if p.ProductName != "접이식 설거지통" || p.AffiliateLink != "https://link.coupang.com/a" || p.LinkAuto ||
		!strings.Contains(p.ProductURL, "coupang.com/np/search") || p.Memo != "물기 잘 빠짐" {
		t.Fatalf("초안=%+v", p)
	}
}

func TestHandleAuto_토스_카테고리를_고르면_그_카테고리에서_고른다(t *testing.T) {
	f := &fakeTossServer{
		best:    []TossProduct{{TacaItemID: 1, DisplayName: "베스트상품"}},
		catBest: map[string][]TossProduct{"10": {{TacaItemID: 10, DisplayName: "주방A"}, {TacaItemID: 11, DisplayName: "주방B"}, {TacaItemID: 12, DisplayName: "주방C"}}},
	}
	a := manualApp(t, f)
	a.gemini, _ = fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, okBody("1\n---\n본문\n---\n답글하나"))
	})
	const user = "auto-cat"

	login := httptest.NewRecorder()
	a.session.issue(login, user, false)
	form := url.Values{"affiliate": {"toss"}, "source": {"cat"}, "category": {"cat:10"}}
	req := httptest.NewRequest(http.MethodPost, "/auto", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookieFrom(t, login))
	a.handleAuto(httptest.NewRecorder(), req)

	posts, _ := a.db.ListByStatus(context.Background(), user, "", StatusGenerating, StatusPending)
	if len(posts) != autoBatchSize {
		t.Fatalf("초안 %d장", len(posts))
	}
	got := map[string]bool{}
	for _, p := range posts {
		t.Cleanup(func() { a.db.DeletePost(context.Background(), user, p.ID) })
		got[waitGenerated(t, a.db, user, p.ID).ProductName] = true
	}
	if got["베스트상품"] || len(got) != 3 {
		t.Fatalf("고른 상품=%v, 카테고리 상품 세 개가 겹치지 않아야 한다", got)
	}
}

func TestHandleRefresh_직접_고른_초안은_상품을_두고_글만_새로_쓴다(t *testing.T) {
	a := manualApp(t, nil)
	ctx := context.Background()
	const user = "refresh-manual"
	id, _ := a.db.CreateManualDraft(ctx, user, ManualDraft{Affiliate: AffiliateCoupang, ProductName: "설거지통", AffiliateLink: "https://link.coupang.com/a"})
	t.Cleanup(func() { a.db.DeletePost(ctx, user, id) })
	a.db.SetGenerated(ctx, id, "옛 본문", "", "")

	login := httptest.NewRecorder()
	a.session.issue(login, user, false)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /drafts/{id}/refresh", a.handleRefresh)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/drafts/%d/refresh", id), nil)
	req.AddCookie(cookieFrom(t, login))
	mux.ServeHTTP(httptest.NewRecorder(), req)

	p := waitGenerated(t, a.db, user, id)
	if p.ProductName != "설거지통" || p.AffiliateLink != "https://link.coupang.com/a" || p.Body != "본문이야" {
		t.Fatalf("초안=%+v", p)
	}
}

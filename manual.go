package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// manualForm은 홈의 "내가 골라요" 입력이다. 오류가 나면 입력을 그대로 되돌려 보여준다.
type manualForm struct {
	Affiliate     string
	ProductLink   string // 토스 상품 주소나 공유 링크
	AffiliateLink string // 쿠팡 파트너스 링크 (토스 API가 없으면 토스 쉐어링크)
	ProductName   string
	Memo          string
}

// handleManualSubmit은 사용자가 고른 상품으로 초안을 만들고 글은 AI가 쓰게 한다.
//
// 토스는 API가 연결돼 있으면 상품 링크만 받는다. 상품 이름을 가져오고
// 쉐어링크를 계정 subTag로 발급한다. 메모는 없어도 된다.
func (a *app) handleManualSubmit(w http.ResponseWriter, r *http.Request) {
	userID := a.session.userID(r)
	form := manualForm{
		Affiliate:     r.FormValue("affiliate"),
		ProductLink:   strings.TrimSpace(r.FormValue("product_link")),
		AffiliateLink: strings.TrimSpace(r.FormValue("affiliate_link")),
		ProductName:   strings.TrimSpace(r.FormValue("product_name")),
		Memo:          strings.TrimSpace(r.FormValue("memo")),
	}
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		a.renderList(w, r, listExtra{Error: msg, Form: form})
	}

	if _, err := disclosureFor(form.Affiliate); err != nil {
		fail("제휴사를 골라주세요.")
		return
	}

	d := ManualDraft{Affiliate: form.Affiliate, Memo: form.Memo}
	if form.Affiliate == AffiliateToss && a.toss != nil {
		if form.ProductLink == "" {
			fail("토스 상품 링크를 붙여넣어 주세요.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		detail, link, err := a.tossManualLink(ctx, userID, form.ProductLink)
		if err != nil {
			log.Printf("토스 상품 링크 처리 실패: %v", err)
			fail(manualFailMessage(err))
			return
		}
		d.ProductName, d.ProductURL = detail.DisplayName, detail.ProductURL
		d.AffiliateLink, d.LinkAuto, d.TacaItemID = link, true, detail.TacaItemID
	} else {
		if form.AffiliateLink == "" {
			fail("제휴 링크를 붙여넣어 주세요.")
			return
		}
		if form.ProductName == "" {
			fail("상품 이름을 적어주세요.")
			return
		}
		d.ProductName, d.AffiliateLink = form.ProductName, form.AffiliateLink
		d.ProductURL = SearchURL(form.Affiliate, form.ProductName)
	}

	id, err := a.db.CreateManualDraft(r.Context(), userID, d)
	if err != nil {
		log.Printf("초안 생성 실패: %v", err)
		fail("저장하지 못했어요.")
		return
	}

	// 생성은 백그라운드로 넘기고 즉시 목록으로 보낸다.
	go a.generate(id, d.Affiliate, d.ProductName, d.Memo)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// errTossLinkUnknown은 붙여넣은 주소에서 토스 상품을 알아내지 못한 경우다.
var errTossLinkUnknown = errors.New("토스 상품 주소가 아니다")

// errTossProductGone은 상품이 판매 종료 등으로 조회되지 않는 경우다.
var errTossProductGone = errors.New("조회되지 않는 토스 상품이다")

// tossManualLink는 붙여넣은 토스 링크의 상품을 찾아 쉐어링크를 발급한다.
func (a *app) tossManualLink(ctx context.Context, userID, raw string) (*ProductDetail, string, error) {
	tacaID, err := a.tossProductID(ctx, raw)
	if err != nil {
		return nil, "", err
	}

	token, err := a.lockedTossToken(ctx)
	if err != nil {
		return nil, "", err
	}
	found, _, err := a.toss.ProductDetailsByTacaIDs(ctx, token, []int64{tacaID})
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, "", err
	}
	if len(found) == 0 {
		return nil, "", errTossProductGone
	}
	detail := &found[0]

	tag, err := a.ensureSubTag(ctx, token, userID)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, "", err
	}
	link, err := a.toss.CreateLink(ctx, token, detail.TacaItemID, tag)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, "", err
	}
	return detail, link, nil
}

// tossProductPath는 상품 페이지 주소의 /t/{tacaId} 부분이다.
var tossProductPath = regexp.MustCompile(`^/t/(\d+)`)

// tossProductID는 토스 링크에서 상품 ID(tacaId)를 알아낸다.
//
// 상품 페이지 주소(toss.shopping/t/123)는 바로 읽는다. 앱의 공유 버튼으로 만든
// 짧은 링크는 따라가서 도착한 상품 주소에서 읽는다. 사용자가 붙여넣은 주소로
// 서버가 요청을 보내게 되므로 토스 도메인만 따라간다.
func (a *app) tossProductID(ctx context.Context, raw string) (int64, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !isTossHost(u.Hostname()) {
		return 0, errTossLinkUnknown
	}
	if id, ok := tossIDFromURL(u); ok {
		return id, nil
	}

	var found int64
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if id, ok := tossIDFromURL(req.URL); ok {
				found = id
				return http.ErrUseLastResponse
			}
			if len(via) >= 5 || !isTossHost(req.URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	if a.tossLinkClient != nil {
		client.Transport = a.tossLinkClient.Transport
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, errTossLinkUnknown
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, errTossLinkUnknown
	}
	resp.Body.Close()
	if found == 0 {
		return 0, errTossLinkUnknown
	}
	return found, nil
}

func tossIDFromURL(u *url.URL) (int64, bool) {
	if !isTossHost(u.Hostname()) {
		return 0, false
	}
	m := tossProductPath.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	return id, err == nil
}

func isTossHost(host string) bool {
	for _, base := range []string{"toss.im", "toss.shopping"} {
		if host == base || strings.HasSuffix(host, "."+base) {
			return true
		}
	}
	return false
}

func manualFailMessage(err error) string {
	switch {
	case errors.Is(err, errTossLinkUnknown):
		return "토스 상품 링크를 알아보지 못했어요. 토스쇼핑 상품 페이지 주소나 공유 링크를 붙여넣어 주세요."
	case errors.Is(err, errTossProductGone):
		return "판매가 끝났거나 지금 살 수 없는 상품이에요."
	}
	if msg := draftFailMessage(err); msg != "초안을 만들지 못했습니다." {
		return msg
	}
	return "토스에서 상품 정보를 가져오지 못했어요. 잠시 뒤 다시 시도해주세요."
}

// manualMemo는 모델에게 줄 상품 메모다. 메모가 없으면 이름 말고는 사실을 쓰지 않게 한다.
func manualMemo(productName, memo string) string {
	var b strings.Builder
	if productName != "" {
		b.WriteString("상품: " + productName + "\n")
	}
	if memo != "" {
		b.WriteString(memo)
	} else {
		b.WriteString("(메모 없음. 상품 이름 말고는 제품에 대한 사실을 쓰지 말고, 쓰는 상황과 느낌으로만 써라.)")
	}
	return b.String()
}

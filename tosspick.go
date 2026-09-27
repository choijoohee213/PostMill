package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// tossTokenMargin 안에 만료되는 토큰은 미리 새로 받는다.
	tossTokenMargin = 7 * 24 * time.Hour

	// 베스트 랭킹은 1시간, 카테고리 베스트와 카테고리 트리는 하루 단위로 갱신된다.
	// 그보다 자주 받으면 한도만 쓴다.
	tossListTTL     = time.Hour
	tossCategoryTTL = 24 * time.Hour

	// 곧 끝나는 특가는 고르지 않는다. 검수하고 올리는 사이 끝나면
	// 링크를 누른 사람이 특가가 아닌 가격을 보게 된다.
	tossDealMinLeft = 3 * time.Hour

	tossBestSize     = 50
	tossDealSize     = 30 // 하루특가는 30개가 최대다
	tossCategorySize = 30

	// 내 실적 기준으로 고를 때 볼 기간과 카테고리 수.
	tossMineDays       = 30
	tossMineCategories = 3

	// 모델에게 한 번에 보여줄 후보 수.
	tossPromptLimit = 20
)

// 토스 상품을 고를 곳. 카테고리는 "cat:" 뒤에 ID를 붙인다.
const (
	TossSourceBest = "best"
	TossSourceDeal = "deal"
	TossSourceMine = "mine" // 내 링크로 팔린 상품의 카테고리
	tossSourceCat  = "cat:"
)

// tossState는 토큰 발급과 목록 조회를 한 번에 하나만 하게 한다.
// 자동 생성은 세 장을 동시에 만들어서, 막지 않으면 세 번씩 받는다.
// 서버가 잠들면 메모리에서 사라지지만, 그때는 다시 받으면 된다.
type tossState struct {
	mu              sync.Mutex
	lists           map[string]cachedProducts // 목록 종류별로 받아 둔 상품
	categories      []TossCategory
	categoryParents map[int64]int64 // 카테고리 → 상위 카테고리
	categoriesAt    time.Time
	subTags         map[string]bool // 이번에 떠 있는 동안 등록을 확인한 subTag
}

type cachedProducts struct {
	at    time.Time
	items []TossProduct
}

// cachedList는 받아 둔 목록이 ttl 안이면 다시 쓰고, 아니면 fetch로 새로 받는다.
// tossState.mu를 잡은 채로 부른다.
func (a *app) cachedList(ctx context.Context, c *tossConn, key string, ttl time.Duration, fetch func() ([]TossProduct, error)) ([]TossProduct, error) {
	s := &a.tossState
	if hit, ok := s.lists[key]; ok && time.Since(hit.at) < ttl {
		return hit.items, nil
	}
	items, err := fetch()
	if err != nil {
		a.forget(ctx, c, err)
		return nil, err
	}
	if s.lists == nil {
		s.lists = map[string]cachedProducts{}
	}
	s.lists[key] = cachedProducts{at: time.Now(), items: items}
	return items, nil
}

func (a *app) bestList(ctx context.Context, c *tossConn) ([]TossProduct, error) {
	return a.cachedList(ctx, c, TossSourceBest, tossListTTL, func() ([]TossProduct, error) {
		return c.api.BestSelling(ctx, c.token, tossBestSize)
	})
}

func (a *app) categoryList(ctx context.Context, c *tossConn, id int64) ([]TossProduct, error) {
	return a.cachedList(ctx, c, tossSourceCat+strconv.FormatInt(id, 10), tossCategoryTTL, func() ([]TossProduct, error) {
		return c.api.CategoryBest(ctx, c.token, id, tossCategorySize)
	})
}

// tossProducts는 source에 맞는 상품 목록을 돌려준다. 모르는 source는 베스트로 본다.
func (a *app) tossProducts(ctx context.Context, c *tossConn, userID, source string) ([]TossProduct, error) {
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()

	var err error
	var items []TossProduct
	switch {
	case source == TossSourceDeal:
		items, err = a.cachedList(ctx, c, TossSourceDeal, tossListTTL, func() ([]TossProduct, error) {
			return c.api.TodayDeals(ctx, c.token, tossDealSize)
		})
	case source == TossSourceMine:
		items, err = a.mineProducts(ctx, c, userID)
	case strings.HasPrefix(source, tossSourceCat):
		id, perr := strconv.ParseInt(strings.TrimPrefix(source, tossSourceCat), 10, 64)
		if perr != nil {
			items, err = a.bestList(ctx, c)
		} else {
			items, err = a.categoryListOrParent(ctx, c, id)
		}
	default:
		items, err = a.bestList(ctx, c)
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}

// mineProducts는 최근 내 링크로 팔린 상품들의 카테고리에서 잘 팔리는 상품을 모은다.
//
// 실적에는 카테고리가 없어 상품 상세로 알아낸다. 가장 구체적인 카테고리를
// 판매 수량으로 세어 많이 팔린 순으로 몇 개만 본다. 판매가 아직 없으면
// 기준이 없으므로 베스트에서 고른다.
func (a *app) mineProducts(ctx context.Context, c *tossConn, userID string) ([]TossProduct, error) {
	to := time.Now().In(kst)
	perf, err := a.accountPerformance(ctx, c, to.AddDate(0, 0, -(tossMineDays-1)), to)
	if err != nil {
		return nil, err
	}

	sold := map[int64]int64{}
	var ids []int64
	for _, it := range perf.Items {
		if it.SoldQuantity <= 0 {
			continue
		}
		if _, ok := sold[it.ProductID]; !ok && len(ids) < 30 {
			ids = append(ids, it.ProductID)
		}
		sold[it.ProductID] += it.SoldQuantity
	}
	if len(ids) == 0 {
		return a.bestList(ctx, c)
	}

	details, _, err := c.api.ProductDetails(ctx, c.token, ids)
	if err != nil {
		a.forget(ctx, c, err)
		return nil, err
	}
	weight := map[int64]int64{}
	for _, d := range details {
		if n := len(d.CategoryIDs); n > 0 {
			weight[d.CategoryIDs[n-1]] += sold[d.TacaItemID]
		}
	}
	cats := make([]int64, 0, len(weight))
	for id := range weight {
		cats = append(cats, id)
	}
	sort.Slice(cats, func(i, j int) bool {
		if weight[cats[i]] != weight[cats[j]] {
			return weight[cats[i]] > weight[cats[j]]
		}
		return cats[i] < cats[j]
	})
	if len(cats) > tossMineCategories {
		cats = cats[:tossMineCategories]
	}
	if len(cats) == 0 {
		return a.bestList(ctx, c)
	}

	// 카테고리별 목록을 번갈아 섞어 한 카테고리만 앞에 몰리지 않게 한다.
	var lists [][]TossProduct
	for _, id := range cats {
		items, err := a.categoryList(ctx, c, id)
		if err != nil {
			return nil, err
		}
		lists = append(lists, items)
	}
	var mixed []TossProduct
	for i := 0; ; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				mixed = append(mixed, l[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	if len(mixed) == 0 {
		return a.bestList(ctx, c)
	}
	return mixed, nil
}

// categoryNode는 화면에 넘기는 카테고리 트리다. 단계별 선택 칸이 이걸로 그려진다.
type categoryNode struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Children []categoryNode `json:"children,omitempty"`
}

// loadCategories는 카테고리 트리를 하루에 한 번 받아 둔다. 부모 관계도 함께 기억해
// 랭킹이 비어 있는 카테고리에서 한 단계씩 올라갈 수 있게 한다.
// tossState.mu를 잡은 채로 부른다.
func (a *app) loadCategories(ctx context.Context, c *tossConn) error {
	s := &a.tossState
	if s.categories != nil && time.Since(s.categoriesAt) < tossCategoryTTL {
		return nil
	}
	cats, err := c.api.Categories(ctx, c.token)
	if err != nil {
		a.forget(ctx, c, err)
		return err
	}
	parents := map[int64]int64{}
	var walk func(parent int64, list []TossCategory)
	walk = func(parent int64, list []TossCategory) {
		for _, c := range list {
			if parent != 0 {
				parents[c.CategoryID] = parent
			}
			walk(c.CategoryID, c.Children)
		}
	}
	walk(0, cats)
	s.categories, s.categoryParents, s.categoriesAt = cats, parents, time.Now()
	return nil
}

// tossCategoryTree는 화면에 넘길 카테고리 트리다. 토스가 응답하지 않으면 비운다.
func (a *app) tossCategoryTree(ctx context.Context, c *tossConn) []categoryNode {
	if c == nil {
		return nil
	}
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()

	if err := a.loadCategories(ctx, c); err != nil {
		log.Printf("토스 카테고리 조회 실패: %v", err)
		return nil
	}
	var convert func([]TossCategory) []categoryNode
	convert = func(list []TossCategory) []categoryNode {
		out := make([]categoryNode, 0, len(list))
		for _, c := range list {
			out = append(out, categoryNode{ID: c.CategoryID, Name: c.DisplayName, Children: convert(c.Children)})
		}
		return out
	}
	return convert(a.tossState.categories)
}

// categoryListOrParent는 카테고리 베스트를 받고, 랭킹이 비어 있으면 한 단계씩 위로
// 올라가 받는다. 토스 문서에 랭킹 데이터가 없는 카테고리도 있다고 되어 있다.
// 끝까지 비면 베스트를 쓴다. tossState.mu를 잡은 채로 부른다.
func (a *app) categoryListOrParent(ctx context.Context, c *tossConn, id int64) ([]TossProduct, error) {
	if err := a.loadCategories(ctx, c); err != nil {
		// 트리를 못 받으면 올라갈 수 없을 뿐, 고른 카테고리는 그대로 조회한다.
		log.Printf("토스 카테고리 조회 실패: %v", err)
	}
	for seen := 0; id != 0 && seen < 6; seen++ {
		items, err := a.categoryList(ctx, c, id)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			return items, nil
		}
		id = a.tossState.categoryParents[id]
	}
	return a.bestList(ctx, c)
}

// tossCandidates는 고를 만한 상품을 추린다.
//
// 품절, 곧 끝나는 특가, 최근에 다룬 상품, 중복은 뺀다. 초안 여러 장을 한 번에
// 쓰게 하므로 겹치지 않게 고르는 것은 모델에게 맡긴다.
func tossCandidates(products []TossProduct, avoid []string, now time.Time) []TossProduct {
	skip := make(map[string]bool, len(avoid))
	for _, name := range avoid {
		skip[name] = true
	}
	seen := map[int64]bool{}

	var all []TossProduct
	for _, p := range products {
		if p.IsSoldOut || p.TacaItemID == 0 || skip[p.DisplayName] || seen[p.TacaItemID] {
			continue
		}
		if p.EndAt != "" {
			end, err := time.Parse(time.RFC3339, p.EndAt)
			if err != nil || end.Sub(now) < tossDealMinLeft {
				continue
			}
		}
		seen[p.TacaItemID] = true
		all = append(all, p)
	}

	if len(all) > tossPromptLimit {
		all = all[:tossPromptLimit]
	}
	return all
}

// errNoTossCandidates는 고를 상품이 하나도 남지 않은 경우다.
var errNoTossCandidates = errors.New("고를 만한 토스 상품이 없다")

// tossSubTag는 PostMill 사용자(스레드 계정)의 subTag다. 키는 사람마다 자기
// 것을 쓰므로 실적을 나누는 데는 쓰지 않지만, 토스 어드민에서 이 링크가 어디서
// 나갔는지 알아볼 수 있게 붙여 둔다.
// 스레드 사용자 id는 숫자이고 관리자는 "admin"이라 허용 문자 규칙에 맞는다.
func tossSubTag(userID string) string { return "u-" + userID }

// ensureSubTag는 사용자의 subTag를 등록한다. 한 번 등록한 것은 서버가
// 떠 있는 동안 기억해 다시 부르지 않는다. 다시 불러도 해는 없다.
func (a *app) ensureSubTag(ctx context.Context, c *tossConn, userID string) (string, error) {
	tag := tossSubTag(userID)
	a.tossState.mu.Lock()
	done := a.tossState.subTags[tag]
	a.tossState.mu.Unlock()
	if done {
		return tag, nil
	}

	label := ""
	if u, ok, err := a.db.GetThreadsUser(ctx, userID); err == nil && ok && u.Username != "" {
		label = "@" + u.Username
	}
	if err := c.api.EnsureSubTag(ctx, c.token, tag, label); err != nil {
		return "", err
	}

	a.tossState.mu.Lock()
	if a.tossState.subTags == nil {
		a.tossState.subTags = map[string]bool{}
	}
	a.tossState.subTags[tag] = true
	a.tossState.mu.Unlock()
	return tag, nil
}

// suggestToss는 토스 목록에서 서로 다른 상품을 골라 초안 여러 장을 한 번에 쓰게 하고,
// 고른 상품마다 쉐어링크를 발급한다. 장마다 결과나 실패 이유를 돌려준다.
func (a *app) suggestToss(ctx context.Context, userID, source string, avoid []string, specs []draftSpec, showPrice bool) ([]*AutoDraft, []error) {
	drafts := make([]*AutoDraft, len(specs))
	fails := make([]error, len(specs))
	failAll := func(err error) ([]*AutoDraft, []error) {
		for i := range fails {
			fails[i] = err
		}
		return drafts, fails
	}

	c, err := a.tossConnFor(ctx, userID)
	if err != nil {
		return failAll(err)
	}
	products, err := a.tossProducts(ctx, c, userID, source)
	if err != nil {
		return failAll(err)
	}
	candidates := tossCandidates(products, avoid, time.Now())
	if len(candidates) == 0 {
		return failAll(errNoTossCandidates)
	}

	hooks := make([]hookType, len(specs))
	for i, sp := range specs {
		hooks[i] = sp.Hook
	}
	picks, written, err := a.gemini.SuggestFromTossBatch(ctx, candidates, hooks, showPrice)
	if err != nil {
		return failAll(err)
	}

	tag, err := a.ensureSubTag(ctx, c, userID)
	if err != nil {
		a.forget(ctx, c, err)
		return failAll(err)
	}
	for i, d := range written {
		if d == nil || picks[i] < 0 {
			continue
		}
		picked := candidates[picks[i]]
		link, err := c.api.CreateLink(ctx, c.token, picked.TacaItemID, tag)
		if err != nil {
			// 발급이 막힌 상품은 그 장만 실패로 둔다.
			a.forget(ctx, c, err)
			fails[i] = err
			continue
		}
		d.AffiliateLink = link
		d.TacaItemID = picked.TacaItemID
		d.ThumbnailURL = picked.ThumbnailURL
		// 게시 직전에 이 값이 그대로인지 확인한다. 쓰지 않았으면 0이다.
		if showPrice {
			d.ShownPrice = picked.DisplayPrice
			// 하루특가면 본문이 마감을 말할 수 있다. 마감이 지나면 그 말이
			// 거짓이 되므로 시각을 남겨 게시를 멈춘다.
			if t, err := time.Parse(time.RFC3339, picked.EndAt); err == nil {
				d.DealEndsAt = &t
			}
		}
		// 추적이 없는 일반 주소다. 상품을 확인하는 버튼에만 쓰고 게시하지 않는다.
		d.ProductURL = picked.ProductURL
		drafts[i] = d
	}
	return drafts, fails
}

// tossUnavailableReason은 자동으로 고른 토스 상품을 지금 살 수 없으면 그 이유를 돌려준다.
//
// 초안을 만들고 게시하기까지 시간이 지나 그사이 품절되거나 판매가 끝날 수 있다.
// 그런 링크를 올리면 수익도 없고 보는 사람만 헛걸음한다.
// 토스 API가 응답하지 않을 때는 게시를 막지 않는다. 토스 장애로 게시까지
// 멈추면 안 되고, 상품은 대개 그대로 살 수 있다.
func (a *app) tossUnavailableReason(ctx context.Context, p *Post) string {
	// 마감은 저장해 둔 시각만 보면 되므로 토스를 부르지 않아도 안다.
	// 본문이 "오늘까지"라고 말해두고 마감 뒤에 올라가면 거짓이 된다.
	if p.DealEndsAt != nil && time.Now().After(*p.DealEndsAt) {
		return "특가가 끝나서 게시를 멈췄어요. 본문이 마감을 말하고 있어요. 재생성해주세요."
	}
	// 사용자가 링크를 직접 바꿨으면 그 링크가 어느 상품인지 알 수 없다.
	if p.Affiliate != AffiliateToss || !p.LinkAuto || p.TacaItemID == 0 {
		return ""
	}
	// 글을 쓴 사람의 키로 확인한다. 링크도 그 키로 발급됐다.
	c, err := a.tossConnFor(ctx, p.UserID)
	if err != nil {
		log.Printf("게시 전 상품 확인 생략 (id=%d): %v", p.ID, err)
		return ""
	}
	found, notFound, err := c.api.ProductDetails(ctx, c.token, []int64{p.TacaItemID})
	if err != nil {
		a.forget(ctx, c, err)
		log.Printf("게시 전 상품 확인 생략 (id=%d): %v", p.ID, err)
		return ""
	}
	for _, id := range notFound {
		if id == p.TacaItemID {
			return "판매가 끝났거나 지금 살 수 없는 상품이라 게시를 멈췄어요. 재생성해서 다른 상품으로 바꿔주세요."
		}
	}
	for _, st := range found {
		if st.TacaItemID != p.TacaItemID {
			continue
		}
		if st.IsSoldOut {
			return "품절된 상품이라 게시를 멈췄어요. 재생성해서 다른 상품으로 바꿔주세요."
		}
		// 본문에 가격을 쓴 글은 그 가격이 그대로일 때만 올린다. 이미 올라간 글은
		// 고칠 수 없으므로, 틀린 가격이 올라가면 되돌릴 방법이 없다.
		if p.ShownPrice != 0 && st.DisplayPrice != p.ShownPrice {
			return fmt.Sprintf("가격이 %d원에서 %d원으로 바뀌어 게시를 멈췄어요. 본문을 고치거나 재생성해주세요.",
				p.ShownPrice, st.DisplayPrice)
		}
	}
	return ""
}

// draftFailMessage는 자동 초안 실패를 사용자가 알아볼 말로 바꾼다.
func draftFailMessage(err error) string {
	var te *tossError
	switch {
	case errors.Is(err, errNoTossCandidates):
		return "지금 고를 만한 토스 상품이 없어요. 조금 뒤 다시 시도해주세요."
	case errors.As(err, &te) && te.Code == tossAccessDenied:
		return "토스 API가 접근을 거부했어요. 키와 출발지 IP를 확인해주세요."
	case errors.As(err, &te) && te.Code == tossQuotaExceeded:
		return "오늘 토스 API 사용량을 다 썼어요. 자정에 풀려요."
	case errors.Is(err, errDailyQuota):
		// Gemini 무료 한도는 태평양 시간 자정에 초기화된다.
		return "오늘 AI 사용량을 다 썼어요. 한국 시간 오후 4~5시쯤 다시 풀려요."
	default:
		return "초안을 만들지 못했습니다."
	}
}

package main

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// app_state에 저장하는 토스 토큰 키. 토큰은 1년 가까이 유효하고
// 과도한 재발급은 제한되므로, 재시작해도 다시 받지 않게 DB에 둔다.
const (
	stateTossToken     = "toss_access_token"
	stateTossExpiresAt = "toss_token_expires_at"
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

// tossToken은 저장된 토큰을 쓰고, 없거나 곧 만료되면 새로 받는다.
// tossState.mu를 잡은 채로 부른다.
func (a *app) tossToken(ctx context.Context) (string, error) {
	token, ok, err := a.db.GetState(ctx, stateTossToken)
	if err != nil {
		return "", err
	}
	if ok {
		if raw, found, _ := a.db.GetState(ctx, stateTossExpiresAt); found {
			if exp, err := time.Parse(time.RFC3339, raw); err == nil && time.Until(exp) > tossTokenMargin {
				return token, nil
			}
		}
	}

	token, exp, err := a.toss.IssueToken(ctx)
	if err != nil {
		return "", err
	}
	if err := a.db.SetState(ctx, stateTossToken, token); err != nil {
		return "", err
	}
	if err := a.db.SetState(ctx, stateTossExpiresAt, exp.Format(time.RFC3339)); err != nil {
		return "", err
	}
	return token, nil
}

// forgetTossToken은 401을 받은 토큰을 버린다. 다음 시도에서 새로 받는다.
func (a *app) forgetTossToken(ctx context.Context, err error) {
	var te *tossError
	if errors.As(err, &te) && te.HTTPStatus == 401 {
		a.db.SetState(ctx, stateTossExpiresAt, "")
	}
}

// cachedList는 받아 둔 목록이 ttl 안이면 다시 쓰고, 아니면 fetch로 새로 받는다.
// tossState.mu를 잡은 채로 부른다.
func (a *app) cachedList(ctx context.Context, key string, ttl time.Duration, fetch func() ([]TossProduct, error)) ([]TossProduct, error) {
	s := &a.tossState
	if c, ok := s.lists[key]; ok && time.Since(c.at) < ttl {
		return c.items, nil
	}
	items, err := fetch()
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, err
	}
	if s.lists == nil {
		s.lists = map[string]cachedProducts{}
	}
	s.lists[key] = cachedProducts{at: time.Now(), items: items}
	return items, nil
}

func (a *app) bestList(ctx context.Context, token string) ([]TossProduct, error) {
	return a.cachedList(ctx, TossSourceBest, tossListTTL, func() ([]TossProduct, error) {
		return a.toss.BestSelling(ctx, token, tossBestSize)
	})
}

func (a *app) categoryList(ctx context.Context, token string, id int64) ([]TossProduct, error) {
	return a.cachedList(ctx, tossSourceCat+strconv.FormatInt(id, 10), tossCategoryTTL, func() ([]TossProduct, error) {
		return a.toss.CategoryBest(ctx, token, id, tossCategorySize)
	})
}

// tossProducts는 source에 맞는 상품 목록을 돌려준다. 모르는 source는 베스트로 본다.
func (a *app) tossProducts(ctx context.Context, userID, source string) (string, []TossProduct, error) {
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()

	token, err := a.tossToken(ctx)
	if err != nil {
		return "", nil, err
	}

	var items []TossProduct
	switch {
	case source == TossSourceDeal:
		items, err = a.cachedList(ctx, TossSourceDeal, tossListTTL, func() ([]TossProduct, error) {
			return a.toss.TodayDeals(ctx, token, tossDealSize)
		})
	case source == TossSourceMine:
		items, err = a.mineProducts(ctx, token, userID)
	case strings.HasPrefix(source, tossSourceCat):
		id, perr := strconv.ParseInt(strings.TrimPrefix(source, tossSourceCat), 10, 64)
		if perr != nil {
			items, err = a.bestList(ctx, token)
		} else {
			items, err = a.categoryListOrParent(ctx, token, id)
		}
	default:
		items, err = a.bestList(ctx, token)
	}
	if err != nil {
		return "", nil, err
	}
	return token, items, nil
}

// mineProducts는 최근 내 링크로 팔린 상품들의 카테고리에서 잘 팔리는 상품을 모은다.
//
// 실적에는 카테고리가 없어 상품 상세로 알아낸다. 가장 구체적인 카테고리를
// 판매 수량으로 세어 많이 팔린 순으로 몇 개만 본다. 판매가 아직 없으면
// 기준이 없으므로 베스트에서 고른다.
func (a *app) mineProducts(ctx context.Context, token, userID string) ([]TossProduct, error) {
	to := time.Now().In(kst)
	perf, err := a.accountPerformance(ctx, token, userID, to.AddDate(0, 0, -(tossMineDays-1)), to)
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
		return a.bestList(ctx, token)
	}

	details, _, err := a.toss.ProductDetails(ctx, token, ids)
	if err != nil {
		a.forgetTossToken(ctx, err)
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
		return a.bestList(ctx, token)
	}

	// 카테고리별 목록을 번갈아 섞어 한 카테고리만 앞에 몰리지 않게 한다.
	var lists [][]TossProduct
	for _, id := range cats {
		items, err := a.categoryList(ctx, token, id)
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
		return a.bestList(ctx, token)
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
func (a *app) loadCategories(ctx context.Context, token string) error {
	s := &a.tossState
	if s.categories != nil && time.Since(s.categoriesAt) < tossCategoryTTL {
		return nil
	}
	cats, err := a.toss.Categories(ctx, token)
	if err != nil {
		a.forgetTossToken(ctx, err)
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
func (a *app) tossCategoryTree(ctx context.Context) []categoryNode {
	if a.toss == nil {
		return nil
	}
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()

	token, err := a.tossToken(ctx)
	if err == nil {
		err = a.loadCategories(ctx, token)
	}
	if err != nil {
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
func (a *app) categoryListOrParent(ctx context.Context, token string, id int64) ([]TossProduct, error) {
	if err := a.loadCategories(ctx, token); err != nil {
		// 트리를 못 받으면 올라갈 수 없을 뿐, 고른 카테고리는 그대로 조회한다.
		log.Printf("토스 카테고리 조회 실패: %v", err)
	}
	for seen := 0; id != 0 && seen < 6; seen++ {
		items, err := a.categoryList(ctx, token, id)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			return items, nil
		}
		id = a.tossState.categoryParents[id]
	}
	return a.bestList(ctx, token)
}

// tossCandidates는 고를 만한 상품을 추린다.
//
// 품절, 곧 끝나는 특가, 최근에 다룬 상품, 중복은 뺀다.
// slot이 0 이상이면 slots개로 나눈 몫 중 하나만 준다. 동시에 만드는 초안이
// 서로 뭘 골랐는지 모르므로, 후보를 겹치지 않게 나눠 같은 상품을 피한다.
func tossCandidates(products []TossProduct, avoid []string, now time.Time, slot, slots int) []TossProduct {
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

	var out []TossProduct
	for i, p := range all {
		if slot >= 0 && slots > 0 && i%slots != slot {
			continue
		}
		out = append(out, p)
		if len(out) == tossPromptLimit {
			break
		}
	}
	return out
}

// errNoTossCandidates는 고를 상품이 하나도 남지 않은 경우다.
var errNoTossCandidates = errors.New("고를 만한 토스 상품이 없다")

// tossSubTag는 PostMill 사용자(스레드 계정)의 subTag다. 토스 키는 사업자당
// 하나라 여러 명이 쓰면 실적이 섞이므로, 링크를 계정별 subTag로 발급해 나눈다.
// 스레드 사용자 id는 숫자이고 관리자는 "admin"이라 허용 문자 규칙에 맞는다.
func tossSubTag(userID string) string { return "u-" + userID }

// ensureSubTag는 사용자의 subTag를 등록한다. 한 번 등록한 것은 서버가
// 떠 있는 동안 기억해 다시 부르지 않는다. 다시 불러도 해는 없다.
func (a *app) ensureSubTag(ctx context.Context, token, userID string) (string, error) {
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
	if err := a.toss.EnsureSubTag(ctx, token, tag, label); err != nil {
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

// suggestToss는 토스 목록에서 상품을 고르게 하고, 그 상품의 쉐어링크를 발급한다.
func (a *app) suggestToss(ctx context.Context, userID, source string, avoid []string, slot int, hook hookType) (*AutoDraft, error) {
	token, products, err := a.tossProducts(ctx, userID, source)
	if err != nil {
		return nil, err
	}
	candidates := tossCandidates(products, avoid, time.Now(), slot, autoBatchSize)
	if len(candidates) == 0 {
		return nil, errNoTossCandidates
	}

	i, d, err := a.gemini.SuggestFromToss(ctx, candidates, hook)
	if err != nil {
		return nil, err
	}
	picked := candidates[i]

	tag, err := a.ensureSubTag(ctx, token, userID)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, err
	}
	link, err := a.toss.CreateLink(ctx, token, picked.TacaItemID, tag)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, err
	}
	d.AffiliateLink = link
	d.TacaItemID = picked.TacaItemID
	// 추적이 없는 일반 주소다. 상품을 확인하는 버튼에만 쓰고 게시하지 않는다.
	d.ProductURL = picked.ProductURL
	return d, nil
}

// lockedTossToken은 목록 조회와 겹치지 않게 토큰만 받는다.
func (a *app) lockedTossToken(ctx context.Context) (string, error) {
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()
	return a.tossToken(ctx)
}

// tossUnavailableReason은 자동으로 고른 토스 상품을 지금 살 수 없으면 그 이유를 돌려준다.
//
// 초안을 만들고 게시하기까지 시간이 지나 그사이 품절되거나 판매가 끝날 수 있다.
// 그런 링크를 올리면 수익도 없고 보는 사람만 헛걸음한다.
// 토스 API가 응답하지 않을 때는 게시를 막지 않는다. 토스 장애로 게시까지
// 멈추면 안 되고, 상품은 대개 그대로 살 수 있다.
func (a *app) tossUnavailableReason(ctx context.Context, p *Post) string {
	// 사용자가 링크를 직접 바꿨으면 그 링크가 어느 상품인지 알 수 없다.
	if a.toss == nil || p.Affiliate != AffiliateToss || !p.LinkAuto || p.TacaItemID == 0 {
		return ""
	}
	token, err := a.lockedTossToken(ctx)
	if err != nil {
		log.Printf("게시 전 상품 확인 생략 (id=%d): %v", p.ID, err)
		return ""
	}
	found, notFound, err := a.toss.ProductDetails(ctx, token, []int64{p.TacaItemID})
	if err != nil {
		a.forgetTossToken(ctx, err)
		log.Printf("게시 전 상품 확인 생략 (id=%d): %v", p.ID, err)
		return ""
	}
	for _, id := range notFound {
		if id == p.TacaItemID {
			return "판매가 끝났거나 지금 살 수 없는 상품이라 게시를 멈췄어요. 재생성해서 다른 상품으로 바꿔주세요."
		}
	}
	for _, st := range found {
		if st.TacaItemID == p.TacaItemID && st.IsSoldOut {
			return "품절된 상품이라 게시를 멈췄어요. 재생성해서 다른 상품으로 바꿔주세요."
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
	default:
		return "초안을 만들지 못했습니다."
	}
}

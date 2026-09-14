package main

import (
	"context"
	"errors"
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

	// 베스트 랭킹은 1시간 단위로 갱신된다. 그보다 자주 받으면 한도만 쓴다.
	tossListTTL = time.Hour

	// 곧 끝나는 특가는 고르지 않는다. 검수하고 올리는 사이 끝나면
	// 링크를 누른 사람이 특가가 아닌 가격을 보게 된다.
	tossDealMinLeft = 3 * time.Hour

	tossBestSize = 50
	tossDealSize = 30 // 하루특가는 30개가 최대다

	// 모델에게 한 번에 보여줄 후보 수.
	tossPromptLimit = 20
)

// tossState는 토큰 발급과 목록 조회를 한 번에 하나만 하게 한다.
// 자동 생성은 세 장을 동시에 만들어서, 막지 않으면 세 번씩 받는다.
type tossState struct {
	mu        sync.Mutex
	fetchedAt time.Time
	best      []TossProduct
	deals     []TossProduct
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

// tossLists는 베스트와 하루특가 목록을 돌려준다. 한 시간 안에 받은 것은 다시 쓴다.
// 서버가 잠들면 메모리에서 사라지지만, 그때는 다시 받으면 된다.
func (a *app) tossLists(ctx context.Context) (token string, best, deals []TossProduct, err error) {
	s := &a.tossState
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err = a.tossToken(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	if time.Since(s.fetchedAt) < tossListTTL {
		return token, s.best, s.deals, nil
	}

	best, err = a.toss.BestSelling(ctx, token, tossBestSize)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return "", nil, nil, err
	}
	// 특가는 없어도 베스트만으로 고를 수 있다.
	deals, err = a.toss.TodayDeals(ctx, token, tossDealSize)
	if err != nil {
		a.forgetTossToken(ctx, err)
		deals = nil
	}

	s.best, s.deals, s.fetchedAt = best, deals, time.Now()
	return token, best, deals, nil
}

// tossCandidates는 고를 만한 상품을 추린다.
//
// 품절, 곧 끝나는 특가, 최근에 다룬 상품은 뺀다. 특가를 앞에 둔다.
// slot이 0 이상이면 slots개로 나눈 몫 중 하나만 준다. 동시에 만드는 초안이
// 서로 뭘 골랐는지 모르므로, 후보를 겹치지 않게 나눠 같은 상품을 피한다.
func tossCandidates(best, deals []TossProduct, avoid []string, now time.Time, slot, slots int) []TossProduct {
	skip := make(map[string]bool, len(avoid))
	for _, name := range avoid {
		skip[name] = true
	}
	seen := map[int64]bool{}

	var all []TossProduct
	add := func(p TossProduct) {
		if p.IsSoldOut || p.TacaItemID == 0 || skip[p.DisplayName] || seen[p.TacaItemID] {
			return
		}
		seen[p.TacaItemID] = true
		all = append(all, p)
	}
	for _, p := range deals {
		end, err := time.Parse(time.RFC3339, p.EndAt)
		if err != nil || end.Sub(now) < tossDealMinLeft {
			continue
		}
		add(p)
	}
	for _, p := range best {
		p.EndAt = ""
		add(p)
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

// suggestToss는 토스 목록에서 상품을 고르게 하고, 그 상품의 쉐어링크를 발급한다.
func (a *app) suggestToss(ctx context.Context, avoid []string, slot int) (*AutoDraft, error) {
	token, best, deals, err := a.tossLists(ctx)
	if err != nil {
		return nil, err
	}
	candidates := tossCandidates(best, deals, avoid, time.Now(), slot, autoBatchSize)
	if len(candidates) == 0 {
		return nil, errNoTossCandidates
	}

	i, d, err := a.gemini.SuggestFromToss(ctx, candidates)
	if err != nil {
		return nil, err
	}
	picked := candidates[i]

	link, err := a.toss.CreateLink(ctx, token, picked.TacaItemID)
	if err != nil {
		a.forgetTossToken(ctx, err)
		return nil, err
	}
	d.AffiliateLink = link
	// 추적이 없는 일반 주소다. 상품을 확인하는 버튼에만 쓰고 게시하지 않는다.
	d.ProductURL = picked.ProductURL
	return d, nil
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

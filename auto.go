package main

import (
	"context"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// autoBatchSize는 자동 생성 버튼 한 번에 만드는 초안 수다.
// 하루 3~5건 발행하는 도구라 이 정도면 고를 거리가 충분하다.
const autoBatchSize = 3

// coupangArea는 쿠팡 AI 초안의 분야다. 쿠팡은 API가 없어 AI가 이 분야 안에서
// 상품 종류를 제안한다.
type coupangArea struct {
	Key   string
	Label string
	Hint  string // 모델에게 주는 말
}

var coupangAreas = []coupangArea{
	{"kitchen", "주방", "주방이나 요리와 관련된 것"},
	{"clean", "청소·수납", "청소나 정리수납과 관련된 것"},
	{"bath", "욕실·세탁", "욕실, 세탁, 또는 잠자리와 관련된 것"},
	{"out", "외출", "외출이나 이동할 때 쓰는 것"},
	{"desk", "책상 주변", "책상이나 전자기기 주변에서 쓰는 것"},
}

func isTossSource(pick string) bool {
	return pick == TossSourceBest || pick == TossSourceDeal || pick == TossSourceMine ||
		strings.HasPrefix(pick, tossSourceCat)
}

// defaultPick은 고를 곳을 따로 정하지 않았을 때 쓴다 (한 장 더, 재생성, 다시 시도).
// 토스는 베스트, 쿠팡은 아무 분야나.
func defaultPick(affiliate string) string {
	if affiliate == AffiliateToss {
		return TossSourceBest
	}
	return coupangAreas[rand.IntN(len(coupangAreas))].Hint
}

// handleAuto는 AI가 상품 선정부터 본문까지 만든 초안을 여러 개 만든다.
// 만들자마자 목록으로 보내고, 생성은 백그라운드에서 돈다.
func (a *app) handleAuto(w http.ResponseWriter, r *http.Request) {
	userID := a.session.userID(r)
	affiliate := r.FormValue("affiliate")
	if _, err := disclosureFor(affiliate); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	avoid := a.recentProducts(r.Context(), userID)

	// 기존 자동 초안을 버리고 새로 채운다. 버튼을 누를 때마다 세 장이
	// 통째로 바뀌는 것이 이 버튼의 의미다.
	// 링크를 이미 넣어둔 초안은 사용자가 손을 댄 것이므로 남긴다.
	if err := a.db.ClearAutoDrafts(r.Context(), userID); err != nil {
		log.Printf("기존 자동 초안 정리 실패: %v", err)
	}

	// 쿠팡은 분야를 고르지 않았으면 무작위 지점부터 돌려써서 세 장이 서로 다른
	// 분야가 되게 한다. 토스는 고른 목록(베스트·특가·내 실적·카테고리)에서 고른다.
	start := rand.IntN(len(coupangAreas))
	// 세 장이 서로 다른 훅으로 시작하게 한다.
	hookStart := rand.IntN(len(hookTypes))
	area := r.FormValue("area")
	source := r.FormValue("source")
	if source == "cat" {
		source = r.FormValue("category")
	}

	for i := 0; i < autoBatchSize; i++ {
		id, err := a.db.CreateDraft(r.Context(), userID, affiliate, "", "", "")
		if err != nil {
			log.Printf("자동 초안 생성 실패: %v", err)
			break
		}
		pick := source
		if affiliate != AffiliateToss || a.toss == nil {
			pick = coupangAreas[(start+i)%len(coupangAreas)].Hint
			for _, ar := range coupangAreas {
				if ar.Key == area {
					pick = ar.Hint
				}
			}
		}
		go a.suggest(id, userID, affiliate, avoid, pick, i, hookTypes[(hookStart+i)%len(hookTypes)])
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleAutoOne은 카드 한 장만 더 채운다.
// 보류나 게시로 세 장이 안 될 때 쓴다.
func (a *app) handleAutoOne(w http.ResponseWriter, r *http.Request) {
	userID := a.session.userID(r)
	affiliate := r.FormValue("affiliate")
	if _, err := disclosureFor(affiliate); err != nil {
		affiliate = AffiliateCoupang
	}

	id, err := a.db.CreateDraft(r.Context(), userID, affiliate, "", "", "")
	if err != nil {
		log.Printf("초안 추가 실패: %v", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	go a.suggest(id, userID, affiliate, a.recentProducts(r.Context(), userID), defaultPick(affiliate), -1, randomHook())

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleRefresh는 카드 하나를 버리고 새로 만든다.
func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	// 직접 고른 상품은 그대로 두고 글만 새로 쓴다.
	if p.IsManual() {
		a.handleRegenerate(w, r)
		return
	}
	if err := a.db.ResetForRegenerate(r.Context(), p.UserID, p.ID); err != nil {
		log.Printf("새로고침 준비 실패 (id=%d): %v", p.ID, err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// 새 상품으로 바뀌므로 붙여둔 사진은 맞지 않는다.
	if err := a.db.DeleteImages(r.Context(), p.UserID, p.ID); err != nil {
		log.Printf("재생성 사진 삭제 실패 (id=%d): %v", p.ID, err)
	}
	go a.suggest(p.ID, p.UserID, p.Affiliate, a.recentProducts(r.Context(), p.UserID), defaultPick(p.Affiliate), -1, randomHook())

	http.Redirect(w, r, backTo(r), http.StatusSeeOther)
}

// recentProducts는 최근에 다룬 상품 이름을 모은다.
// 같은 상품을 계속 제안하지 않게 하려는 것이다.
func (a *app) recentProducts(ctx context.Context, userID string) []string {
	rows, err := a.db.pool.Query(ctx,
		`SELECT product_name FROM posts
		 WHERE user_id = $1 AND product_name <> '' ORDER BY id DESC LIMIT 20`, userID)
	if err != nil {
		log.Printf("최근 상품 조회 실패: %v", err)
		return nil
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// suggest는 요청과 무관하게 도는 백그라운드 작업이다.
//
// 토스는 API가 연결돼 있으면 pick(베스트·특가·내 실적·카테고리)의 실제 상품에서
// 고르고 쉐어링크까지 발급한다. 쿠팡은 pick이 모델에게 줄 분야다.
// slot은 동시에 만드는 초안끼리 후보를 나누는 번호다 (-1이면 전부).
func (a *app) suggest(id int64, userID, affiliate string, avoid []string, pick string, slot int, hook hookType) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var d *AutoDraft
	var err error
	if affiliate == AffiliateToss && a.toss != nil {
		d, err = a.suggestToss(ctx, userID, pick, avoid, slot, hook)
	} else {
		if isTossSource(pick) {
			pick = defaultPick(AffiliateCoupang) // 토스 API가 없으면 분야 힌트로 바꾼다
		}
		d, err = a.gemini.SuggestDraft(ctx, affiliate, avoid, pick, hook)
	}
	if err != nil {
		log.Printf("자동 초안 실패 (id=%d): %v", id, err)
		if dbErr := a.db.SetGenerateError(ctx, id, draftFailMessage(err)); dbErr != nil {
			log.Printf("실패 기록도 실패 (id=%d): %v", id, dbErr)
		}
		return
	}

	if err := a.db.SetAutoGenerated(ctx, id, *d); err != nil {
		log.Printf("자동 초안 저장 실패 (id=%d): %v", id, err)
	}
}

// handleSaveLink는 상세 화면에서 입력한 제휴 링크를 저장한다.
func (a *app) handleSaveLink(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	link := r.FormValue("affiliate_link")
	if err := a.db.UpdateLink(r.Context(), p.UserID, p.ID, link); err != nil {
		log.Printf("링크 저장 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "링크를 저장하지 못했습니다.")
		return
	}
	http.Redirect(w, r, "/drafts/"+strconv.FormatInt(p.ID, 10), http.StatusSeeOther)
}

// backTo는 작업을 마친 뒤 돌아갈 곳이다. 목록에서 눌렀으면 목록으로 돌아간다.
func backTo(r *http.Request) string {
	if to := r.FormValue("back"); to == "list" {
		return "/"
	}
	return "/"
}

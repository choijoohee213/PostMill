package main

import (
	"context"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kst는 토스 실적의 날짜 기준이다. Render 컨테이너에 tzdata가 없을 수 있어
// LoadLocation 대신 고정 시차를 쓴다.
var kst = time.FixedZone("KST", 9*3600)

type statsData struct {
	Enabled bool
	Error   string

	Month     string // YYYY-MM
	PrevMonth string
	NextMonth string // 이번 달이면 비어 있다
	Range     string // 화면에 보여줄 조회 기간

	Perf        *Performance
	Settled     int64
	Rows        []statsRow
	LastUpdated string // 토스가 마지막으로 집계한 시각

	Posted      int // 이 달에 게시한 토스 글 수
	PostedTotal int // 지금까지 게시한 토스 글 수
}

// statsRow는 상품 하나의 실적이다. 직접·간접 기여 행을 상품 단위로 합친다.
type statsRow struct {
	ProductID int64
	Name      string
	Sold      int64
	Refunded  int64
	Expected  int64
	Indirect  bool   // 링크를 타고 와서 다른 상품을 산 몫이 섞였는지
	PostID    int64  // 이 상품으로 쓴 내 글. 없으면 0
	Permalink string // 그 글의 Threads 주소
}

// handleStats는 내 스레드 계정의 토스 쉐어링크 실적을 월 단위로 보여준다.
func (a *app) handleStats(w http.ResponseWriter, r *http.Request) {
	data := statsData{Enabled: a.toss != nil}
	if !data.Enabled {
		a.render(w, "stats.html", data)
		return
	}

	now := time.Now().In(kst)
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, kst)
	month := thisMonth
	if m, err := time.ParseInLocation("2006-01", r.URL.Query().Get("month"), kst); err == nil && !m.After(thisMonth) {
		month = m
	}
	from := month
	to := month.AddDate(0, 1, -1)
	if to.After(now) {
		to = now
	}
	data.Month = month.Format("2006-01")
	data.PrevMonth = month.AddDate(0, -1, 0).Format("2006-01")
	if month.Before(thisMonth) {
		data.NextMonth = month.AddDate(0, 1, 0).Format("2006-01")
	}
	data.Range = from.Format("1월 2일") + " ~ " + to.Format("1월 2일")

	userID := a.session.userID(r)
	// 게시 수는 PostMill 기록이라 토스가 응답하지 않아도 보여준다.
	var err error
	data.Posted, data.PostedTotal, err = a.db.CountPublishedToss(r.Context(), userID, from, month.AddDate(0, 1, 0))
	if err != nil {
		log.Printf("게시 수 조회 실패: %v", err)
	}

	token, err := a.lockedTossToken(r.Context())
	if err == nil {
		data.Perf, data.Settled, err = a.accountStats(r.Context(), token, userID, from, to, data.Month)
	}
	if err != nil {
		a.forgetTossToken(r.Context(), err)
		log.Printf("토스 실적 조회 실패: %v", err)
		data.Perf = nil
		data.Error = statsFailMessage(err)
		a.render(w, "stats.html", data)
		return
	}

	if t, err := time.ParseInLocation("2006-01-02T15:04:05", data.Perf.Summary.LastUpdatedAt, kst); err == nil {
		data.LastUpdated = t.Format("1월 2일 15:04")
	}
	data.Rows = mergeStatsRows(data.Perf.Items)
	a.attachPosts(r.Context(), userID, data.Rows)
	a.render(w, "stats.html", data)
}

// stateTossOwner는 계정 구분(subTag) 없이 발급된 링크의 실적을 가져가는 계정이다.
// subTag를 붙이기 전에는 한 계정만 토스를 썼으므로 그 계정의 몫으로 본다.
const stateTossOwner = "toss_untagged_owner"

// tossOwner는 처음 토스 글을 만든 계정을 주인으로 정하고 기억한다.
// 기억해 두지 않으면 옛 글을 지웠을 때 주인이 바뀔 수 있다.
func (a *app) tossOwner(ctx context.Context) (string, error) {
	if owner, ok, err := a.db.GetState(ctx, stateTossOwner); err != nil || (ok && owner != "") {
		return owner, err
	}
	owner, err := a.db.FirstTossAuthor(ctx)
	if err != nil || owner == "" {
		return owner, err
	}
	return owner, a.db.SetState(ctx, stateTossOwner, owner)
}

// accountStats는 한 계정의 실적과 정산 확정 수익금을 구한다.
//
// 보통은 그 계정 subTag의 실적이다. 주인 계정은 subTag 없이 발급한 옛 링크까지
// 제 몫이므로, 거래처 전체에서 다른 계정 subTag의 실적을 뺀 값을 쓴다.
// 등록되지 않은 subTag로 조회하면 토스가 거절하므로 등록된 것만 조회한다.
func (a *app) accountStats(ctx context.Context, token, userID string, from, to time.Time, month string) (*Performance, int64, error) {
	tags, err := a.toss.ListSubTags(ctx, token)
	if err != nil {
		return nil, 0, err
	}
	owner, err := a.tossOwner(ctx)
	if err != nil {
		return nil, 0, err
	}
	mine := tossSubTag(userID)

	if userID != owner {
		if !slices.Contains(tags, mine) {
			return &Performance{}, 0, nil
		}
		perf, err := a.toss.Performance(ctx, token, from, to, mine)
		if err != nil {
			return nil, 0, err
		}
		settled, err := a.toss.SettledCommission(ctx, token, month, mine)
		return perf, settled, err
	}

	perf, err := a.toss.Performance(ctx, token, from, to, "")
	if err != nil {
		return nil, 0, err
	}
	settled, err := a.toss.SettledCommission(ctx, token, month, "")
	if err != nil {
		return nil, 0, err
	}
	for _, tag := range tags {
		if tag == mine {
			continue
		}
		other, err := a.toss.Performance(ctx, token, from, to, tag)
		if err != nil {
			return nil, 0, err
		}
		otherSettled, err := a.toss.SettledCommission(ctx, token, month, tag)
		if err != nil {
			return nil, 0, err
		}
		subtractPerf(perf, other)
		settled -= otherSettled
	}
	return perf, settled, nil
}

// subtractPerf는 total에서 other 몫을 뺀다. 상품 행은 상품·기여 방식이 같은 것끼리 빼고,
// 남는 게 없는 행은 지운다.
func subtractPerf(total, other *Performance) {
	s, o := &total.Summary, other.Summary
	s.ClickCount -= o.ClickCount
	s.SoldQuantity -= o.SoldQuantity
	s.RefundedQuantity -= o.RefundedQuantity
	s.NetPaymentAmount -= o.NetPaymentAmount
	s.ExpectedCommissionAmount -= o.ExpectedCommissionAmount
	s.ConfirmedCommissionAmount -= o.ConfirmedCommissionAmount

	type key struct {
		id   int64
		attr string
	}
	minus := map[key]PerformanceItem{}
	for _, it := range other.Items {
		k := key{it.ProductID, it.Attribution}
		m := minus[k]
		m.SoldQuantity += it.SoldQuantity
		m.RefundedQuantity += it.RefundedQuantity
		m.ExpectedCommissionAmount += it.ExpectedCommissionAmount
		minus[k] = m
	}
	kept := total.Items[:0]
	for _, it := range total.Items {
		m := minus[key{it.ProductID, it.Attribution}]
		it.SoldQuantity -= m.SoldQuantity
		it.RefundedQuantity -= m.RefundedQuantity
		it.ExpectedCommissionAmount -= m.ExpectedCommissionAmount
		if it.SoldQuantity > 0 || it.RefundedQuantity > 0 || it.ExpectedCommissionAmount != 0 {
			kept = append(kept, it)
		}
	}
	total.Items = kept
}

// mergeStatsRows는 같은 상품의 직접·간접 기여 행을 합치고 예상 수익금 순으로 둔다.
func mergeStatsRows(items []PerformanceItem) []statsRow {
	byID := map[int64]*statsRow{}
	var order []int64
	for _, it := range items {
		row, ok := byID[it.ProductID]
		if !ok {
			row = &statsRow{ProductID: it.ProductID, Name: it.ProductName}
			byID[it.ProductID] = row
			order = append(order, it.ProductID)
		}
		if row.Name == "" {
			row.Name = it.ProductName
		}
		row.Sold += it.SoldQuantity
		row.Refunded += it.RefundedQuantity
		row.Expected += it.ExpectedCommissionAmount
		if it.Attribution != "DIRECT" {
			row.Indirect = true
		}
	}
	rows := make([]statsRow, 0, len(order))
	for _, id := range order {
		rows = append(rows, *byID[id])
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Expected > rows[j].Expected })
	return rows
}

// attachPosts는 실적 상품을 내가 쓴 글과 잇는다. 실패해도 실적은 그대로 보여준다.
func (a *app) attachPosts(ctx context.Context, userID string, rows []statsRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ProductID
	}
	posts, err := a.db.PostsByTacaItems(ctx, userID, ids)
	if err != nil {
		log.Printf("실적 글 연결 실패: %v", err)
		return
	}
	for i := range rows {
		if p, ok := posts[rows[i].ProductID]; ok {
			rows[i].PostID = p.ID
			rows[i].Permalink = p.ThreadPermalink
		}
	}
}

func statsFailMessage(err error) string {
	msg := draftFailMessage(err)
	if msg == "초안을 만들지 못했습니다." {
		return "토스 실적을 불러오지 못했어요. 잠시 뒤 다시 열어주세요."
	}
	return msg
}

// won은 금액에 천 단위 쉼표를 넣는다.
func won(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

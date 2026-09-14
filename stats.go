package main

import (
	"context"
	"log"
	"net/http"
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
	All       bool   // 계정 전체 합산으로 보는지
	Range     string // 화면에 보여줄 조회 기간

	Perf        *Performance
	Settled     int64
	Rows        []statsRow
	LastUpdated string // 토스가 마지막으로 집계한 시각
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

// handleStats는 토스 쉐어링크 실적을 월 단위로 보여준다.
//
// 기본은 내 계정(subTag) 실적이다. subTag를 붙이기 전에 발급한 링크는
// 계정별로 잡히지 않으므로 전체 합산으로도 볼 수 있게 한다.
func (a *app) handleStats(w http.ResponseWriter, r *http.Request) {
	data := statsData{Enabled: a.toss != nil, All: r.URL.Query().Get("scope") == "all"}
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
	subTag := tossSubTag(userID)
	if data.All {
		subTag = ""
	}

	token, err := a.lockedTossToken(r.Context())
	if err == nil {
		data.Perf, err = a.toss.Performance(r.Context(), token, from, to, subTag)
	}
	if err == nil {
		data.Settled, err = a.toss.SettledCommission(r.Context(), token, data.Month, subTag)
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

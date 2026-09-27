package main

import (
	"html/template"
	"strings"
	"testing"
	"time"
)

// 검수대기는 올라갈 모양을 그대로 보여주고, 게시완료는 짧게 훑을 수 있어야 한다.
// 둘이 뒤바뀌어도 화면은 멀쩡해 보이므로 여기서 잡는다.
func TestList_탭마다_펼침이_다르다(t *testing.T) {
	tpl := template.Must(template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html"))

	render := func(tab, status string) string {
		t.Helper()
		p := &Post{ID: 7, Affiliate: AffiliateToss, ProductName: "무선 청소기",
			Body: "첫 줄\n둘째 줄\n셋째 줄\n넷째 줄", Topic: "자취",
			Status: status, AffiliateLink: "https://toss.shopping/t/1", CreatedAt: time.Now()}
		var sb strings.Builder
		data := listData{Tabs: tabs, ActiveTab: tab, Affiliates: affiliateOptions,
			Posts: []*Post{p}, BatchSize: autoBatchSize,
			Images: map[int64][]PostImage{7: {{ID: 1, Token: "tok1"}}}}
		if err := tpl.ExecuteTemplate(&sb, "list.html", data); err != nil {
			t.Fatalf("%s 렌더 실패: %v", tab, err)
		}
		return sb.String()
	}

	review := render("review", StatusPending)
	for _, want := range []string{"넷째 줄", disclosureToss, "/media/tok1", "› 자취"} {
		if !strings.Contains(review, want) {
			t.Errorf("검수대기에 %q가 없다", want)
		}
	}

	published := render("published", StatusPublished)
	if !strings.Contains(published, "첫 줄") {
		t.Error("게시완료에 본문 미리보기가 없다")
	}
	for _, unwanted := range []string{"넷째 줄", disclosureToss, "/media/tok1"} {
		if strings.Contains(published, unwanted) {
			t.Errorf("게시완료가 %q까지 펼쳤다", unwanted)
		}
	}
}

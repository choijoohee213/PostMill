package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestSplitParts_세_부분으로_나눈다(t *testing.T) {
	parts := splitParts("상품 이름\n---\n본문 줄1\n본문 줄2\n---\n디테일", 3)
	if len(parts) != 3 {
		t.Fatalf("%d개: %q", len(parts), parts)
	}
	if parts[0] != "상품 이름" || parts[1] != "본문 줄1\n본문 줄2" || parts[2] != "디테일" {
		t.Fatalf("%q", parts)
	}
}

func TestSplitParts_구분자가_더_많아도_n개까지만(t *testing.T) {
	// 디테일 안에 --- 가 들어가도 잘리면 안 된다.
	parts := splitParts("상품\n---\n본문\n---\n디테일1\n---\n디테일2", 3)
	if len(parts) != 3 {
		t.Fatalf("%d개", len(parts))
	}
	if parts[2] != "디테일1\n---\n디테일2" {
		t.Fatalf("디테일이 잘렸다: %q", parts[2])
	}
}

func TestSplitParts_구분자가_모자라면_조각이_적다(t *testing.T) {
	if parts := splitParts("본문만", 3); len(parts) != 1 {
		t.Fatalf("%d개: %q", len(parts), parts)
	}
}

func TestFirstLine_빈_줄을_건너뛴다(t *testing.T) {
	if got := firstLine("\n\n  실리콘 주방장갑  \n다음 줄"); got != "실리콘 주방장갑" {
		t.Fatalf("got=%q", got)
	}
	if got := firstLine("\n \n"); got != "" {
		t.Fatalf("got=%q", got)
	}
}

func TestSearchURL_쿠팡은_검색_주소를_만든다(t *testing.T) {
	got := SearchURL(AffiliateCoupang, "접이식 장바구니 카트")
	if !strings.HasPrefix(got, "https://www.coupang.com/np/search?q=") {
		t.Fatalf("got=%q", got)
	}
	// 상품 이름이 그대로 검색어로 들어가야 한다.
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query().Get("q"); q != "접이식 장바구니 카트" {
		t.Fatalf("q=%q", q)
	}
}

func TestSearchURL_토스는_비운다(t *testing.T) {
	// 쉐어링크는 앱에서만 만들 수 있어 웹 검색 주소가 쓸모가 적다.
	if got := SearchURL(AffiliateToss, "무선 이어폰"); got != "" {
		t.Fatalf("got=%q", got)
	}
}

func TestSearchURL_이름이_비면_비운다(t *testing.T) {
	if got := SearchURL(AffiliateCoupang, "   "); got != "" {
		t.Fatalf("got=%q", got)
	}
}

package main

import (
	"strings"
	"testing"
)

func TestCompose_문구가_맨_앞에_온다(t *testing.T) {
	got, err := Compose(AffiliateCoupang, "본문입니다", "https://link.example/a")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if !strings.HasPrefix(got, disclosureCoupang) {
		t.Fatalf("문구가 맨 앞에 없음:\n%s", got)
	}
	want := disclosureCoupang + "\n\n본문입니다\nhttps://link.example/a"
	if got != want {
		t.Fatalf("조립 결과가 다름:\n got=%q\nwant=%q", got, want)
	}
}

func TestCompose_제휴사별_문구(t *testing.T) {
	tests := []struct {
		affiliate string
		want      string
	}{
		{AffiliateCoupang, disclosureCoupang},
		{AffiliateToss, disclosureToss},
	}
	for _, tt := range tests {
		got, err := Compose(tt.affiliate, "본문", "https://l")
		if err != nil {
			t.Fatalf("%s: %v", tt.affiliate, err)
		}
		if !strings.HasPrefix(got, tt.want) {
			t.Errorf("%s: 문구가 다름\n%s", tt.affiliate, got)
		}
	}
}

func TestCompose_알_수_없는_제휴사는_거부(t *testing.T) {
	if _, err := Compose("naver", "본문", "https://l"); err == nil {
		t.Fatal("알 수 없는 제휴사인데 에러가 없음")
	}
}

func TestCompose_본문이나_링크가_비면_거부(t *testing.T) {
	tests := []struct {
		name string
		body string
		link string
	}{
		{"본문 없음", "", "https://l"},
		{"본문이 공백뿐", "   \n\t ", "https://l"},
		{"링크 없음", "본문", ""},
		{"링크가 공백뿐", "본문", "  "},
	}
	for _, tt := range tests {
		if _, err := Compose(AffiliateCoupang, tt.body, tt.link); err == nil {
			t.Errorf("%s: 에러가 없음", tt.name)
		}
	}
}

func TestCompose_본문_앞뒤_공백은_제거한다(t *testing.T) {
	got, err := Compose(AffiliateCoupang, "\n  본문  \n\n", "  https://l  ")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	want := disclosureCoupang + "\n\n본문\nhttps://l"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCompose_500자를_넘으면_거부(t *testing.T) {
	// 문구와 링크를 포함한 조립 결과 기준으로 판정해야 한다.
	room := MaxChars - CharCount(disclosureCoupang) - len("\n\n") - len("\nhttps://l")

	ok := strings.Repeat("가", room)
	if _, err := Compose(AffiliateCoupang, ok, "https://l"); err != nil {
		t.Fatalf("정확히 %d자인데 거부됨: %v", MaxChars, err)
	}

	tooLong := strings.Repeat("가", room+1)
	if _, err := Compose(AffiliateCoupang, tooLong, "https://l"); err == nil {
		t.Fatalf("%d자인데 통과됨", MaxChars+1)
	}
}

func TestCharCount_한글은_바이트가_아니라_글자로_센다(t *testing.T) {
	if got := CharCount("가나다"); got != 3 {
		t.Fatalf("CharCount(\"가나다\") = %d, want 3", got)
	}
	if got := CharCount("abc"); got != 3 {
		t.Fatalf("CharCount(\"abc\") = %d, want 3", got)
	}
}

func TestValidate_문구가_없으면_발행_중단(t *testing.T) {
	if err := Validate(AffiliateCoupang, "본문만 있고 문구가 없다"); err == nil {
		t.Fatal("문구가 없는데 통과됨")
	}
}

func TestValidate_문구가_중간에_있으면_발행_중단(t *testing.T) {
	// 고지 미흡으로 수익 몰수·계정 정지 사유가 된다.
	text := "본문 먼저 나오고\n\n" + disclosureCoupang
	if err := Validate(AffiliateCoupang, text); err == nil {
		t.Fatal("문구가 중간에 있는데 통과됨")
	}
}

func TestValidate_다른_제휴사_문구는_거부(t *testing.T) {
	text := disclosureToss + "\n\n본문\nhttps://l"
	if err := Validate(AffiliateCoupang, text); err == nil {
		t.Fatal("쿠팡 글에 토스 문구가 붙었는데 통과됨")
	}
}

func TestValidate_조립_결과는_통과한다(t *testing.T) {
	for _, a := range []string{AffiliateCoupang, AffiliateToss} {
		got, err := Compose(a, "본문", "https://l")
		if err != nil {
			t.Fatalf("%s: Compose: %v", a, err)
		}
		if err := Validate(a, got); err != nil {
			t.Errorf("%s: 조립 결과가 검증에서 거부됨: %v", a, err)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func TestCompose_본문만_담는다(t *testing.T) {
	got, err := Compose(AffiliateCoupang, "본문입니다")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if got != "본문입니다" {
		t.Fatalf("got=%q", got)
	}
}

func TestCompose_본문에_문구를_넣지_않는다(t *testing.T) {
	// 대가성 문구는 답글로 나간다. 본문에 들어가면 500자를 그만큼 잡아먹는다.
	got, err := Compose(AffiliateCoupang, "본문입니다")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if strings.Contains(got, disclosureCoupang) {
		t.Fatalf("본문에 문구가 들어감:\n%s", got)
	}
}

func TestCompose_본문에_링크를_넣지_않는다(t *testing.T) {
	// 본문에 외부 링크가 있으면 스레드 알고리즘이 노출을 낮춘다.
	got, err := Compose(AffiliateCoupang, "본문입니다")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if strings.Contains(got, "http") {
		t.Fatalf("본문에 링크가 들어감:\n%s", got)
	}
}

func TestCompose_알_수_없는_제휴사는_거부(t *testing.T) {
	if _, err := Compose("naver", "본문"); err == nil {
		t.Fatal("알 수 없는 제휴사인데 에러가 없음")
	}
}

func TestCompose_본문이_비면_거부(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"본문 없음", ""},
		{"본문이 공백뿐", "   \n\t "},
	} {
		if _, err := Compose(AffiliateCoupang, tt.body); err == nil {
			t.Errorf("%s: 에러가 없음", tt.name)
		}
	}
}

func TestCompose_본문_앞뒤_공백은_제거한다(t *testing.T) {
	got, err := Compose(AffiliateCoupang, "\n  본문  \n\n")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if got != "본문" {
		t.Fatalf("got=%q", got)
	}
}

func TestCompose_500자를_넘으면_거부(t *testing.T) {
	if _, err := Compose(AffiliateCoupang, strings.Repeat("가", MaxChars)); err != nil {
		t.Fatalf("정확히 %d자인데 거부됨: %v", MaxChars, err)
	}
	if _, err := Compose(AffiliateCoupang, strings.Repeat("가", MaxChars+1)); err == nil {
		t.Fatalf("%d자인데 통과됨", MaxChars+1)
	}
}

func TestComposeReply_문구와_링크가_함께_간다(t *testing.T) {
	got, err := ComposeReply(AffiliateCoupang, "  https://link.example/a  ")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	want := disclosureCoupang + "\nhttps://link.example/a"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestComposeReply_문구가_답글_맨_앞에_온다(t *testing.T) {
	// 링크보다 뒤에 오면 더보기에 가려 아예 안 보인다.
	got, err := ComposeReply(AffiliateToss, "https://link.example/a")
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if !strings.HasPrefix(got, disclosureToss) {
		t.Fatalf("문구가 맨 앞에 없음:\n%s", got)
	}
}

func TestComposeReply_제휴사별_문구(t *testing.T) {
	for _, tt := range []struct{ affiliate, want string }{
		{AffiliateCoupang, disclosureCoupang},
		{AffiliateToss, disclosureToss},
	} {
		got, err := ComposeReply(tt.affiliate, "https://link.example/a")
		if err != nil {
			t.Fatalf("%s: %v", tt.affiliate, err)
		}
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: 문구가 다름\n%s", tt.affiliate, got)
		}
	}
}

func TestComposeReply_빈_링크는_거부(t *testing.T) {
	// 링크 없이 문구만 올리면 답글이 아무 일도 하지 않는다.
	if _, err := ComposeReply(AffiliateCoupang, "   "); err == nil {
		t.Fatal("빈 링크인데 에러가 없음")
	}
}

func TestComposeReply_알_수_없는_제휴사는_거부(t *testing.T) {
	if _, err := ComposeReply("naver", "https://link.example/a"); err == nil {
		t.Fatal("알 수 없는 제휴사인데 에러가 없음")
	}
}

func TestComposeReply_500자를_넘으면_거부(t *testing.T) {
	room := MaxChars - CharCount(disclosureCoupang) - len("\n")
	if _, err := ComposeReply(AffiliateCoupang, strings.Repeat("a", room)); err != nil {
		t.Fatalf("정확히 %d자인데 거부됨: %v", MaxChars, err)
	}
	if _, err := ComposeReply(AffiliateCoupang, strings.Repeat("a", room+1)); err == nil {
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

func TestValidate_상한을_넘으면_발행_중단(t *testing.T) {
	if err := Validate(AffiliateCoupang, strings.Repeat("가", MaxChars+1)); err == nil {
		t.Fatal("상한을 넘었는데 통과됨")
	}
}

func TestValidate_알_수_없는_제휴사는_거부(t *testing.T) {
	if err := Validate("naver", "본문"); err == nil {
		t.Fatal("알 수 없는 제휴사인데 통과됨")
	}
}

func TestValidate_조립_결과는_통과한다(t *testing.T) {
	for _, a := range []string{AffiliateCoupang, AffiliateToss} {
		got, err := Compose(a, "본문")
		if err != nil {
			t.Fatalf("%s: Compose: %v", a, err)
		}
		if err := Validate(a, got); err != nil {
			t.Errorf("%s: 조립 결과가 검증에서 거부됨: %v", a, err)
		}
	}
}

func TestBodyRoom_문구가_빠져_상한을_그대로_쓴다(t *testing.T) {
	// 문구가 답글로 나가므로 본문은 500자를 온전히 쓴다.
	for _, a := range []string{AffiliateCoupang, AffiliateToss} {
		room, err := BodyRoom(a)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if room != MaxChars {
			t.Errorf("%s: room=%d, want %d", a, room, MaxChars)
		}
	}
}

func TestBodyRoom_정확히_room만큼은_통과한다(t *testing.T) {
	room, err := BodyRoom(AffiliateCoupang)
	if err != nil {
		t.Fatalf("에러: %v", err)
	}
	if _, err := Compose(AffiliateCoupang, strings.Repeat("가", room)); err != nil {
		t.Fatalf("%d자인데 거부됨: %v", room, err)
	}
	if _, err := Compose(AffiliateCoupang, strings.Repeat("가", room+1)); err == nil {
		t.Fatalf("%d자인데 통과됨", room+1)
	}
}

func TestBodyRoom_알_수_없는_제휴사는_거부(t *testing.T) {
	if _, err := BodyRoom("naver"); err == nil {
		t.Fatal("에러가 없음")
	}
}

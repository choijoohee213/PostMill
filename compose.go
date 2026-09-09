package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 대가성 문구. AI에게 생성시키지 않고 코드 상수로 관리한다 (SPEC 5-1).
// 이 문구가 게시물 첫 부분에 없으면 양쪽 모두 고지 미흡으로 보고
// 수익 몰수 또는 계정 정지 사유가 된다.
const (
	disclosureCoupang = "이 게시물은 쿠팡 파트너스 활동의 일환으로, 이에 따른 일정액의 수수료를 제공받습니다."
	disclosureToss    = "이 콘텐츠는 토스쇼핑 쉐어링크 활동의 일환으로, 링크를 통한 구매가 발생하면 일정 수수료를 지급받습니다."
)

// MaxChars는 문구 + 본문을 조립한 최종 형태의 글자 수 상한이다.
// 제휴 링크는 본문에 넣지 않고 답글로 따로 올리므로 여기에 포함되지 않는다.
const MaxChars = 500

// CharCount는 바이트가 아니라 글자 수를 센다. 한글은 바이트로 세면 3배가 된다.
func CharCount(s string) int { return utf8.RuneCountInString(s) }

func disclosureFor(affiliate string) (string, error) {
	switch affiliate {
	case AffiliateCoupang:
		return disclosureCoupang, nil
	case AffiliateToss:
		return disclosureToss, nil
	default:
		return "", fmt.Errorf("알 수 없는 제휴사: %q", affiliate)
	}
}

// Compose는 발행할 본문 텍스트를 조립하고 스스로 검증한다.
// 검증에 실패하면 문자열을 반환하지 않으므로, 검증되지 않은 텍스트가
// 밖으로 나갈 수 없다. 미리보기와 발행이 모두 이 함수를 쓴다.
//
// 제휴 링크는 여기 들어가지 않는다. 스레드 알고리즘이 본문의 외부 링크가
// 있는 글의 노출을 낮추기 때문에, 링크는 발행 직후 답글로 따로 올린다.
// 대가성 문구는 규정상 본문 첫 부분에 있어야 하므로 답글로 뺄 수 없다.
func Compose(affiliate, body string) (string, error) {
	disclosure, err := disclosureFor(affiliate)
	if err != nil {
		return "", err
	}

	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("본문이 비어 있다")
	}

	text := disclosure + "\n\n" + body
	if err := Validate(affiliate, text); err != nil {
		return "", err
	}
	return text, nil
}

// ComposeReply는 본문 글에 답글로 달 링크를 만든다.
func ComposeReply(affiliateLink string) (string, error) {
	affiliateLink = strings.TrimSpace(affiliateLink)
	if affiliateLink == "" {
		return "", fmt.Errorf("제휴 링크가 비어 있다")
	}
	return affiliateLink, nil
}

// Validate는 발행 직전 가드다. 문구가 맨 앞에 있는지, 길이가 상한 안인지 확인한다.
func Validate(affiliate, text string) error {
	disclosure, err := disclosureFor(affiliate)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(text, disclosure) {
		return fmt.Errorf("대가성 문구가 맨 앞에 없다: 발행 중단")
	}
	if n := CharCount(text); n > MaxChars {
		return fmt.Errorf("조립 결과가 %d자로 상한 %d자를 넘는다", n, MaxChars)
	}
	return nil
}

// BodyRoom은 문구를 뺀 뒤 본문에 쓸 수 있는 글자 수를 반환한다.
// 편집 화면의 카운터와 초안 생성 프롬프트가 같은 값을 쓴다.
func BodyRoom(affiliate string) (int, error) {
	disclosure, err := disclosureFor(affiliate)
	if err != nil {
		return 0, err
	}
	// 조립 형태: 문구 + "\n\n" + 본문
	room := MaxChars - CharCount(disclosure) - 2
	if room < 0 {
		room = 0
	}
	return room, nil
}

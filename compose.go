package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// 대가성 문구. AI에게 생성시키지 않고 코드 상수로 관리한다 (SPEC 5-1).
// 제휴사가 지정한 문장이라 줄이거나 바꿔 쓸 수 없다.
//
// 본문이 아니라 링크와 함께 답글로 나가고, 답글 안에서도 맨 위에 온다.
// 본문 500자를 문구에 쓰지 않으려고 고른 배치다. 스레드가 타임라인에서
// 첫 답글까지 본문과 함께 보여주므로, 맨 위에 두면 더 눌러보지 않아도
// 읽힌다. 링크 아래로 내려가면 그 이점이 사라진다.
const (
	disclosureCoupang = "이 게시물은 쿠팡 파트너스 활동의 일환으로, 이에 따른 일정액의 수수료를 제공받습니다."
	disclosureToss    = "이 콘텐츠는 토스쇼핑 쉐어링크 활동의 일환으로, 링크를 통한 구매가 발생하면 일정 수수료를 지급받습니다."
)

// MaxChars는 게시물 하나에 허용되는 글자 수 상한이다. 본문과 답글에 각각 걸린다.
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
// 대가성 문구와 제휴 링크는 여기 들어가지 않고 답글 하나로 함께 나간다.
// 링크는 스레드 알고리즘이 본문의 외부 링크가 있는 글의 노출을 낮추기
// 때문이고, 문구는 본문 자리를 아끼기 위해서다. ComposeReply를 보라.
func Compose(affiliate, body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("본문이 비어 있다")
	}
	if err := Validate(affiliate, body); err != nil {
		return "", err
	}
	return body, nil
}

// ComposeReply는 본문 글에 달 답글 하나를 만든다. 답글은 이것뿐이다.
//
// 대가성 문구와 링크가 함께 들어간다. 문구가 본문이 아니라 여기 있는 것은
// 본문 500자를 문구에 쓰지 않으려고 고른 배치다. disclosureCoupang 위의
// 설명을 보라.
func ComposeReply(affiliate, affiliateLink string) (string, error) {
	disclosure, err := disclosureFor(affiliate)
	if err != nil {
		return "", err
	}
	affiliateLink = strings.TrimSpace(affiliateLink)
	if affiliateLink == "" {
		return "", fmt.Errorf("제휴 링크가 비어 있다")
	}

	text := disclosure + "\n" + affiliateLink
	if n := CharCount(text); n > MaxChars {
		return "", fmt.Errorf("답글이 %d자로 상한 %d자를 넘는다", n, MaxChars)
	}
	return text, nil
}

// Validate는 발행 직전 가드다. 제휴사를 아는지, 길이가 상한 안인지 확인한다.
func Validate(affiliate, text string) error {
	if _, err := disclosureFor(affiliate); err != nil {
		return err
	}
	if n := CharCount(text); n > MaxChars {
		return fmt.Errorf("조립 결과가 %d자로 상한 %d자를 넘는다", n, MaxChars)
	}
	return nil
}

// BodyRoom은 본문에 쓸 수 있는 글자 수를 반환한다.
// 대가성 문구가 답글로 빠졌으므로 본문은 상한을 그대로 쓴다.
// 편집 화면의 카운터와 초안 생성 프롬프트가 같은 값을 쓴다.
func BodyRoom(affiliate string) (int, error) {
	if _, err := disclosureFor(affiliate); err != nil {
		return 0, err
	}
	return MaxChars, nil
}

// 주제(topic_tag)는 글 하나에 하나만 붙는다. 50자까지이고
// 마침표와 &는 쓸 수 없다 (Threads API 규칙).
const TopicMaxChars = 50

// NormalizeTopic은 입력한 주제를 Threads가 받는 모양으로 다듬는다.
// 비어 있으면 주제 없이 올린다는 뜻이라 오류가 아니다.
func NormalizeTopic(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, ".&") {
		return "", fmt.Errorf("주제에는 마침표와 &를 쓸 수 없어요")
	}
	if CharCount(s) > TopicMaxChars {
		return "", fmt.Errorf("주제는 %d자까지예요", TopicMaxChars)
	}
	return s, nil
}

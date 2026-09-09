package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// 초안 생성에 쓰는 모델.
const draftModel = "claude-opus-5"

const draftSystemPrompt = `너는 스레드(Threads)에 올릴 짧은 제휴 마케팅 글의 초안을 쓴다.

지켜야 할 것:
- 한국어 구어체. 친구에게 말하듯 담백하게 쓴다.
- 사용자가 준 메모에 있는 사실만 쓴다. 가격, 성능 수치, 할인율, 사용 기간처럼
  메모에 없는 구체적인 정보를 지어내지 않는다.
- 과장 광고 표현을 쓰지 않는다. "최저가", "무조건", "인생템", "역대급" 같은 말을 피한다.
- 이모지는 쓰지 않는다.
- 해시태그를 붙이지 않는다.
- 링크를 넣지 않는다. 링크는 시스템이 따로 붙인다.
- 대가성 문구나 광고 고지 문구를 쓰지 않는다. 이것도 시스템이 따로 붙인다.
- 문단은 짧게. 2~4줄 정도로 끊어 쓴다.

본문만 출력한다. 설명이나 머리말, 따옴표를 덧붙이지 않는다.`

// GenerateDraft는 메모를 바탕으로 본문 초안을 만든다.
// 대가성 문구와 링크는 포함하지 않는다 (compose.go가 발행 시점에 붙인다).
func GenerateDraft(ctx context.Context, client anthropic.Client, affiliate, memo string, room int) (string, error) {
	prompt := fmt.Sprintf(`제휴사: %s
상품 메모:
%s

위 메모를 바탕으로 본문을 써라. %d자를 넘기지 마라.`, affiliateKo(affiliate), memo, room)

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     draftModel,
		MaxTokens: 2000,
		System: []anthropic.TextBlockParam{{
			Text: draftSystemPrompt,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("모델이 생성을 거부했다: %s", resp.StopDetails.Explanation)
	}

	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}

	body := strings.TrimSpace(b.String())
	if body == "" {
		return "", fmt.Errorf("모델이 빈 응답을 반환했다")
	}
	if n := CharCount(body); n > room {
		return "", fmt.Errorf("생성된 본문이 %d자로 여유 %d자를 넘는다", n, room)
	}
	return body, nil
}

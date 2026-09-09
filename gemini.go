package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 초안 생성에 쓰는 모델. 무료 티어에서 쓸 수 있는 모델이어야 한다.
const geminiModel = "gemini-2.5-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

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

// Gemini는 Generative Language API를 표준 net/http로 호출한다.
// 클라이언트 라이브러리를 쓰지 않는다 (SPEC 9-1).
type Gemini struct {
	APIKey string
	HTTP   *http.Client
}

func NewGemini(apiKey string) *Gemini {
	return &Gemini{
		APIKey: apiKey,
		HTTP:   &http.Client{Timeout: 2 * time.Minute},
	}
}

type geminiRequest struct {
	SystemInstruction geminiContent   `json:"system_instruction"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// GenerateDraft는 메모를 바탕으로 본문 초안을 만든다.
// 대가성 문구와 링크는 포함하지 않는다 (compose.go가 발행 시점에 붙인다).
func (g *Gemini) GenerateDraft(ctx context.Context, affiliate, memo string, room int) (string, error) {
	prompt := fmt.Sprintf(`제휴사: %s
상품 메모:
%s

위 메모를 바탕으로 본문을 써라. %d자를 넘기지 마라.`, affiliateKo(affiliate), memo, room)

	payload, err := json.Marshal(geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: draftSystemPrompt}}},
		Contents:          []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig:  geminiGenConfig{MaxOutputTokens: 2000},
	})
	if err != nil {
		return "", err
	}

	url := geminiEndpoint + geminiModel + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("응답을 해석하지 못했다 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		msg := parsed.Error.Message
		if msg == "" {
			msg = "본문 없음"
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if parsed.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("요청이 차단되었다: %s", parsed.PromptFeedback.BlockReason)
	}
	if len(parsed.Candidates) == 0 {
		return "", fmt.Errorf("모델이 후보를 반환하지 않았다")
	}

	c := parsed.Candidates[0]
	// STOP이 아니면 잘렸거나 차단된 것이므로 그대로 쓰지 않는다.
	if c.FinishReason != "" && c.FinishReason != "STOP" {
		return "", fmt.Errorf("생성이 정상 종료되지 않았다: %s", c.FinishReason)
	}

	var b strings.Builder
	for _, p := range c.Content.Parts {
		b.WriteString(p.Text)
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

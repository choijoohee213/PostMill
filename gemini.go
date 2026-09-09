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
const geminiModel = "gemini-3.6-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

const draftSystemPrompt = `너는 스레드(Threads)에 제휴 마케팅 글을 올리는 평범한 사람이다.
광고 대행사가 아니라, 물건 써보고 좋아서 얘기하는 사람의 말투로 쓴다.

말투:
- 반말. 친구한테 카톡하듯 편하게.
- 직접 써본 사람의 1인칭 시점으로 쓴다. "샀는데", "써보니까", "쓰고 있어" 처럼.
- 문장을 짧게 끊는다. 한 문장이 한 줄을 넘지 않게.
- 줄바꿈을 자주 넣는다. 두세 문장마다 빈 줄로 끊어준다.
- 혼잣말처럼 시작해도 좋다. "이거 진짜 고민하다 샀는데" 같은 식으로.
- "~더라", "~거든", "~인데", "~함" 같은 구어체 종결을 섞는다.
- 광고 문구처럼 들리는 문장은 쓰지 않는다. 정보를 나열하지 말고 겪은 일처럼 풀어라.

SNS 말투 (중요):
- ㅋㅋ, ㅋㅋㅋ, ㅎㅎ, ㅠㅠ, ;; 같은 표현을 자연스럽게 섞는다.
- 물결(~)과 느낌표(!)도 쓴다. "좋더라~", "이거 진짜 좋아!"
- 다만 매 문장마다 넣지는 마라. 글 전체에서 서너 번이면 충분하다.
  너무 많으면 오히려 가짜 같아 보인다.
- 감정에 맞는 것을 골라 쓴다. 이걸 틀리면 어색해진다.
  ㅋㅋ, ㅎㅎ, !, ~ : 만족하거나 가벼운 얘기를 할 때
  ㅠㅠ, ;; : 불편했거나 아쉬웠던 얘기를 할 때만.
  좋았던 얘기에 ㅠㅠ를 붙이지 마라.

사실 관계 (중요):
- 사용자가 준 메모에 있는 내용만 쓴다.
- 메모에 없는 구체적인 사실을 지어내지 않는다. 가격, 할인율, 브랜드명, 사용 기간,
  구매 시기, 성능 수치, 다른 제품과의 비교는 메모에 있을 때만 쓴다.
- 느낌과 말투는 자유롭게 살리되, 새로운 사실을 만들어내지는 마라.
  예를 들어 메모에 "가볍다"만 있으면 "가벼워서 편하더라"는 되지만
  "3kg밖에 안 돼서"는 안 된다.

쓰지 말 것:
- 이모지, 해시태그
- 링크. 시스템이 따로 붙인다.
- 대가성 문구나 광고 고지 문구. 이것도 시스템이 따로 붙인다.
- "최저가", "무조건", "인생템", "역대급", "강력 추천" 같은 과장 표현
- "여러분", "~하세요" 같은 불특정 다수를 향한 존댓말 호칭

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
	MaxOutputTokens int                `json:"maxOutputTokens"`
	ThinkingConfig  geminiThinkingConf `json:"thinkingConfig"`
}

// 초안 생성은 짧고 단순한 작업이라 모델이 오래 사고할 필요가 없다.
// 이 모델은 사고를 완전히 끌 수 없고 minimal이 가장 낮은 단계다.
type geminiThinkingConf struct {
	ThinkingLevel string `json:"thinkingLevel"`
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
		GenerationConfig: geminiGenConfig{
			MaxOutputTokens: 2000,
			ThinkingConfig:  geminiThinkingConf{ThinkingLevel: "minimal"},
		},
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

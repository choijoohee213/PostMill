package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// 초안 생성에 쓰는 모델. 무료 티어에서 쓸 수 있는 모델이어야 한다.
const geminiModel = "gemini-3.6-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

// 무료 티어는 붐빌 때 503이나 429를 자주 돌려준다. 몇 초 뒤면 대개 풀리므로
// 사용자가 폰에서 재시도 버튼을 누르기 전에 알아서 다시 시도한다.
const maxAttempts = 3

var retryBackoff = []time.Duration{3 * time.Second, 8 * time.Second}

// retryableError는 잠시 뒤 다시 시도하면 풀릴 가능성이 있는 실패다.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }

const draftSystemPrompt = `너는 스레드(Threads)에 제휴 마케팅 글을 올리는 평범한 사람이다.
광고 대행사가 아니라, 물건 써보고 좋아서 한마디 하는 사람의 말투로 쓴다.

글은 두 부분으로 쓴다. 사이에 --- 만 있는 줄을 넣어 나눈다.

[본문] --- 위쪽
- 아주 짧게. 3~4줄, 80자 안팎. 이게 가장 중요하다.
- 스레드는 훑어보는 곳이다. 길면 그냥 넘긴다.
- 구성: 겪던 불편 한 줄 → 이걸로 뭐가 달라졌는지 한 줄 → 질문 한 줄.
- 장점은 딱 하나만. 메모에 여러 개 있어도 가장 공감될 것 하나만 고르고
  나머지는 전부 버린다.
- 마지막 줄은 읽는 사람에게 던지는 질문이다. 답글이 달려야 노출된다.

[디테일] --- 아래쪽
- 답글로 이어 붙일 내용이다. 3~5줄, 120자 안팎.
- 본문에서 안 쓴 나머지 이야기를 여기서 푼다. 구체적인 장면이나
  써보고 느낀 점을 적는다.
- 본문에 쓴 말을 되풀이하지 마라.
- 여기서도 장점을 항목처럼 나열하지 마라. 이야기하듯 쓴다.

양쪽 모두 지킬 것:
- 반말. 친구한테 카톡하듯.
- 직접 써본 1인칭. "샀는데", "써보니까" 처럼.
- 한 줄은 짧게 끊고 줄바꿈을 자주 넣는다.
- "그리고", "또", "게다가", "무엇보다" 로 항목을 이어붙이지 마라.
- ㅋㅋ, ㅎㅎ, ㅠㅠ, ;;, ~, ! 를 섞되 각 부분에서 한두 번이면 충분하다.
  만족스러운 얘기엔 ㅋㅋ ㅎㅎ ! ~, 불편했던 얘기엔 ㅠㅠ ;; 를 쓴다.
  좋았던 얘기에 ㅠㅠ를 붙이지 마라.
- 광고 문구처럼 들리는 문장은 쓰지 않는다.
- 이모지, 해시태그를 쓰지 않는다.
- 링크를 넣지 않는다. 시스템이 따로 붙인다.
- 대가성 문구나 광고 고지 문구를 쓰지 않는다. 이것도 시스템이 붙인다.
- "최저가", "무조건", "인생템", "역대급", "강력 추천" 같은 과장 표현 금지.
- "여러분", "~하세요" 같은 불특정 다수를 향한 존댓말 호칭 금지.

사실 관계:
- 상황과 감정은 지어내도 되지만, 제품에 대한 사실은 메모에 있는 것만 쓴다.
- 가격, 할인율, 브랜드명, 모델명, 성능 수치, 용량, 배터리 시간,
  다른 제품과의 비교는 메모에 있을 때만 쓴다.
- 메모에 없는 기능을 있다고 하지 마라.

다른 말 없이 본문, ---, 디테일만 출력한다.`

// 본문과 디테일을 나누는 구분자.
const draftSeparator = "---"

const (
	// 본문은 타임라인에서 훑어보는 부분이라 아주 짧아야 한다.
	bodyTargetChars = 80
	bodyMaxChars    = 160

	// 디테일은 답글이라 조금 더 길어도 된다.
	detailTargetChars = 120
	detailMaxChars    = 300
)

// Gemini는 Generative Language API를 표준 net/http로 호출한다.
// 클라이언트 라이브러리를 쓰지 않는다 (SPEC 9-1).
type Gemini struct {
	APIKey  string
	HTTP    *http.Client
	BaseURL string // 테스트에서 가짜 서버를 가리키기 위해 주입할 수 있다
}

func NewGemini(apiKey string) *Gemini {
	return &Gemini{
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 90 * time.Second},
		BaseURL: geminiEndpoint,
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
// 일시적인 실패는 maxAttempts만큼 다시 시도한다.
func (g *Gemini) GenerateDraft(ctx context.Context, affiliate, memo string, room int) (string, string, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			wait := retryBackoff[attempt-1]
			log.Printf("초안 생성 재시도 %d/%d (%v 후): %v", attempt+1, maxAttempts, wait, lastErr)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return "", "", ctx.Err()
			}
		}

		body, detail, err := g.generateOnce(ctx, affiliate, memo, room)
		if err == nil {
			return body, detail, nil
		}
		lastErr = err

		var retryable retryableError
		if !errors.As(err, &retryable) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%d번 시도했지만 실패했다: %w", maxAttempts, lastErr)
}

func (g *Gemini) generateOnce(ctx context.Context, affiliate, memo string, room int) (string, string, error) {
	limit := room
	if bodyMaxChars < limit {
		limit = bodyMaxChars
	}

	prompt := fmt.Sprintf(`제휴사: %s
상품 메모:
%s

본문은 %d자 안팎(최대 %d자), 디테일은 %d자 안팎으로 써라.
본문에는 장점 하나만 담고 나머지는 디테일로 보내라.`,
		affiliateKo(affiliate), memo, bodyTargetChars, limit, detailTargetChars)

	payload, err := json.Marshal(geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: draftSystemPrompt}}},
		Contents:          []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: geminiGenConfig{
			MaxOutputTokens: 2000,
			ThinkingConfig:  geminiThinkingConf{ThinkingLevel: "minimal"},
		},
	})
	if err != nil {
		return "", "", err
	}

	url := g.BaseURL + geminiModel + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		// 연결 실패나 타임아웃은 다시 시도해볼 만하다.
		return "", "", retryableError{err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("응답을 해석하지 못했다 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		msg := parsed.Error.Message
		if msg == "" {
			msg = "본문 없음"
		}
		statusErr := fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
		// 429(요청 과다)와 5xx(서버 혼잡)는 잠시 뒤면 풀린다.
		// 400이나 401 같은 요청 자체의 문제는 다시 보내도 같은 결과다.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", "", retryableError{statusErr}
		}
		return "", "", statusErr
	}
	if parsed.PromptFeedback.BlockReason != "" {
		return "", "", fmt.Errorf("요청이 차단되었다: %s", parsed.PromptFeedback.BlockReason)
	}
	if len(parsed.Candidates) == 0 {
		return "", "", fmt.Errorf("모델이 후보를 반환하지 않았다")
	}

	c := parsed.Candidates[0]
	// STOP이 아니면 잘렸거나 차단된 것이므로 그대로 쓰지 않는다.
	if c.FinishReason != "" && c.FinishReason != "STOP" {
		return "", "", fmt.Errorf("생성이 정상 종료되지 않았다: %s", c.FinishReason)
	}

	var b strings.Builder
	for _, p := range c.Content.Parts {
		b.WriteString(p.Text)
	}

	body, detail := splitDraft(b.String())
	// 아래는 생성이 매번 달라지므로 다시 뽑으면 통과할 수 있다.
	if body == "" {
		return "", "", retryableError{fmt.Errorf("모델이 빈 응답을 반환했다")}
	}
	if n := CharCount(body); n > limit {
		return "", "", retryableError{fmt.Errorf("생성된 본문이 %d자로 상한 %d자를 넘는다", n, limit)}
	}
	if n := CharCount(detail); n > detailMaxChars {
		return "", "", retryableError{fmt.Errorf("생성된 디테일이 %d자로 상한 %d자를 넘는다", n, detailMaxChars)}
	}
	return body, detail, nil
}

// splitDraft는 모델 출력을 본문과 디테일로 나눈다.
// 구분자가 없으면 전부 본문으로 본다. 디테일은 없어도 발행할 수 있다.
func splitDraft(raw string) (body, detail string) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == draftSeparator {
			return strings.TrimSpace(strings.Join(lines[:i], "\n")),
				strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return strings.TrimSpace(raw), ""
}

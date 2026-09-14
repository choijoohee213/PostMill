package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 초안 생성에 쓰는 모델. 무료 티어에서 쓸 수 있는 모델이어야 한다.
const geminiModel = "gemini-3.6-flash"

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/"

// 무료 티어는 붐빌 때 503이나 429를 자주 돌려준다. 몇 초 뒤면 대개 풀리므로
// 사용자가 폰에서 재시도 버튼을 누르기 전에 알아서 다시 시도한다.
const maxAttempts = 3

var retryBackoff = []time.Duration{3 * time.Second, 8 * time.Second}

// maxRetryWait보다 오래 기다리라고 하면 다시 시도하지 않는다.
const maxRetryWait = 70 * time.Second

// retryableError는 잠시 뒤 다시 시도하면 풀릴 가능성이 있는 실패다.
type retryableError struct {
	err  error
	wait time.Duration // 모델이 이만큼 기다리라고 알려준 시간. 없으면 0
}

// errDailyQuota는 오늘 쓸 수 있는 요청을 다 쓴 경우다. 기다려도 오늘은
// 풀리지 않으므로 다시 시도하지 않는다. 다시 시도하면 실패만 쌓인다.
var errDailyQuota = errors.New("Gemini 하루 사용량을 다 썼다")

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// errRateLimited는 한도(429)에 걸린 경우다. 키가 여러 개면 다음 키로 넘어간다.
var errRateLimited = errors.New("Gemini 한도에 걸렸다")

const draftSystemPrompt = `너는 스레드(Threads)에 제휴 마케팅 글을 올리는 평범한 사람이다.
광고 대행사가 아니라, 물건 써보고 좋아서 한마디 하는 사람의 말투로 쓴다.

글은 두 부분으로 쓴다. 사이에 --- 만 있는 줄을 넣어 나눈다.

[본문] --- 위쪽
- 아주 짧게. 3~4줄, 80자 안팎. 이게 가장 중요하다.
- 스레드는 훑어보는 곳이다. 길면 그냥 넘긴다.
- 구성은 요청에 적힌 훅 유형을 따른다.
- 장점은 딱 하나만. 메모에 여러 개 있어도 가장 공감될 것 하나만 고르고
  나머지는 전부 버린다.
- 읽는 사람이 답글을 달고 싶어지게 끝낸다. 답글이 달려야 노출된다.

[디테일] --- 아래쪽
- 답글로 이어 붙일 내용이다. 3~5줄, 120자 안팎.
- 본문에서 안 쓴 나머지 이야기를 여기서 푼다. 언제 어디서 쓰는지 같은
  장면이나 써보고 느낀 기분을 적는다.
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

` + factRules + `
다른 말 없이 본문, ---, 디테일만 출력한다.`

// hookType은 본문 첫 줄을 여는 방식이다. 모든 글이 같은 구조면 타임라인에서
// 같은 사람이 같은 글을 반복하는 것처럼 보이므로 초안마다 바꾼다.
// 논쟁형과 정보성은 쓰지 않는다. 논쟁은 제휴 글에서 반감을 사고,
// 정보성은 확인되지 않은 사실을 지어내기 쉽다.
type hookType struct {
	Name  string
	Guide string
}

var hookTypes = []hookType{
	{"공감형", `첫 줄은 많은 사람이 겪는 사소한 불편을 "나만 그래?" 하듯 꺼낸다. 둘째 줄에 이걸로 뭐가 달라졌는지, 마지막 줄은 읽는 사람에게 묻는다.`},
	{"후기형", `첫 줄은 써본 뒤의 솔직한 한마디로 연다. 사용 기간 같은 숫자는 쓰지 않는다. 둘째 줄에 제일 좋았던 점 하나, 마지막 줄은 읽는 사람에게 묻는다.`},
	{"비교형", `첫 줄은 이걸 쓰기 전에 하던 방식을 말한다. 둘째 줄에 지금은 어떻게 하는지, 마지막 줄은 읽는 사람에게 묻는다.`},
	{"질문형", `첫 줄부터 읽는 사람에게 묻는다. 둘째 줄에 나는 이걸로 해결했다고 말하고, 마지막 줄은 짧은 한마디로 맺는다.`},
}

// randomHook은 초안 하나만 만들 때 쓴다.
func randomHook() hookType { return hookTypes[rand.IntN(len(hookTypes))] }

// hookPrompt는 요청 끝에 붙일 훅 안내다.
func hookPrompt(h hookType) string {
	return "\n\n이번 본문의 훅은 " + h.Name + "이다. " + h.Guide
}

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
	// APIKeys는 차례로 쓸 키다. 쓰던 키가 한도에 걸리면 다음 키로 넘어간다.
	// 한도는 키가 아니라 Google Cloud 프로젝트 단위라 키마다 프로젝트가 달라야 한다.
	APIKeys []string
	HTTP    *http.Client
	BaseURL string // 테스트에서 가짜 서버를 가리키기 위해 주입할 수 있다

	mu  sync.Mutex
	cur int // 다음 요청을 보낼 키
}

// NewGemini는 쉼표로 구분한 키 목록을 받는다. 키 하나여도 된다.
func NewGemini(apiKeys string) *Gemini {
	var keys []string
	for _, k := range strings.Split(apiKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return &Gemini{
		APIKeys: keys,
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
		// 429에는 어떤 한도에 걸렸는지(QuotaFailure)와 기다릴 시간(RetryInfo)이 온다.
		Details []struct {
			Violations []struct {
				QuotaID string `json:"quotaId"`
			} `json:"violations"`
			RetryDelay string `json:"retryDelay"`
		} `json:"details"`
	} `json:"error"`
}

// GenerateDraft는 메모를 바탕으로 본문 초안을 만든다.
// 대가성 문구와 링크는 포함하지 않는다 (compose.go가 발행 시점에 붙인다).
// 일시적인 실패는 maxAttempts만큼 다시 시도한다.
func (g *Gemini) GenerateDraft(ctx context.Context, affiliate, memo string, room int, hook hookType) (string, string, error) {
	var body, detail string
	err := retry(ctx, "초안 생성", func() error {
		var err error
		body, detail, err = g.generateOnce(ctx, affiliate, memo, room, hook)
		return err
	})
	return body, detail, err
}

// retry는 다시 시도할 만한 실패(retryableError)면 maxAttempts만큼 다시 부른다.
// 모델이 기다리라고 알려준 시간이 있으면 그만큼 기다린다.
func retry(ctx context.Context, label string, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			wait := retryBackoff[attempt-1]
			var re retryableError
			if errors.As(lastErr, &re) && re.wait > wait {
				wait = re.wait
			}
			log.Printf("%s 재시도 %d/%d (%v 후): %v", label, attempt+1, maxAttempts, wait, lastErr)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		var re retryableError
		if !errors.As(err, &re) {
			return err
		}
	}
	return fmt.Errorf("%d번 시도했지만 실패했다: %w", maxAttempts, lastErr)
}

func (g *Gemini) generateOnce(ctx context.Context, affiliate, memo string, room int, hook hookType) (string, string, error) {
	limit := room
	if bodyMaxChars < limit {
		limit = bodyMaxChars
	}

	prompt := fmt.Sprintf(`제휴사: %s
상품 메모:
%s

본문은 %d자 안팎(최대 %d자), 디테일은 %d자 안팎으로 써라.
본문에는 장점 하나만 담고 나머지는 디테일로 보내라.`,
		affiliateKo(affiliate), memo, bodyTargetChars, limit, detailTargetChars) + hookPrompt(hook)

	raw, err := g.call(ctx, draftSystemPrompt, prompt)
	if err != nil {
		return "", "", err
	}

	body, detail := splitDraft(raw)
	// 아래는 생성이 매번 달라지므로 다시 뽑으면 통과할 수 있다.
	if body == "" {
		return "", "", retryableError{err: fmt.Errorf("모델이 빈 응답을 반환했다")}
	}
	if n := CharCount(body); n > limit {
		return "", "", retryableError{err: fmt.Errorf("생성된 본문이 %d자로 상한 %d자를 넘는다", n, limit)}
	}
	if n := CharCount(detail); n > detailMaxChars {
		return "", "", retryableError{err: fmt.Errorf("생성된 디테일이 %d자로 상한 %d자를 넘는다", n, detailMaxChars)}
	}
	return body, detail, nil
}

// call은 시스템 프롬프트와 요청을 보내고 응답 텍스트를 돌려준다.
// 초안 생성과 자동 제안이 같은 호출부를 쓴다.
func (g *Gemini) call(ctx context.Context, system, prompt string) (string, error) {
	return g.callTokens(ctx, system, prompt, 2000)
}

// callTokens는 출력 토큰 상한을 정해 부른다. 초안 여러 장을 한 번에 받을 때 늘린다.
func (g *Gemini) callTokens(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	payload, err := json.Marshal(geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: system}}},
		Contents:          []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: geminiGenConfig{
			MaxOutputTokens: maxTokens,
			ThinkingConfig:  geminiThinkingConf{ThinkingLevel: "minimal"},
		},
	})
	if err != nil {
		return "", err
	}

	g.mu.Lock()
	start, n := g.cur, len(g.APIKeys)
	g.mu.Unlock()
	if n == 0 {
		return "", errors.New("Gemini API 키가 없다")
	}

	// 쓰던 키부터 차례로 보내고, 한도에 걸린 키는 건너뛴다. 모든 키가 걸리면
	// 마지막 실패를 돌려준다(분당 한도면 retry가 기다렸다 다시 부른다).
	var lastErr error
	for i := 0; i < n; i++ {
		k := (start + i) % n
		text, err := g.callKey(ctx, g.APIKeys[k], payload)
		if !errors.Is(err, errRateLimited) && !errors.Is(err, errDailyQuota) {
			return text, err
		}
		lastErr = err
		g.mu.Lock()
		g.cur = (k + 1) % n
		g.mu.Unlock()
		if n > 1 {
			log.Printf("Gemini 키 %d/%d 한도 초과, 다음 키로 넘어간다: %v", k+1, n, err)
		}
	}
	return "", lastErr
}

// callKey는 키 하나로 요청을 보낸다.
func (g *Gemini) callKey(ctx context.Context, key string, payload []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.BaseURL+geminiModel+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)

	resp, err := g.HTTP.Do(req)
	if err != nil {
		// 연결 실패나 타임아웃은 다시 시도해볼 만하다.
		return "", retryableError{err: err}
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
		statusErr := fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
		if resp.StatusCode == http.StatusTooManyRequests {
			var wait time.Duration
			for _, d := range parsed.Error.Details {
				for _, v := range d.Violations {
					// 하루 한도는 기다려도 오늘은 풀리지 않는다.
					if strings.Contains(v.QuotaID, "PerDay") {
						return "", fmt.Errorf("%w: %v", errDailyQuota, statusErr)
					}
				}
				if w, err := time.ParseDuration(d.RetryDelay); err == nil {
					wait = w
				}
			}
			// 분당 한도는 알려준 시간만큼 기다리면 풀린다. 너무 길면 요청이 끝나기 전에
			// 못 풀리므로 포기한다.
			limited := fmt.Errorf("%w: %v", errRateLimited, statusErr)
			if wait > maxRetryWait {
				return "", limited
			}
			return "", retryableError{err: limited, wait: wait}
		}
		// 5xx(서버 혼잡)는 잠시 뒤면 풀린다.
		// 400이나 401 같은 요청 자체의 문제는 다시 보내도 같은 결과다.
		if resp.StatusCode >= 500 {
			return "", retryableError{err: statusErr}
		}
		return "", statusErr
	}
	if parsed.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("요청이 차단되었다: %s", parsed.PromptFeedback.BlockReason)
	}
	if len(parsed.Candidates) == 0 {
		return "", retryableError{err: fmt.Errorf("모델이 후보를 반환하지 않았다")}
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
	return strings.TrimSpace(b.String()), nil
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

const autoSystemPrompt = `너는 스레드(Threads)에 제휴 마케팅 글을 올리는 평범한 사람이다.
상품을 직접 고르고, 그 상품에 대한 글을 쓴다.

먼저 상품을 고른다:
- 쿠팡이나 온라인에서 쉽게 살 수 있는 생활용품 중에서 고른다.
- 만원에서 오만원 사이의 흔한 물건이 좋다. 비싸거나 특이한 물건은 피한다.
- 특정 브랜드나 모델명을 쓰지 마라. "접이식 장바구니 카트", "실리콘 주방장갑"처럼
  종류로만 적는다. 사용자가 이 이름으로 검색해서 직접 상품을 고를 것이다.
- 사람들이 "아 이거 나도 불편했는데" 할 만한, 사소한 불편을 해결하는 물건이 좋다.

출력 형식은 세 부분이고 사이에 --- 만 있는 줄을 넣는다.

[1] 상품 이름 한 줄. 검색어로 쓸 수 있게 짧게.
` + autoReplyParts + `
` + autoVoiceRules + `
` + factRules + `
다른 말 없이 세 부분만 출력한다.`

// factRules는 모든 초안이 지키는 사실 규칙이다.
//
// 메모 없이 만들면 모델이 그럴듯한 기능을 지어냈다("밑에 물 빼는 구멍이 있어서",
// "달그락 소리가 안 남"). 없는 기능을 쓰면 과장 광고가 되고 산 사람이 속았다고
// 느낀다. 막연히 "지어내지 마라"로는 부족해 실제로 나온 문장을 예로 든다.
const factRules = `지어내지 말 것 (가장 중요하다):
- 확인된 정보는 상품 이름과, 요청에 메모가 있으면 그 메모뿐이다.
- 제품의 기능, 구조, 소재의 성질, 크기, 무게, 튼튼함, 소리, 냄새, 세척이나 관리,
  구성품은 확인된 정보에 적힌 것만 쓴다.
- 상품 이름에 들어 있는 말은 써도 된다. "접이식 실리콘 설거지통"이면 접힌다는 것,
  실리콘이라는 것, 설거지할 때 쓴다는 것까지만 사실이다.
- 확인된 정보에 없으면 이런 문장은 쓰지 않는다:
  "밑에 물 빼는 구멍이 있어서", "물 가득 채워도 안 쓰러짐", "달그락 소리가 안 남",
  "물때가 안 낌", "세척이 쉬움", "생각보다 가벼움"
- 가격, 할인율, 브랜드명, 모델명, 수치, 사용 기간 숫자, 다른 제품과의 비교도
  확인된 정보에 있을 때만 쓴다.
- 대신 마음껏 써도 되는 것: 이걸 사게 된 상황, 쓰기 전의 불편, 언제 어디서 쓰는지,
  쓰고 나서 기분이나 생활이 어떻게 달라졌는지.
- 쓸까 말까 애매하면 쓰지 말고 상황과 기분으로 돌려 말한다.
`

// autoReplyParts는 자동 초안의 본문과 답글 하나의 형식이다. 상품을 고르는 방식만
// 다르고 글 형식은 같으므로 쿠팡·토스 프롬프트가 함께 쓴다.
const autoReplyParts = `---
[2] 본문. 아주 짧게, 3~4줄, 80자 안팎.
    구성은 요청에 적힌 훅 유형을 따른다. 장점은 딱 하나만 담는다.
---
[3] 답글. 3~5줄, 120자 안팎.
    본문에서 안 쓴 이야기를 푼다. 언제 어디서 쓰는지, 생활이 어떻게 달라졌는지 같은
    장면이나 기분을 이야기하듯 쓴다. 본문에 쓴 말을 되풀이하지 마라.
`

// autoVoiceRules는 자동 초안의 말투 규칙이다.
const autoVoiceRules = `말투 (2, 3 모두):
- 반말. 친구한테 카톡하듯. 직접 써본 1인칭으로 쓴다.
- 한 줄은 짧게 끊고 줄바꿈을 자주 넣는다.
- "그리고", "또", "게다가", "무엇보다" 로 항목을 이어붙이지 마라.
- ㅋㅋ, ㅎㅎ, ㅠㅠ, ;;, ~, ! 를 각 부분에서 한두 번 섞는다.
  만족스러운 얘기엔 ㅋㅋ ㅎㅎ ! ~, 불편했던 얘기엔 ㅠㅠ ;; 를 쓴다.
- 광고 문구처럼 들리는 문장, 이모지, 해시태그, 링크를 쓰지 않는다.
- 대가성 문구를 쓰지 않는다. 시스템이 따로 붙인다.
- "최저가", "무조건", "인생템", "역대급" 같은 과장 표현 금지.
`

const tossSystemPrompt = `너는 스레드(Threads)에 제휴 마케팅 글을 올리는 평범한 사람이다.
토스쇼핑에서 지금 잘 팔리거나 특가인 상품 목록을 받아, 그중 하나를 골라 글을 쓴다.

먼저 상품을 고른다:
- 사람들이 "아 이거 나도 불편했는데" 할 만한, 생활 속 사소한 불편을 해결하는 물건이 좋다.
- 리뷰가 많고 평점이 높은 것을 우선한다.
- 만원에서 오만원 사이의 물건이 좋다. 너무 비싼 물건은 피한다.
- 옷이나 신발처럼 사이즈를 골라야 하는 것, 신선식품은 피한다.

출력 형식은 세 부분이고 사이에 --- 만 있는 줄을 넣는다.

[1] 고른 상품의 번호만. 숫자 하나.
` + autoReplyParts + `
` + autoVoiceRules + `
` + factRules + `
- 상품명을 그대로 옮기지 말고 "이 무선 청소기"처럼 종류로 말한다.
- 목록의 가격·할인·리뷰는 고르는 데만 쓰고 글에는 쓰지 않는다. 금방 바뀐다.

다른 말 없이 세 부분만 출력한다.`

// AutoDraft는 AI가 상품까지 고른 초안이다.
type AutoDraft struct {
	ProductName   string
	ProductURL    string
	AffiliateLink string // 토스 API로 발급한 쉐어링크. 쿠팡은 비어 있다
	TacaItemID    int64  // 토스 API로 고른 상품. 쿠팡은 0
	Body          string
	Detail        string
}

// draftSpec은 한 번에 만드는 초안 중 한 장의 요구다.
type draftSpec struct {
	Hint string // 쿠팡: 고를 분야. 토스는 쓰지 않는다
	Hook hookType
}

// draftBatchSeparator는 한 응답에 담긴 초안과 초안 사이의 구분자다.
const draftBatchSeparator = "====="

// batchTokensPerDraft는 초안 한 장에 넉넉히 잡는 출력 토큰이다.
const batchTokensPerDraft = 1500

// SuggestDrafts는 상품 선정부터 본문까지 초안 여러 장을 한 번의 호출로 만든다.
//
// 한 장씩 부르면 무료 한도를 장수만큼 쓰고, 동시에 만드는 초안끼리 서로 뭘
// 골랐는지 몰라 상품이 겹친다. 한 번에 만들면 둘 다 해결된다.
// 형식이 틀린 장은 nil로 돌려준다. 한 장도 못 건졌을 때만 다시 시도한다.
// avoid에 적힌 상품은 피한다.
func (g *Gemini) SuggestDrafts(ctx context.Context, affiliate string, avoid []string, specs []draftSpec) ([]*AutoDraft, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "초안 %d개를 쓴다. 초안마다 서로 다른 상품을 고른다.\n", len(specs))
	for i, sp := range specs {
		fmt.Fprintf(&b, "\n초안 %d: 훅은 %s이다. %s", i+1, sp.Hook.Name, sp.Hook.Guide)
		if sp.Hint != "" {
			fmt.Fprintf(&b, " 상품은 %s으로 고른다.", sp.Hint)
		}
	}
	if len(avoid) > 0 {
		b.WriteString("\n\n아래 상품은 이미 다뤘으니 피해라:\n- " + strings.Join(avoid, "\n- "))
	}
	b.WriteString(batchFormat(len(specs)))

	var drafts []*AutoDraft
	err := retry(ctx, "자동 초안", func() error {
		raw, err := g.callTokens(ctx, autoSystemPrompt, b.String(), batchTokensPerDraft*len(specs))
		if err != nil {
			return err
		}
		drafts = make([]*AutoDraft, len(specs))
		ok := 0
		for i, chunk := range splitBatch(raw, len(specs)) {
			parts := splitParts(chunk, 3)
			if len(parts) < 3 {
				continue
			}
			d := &AutoDraft{ProductName: firstLine(parts[0]), Body: parts[1], Detail: parts[2]}
			if d.ProductName == "" || validDraft(d) != nil {
				continue
			}
			d.ProductURL = SearchURL(affiliate, d.ProductName)
			drafts[i] = d
			ok++
		}
		if ok == 0 {
			return retryableError{err: fmt.Errorf("초안을 한 장도 알아볼 수 없다")}
		}
		return nil
	})
	return drafts, err
}

// SuggestFromTossBatch는 토스 상품 목록에서 서로 다른 상품을 골라 초안 여러 장을
// 한 번에 쓴다. 장마다 고른 상품의 목록 내 위치를 돌려주고, 못 쓴 장은 -1과 nil이다.
func (g *Gemini) SuggestFromTossBatch(ctx context.Context, products []TossProduct, hooks []hookType) ([]int, []*AutoDraft, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "아래 상품 중에서 서로 다른 상품 %d개를 골라 초안마다 하나씩 쓴다.\n\n", len(hooks))
	for i, p := range products {
		fmt.Fprintf(&b, "%d. %s | %d원", i+1, p.DisplayName, p.DisplayPrice)
		if p.DiscountRate > 0 {
			fmt.Fprintf(&b, " (%.0f%% 할인)", p.DiscountRate)
		}
		if p.ReviewCount > 0 {
			fmt.Fprintf(&b, " | 리뷰 %.1f점 %d개", p.ReviewScore, p.ReviewCount)
		}
		if p.EndAt != "" {
			b.WriteString(" | 하루특가")
		}
		b.WriteString("\n")
	}
	for i, h := range hooks {
		fmt.Fprintf(&b, "\n초안 %d: 훅은 %s이다. %s", i+1, h.Name, h.Guide)
	}
	b.WriteString(batchFormat(len(hooks)))

	var picks []int
	var drafts []*AutoDraft
	err := retry(ctx, "토스 초안", func() error {
		raw, err := g.callTokens(ctx, tossSystemPrompt, b.String(), batchTokensPerDraft*len(hooks))
		if err != nil {
			return err
		}
		picks = make([]int, len(hooks))
		drafts = make([]*AutoDraft, len(hooks))
		used := map[int]bool{}
		ok := 0
		for i, chunk := range splitBatch(raw, len(hooks)) {
			picks[i] = -1
			parts := splitParts(chunk, 3)
			if len(parts) < 3 {
				continue
			}
			n, err := strconv.Atoi(strings.Trim(firstLine(parts[0]), " .[]번"))
			// 같은 상품을 두 장에 쓰면 뒤의 것은 버린다.
			if err != nil || n < 1 || n > len(products) || used[n] {
				continue
			}
			d := &AutoDraft{ProductName: products[n-1].DisplayName, Body: parts[1], Detail: parts[2]}
			if validDraft(d) != nil {
				continue
			}
			used[n] = true
			picks[i], drafts[i] = n-1, d
			ok++
		}
		for i := len(splitBatch(raw, len(hooks))); i < len(hooks); i++ {
			picks[i] = -1
		}
		if ok == 0 {
			return retryableError{err: fmt.Errorf("초안을 한 장도 알아볼 수 없다")}
		}
		return nil
	})
	return picks, drafts, err
}

// batchFormat은 요청 끝에 붙이는 출력 형식 안내다.
func batchFormat(n int) string {
	if n == 1 {
		return "\n\n초안 하나만 출력한다."
	}
	return fmt.Sprintf("\n\n초안 %d개를 순서대로 출력하고, 초안과 초안 사이에는 %s 만 있는 줄을 넣는다.", n, draftBatchSeparator)
}

// splitBatch는 응답을 초안별로 나눈다. 최대 n개까지만 돌려준다.
func splitBatch(raw string, n int) []string {
	var chunks []string
	var cur []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == draftBatchSeparator {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	chunks = append(chunks, strings.Join(cur, "\n"))
	if len(chunks) > n {
		chunks = chunks[:n]
	}
	return chunks
}

// validDraft는 본문과 답글이 길이 안에 드는지 본다.
func validDraft(d *AutoDraft) error {
	if d.Body == "" {
		return fmt.Errorf("본문이 비었다")
	}
	if n := CharCount(d.Body); n > bodyMaxChars {
		return fmt.Errorf("본문이 %d자로 상한 %d자를 넘는다", n, bodyMaxChars)
	}
	if n := CharCount(d.Detail); n > detailMaxChars {
		return fmt.Errorf("답글이 %d자로 상한 %d자를 넘는다", n, detailMaxChars)
	}
	return nil
}

// SearchURL은 상품을 찾아볼 검색 주소를 만든다.
//
// AI에게 상품 페이지 주소를 직접 쓰게 하지 않는다. 없는 상품 번호를
// 지어내면 404가 되기 때문이다. 검색 주소는 항상 실제 상품으로 이어진다.
func SearchURL(affiliate, productName string) string {
	q := url.QueryEscape(strings.TrimSpace(productName))
	if q == "" {
		return ""
	}
	switch affiliate {
	case AffiliateCoupang:
		return "https://www.coupang.com/np/search?q=" + q
	default:
		// 토스 쉐어링크는 앱에서만 만들 수 있어 웹 검색 주소가 쓸모가 적다.
		return ""
	}
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// splitParts는 --- 만 있는 줄을 기준으로 최대 n조각으로 나눈다.
func splitParts(raw string, n int) []string {
	var parts []string
	var cur []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == draftSeparator && len(parts) < n-1 {
			parts = append(parts, strings.TrimSpace(strings.Join(cur, "\n")))
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	parts = append(parts, strings.TrimSpace(strings.Join(cur, "\n")))
	return parts
}

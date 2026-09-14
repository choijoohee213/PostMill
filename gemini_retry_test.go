package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 테스트가 실제 백오프만큼 기다리지 않도록 짧게 바꾼다.
func shortBackoff(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoff = orig })
}

func fakeGemini(t *testing.T, handler http.HandlerFunc) (*Gemini, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	g := NewGemini("test-key")
	g.HTTP = srv.Client()
	g.BaseURL = srv.URL + "/"
	return g, &calls
}

func okBody(text string) string {
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%q}]},"finishReason":"STOP"}]}`, text)
}

func TestRetry_503은_다시_시도한다(t *testing.T) {
	shortBackoff(t)
	var n int
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"high demand"}}`)
			return
		}
		fmt.Fprint(w, okBody("세 번째에 성공한 본문"))
	})

	body, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0])
	if err != nil {
		t.Fatalf("재시도로 성공했어야 한다: %v", err)
	}
	if body != "세 번째에 성공한 본문" {
		t.Fatalf("본문이 다름: %q", body)
	}
	if *calls != 3 {
		t.Fatalf("호출 %d회, 3회여야 한다", *calls)
	}
}

func TestRetry_429도_다시_시도한다(t *testing.T) {
	shortBackoff(t)
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limit"}}`)
	})

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0]); err == nil {
		t.Fatal("계속 실패했으므로 에러여야 한다")
	}
	if *calls != maxAttempts {
		t.Fatalf("호출 %d회, %d회여야 한다", *calls, maxAttempts)
	}
}

func TestRetry_400은_다시_시도하지_않는다(t *testing.T) {
	shortBackoff(t)
	// 잘못된 키나 잘못된 요청은 다시 보내도 같은 결과다.
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"API key not valid"}}`)
	})

	_, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0])
	if err == nil {
		t.Fatal("에러여야 한다")
	}
	if *calls != 1 {
		t.Fatalf("호출 %d회, 1회여야 한다 (재시도 금지)", *calls)
	}
}

func TestRetry_길이초과는_다시_뽑는다(t *testing.T) {
	shortBackoff(t)
	// 생성은 매번 결과가 달라지므로 다시 뽑으면 짧게 나올 수 있다.
	var n int
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			fmt.Fprint(w, okBody(strings.Repeat("가", 500)))
			return
		}
		fmt.Fprint(w, okBody("짧은 본문"))
	})

	body, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 10, hookTypes[0])
	if err != nil {
		t.Fatalf("두 번째에 성공했어야 한다: %v", err)
	}
	if body != "짧은 본문" {
		t.Fatalf("본문이 다름: %q", body)
	}
	if *calls != 2 {
		t.Fatalf("호출 %d회, 2회여야 한다", *calls)
	}
}

func TestRetry_차단된_응답은_다시_시도하지_않는다(t *testing.T) {
	shortBackoff(t)
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"promptFeedback":{"blockReason":"SAFETY"}}`)
	})

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0]); err == nil {
		t.Fatal("차단은 에러여야 한다")
	}
	if *calls != 1 {
		t.Fatalf("호출 %d회, 1회여야 한다", *calls)
	}
}

func TestRetry_잘린_응답은_버린다(t *testing.T) {
	shortBackoff(t)
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"잘린 본"}]},"finishReason":"MAX_TOKENS"}]}`)
	})

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0]); err == nil {
		t.Fatal("STOP이 아니면 에러여야 한다")
	}
}

func quotaBody(quotaID, retryDelay string) string {
	return fmt.Sprintf(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[
		{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":%q}]},
		{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":%q}]}}`, quotaID, retryDelay)
}

// 하루 한도는 기다려도 오늘은 풀리지 않는다. 다시 보내면 실패만 쌓인다.
func TestRetry_하루_한도는_다시_시도하지_않는다(t *testing.T) {
	shortBackoff(t)
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, quotaBody("GenerateRequestsPerDayPerProjectPerModel-FreeTier", "36s"))
	})
	_, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0])
	if !errors.Is(err, errDailyQuota) {
		t.Fatalf("err=%v", err)
	}
	if *calls != 1 {
		t.Fatalf("호출 %d회, 한 번이어야 한다", *calls)
	}
	if msg := draftFailMessage(err); !strings.Contains(msg, "AI 사용량") {
		t.Fatalf("안내 문구=%q", msg)
	}
}

// 분당 한도는 알려준 시간만큼 기다리면 풀린다.
func TestRetry_분당_한도는_알려준_시간만큼_기다린다(t *testing.T) {
	shortBackoff(t)
	n := 0
	var gaps []time.Duration
	last := time.Now()
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		gaps = append(gaps, time.Since(last))
		last = time.Now()
		n++
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, quotaBody("GenerateRequestsPerMinutePerProjectPerModel-FreeTier", "0.2s"))
			return
		}
		fmt.Fprint(w, okBody("본문\n---\n디테일"))
	})
	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0]); err != nil {
		t.Fatal(err)
	}
	if n != 2 || gaps[1] < 200*time.Millisecond {
		t.Fatalf("호출 %d회, 두 번째까지 %v 기다렸다. 0.2초 이상이어야 한다", n, gaps[1])
	}
}

func TestRetry_너무_오래_기다리라면_포기한다(t *testing.T) {
	shortBackoff(t)
	g, calls := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, quotaBody("GenerateRequestsPerMinutePerProjectPerModel-FreeTier", "3600s"))
	})
	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400, hookTypes[0]); err == nil {
		t.Fatal("성공했다")
	}
	if *calls != 1 {
		t.Fatalf("호출 %d회, 한 번이어야 한다", *calls)
	}
}

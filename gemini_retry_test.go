package main

import (
	"context"
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

	body, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400)
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

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400); err == nil {
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

	_, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400)
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

	body, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 10)
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

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400); err == nil {
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

	if _, _, err := g.GenerateDraft(context.Background(), AffiliateCoupang, "메모", 400); err == nil {
		t.Fatal("STOP이 아니면 에러여야 한다")
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// promptRecorder는 모델에게 간 요청 문장을 모은다.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (p *promptRecorder) gemini(t *testing.T, reply string) *Gemini {
	t.Helper()
	g, _ := fakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		var req geminiRequest
		json.NewDecoder(r.Body).Decode(&req)
		p.mu.Lock()
		p.prompts = append(p.prompts, req.Contents[0].Parts[0].Text)
		p.mu.Unlock()
		fmt.Fprint(w, okBody(reply))
	})
	return g
}

func TestHook_세_가지_생성_모두_훅을_모델에게_알린다(t *testing.T) {
	h := hookTypes[2]
	ctx := context.Background()

	rec := &promptRecorder{}
	if _, _, err := rec.gemini(t, "본문\n---\n디테일").GenerateDraft(ctx, AffiliateCoupang, "메모", 400, h); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.gemini(t, "상품\n---\n본문\n---\n답1\n---\n답2").SuggestDraft(ctx, AffiliateCoupang, nil, "", h); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rec.gemini(t, "1\n---\n본문\n---\n답1\n---\n답2").SuggestFromToss(ctx, []TossProduct{{DisplayName: "a"}}, h); err != nil {
		t.Fatal(err)
	}
	if len(rec.prompts) != 3 {
		t.Fatalf("요청 %d개", len(rec.prompts))
	}
	for i, p := range rec.prompts {
		if !strings.Contains(p, "훅은 "+h.Name) || !strings.Contains(p, h.Guide) {
			t.Errorf("%d번째 요청에 훅 안내가 없다:\n%s", i, p)
		}
	}
}

func TestHook_프롬프트에_고정_구조가_남아있지_않다(t *testing.T) {
	for name, p := range map[string]string{"draft": draftSystemPrompt, "auto": autoSystemPrompt, "toss": tossSystemPrompt} {
		if strings.Contains(p, "겪던 불편 한 줄") {
			t.Errorf("%s 프롬프트에 예전 고정 구성이 남았다", name)
		}
		if !strings.Contains(p, "훅 유형") {
			t.Errorf("%s 프롬프트가 훅 유형을 따르라고 하지 않는다", name)
		}
	}
}

func TestHandleAuto_세_장이_서로_다른_훅으로_시작한다(t *testing.T) {
	a := manualApp(t, nil)
	rec := &promptRecorder{}
	a.gemini = rec.gemini(t, "상품\n---\n본문\n---\n답1\n---\n답2")
	const user = "hook-batch"

	login := httptest.NewRecorder()
	a.session.issue(login, user, false)
	form := url.Values{"affiliate": {"coupang"}}
	req := httptest.NewRequest(http.MethodPost, "/auto", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookieFrom(t, login))
	a.handleAuto(httptest.NewRecorder(), req)

	posts, _ := a.db.ListByStatus(context.Background(), user, "", StatusGenerating, StatusPending)
	for _, p := range posts {
		t.Cleanup(func() { a.db.DeletePost(context.Background(), user, p.ID) })
		waitGenerated(t, a.db, user, p.ID)
	}

	used := map[string]bool{}
	for _, p := range rec.prompts {
		for _, h := range hookTypes {
			if strings.Contains(p, "훅은 "+h.Name) {
				used[h.Name] = true
			}
		}
	}
	if len(rec.prompts) != autoBatchSize || len(used) != autoBatchSize {
		t.Fatalf("요청 %d개, 쓴 훅 %v. 세 장이 모두 달라야 한다", len(rec.prompts), used)
	}
}

// 메모 없이 만들면 모델이 없는 기능을 지어냈다. 세 프롬프트 모두 같은 사실 규칙을 따라야 한다.
func TestFactRules_모든_프롬프트가_사실_규칙을_담는다(t *testing.T) {
	for name, p := range map[string]string{"draft": draftSystemPrompt, "auto": autoSystemPrompt, "toss": tossSystemPrompt} {
		if !strings.Contains(p, factRules) {
			t.Errorf("%s 프롬프트에 사실 규칙이 없다", name)
		}
		// 기능 이야기를 부르던 예전 안내가 남으면 안 된다.
		if strings.Contains(p, "관리나 보관") || strings.Contains(p, "구체적인 장면이나\n  써보고") {
			t.Errorf("%s 프롬프트에 기능 이야기를 부르는 안내가 남았다", name)
		}
	}
}

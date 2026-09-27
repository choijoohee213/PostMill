package main

// 임시 진단용. 토스 상품 응답에 이미지 URL이 들어오는지 확인하려고 둔다.
// 토스 Open API는 등록된 서버에서만 응답하므로 로컬에서는 확인할 수 없어
// 배포 환경에서 한 번 열어보고 이 파일과 main.go의 라우트를 지운다.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func (a *app) handleTossFields(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if a.toss == nil {
		fmt.Fprintln(w, "토스 API 키가 없습니다.")
		return
	}

	token, err := a.lockedTossToken(r.Context())
	if err != nil {
		fmt.Fprintf(w, "토큰 발급 실패: %v\n", err)
		return
	}

	for _, path := range []string{"products/best-selling?size=1", "products/today-deals?size=1"} {
		fmt.Fprintf(w, "=== %s ===\n", path)

		var out struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := a.toss.do(r.Context(), token, http.MethodGet, path, nil, &out); err != nil {
			fmt.Fprintf(w, "호출 실패: %v\n\n", err)
			continue
		}
		if len(out.Items) == 0 {
			fmt.Fprint(w, "items 비어 있음\n\n")
			continue
		}

		item := out.Items[0]
		keys := make([]string, 0, len(item))
		for k := range item {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "필드: %s\n\n", strings.Join(keys, ", "))

		for _, k := range keys {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "image") || strings.Contains(lower, "img") ||
				strings.Contains(lower, "thumb") || strings.Contains(lower, "photo") {
				fmt.Fprintf(w, "이미지로 보이는 필드: %s = %s\n", k, item[k])
			}
		}

		pretty, _ := json.MarshalIndent(item, "", "  ")
		fmt.Fprintf(w, "\n첫 상품 전체:\n%s\n\n", pretty)
	}
}

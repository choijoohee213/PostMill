package main

// 임시 진단용. 상세 조회(products/detail)에 가격이 들어오는지 확인하려고 둔다.
// 게시 직전에 가격이 그대로인지 검증할 수 있어야 글에 가격을 쓸 수 있다.
// 토스 Open API는 등록된 서버에서만 응답하므로 배포 환경에서 한 번 열어보고
// 이 파일과 main.go의 라우트를 지운다.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

	dump := func(label, path string) []map[string]json.RawMessage {
		fmt.Fprintf(w, "=== %s ===\n", path)
		var out struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := a.toss.do(r.Context(), token, http.MethodGet, path, nil, &out); err != nil {
			fmt.Fprintf(w, "호출 실패: %v\n\n", err)
			return nil
		}
		if len(out.Items) == 0 {
			fmt.Fprint(w, "items 비어 있음\n\n")
			return nil
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
			if strings.Contains(lower, "price") || strings.Contains(lower, "discount") ||
				strings.Contains(lower, "sale") || strings.Contains(lower, "end") {
				fmt.Fprintf(w, "가격으로 보이는 필드: %s = %s\n", k, item[k])
			}
		}

		pretty, _ := json.MarshalIndent(item, "", "  ")
		fmt.Fprintf(w, "\n첫 상품 전체:\n%s\n\n", pretty)
		return out.Items
	}

	// 목록에서 상품 하나를 고른 뒤 그 id로 상세를 조회한다.
	// 목록에 가격이 있는 것은 이미 안다. 상세에도 있는지가 관건이다.
	items := dump("목록", "products/best-selling?size=1")
	if len(items) == 0 {
		return
	}
	raw, ok := items[0]["tacaItemId"]
	if !ok {
		fmt.Fprintln(w, "목록 응답에 tacaItemId가 없어 상세를 조회할 수 없습니다.")
		return
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		fmt.Fprintf(w, "tacaItemId를 읽지 못했습니다: %v\n", err)
		return
	}
	dump("상세", "products/detail?tacaItemIds="+strconv.FormatInt(id, 10))
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 토스 쉐어링크 Open API. 운영 환경만 있다.
// https://sharelink-docs.toss.im/developers/developers/open-api
const (
	tossAPI      = "https://sharelink.toss.im/openapi/"
	tossTokenAPI = "https://oauth2.cert.toss.im/token"
)

// Toss는 쉐어링크 Open API를 표준 net/http로 호출한다.
type Toss struct {
	HTTP     *http.Client
	BaseURL  string // 테스트에서 가짜 서버를 가리키기 위해 주입할 수 있다
	TokenURL string

	AccessKey   string
	SecretKey   string
	PublisherID string // 링크 발급 주체. 수익이 이 값으로 귀속된다
}

func NewToss(accessKey, secretKey, publisherID string) *Toss {
	return &Toss{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		BaseURL:     tossAPI,
		TokenURL:    tossTokenAPI,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		PublisherID: publisherID,
	}
}

// TossProduct는 상품 목록 API의 상품 카드다. 쓰는 필드만 받는다.
type TossProduct struct {
	TacaItemID    int64   `json:"tacaItemId"`
	DisplayName   string  `json:"displayName"`
	ProductURL    string  `json:"productUrl"` // 추적이 없는 일반 링크. 게시글에 넣지 않는다
	DisplayPrice  int64   `json:"displayPrice"`
	OriginalPrice int64   `json:"originalPrice"`
	DiscountRate  float64 `json:"discountRate"`
	IsSoldOut     bool    `json:"isSoldOut"`
	ReviewScore   float64 `json:"reviewScore"`
	ReviewCount   int     `json:"reviewCount"`
	EndAt         string  `json:"endAt"` // 하루특가에만 있다
}

// tossError는 토스가 실패로 응답한 경우다. HTTP 200이어도 실패일 수 있다.
type tossError struct {
	HTTPStatus int
	Code       string
	Reason     string
}

func (e *tossError) Error() string {
	return fmt.Sprintf("토스 API 실패 (HTTP %d, %s): %s", e.HTTPStatus, e.Code, e.Reason)
}

// 토스가 알려주는 대표적인 실패 코드.
const (
	tossAccessDenied  = "SHARELINK_OPENAPI_ACCESS_DENIED"
	tossQuotaExceeded = "SHARELINK_OPENAPI_QUOTA_EXCEEDED"
)

// IssueToken은 액세스 토큰을 새로 받는다. 유효기간이 1년 가까이 되므로
// 호출할 때마다 부르지 말고 저장해 두고 쓴다 (과도한 재발급은 제한된다).
func (t *Toss) IssueToken(ctx context.Context) (string, time.Time, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", t.AccessKey)
	form.Set("client_secret", t.SecretKey)
	form.Set("scope", "sharelink:read sharelink:write")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("토스 토큰 발급 실패 (HTTP %d)", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("토스 토큰 응답을 해석하지 못했다")
	}
	return out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// BestSelling은 카테고리 구분 없이 많이 팔리는 상품이다. 1시간 단위로 갱신된다.
func (t *Toss) BestSelling(ctx context.Context, token string, size int) ([]TossProduct, error) {
	return t.listProducts(ctx, token, "products/best-selling", size)
}

// TodayDeals는 그날 하루만 파는 특가 상품이다. 편성이 없으면 0건이다.
func (t *Toss) TodayDeals(ctx context.Context, token string, size int) ([]TossProduct, error) {
	return t.listProducts(ctx, token, "products/today-deals", size)
}

func (t *Toss) listProducts(ctx context.Context, token, path string, size int) ([]TossProduct, error) {
	var out struct {
		Items []TossProduct `json:"items"`
	}
	q := url.Values{}
	q.Set("size", strconv.Itoa(size))
	if err := t.do(ctx, token, http.MethodGet, path+"?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateLink는 상품의 쉐어링크를 발급한다. 수익은 이 링크로만 집계된다.
// 같은 상품을 다시 요청하면 기존 링크가 돌아오고 한도를 쓰지 않는다.
func (t *Toss) CreateLink(ctx context.Context, token string, tacaItemID int64) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"tacaItemId":  tacaItemID,
		"publisherId": t.PublisherID,
	})
	var out struct {
		ShortURL  string `json:"shortUrl"`
		OriginURL string `json:"originUrl"`
	}
	if err := t.do(ctx, token, http.MethodPost, "links", body, &out); err != nil {
		return "", err
	}
	if out.ShortURL != "" {
		return out.ShortURL, nil
	}
	if out.OriginURL != "" {
		return out.OriginURL, nil
	}
	return "", fmt.Errorf("토스가 링크를 돌려주지 않았다")
}

// do는 공통 응답 형식({resultType, success, error})을 풀어 success를 out에 담는다.
func (t *Toss) do(ctx context.Context, token, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}

	var env struct {
		ResultType string          `json:"resultType"`
		Success    json.RawMessage `json:"success"`
		Error      struct {
			ErrorCode string `json:"errorCode"`
			Reason    string `json:"reason"`
		} `json:"error"`
	}
	// 게이트웨이에서 먼저 거절하면 공통 형식이 아닐 수 있다.
	if err := json.Unmarshal(raw, &env); err != nil || env.ResultType == "" {
		return &tossError{HTTPStatus: resp.StatusCode, Reason: "응답 형식이 아니다"}
	}
	// HTTP 200이어도 FAIL이면 실패다.
	if env.ResultType != "SUCCESS" {
		return &tossError{HTTPStatus: resp.StatusCode, Code: env.Error.ErrorCode, Reason: env.Error.Reason}
	}
	return json.Unmarshal(env.Success, out)
}

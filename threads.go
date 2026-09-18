package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const threadsAPI = "https://graph.threads.net/v1.0/"
const threadsTokenAPI = "https://graph.threads.net/"

// app_state에 저장하는 키.
const (
	stateAccessToken = "threads_access_token"
	stateExpiresAt   = "threads_token_expires_at"
)

// Threads는 Graph API를 표준 net/http로 호출한다.
type Threads struct {
	HTTP    *http.Client
	BaseURL string // 그래프 API. 테스트에서 가짜 서버를 가리키기 위해 주입할 수 있다

	// 토큰 발급·갱신은 그래프 API와 호스트가 달라 따로 둔다.
	TokenURL string
}

func NewThreads() *Threads {
	return &Threads{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		BaseURL:  threadsAPI,
		TokenURL: threadsTokenAPI,
	}
}

type threadsError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// apiError는 Threads가 상태 코드와 함께 돌려준 오류다.
// 잠시 뒤 다시 하면 되는 오류인지 가리기 위해 코드를 들고 다닌다.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Threads API 오류 (HTTP %d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Threads API 오류 (HTTP %d)", e.Status)
}

// transient는 Threads 쪽 일시적인 오류인지 본다. 실제로 답글을 올리다
// "HTTP 500: An unexpected error has occurred. Please retry your request later."를
// 받아 링크가 빠진 적이 있다. 429도 조금 기다리면 풀린다.
func transient(err error) bool {
	var e *apiError
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests
	}
	// 연결이 끊기거나 응답이 늦은 것도 다시 해볼 만하다.
	// 시간이 다 된 경우는 기다려도 소용없다.
	var netErr *url.Error
	return errors.As(err, &netErr) &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// retryDelays는 일시적 오류를 다시 시도하기까지 기다리는 시간이다.
var retryDelays = []time.Duration{2 * time.Second, 6 * time.Second}

// retryTransient는 일시적 오류면 잠시 뒤 다시 한다.
//
// 같은 요청을 두 번 보내도 괜찮은 곳에만 쓴다. 컨테이너를 또 만드는 것은
// 발행하지 않으면 사라지므로 안전하지만, 발행(threads_publish)은 그렇지 않다.
func retryTransient[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	out, err := fn()
	for i := 0; err != nil && transient(err) && i < len(retryDelays); i++ {
		select {
		case <-time.After(retryDelays[i]):
		case <-ctx.Done():
			return out, err
		}
		out, err = fn()
	}
	return out, err
}

// post는 Threads API에 POST하고 JSON 응답의 id를 반환한다.
func (t *Threads) post(ctx context.Context, path string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		return "", &apiError{Status: resp.StatusCode, Message: e.Error.Message}
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("응답을 해석하지 못했다")
	}
	if out.ID == "" {
		return "", fmt.Errorf("응답에 id가 없다")
	}
	return out.ID, nil
}

// createContainer는 발행할 내용을 담은 컨테이너를 만든다.
// replyTo가 비어 있지 않으면 그 글에 대한 답글이 된다.
// topic이 있으면 글의 주제로 붙는다 (답글에는 붙지 않는다).
func (t *Threads) createContainer(ctx context.Context, token, text, replyTo, topic string) (string, error) {
	form := url.Values{}
	form.Set("media_type", "TEXT")
	form.Set("text", text)
	form.Set("access_token", token)
	if replyTo != "" {
		form.Set("reply_to_id", replyTo)
	}
	if topic != "" && replyTo == "" {
		form.Set("topic_tag", topic)
	}
	// 컨테이너는 발행하지 않으면 사라지므로 다시 만들어도 안전하다.
	return retryTransient(ctx, func() (string, error) {
		return t.post(ctx, "me/threads", form)
	})
}

func (t *Threads) publishContainer(ctx context.Context, token, containerID string) (string, error) {
	form := url.Values{}
	form.Set("creation_id", containerID)
	form.Set("access_token", token)
	return t.post(ctx, "me/threads_publish", form)
}

// 컨테이너는 만든 직후 IN_PROGRESS 상태이고, 준비되면 FINISHED가 된다.
// 준비되기 전에 발행하면 실패하므로 기다렸다가 발행한다.
var (
	containerPollInterval = 2 * time.Second
	containerPollTimeout  = 60 * time.Second
)

// waitForContainer는 컨테이너가 발행 가능한 상태가 될 때까지 기다린다.
func (t *Threads) waitForContainer(ctx context.Context, token, containerID string) error {
	deadline := time.Now().Add(containerPollTimeout)
	for {
		status, errMsg, err := t.containerStatus(ctx, token, containerID)
		if err != nil {
			// 일시적인 오류면 다음 차례에 다시 물어본다.
			if !transient(err) || time.Now().After(deadline) {
				return err
			}
			select {
			case <-time.After(containerPollInterval):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		switch status {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			if errMsg != "" {
				return fmt.Errorf("컨테이너가 %s 상태다: %s", status, errMsg)
			}
			return fmt.Errorf("컨테이너가 %s 상태다", status)
		}
		// IN_PROGRESS 또는 알 수 없는 상태면 조금 더 기다린다.
		if time.Now().After(deadline) {
			return fmt.Errorf("컨테이너가 %s 상태에서 %v 안에 준비되지 않았다", status, containerPollTimeout)
		}
		select {
		case <-time.After(containerPollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *Threads) containerStatus(ctx context.Context, token, containerID string) (string, string, error) {
	u := fmt.Sprintf("%s%s?fields=status,error_message&access_token=%s",
		t.BaseURL, containerID, url.QueryEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		return "", "", fmt.Errorf("컨테이너 상태 조회 실패: %w",
			&apiError{Status: resp.StatusCode, Message: e.Error.Message})
	}

	var out struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("컨테이너 상태 응답을 해석하지 못했다")
	}
	return out.Status, out.ErrorMessage, nil
}

// PublishText는 컨테이너를 만들고, 준비될 때까지 기다린 뒤 발행한다.
// replyTo가 비어 있지 않으면 그 글에 대한 답글이 된다.
// topic은 본문 글의 주제다. 답글에는 주제를 붙일 수 없어 무시된다.
//
// 사슬을 잇는 일은 이 함수가 하지 않는다. 각 단계 결과를 DB에 남겨야
// 중간에 끊겨도 이어서 마칠 수 있으므로, 순서는 앱이 관리한다.
func (t *Threads) PublishText(ctx context.Context, token, text, replyTo, topic string) (string, error) {
	containerID, err := t.createContainer(ctx, token, text, replyTo, topic)
	if err != nil {
		return "", err
	}
	if err := t.waitForContainer(ctx, token, containerID); err != nil {
		return "", err
	}
	return t.publishContainer(ctx, token, containerID)
}

// PublishImages는 사진이 붙은 게시물을 올린다. 답글에는 쓰지 않는다.
//
// 한 장이면 IMAGE, 여러 장이면 CAROUSEL이다. 캐러셀은 사진마다 항목
// 컨테이너를 먼저 만들고, 그 id들을 묶은 컨테이너를 발행한다.
func (t *Threads) PublishImages(ctx context.Context, token, text, topic string, imageURLs []string) (string, error) {
	form := url.Values{}
	form.Set("text", text)
	form.Set("access_token", token)
	if topic != "" {
		form.Set("topic_tag", topic)
	}

	if len(imageURLs) == 1 {
		form.Set("media_type", "IMAGE")
		form.Set("image_url", imageURLs[0])
	} else {
		children := make([]string, 0, len(imageURLs))
		for _, u := range imageURLs {
			item := url.Values{}
			item.Set("media_type", "IMAGE")
			item.Set("image_url", u)
			item.Set("is_carousel_item", "true")
			item.Set("access_token", token)
			id, err := retryTransient(ctx, func() (string, error) {
				return t.post(ctx, "me/threads", item)
			})
			if err != nil {
				return "", err
			}
			if err := t.waitForContainer(ctx, token, id); err != nil {
				return "", err
			}
			children = append(children, id)
		}
		form.Set("media_type", "CAROUSEL")
		form.Set("children", strings.Join(children, ","))
	}

	containerID, err := retryTransient(ctx, func() (string, error) {
		return t.post(ctx, "me/threads", form)
	})
	if err != nil {
		return "", err
	}
	if err := t.waitForContainer(ctx, token, containerID); err != nil {
		return "", err
	}
	return t.publishContainer(ctx, token, containerID)
}

// Permalink는 올라간 글의 주소를 조회한다.
func (t *Threads) Permalink(ctx context.Context, token, postID string) (string, error) {
	u := fmt.Sprintf("%s%s?fields=permalink&access_token=%s",
		t.BaseURL, postID, url.QueryEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Permalink string `json:"permalink"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("퍼머링크 조회 실패 (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Permalink == "" {
		return "", fmt.Errorf("퍼머링크가 응답에 없다")
	}
	return out.Permalink, nil
}

// refreshWindow만큼 만료가 남지 않았으면 갱신한다.
// 토큰은 60일짜리이고 하루 3~4회 접속하므로 여유를 크게 잡아도 놓치지 않는다.
const refreshWindow = 7 * 24 * time.Hour

// RefreshToken은 장기 토큰을 갱신하고 새 토큰과 만료 시각을 반환한다.
func (t *Threads) RefreshToken(ctx context.Context, token string) (string, time.Time, error) {
	return t.refreshAt(ctx, t.TokenURL, token)
}

// refreshAt은 갱신 엔드포인트를 인자로 받는다. 이 엔드포인트는 BaseURL과
// 호스트가 달라서 테스트에서 따로 주입해야 한다.
func (t *Threads) refreshAt(ctx context.Context, base, token string) (string, time.Time, error) {
	u := fmt.Sprintf("%srefresh_access_token?grant_type=th_refresh_token&access_token=%s",
		base, url.QueryEscape(token))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return "", time.Time{}, fmt.Errorf("토큰 갱신 실패: %s", e.Error.Message)
		}
		return "", time.Time{}, fmt.Errorf("토큰 갱신 실패 (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("갱신 응답을 해석하지 못했다")
	}
	return out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// ExchangeToken은 대시보드에서 받은 1시간짜리 단기 토큰을
// 60일짜리 장기 토큰으로 교환한다.
func (t *Threads) ExchangeToken(ctx context.Context, appSecret, shortToken string) (string, time.Time, error) {
	return t.exchangeAt(ctx, t.TokenURL, appSecret, shortToken)
}

func (t *Threads) exchangeAt(ctx context.Context, base, appSecret, shortToken string) (string, time.Time, error) {
	u := fmt.Sprintf("%saccess_token?grant_type=th_exchange_token&client_secret=%s&access_token=%s",
		base, url.QueryEscape(appSecret), url.QueryEscape(shortToken))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return "", time.Time{}, fmt.Errorf("%s", e.Error.Message)
		}
		return "", time.Time{}, fmt.Errorf("토큰 교환 실패 (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("교환 응답을 해석하지 못했다")
	}
	return out.AccessToken, time.Now().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// threadsScopes는 이 앱이 필요한 권한이다.
// 답글로 링크를 올리므로 threads_manage_replies가 필요하다.
// 토큰 생성기에서 이 권한들이 모두 켜져 있어야 한다.
const threadsScopes = "threads_basic,threads_content_publish,threads_manage_replies"

// ThreadsMe는 토큰이 가리키는 계정이다.
type ThreadsMe struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Me는 토큰의 주인이 누구인지 확인한다.
func (t *Threads) Me(ctx context.Context, token string) (*ThreadsMe, error) {
	u := fmt.Sprintf("%sme?fields=id,username&access_token=%s", t.BaseURL, url.QueryEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return nil, fmt.Errorf("%s", e.Error.Message)
		}
		return nil, fmt.Errorf("계정 조회 실패 (HTTP %d)", resp.StatusCode)
	}

	var me ThreadsMe
	if err := json.Unmarshal(raw, &me); err != nil || me.ID == "" {
		return nil, fmt.Errorf("계정 응답을 해석하지 못했다")
	}
	return &me, nil
}

package main

import (
	"context"
	"encoding/json"
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
		if e.Error.Message != "" {
			return "", fmt.Errorf("Threads API 오류 (HTTP %d): %s", resp.StatusCode, e.Error.Message)
		}
		return "", fmt.Errorf("Threads API 오류 (HTTP %d)", resp.StatusCode)
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
func (t *Threads) createContainer(ctx context.Context, token, text, replyTo string) (string, error) {
	form := url.Values{}
	form.Set("media_type", "TEXT")
	form.Set("text", text)
	form.Set("access_token", token)
	if replyTo != "" {
		form.Set("reply_to_id", replyTo)
	}
	return t.post(ctx, "me/threads", form)
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
			return err
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
		if e.Error.Message != "" {
			return "", "", fmt.Errorf("컨테이너 상태 조회 실패: %s", e.Error.Message)
		}
		return "", "", fmt.Errorf("컨테이너 상태 조회 실패 (HTTP %d)", resp.StatusCode)
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

// publishText는 컨테이너를 만들고, 준비될 때까지 기다린 뒤 발행한다.
func (t *Threads) publishText(ctx context.Context, token, text, replyTo string) (string, error) {
	containerID, err := t.createContainer(ctx, token, text, replyTo)
	if err != nil {
		return "", err
	}
	if err := t.waitForContainer(ctx, token, containerID); err != nil {
		return "", err
	}
	return t.publishContainer(ctx, token, containerID)
}

// PublishResult는 발행 결과다. 답글 실패는 본문 발행을 무효로 만들지 않는다.
type PublishResult struct {
	PostID    string
	Permalink string
	ReplyErr  error // 본문은 올라갔지만 링크 답글에 실패한 경우
}

// Publish는 본문을 올리고, 디테일과 링크를 답글로 이어 단다.
//
// 본문 → 디테일 답글 → 링크 답글 순으로 사슬처럼 이어진다. 디테일이
// 비어 있으면 링크를 본문에 바로 단다.
//
// 본문 발행에 성공한 뒤 답글이 실패하면 ReplyErr에 담아 반환한다.
// 이때 게시물은 이미 스레드에 올라가 있으므로 실패로 되돌리면 안 된다.
// 되돌리면 사용자가 다시 발행을 눌러 같은 글이 두 번 올라간다.
func (t *Threads) Publish(ctx context.Context, token, body string, details []string, replyText string) (*PublishResult, error) {
	postID, err := t.publishText(ctx, token, body, "")
	if err != nil {
		return nil, err
	}

	res := &PublishResult{PostID: postID}

	// 답글을 차례로 이어 달고, 링크는 사슬의 끝에 단다.
	// 중간 답글이 실패해도 멈추지 않는다. 마지막 성공 지점에 이어 붙인다.
	parent := postID
	addErr := func(label string, err error) {
		e := fmt.Errorf("%s: %w", label, err)
		if res.ReplyErr == nil {
			res.ReplyErr = e
		} else {
			res.ReplyErr = fmt.Errorf("%v, %w", res.ReplyErr, e)
		}
	}

	for i, d := range details {
		if strings.TrimSpace(d) == "" {
			continue
		}
		id, err := t.publishText(ctx, token, d, parent)
		if err != nil {
			addErr(fmt.Sprintf("답글 %d", i+1), err)
			continue
		}
		parent = id
	}

	if _, err := t.publishText(ctx, token, replyText, parent); err != nil {
		addErr("링크 답글", err)
	}

	// 퍼머링크 조회에 실패해도 발행은 성공이다. 링크만 비워둔다.
	if link, err := t.permalink(ctx, token, postID); err == nil {
		res.Permalink = link
	}
	return res, nil
}

func (t *Threads) permalink(ctx context.Context, token, postID string) (string, error) {
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

// threadsAuthorizeURL은 사용자가 권한을 승인하는 화면이다.
//
// threads.net은 www.threads.com으로 301 리다이렉트된다. 모바일에서는
// 그 한 번의 중간 이동에서 Threads 앱이 링크를 가로채 흐름이 끊기는 일이
// 잦으므로, 처음부터 최종 주소로 보낸다.
const threadsAuthorizeURL = "https://www.threads.com/oauth/authorize"

// threadsScopes는 이 앱이 필요한 권한이다.
// 답글로 링크를 올리므로 threads_manage_replies가 필요하다.
const threadsScopes = "threads_basic,threads_content_publish,threads_manage_replies"

// AuthorizeURL은 사용자를 보낼 승인 화면 주소를 만든다.
//
// forceReauth를 켜면 브라우저에 남은 Meta 세션을 무시하고 다시 로그인하게
// 한다. 인스타 계정이 여러 개 묶여 있으면 승인이 대표 계정으로 즉시
// 처리되어 계정을 고를 틈이 없는데, 그때 쓰는 길이다.
func AuthorizeURL(appID, redirectURI, state string, forceReauth bool) string {
	q := url.Values{}
	q.Set("client_id", appID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", threadsScopes)
	q.Set("response_type", "code")
	q.Set("state", state)
	if forceReauth {
		q.Set("force_reauth", "true")
	}
	return threadsAuthorizeURL + "?" + q.Encode()
}

// CodeToToken은 콜백으로 받은 code를 1시간짜리 단기 토큰으로 바꾼다.
func (t *Threads) CodeToToken(ctx context.Context, appID, appSecret, redirectURI, code string) (string, error) {
	return t.codeToTokenAt(ctx, t.TokenURL, appID, appSecret, redirectURI, code)
}

func (t *Threads) codeToTokenAt(ctx context.Context, base, appID, appSecret, redirectURI, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", appID)
	form.Set("client_secret", appSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e threadsError
		json.Unmarshal(raw, &e)
		if e.Error.Message != "" {
			return "", fmt.Errorf("%s", e.Error.Message)
		}
		return "", fmt.Errorf("코드 교환 실패 (HTTP %d)", resp.StatusCode)
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("코드 교환 응답을 해석하지 못했다")
	}
	return out.AccessToken, nil
}

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

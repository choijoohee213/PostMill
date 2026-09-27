package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tossConn은 한 사용자가 쓸 토스 연결이다. 어느 키로 부르는지(api)와 그 키로
// 받아 둔 토큰(token)을 함께 들고 다닌다.
//
// 토스 키는 사업자당 하나라, 한 키를 여러 명이 쓰면 링크도 정산도 그 한 계정으로
// 모인다. 자기 키를 등록한 사용자는 자기 계정으로 발급되어 토스가 그 사람에게
// 직접 정산한다. 등록하지 않았으면 서버 공용 키(환경변수)를 쓴다.
type tossConn struct {
	api   *Toss
	token string
	own   bool // 사용자가 등록한 자기 키인지
}

// errNoTossKey는 쓸 수 있는 토스 키가 하나도 없는 경우다.
var errNoTossKey = errors.New("토스 키가 없다")

// tossAPIFor는 이 사용자가 쓸 토스 클라이언트다. 키가 없으면 nil을 돌려준다.
// 화면에 토스 기능을 보여줄지 정하는 데에도 쓴다.
func (a *app) tossAPIFor(ctx context.Context, userID string) (*Toss, bool) {
	if userID != "" {
		k, ok, err := a.db.GetTossKey(ctx, userID)
		if err != nil {
			log.Printf("토스 키 조회 실패 (%s): %v", userID, err)
		} else if ok {
			return NewToss(k.AccessKey, k.SecretKey, k.PublisherID), true
		}
	}
	if a.toss == nil {
		return nil, false
	}
	return a.toss, false
}

// tossConnFor는 이 사용자의 연결을 토큰까지 갖춰 돌려준다.
func (a *app) tossConnFor(ctx context.Context, userID string) (*tossConn, error) {
	api, own := a.tossAPIFor(ctx, userID)
	if api == nil {
		return nil, errNoTossKey
	}
	a.tossState.mu.Lock()
	defer a.tossState.mu.Unlock()
	return a.connToken(ctx, api, own)
}

// connToken은 저장된 토큰을 쓰고, 없거나 곧 만료되면 새로 받는다.
// 토큰은 키마다 하나이므로 공용 키를 쓰는 사람들은 같은 토큰을 나눠 쓴다.
// tossState.mu를 잡은 채로 부른다.
func (a *app) connToken(ctx context.Context, api *Toss, own bool) (*tossConn, error) {
	if token, exp, ok, err := a.db.GetTossToken(ctx, api.PublisherID); err != nil {
		return nil, err
	} else if ok && time.Until(exp) > tossTokenMargin {
		return &tossConn{api: api, token: token, own: own}, nil
	}

	token, exp, err := api.IssueToken(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.db.SetTossToken(ctx, api.PublisherID, token, exp); err != nil {
		return nil, err
	}
	return &tossConn{api: api, token: token, own: own}, nil
}

// forget은 401을 받은 토큰을 버린다. 다음 시도에서 새로 받는다.
func (a *app) forget(ctx context.Context, c *tossConn, err error) {
	var te *tossError
	if c == nil || !errors.As(err, &te) || te.HTTPStatus != 401 {
		return
	}
	if err := a.db.ClearTossToken(ctx, c.api.PublisherID); err != nil {
		log.Printf("토스 토큰 삭제 실패: %v", err)
	}
}

// subTagKey는 subTag 등록을 기억할 때 쓰는 키다. 같은 subTag라도 키가 다르면
// 등록된 계정이 다르므로 키마다 따로 기억한다.
func subTagKey(c *tossConn, tag string) string { return c.api.PublisherID + "/" + tag }

// handleTossKey는 설정 화면에서 넣은 토스 키를 저장한다.
func (a *app) handleTossKey(w http.ResponseWriter, r *http.Request) {
	userID := a.session.userID(r)
	if userID == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	k := &TossKey{
		UserID:      userID,
		AccessKey:   strings.TrimSpace(r.FormValue("access_key")),
		SecretKey:   strings.TrimSpace(r.FormValue("secret_key")),
		PublisherID: strings.TrimSpace(r.FormValue("publisher_id")),
	}
	if k.AccessKey == "" || k.SecretKey == "" || k.PublisherID == "" {
		settingsFailed(w, r, "세 칸을 모두 채워주세요.")
		return
	}

	// 토큰을 받아 본다. 여기서 막히면 저장해도 쓸 수 없는 키다.
	api := NewToss(k.AccessKey, k.SecretKey, k.PublisherID)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token, exp, err := api.IssueToken(ctx)
	if err != nil {
		log.Printf("토스 키 확인 실패 (%s): %v", userID, err)
		settingsFailed(w, r, "토스가 이 키를 받지 않았어요. 키와 publisher ID를 다시 확인해주세요.")
		return
	}

	if err := a.db.SetTossKey(ctx, k); err != nil {
		log.Printf("토스 키 저장 실패 (%s): %v", userID, err)
		settingsFailed(w, r, "키를 저장하지 못했어요.")
		return
	}
	if err := a.db.SetTossToken(ctx, k.PublisherID, token, exp); err != nil {
		log.Printf("토스 토큰 저장 실패 (%s): %v", userID, err)
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// handleTossKeyDelete는 등록한 키를 지운다. 이후로는 공용 키를 쓴다.
func (a *app) handleTossKeyDelete(w http.ResponseWriter, r *http.Request) {
	userID := a.session.userID(r)
	if userID == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := a.db.DeleteTossKey(r.Context(), userID); err != nil {
		log.Printf("토스 키 삭제 실패 (%s): %v", userID, err)
		settingsFailed(w, r, "키를 지우지 못했어요.")
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func settingsFailed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/settings?error="+url.QueryEscape(msg), http.StatusSeeOther)
}

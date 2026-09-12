package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// publishTimeout은 게시 한 건에 허용하는 시간이다.
// 게시물과 답글 세 개를 차례로 올리고 각 단계마다 컨테이너가 준비될
// 때까지 기다리므로 넉넉히 잡는다.
const publishTimeout = 8 * time.Minute

// startPublish는 게시를 백그라운드로 넘긴다.
//
// 요청 안에서 처리하면 안 된다. 게시는 네 번의 발행과 그만큼의 대기로
// 이루어져 요청 시간을 넘기고, 요청이 끊기면 컨텍스트가 취소되어
// 답글이 중간에 빠진 채 멈춘다. 실제로 그런 일이 있었다.
func (a *app) startPublish(p *Post, token, body, link string) {
	go a.runPublish(p, token, body, link)
}

// runPublish는 게시물과 답글을 차례로 올리며 각 단계를 DB에 남긴다.
//
// 남기는 이유는 중간에 끊겼을 때 이어서 마치기 위해서다. 어디까지
// 올라갔는지 모르면 같은 글을 다시 올리게 된다.
func (a *app) runPublish(p *Post, token, body, link string) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	fail := func(reason string, err error) {
		log.Printf("게시 실패 (id=%d): %v", p.ID, err)
		// 상태는 publishing으로 남겨 이어서 마칠 수 있게 한다.
		// 본문이 올라간 뒤라면 실패로 되돌리면 다시 눌러 두 번 올라간다.
		if dbErr := a.db.SetPublishNote(ctx, p.ID, reason); dbErr != nil {
			log.Printf("실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
		}
	}

	postID := p.ThreadPostID
	if postID == "" {
		id, err := a.threads.PublishText(ctx, token, body, "")
		if err != nil {
			// 본문조차 올라가지 않았으므로 되돌려도 안전하다.
			log.Printf("본문 게시 실패 (id=%d): %v", p.ID, err)
			if dbErr := a.db.MarkFailed(ctx, p.ID, "게시 실패: "+err.Error()); dbErr != nil {
				log.Printf("실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
			}
			return
		}
		postID = id

		// 퍼머링크는 실패해도 게시를 막지 않는다. 주소만 비워둔다.
		permalink, err := a.threads.Permalink(ctx, token, postID)
		if err != nil {
			log.Printf("퍼머링크 조회 실패 (id=%d): %v", p.ID, err)
		}
		if err := a.db.SetPublishedBody(ctx, p.ID, postID, permalink); err != nil {
			// 여기서 실패하면 다음에 이어받을 때 본문을 또 올리게 된다.
			fail("게시했지만 진행 상태를 저장하지 못했습니다. 스레드에서 확인해주세요.", err)
			return
		}
		p.LastReplyID = postID
		p.RepliesDone = 0
	}

	// 게시물 → 답글1 → 답글2 → 링크 순으로 사슬을 잇는다.
	replies := []string{p.Detail, p.Detail2, link}
	parent := p.LastReplyID
	if parent == "" {
		parent = postID
	}

	for i := p.RepliesDone; i < len(replies); i++ {
		text := strings.TrimSpace(replies[i])
		if text == "" {
			// 빈 답글은 올리지 않지만, 지나간 것으로 기록해야 다시 시도하지 않는다.
			if err := a.db.SetReplyDone(ctx, p.ID, i+1, parent); err != nil {
				fail("진행 상태를 저장하지 못했습니다.", err)
				return
			}
			continue
		}

		replyID, err := a.threads.PublishText(ctx, token, text, parent)
		if err != nil {
			fail(fmt.Sprintf("답글 %d을 올리지 못했습니다. 이어서 게시를 눌러보세요.", i+1), err)
			return
		}
		parent = replyID

		if err := a.db.SetReplyDone(ctx, p.ID, i+1, parent); err != nil {
			fail("답글은 올라갔지만 진행 상태를 저장하지 못했습니다. 스레드에서 확인해주세요.", err)
			return
		}
	}

	permalink := p.ThreadPermalink
	if permalink == "" {
		if link, err := a.threads.Permalink(ctx, token, postID); err == nil {
			permalink = link
		}
	}
	if err := a.db.MarkPublished(ctx, p.ID, permalink); err != nil {
		log.Printf("게시 완료 기록 실패 (id=%d): %v", p.ID, err)
		return
	}
	log.Printf("게시 완료 (id=%d) %s", p.ID, permalink)
}

// handleResumePublish는 끊긴 게시를 이어서 마친다.
//
// 본문이 이미 올라가 있으면 건너뛰고 남은 답글만 올린다.
// 그래서 같은 글이 두 번 올라가지 않는다.
func (a *app) handleResumePublish(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if p.Status != StatusPublishing {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	u, ok := a.currentUser(r)
	if !ok {
		a.renderEdit(w, r, p, "Threads 계정이 연결되지 않아 게시할 수 없어요. 토큰으로 로그인해주세요.")
		return
	}

	// 두 요청이 동시에 이어받는 것을 막는다.
	claimed, err := a.db.ClaimForResume(r.Context(), p.UserID, p.ID)
	if err != nil {
		log.Printf("이어서 게시 선점 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "게시를 이어가지 못했어요.")
		return
	}
	if !claimed {
		// 이미 누군가 이어받았거나 아직 진행 중이다.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	text, err := Compose(p.Affiliate, p.Body)
	if err != nil {
		a.renderEdit(w, r, p, "게시할 수 없어요: "+err.Error())
		return
	}
	reply, err := ComposeReply(p.AffiliateLink)
	if err != nil {
		a.renderEdit(w, r, p, "게시할 수 없어요: "+err.Error())
		return
	}

	a.startPublish(p, u.AccessToken, text, reply)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

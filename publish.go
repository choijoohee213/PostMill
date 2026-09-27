package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// publishTimeout은 게시 한 건에 허용하는 시간이다.
// 게시물과 답글을 차례로 올리고 각 단계마다 컨테이너가 준비될
// 때까지 기다리므로 넉넉히 잡는다.
const publishTimeout = 8 * time.Minute

// startPublish는 게시를 백그라운드로 넘긴다.
//
// 요청 안에서 처리하면 안 된다. 게시는 두 번의 발행과 그만큼의 대기로
// 이루어져 요청 시간을 넘기고, 요청이 끊기면 컨텍스트가 취소되어
// 답글이 빠진 채 멈춘다. 실제로 그런 일이 있었다.
func (a *app) startPublish(p *Post, token, body, reply string) {
	go a.runPublish(p, token, body, reply)
}

// runPublish는 게시물과 답글을 차례로 올리며 각 단계를 DB에 남긴다.
//
// 남기는 이유는 중간에 끊겼을 때 이어서 마치기 위해서다. 어디까지
// 올라갔는지 모르면 같은 글을 다시 올리게 된다.
func (a *app) runPublish(p *Post, token, body, reply string) {
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
		// 사진은 본문에만 붙는다. 본문이 이미 올라갔으면 다시 볼 필요가 없다.
		imgs, err := a.db.ListImages(ctx, p.ID)
		if err != nil {
			log.Printf("사진 조회 실패 (id=%d): %v", p.ID, err)
			if dbErr := a.db.MarkFailed(ctx, p.ID, "게시 실패: 사진을 불러오지 못했습니다."); dbErr != nil {
				log.Printf("실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
			}
			return
		}

		var id string
		if len(imgs) > 0 {
			if a.publicURL == "" {
				if dbErr := a.db.MarkFailed(ctx, p.ID,
					"게시 실패: 공개 주소가 없어 사진을 올릴 수 없어요. 배포된 서버에서 게시해주세요."); dbErr != nil {
					log.Printf("실패 기록도 실패 (id=%d): %v", p.ID, dbErr)
				}
				return
			}
			urls := make([]string, len(imgs))
			for i, im := range imgs {
				urls[i] = a.mediaURL(im.Token)
			}
			id, err = a.threads.PublishImages(ctx, token, body, p.Topic, urls)
		} else {
			id, err = a.threads.PublishText(ctx, token, body, "", p.Topic)
		}
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

	// 게시물 → 답글(문구 + 링크) 순으로 잇는다. 답글은 이것뿐이다.
	// RepliesDone을 보고 건너뛰는 이유는 끊긴 뒤 이어받을 때 같은 답글을
	// 두 번 올리지 않기 위해서다.
	if p.RepliesDone == 0 {
		parent := p.LastReplyID
		if parent == "" {
			parent = postID
		}
		replyID, err := a.threads.PublishText(ctx, token, reply, parent, "")
		if err != nil {
			fail("답글을 올리지 못했습니다. 이어서 게시를 눌러보세요.", err)
			return
		}
		if err := a.db.SetReplyDone(ctx, p.ID, 1, replyID); err != nil {
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
	// Threads가 사진을 이미 가져갔으므로 지워도 게시물에는 남는다.
	// 실패해도 기한이 지나면 정리된다.
	if err := a.db.DeleteImages(ctx, p.UserID, p.ID); err != nil {
		log.Printf("게시한 글의 사진 삭제 실패 (id=%d): %v", p.ID, err)
	}
	log.Printf("게시 완료 (id=%d) %s", p.ID, permalink)
}

// publishGiveUp이 지나도록 게시가 끝나지 않으면 더 이어보지 않고 상태를 정리한다.
// 본문조차 안 올라갔으면 실패로 되돌려 다시 게시할 수 있게 하고, 본문이 올라갔으면
// 게시완료로 옮기되 사유를 남긴다. 계속 "게시 중"으로 남아 있으면 사용자가
// 무엇이 잘못됐는지 알 수 없다.
const publishGiveUp = 30 * time.Minute

// resumeStuck은 끊긴 채 남은 게시를 이어서 마친다.
//
// 크론이 없으므로 목록을 열 때 훑는다 (SPEC 5-3과 같은 방식). goroutine이 죽거나
// 무료 서버가 잠들어 중간에 끊긴 글이 계속 "게시 중"으로 남는 것을 막는다.
func (a *app) resumeStuck(r *http.Request, u *ThreadsUser) {
	if u == nil {
		return
	}
	ctx := r.Context()
	stuck, err := a.db.ListStuckPublishing(ctx, u.UserID)
	if err != nil {
		log.Printf("끊긴 게시 조회 실패: %v", err)
		return
	}
	for _, p := range stuck {
		if p.PublishStartedAt != nil && time.Since(*p.PublishStartedAt) > publishGiveUp {
			a.giveUpPublish(ctx, p)
			continue
		}

		text, err := Compose(p.Affiliate, p.Body)
		if err != nil {
			log.Printf("끊긴 게시 조립 실패 (id=%d): %v", p.ID, err)
			continue
		}
		reply, err := ComposeReply(p.Affiliate, p.AffiliateLink)
		if err != nil {
			log.Printf("끊긴 게시 링크 없음 (id=%d): %v", p.ID, err)
			continue
		}
		claimed, err := a.db.ClaimForResume(ctx, p.UserID, p.ID)
		if err != nil || !claimed {
			continue
		}
		log.Printf("끊긴 게시를 이어서 마친다 (id=%d)", p.ID)
		a.startPublish(p, u.AccessToken, text, reply)
	}
}

// giveUpPublish는 오래 끌린 게시를 끝낸 것으로 정리한다.
func (a *app) giveUpPublish(ctx context.Context, p *Post) {
	if p.ThreadPostID == "" {
		// 본문조차 올라가지 않았으니 되돌려도 두 번 올라가지 않는다.
		if err := a.db.MarkFailed(ctx, p.ID, "게시가 시작되지 못했어요. 다시 눌러주세요."); err != nil {
			log.Printf("게시 포기 기록 실패 (id=%d): %v", p.ID, err)
		}
		return
	}
	note := p.ErrorMsg
	if note == "" {
		note = "일부가 올라가지 않았어요. 스레드에서 확인해주세요."
	}
	if err := a.db.MarkPublished(ctx, p.ID, p.ThreadPermalink); err != nil {
		log.Printf("게시 포기 기록 실패 (id=%d): %v", p.ID, err)
		return
	}
	if err := a.db.SetPublishNote(ctx, p.ID, note); err != nil {
		log.Printf("게시 포기 사유 기록 실패 (id=%d): %v", p.ID, err)
	}
	log.Printf("게시를 더 이어가지 않는다 (id=%d): %s", p.ID, note)
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
	reply, err := ComposeReply(p.Affiliate, p.AffiliateLink)
	if err != nil {
		a.renderEdit(w, r, p, "게시할 수 없어요: "+err.Error())
		return
	}

	a.startPublish(p, u.AccessToken, text, reply)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

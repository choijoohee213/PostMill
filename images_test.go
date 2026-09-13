package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateImage(t *testing.T) {
	var pngBuf, gifBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 800, 600)))
	gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 800, 600), nil), nil)

	ok := []struct {
		name string
		data []byte
		ct   string
	}{
		{"JPEG", encodeJPEG(t, 1080, 1350), "image/jpeg"},
		{"PNG", pngBuf.Bytes(), "image/png"},
		{"가로 최소", encodeJPEG(t, 320, 320), "image/jpeg"},
		{"가로 최대", encodeJPEG(t, 1440, 900), "image/jpeg"},
	}
	for _, c := range ok {
		ct, err := validateImage(c.data)
		if err != nil || ct != c.ct {
			t.Errorf("%s: ct=%q err=%v", c.name, ct, err)
		}
	}

	bad := []struct {
		name string
		data []byte
	}{
		{"GIF", gifBuf.Bytes()},
		{"사진 아님", []byte("<svg></svg>")},
		{"가로가 좁다", encodeJPEG(t, 319, 400)},
		{"가로가 넓다", encodeJPEG(t, 1441, 900)},
		{"10:1 초과", encodeJPEG(t, 320, 3300)},
		{"너무 크다", append(encodeJPEG(t, 400, 400), make([]byte, maxImageBytes)...)},
	}
	for _, c := range bad {
		if _, err := validateImage(c.data); err == nil {
			t.Errorf("%s: 통과시켰다", c.name)
		}
	}
}

// imageRecorder는 컨테이너 생성 폼을 통째로 기록하는 가짜 Threads다.
type imageRecorder struct {
	mu    sync.Mutex
	forms []map[string]string
}

func (rec *imageRecorder) server(t *testing.T) *Threads {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/me/threads":
			rec.mu.Lock()
			n++
			f := map[string]string{}
			for k := range r.PostForm {
				if k != "access_token" {
					f[k] = r.PostForm.Get(k)
				}
			}
			rec.forms = append(rec.forms, f)
			id := n
			rec.mu.Unlock()
			fmt.Fprintf(w, `{"id":"c%d"}`, id)
		case "/me/threads_publish":
			fmt.Fprintf(w, `{"id":"p%s"}`, strings.TrimPrefix(r.FormValue("creation_id"), "c"))
		default:
			if r.URL.Query().Get("fields") == "status,error_message" {
				fmt.Fprint(w, `{"status":"FINISHED"}`)
				return
			}
			fmt.Fprint(w, `{"permalink":"https://threads/p"}`)
		}
	}))
	t.Cleanup(srv.Close)
	th := NewThreads()
	th.HTTP = srv.Client()
	th.BaseURL = srv.URL + "/"
	return th
}

func TestPublishImages_한_장이면_IMAGE로_올린다(t *testing.T) {
	rec := &imageRecorder{}
	id, err := rec.server(t).PublishImages(context.Background(), "tok", "본문", []string{"https://x/media/a"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "p1" {
		t.Fatalf("id=%s", id)
	}
	if len(rec.forms) != 1 {
		t.Fatalf("컨테이너 %d개: %+v", len(rec.forms), rec.forms)
	}
	f := rec.forms[0]
	if f["media_type"] != "IMAGE" || f["image_url"] != "https://x/media/a" || f["text"] != "본문" {
		t.Fatalf("폼=%+v", f)
	}
}

func TestPublishImages_여러_장이면_캐러셀로_묶는다(t *testing.T) {
	rec := &imageRecorder{}
	urls := []string{"https://x/media/a", "https://x/media/b", "https://x/media/c"}
	id, err := rec.server(t).PublishImages(context.Background(), "tok", "본문", urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.forms) != 4 {
		t.Fatalf("컨테이너 %d개, 항목 3 + 캐러셀 1이어야 한다: %+v", len(rec.forms), rec.forms)
	}
	for i, u := range urls {
		f := rec.forms[i]
		if f["media_type"] != "IMAGE" || f["image_url"] != u || f["is_carousel_item"] != "true" || f["text"] != "" {
			t.Errorf("%d번째 항목 폼=%+v", i, f)
		}
	}
	c := rec.forms[3]
	if c["media_type"] != "CAROUSEL" || c["children"] != "c1,c2,c3" || c["text"] != "본문" {
		t.Fatalf("캐러셀 폼=%+v", c)
	}
	if id != "p4" {
		t.Fatalf("캐러셀 컨테이너를 발행해야 한다: id=%s", id)
	}
}

func TestRunPublish_사진을_본문에_붙이고_게시가_끝나면_지운다(t *testing.T) {
	rec := &imageRecorder{}
	a := publishApp(t, rec.server(t))
	a.publicURL = "https://postmill.example/"
	ctx := context.Background()
	p := newPublishable(t, a.db, "img-publish")

	for _, tok := range []string{"TOKA", "TOKB"} {
		if err := a.db.AddImage(ctx, p.UserID, p.ID, tok+p.UserID+fmt.Sprint(p.ID), "image/jpeg", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	if len(rec.forms) < 3 {
		t.Fatalf("컨테이너 %d개: %+v", len(rec.forms), rec.forms)
	}
	if rec.forms[2]["media_type"] != "CAROUSEL" || rec.forms[2]["text"] != "조립된 본문" {
		t.Fatalf("본문이 캐러셀로 올라가지 않았다: %+v", rec.forms[2])
	}
	if !strings.HasPrefix(rec.forms[0]["image_url"], "https://postmill.example/media/TOKA") {
		t.Fatalf("사진 주소=%q", rec.forms[0]["image_url"])
	}
	// 답글에는 사진이 붙지 않는다.
	for _, f := range rec.forms[3:] {
		if f["media_type"] != "TEXT" {
			t.Errorf("답글 폼=%+v", f)
		}
	}

	after, _ := a.db.GetPost(ctx, p.UserID, p.ID)
	if after.Status != StatusPublished {
		t.Fatalf("상태=%s", after.Status)
	}
	if imgs, _ := a.db.ListImages(ctx, p.ID); len(imgs) != 0 {
		t.Fatalf("게시 후에도 사진 %d장이 남았다", len(imgs))
	}
}

func TestRunPublish_공개_주소가_없으면_사진_글은_실패로_돌린다(t *testing.T) {
	rec := &imageRecorder{}
	a := publishApp(t, rec.server(t))
	ctx := context.Background()
	p := newPublishable(t, a.db, "img-nourl")
	a.db.AddImage(ctx, p.UserID, p.ID, "NOURL"+fmt.Sprint(p.ID), "image/jpeg", []byte("x"))

	a.runPublish(p, "tok", "조립된 본문", "https://link/x")

	if len(rec.forms) != 0 {
		t.Fatalf("아무것도 올리면 안 된다: %+v", rec.forms)
	}
	after, _ := a.db.GetPost(ctx, p.UserID, p.ID)
	if after.Status != StatusFailed {
		t.Fatalf("상태=%s, failed여야 한다", after.Status)
	}
	if imgs, _ := a.db.ListImages(ctx, p.ID); len(imgs) != 1 {
		t.Fatal("실패했으면 사진은 남아야 한다")
	}
}

func TestExpireImages_오래된_사진만_지우고_게시_중인_글은_남긴다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "img-expire"

	draft, _ := db.CreateDraft(ctx, user, AffiliateCoupang, "", "https://link/x", "메모")
	t.Cleanup(func() { db.DeletePost(ctx, user, draft) })
	db.SetGenerated(ctx, draft, "본문", "", "")
	publishing := newPublishable(t, db, user)

	add := func(postID int64, tok string, daysAgo int) {
		t.Helper()
		if err := db.AddImage(ctx, user, postID, tok, "image/jpeg", []byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, err := db.pool.Exec(ctx,
			`UPDATE post_images SET created_at = now() - make_interval(days => $2) WHERE token = $1`,
			tok, daysAgo); err != nil {
			t.Fatal(err)
		}
	}
	suffix := fmt.Sprint(draft)
	add(draft, "OLD"+suffix, 11)
	add(draft, "NEW"+suffix, 9)
	add(publishing.ID, "PUB"+suffix, 11)

	if err := db.ExpireImages(ctx, imageTTL); err != nil {
		t.Fatal(err)
	}

	left := map[string]bool{}
	for _, id := range []int64{draft, publishing.ID} {
		imgs, _ := db.ListImages(ctx, id)
		for _, im := range imgs {
			left[im.Token] = true
		}
	}
	if left["OLD"+suffix] {
		t.Error("10일 지난 사진이 남았다")
	}
	if !left["NEW"+suffix] {
		t.Error("10일 안 된 사진을 지웠다")
	}
	if !left["PUB"+suffix] {
		t.Error("게시 중인 글의 사진을 지웠다")
	}
}

func TestImages_남의_글에는_붙이지도_지우지도_못한다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateDraft(ctx, "img-owner", AffiliateCoupang, "", "", "메모")
	t.Cleanup(func() { db.DeletePost(ctx, "img-owner", id) })

	db.AddImage(ctx, "img-other", id, "OTHER"+fmt.Sprint(id), "image/jpeg", []byte("x"))
	if imgs, _ := db.ListImages(ctx, id); len(imgs) != 0 {
		t.Fatal("남의 글에 사진이 붙었다")
	}

	db.AddImage(ctx, "img-owner", id, "MINE"+fmt.Sprint(id), "image/jpeg", []byte("x"))
	db.DeleteImages(ctx, "img-other", id)
	if imgs, _ := db.ListImages(ctx, id); len(imgs) != 1 {
		t.Fatal("남이 사진을 지웠다")
	}
}

func TestHandleMedia_토큰으로_사진을_내보낸다(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id, _ := db.CreateDraft(ctx, "img-media", AffiliateCoupang, "", "", "메모")
	t.Cleanup(func() { db.DeletePost(ctx, "img-media", id) })
	tok := "MEDIA" + fmt.Sprint(id)
	data := encodeJPEG(t, 400, 400)
	db.AddImage(ctx, "img-media", id, tok, "image/jpeg", data)

	mux := http.NewServeMux()
	a := &app{db: db}
	mux.HandleFunc("GET /media/{token}", a.handleMedia)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/media/"+tok, nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("code=%d ct=%q len=%d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/media/NOPE", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("없는 토큰 code=%d", w.Code)
	}
}

func uploadRequest(t *testing.T, a *app, user string, postID int64, files ...[]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for i, f := range files {
		part, _ := mw.CreateFormFile("images", fmt.Sprintf("p%d.jpg", i))
		part.Write(f)
	}
	mw.Close()

	login := httptest.NewRecorder()
	a.session.issue(login, user, false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /drafts/{id}/images", a.handleImageUpload)
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/drafts/%d/images", postID), &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(cookieFrom(t, login))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestHandleImageUpload(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const user = "img-upload"
	a := &app{db: db, session: testSession()}

	id, _ := db.CreateDraft(ctx, user, AffiliateCoupang, "", "", "메모")
	t.Cleanup(func() { db.DeletePost(ctx, user, id) })
	db.SetGenerated(ctx, id, "본문", "", "")

	good := encodeJPEG(t, 1080, 1080)

	if w := uploadRequest(t, a, user, id, good, good); w.Code != http.StatusNoContent {
		t.Fatalf("정상 업로드 code=%d body=%s", w.Code, w.Body)
	}
	if imgs, _ := db.ListImages(ctx, id); len(imgs) != 2 {
		t.Fatalf("사진 %d장, 2장이어야 한다", len(imgs))
	}

	// 하나라도 걸리면 아무것도 붙지 않는다.
	if w := uploadRequest(t, a, user, id, good, encodeJPEG(t, 100, 100)); w.Code != http.StatusBadRequest {
		t.Fatalf("잘못된 사진 code=%d", w.Code)
	}
	if imgs, _ := db.ListImages(ctx, id); len(imgs) != 2 {
		t.Fatalf("일부만 붙었다: %d장", len(imgs))
	}

	// 상한을 넘기면 거절한다.
	many := make([][]byte, maxImages-1)
	for i := range many {
		many[i] = good
	}
	if w := uploadRequest(t, a, user, id, many...); w.Code != http.StatusBadRequest {
		t.Fatalf("%d장 초과 code=%d", maxImages, w.Code)
	}

	// 남의 글에는 올릴 수 없다.
	if w := uploadRequest(t, a, "img-stranger", id, good); w.Code != http.StatusNotFound {
		t.Fatalf("남의 글 code=%d", w.Code)
	}

	// 게시 중인 글은 사진을 바꿀 수 없다.
	db.ClaimForPublish(ctx, user, id)
	if w := uploadRequest(t, a, user, id, good); w.Code != http.StatusConflict {
		t.Fatalf("게시 중 code=%d", w.Code)
	}
}

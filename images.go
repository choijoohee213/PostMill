package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// maxImages는 글 하나에 붙일 수 있는 사진 수다. Threads 캐러셀은 20장까지지만
	// DB에 바이트로 들고 있으므로 넉넉한 선에서 자른다.
	maxImages = 10

	// maxImageBytes는 한 장의 상한이다. 브라우저에서 가로 1440px JPEG로 줄여
	// 올리므로 보통 수백 KB다. 줄이지 않은 원본은 여기서 걸린다.
	maxImageBytes = 2 << 20

	// imageTTL이 지난 사진은 지운다. 오래 방치된 초안이 DB를 채우지 않게 한다.
	imageTTL = 10 * 24 * time.Hour
)

// validateImage는 Threads가 받는 사진인지 본다: JPEG·PNG, 가로 320~1440px, 비율 10:1 이내.
// 반환값은 저장할 Content-Type이다.
func validateImage(data []byte) (string, error) {
	if len(data) > maxImageBytes {
		return "", fmt.Errorf("사진이 너무 커요 (최대 %dMB)", maxImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("JPEG나 PNG 사진만 올릴 수 있어요")
	}
	var contentType string
	switch format {
	case "jpeg":
		contentType = "image/jpeg"
	case "png":
		contentType = "image/png"
	default:
		return "", errors.New("JPEG나 PNG 사진만 올릴 수 있어요")
	}
	if cfg.Width < 320 || cfg.Width > 1440 {
		return "", fmt.Errorf("사진 가로가 %dpx예요. 320~1440px여야 해요", cfg.Width)
	}
	long, short := max(cfg.Width, cfg.Height), min(cfg.Width, cfg.Height)
	if short == 0 || long > short*10 {
		return "", errors.New("너무 길쭉한 사진은 올릴 수 없어요 (10:1 이내)")
	}
	return contentType, nil
}

// canEditImages는 사진을 붙이거나 뗄 수 있는 상태인지 본다.
func canEditImages(p *Post) bool {
	return p.Status != StatusPublishing && p.Status != StatusPublished
}

// handleImageUpload는 편집 화면에서 고른 사진들을 붙인다.
// 브라우저에서 fetch로 부르므로 실패하면 사유를 본문으로 돌려준다.
func (a *app) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	if !canEditImages(p) {
		http.Error(w, "게시 중이거나 게시한 글은 사진을 바꿀 수 없어요.", http.StatusConflict)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImages*maxImageBytes+1<<20)
	if err := r.ParseMultipartForm(maxImageBytes); err != nil {
		http.Error(w, "사진을 받지 못했어요. 한 번에 너무 많이 올렸을 수 있어요.", http.StatusBadRequest)
		return
	}
	files := r.MultipartForm.File["images"]

	existing, err := a.db.ListImages(r.Context(), p.ID)
	if err != nil {
		log.Printf("사진 목록 조회 실패 (id=%d): %v", p.ID, err)
		http.Error(w, "사진을 저장하지 못했어요.", http.StatusInternalServerError)
		return
	}
	if len(existing)+len(files) > maxImages {
		http.Error(w, fmt.Sprintf("사진은 %d장까지 붙일 수 있어요.", maxImages), http.StatusBadRequest)
		return
	}

	// 전부 검사한 뒤에 저장한다. 중간에 하나가 걸려 일부만 붙는 일이 없게 한다.
	type upload struct {
		contentType string
		data        []byte
	}
	var uploads []upload
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			http.Error(w, "사진을 읽지 못했어요.", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
		f.Close()
		if err != nil {
			http.Error(w, "사진을 읽지 못했어요.", http.StatusBadRequest)
			return
		}
		contentType, err := validateImage(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		uploads = append(uploads, upload{contentType, data})
	}

	for _, u := range uploads {
		if err := a.db.AddImage(r.Context(), p.UserID, p.ID, rand.Text(), u.contentType, u.data); err != nil {
			log.Printf("사진 저장 실패 (id=%d): %v", p.ID, err)
			http.Error(w, "사진을 저장하지 못했어요.", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := a.draftFor(w, r)
	if !ok {
		return
	}
	imageID, err := strconv.ParseInt(r.PathValue("imageID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !canEditImages(p) {
		a.renderEdit(w, r, p, "게시 중이거나 게시한 글은 사진을 바꿀 수 없어요.")
		return
	}
	if err := a.db.DeleteImage(r.Context(), p.UserID, p.ID, imageID); err != nil {
		log.Printf("사진 삭제 실패 (id=%d): %v", p.ID, err)
		a.renderEdit(w, r, p, "사진을 지우지 못했어요.")
		return
	}
	http.Redirect(w, r, "/drafts/"+strconv.FormatInt(p.ID, 10), http.StatusSeeOther)
}

// handleMedia는 사진을 내보낸다. 로그인 없이 열리는 경로다 (Threads가 가져간다).
func (a *app) handleMedia(w http.ResponseWriter, r *http.Request) {
	contentType, data, err := a.db.GetImageData(r.Context(), r.PathValue("token"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("사진 조회 실패: %v", err)
		http.Error(w, "사진을 불러오지 못했습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// mediaURL은 Threads가 사진을 가져갈 절대 주소다. 공개 주소가 없으면 빈 문자열.
func (a *app) mediaURL(token string) string {
	if a.publicURL == "" {
		return ""
	}
	return strings.TrimRight(a.publicURL, "/") + "/media/" + token
}

// expireImages는 오래된 사진을 치운다. 실패해도 화면은 그대로 보여준다.
func (a *app) expireImages(r *http.Request) {
	if err := a.db.ExpireImages(r.Context(), imageTTL); err != nil {
		log.Printf("오래된 사진 정리 실패: %v", err)
	}
}

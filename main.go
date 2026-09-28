package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type app struct {
	db      *DB
	tpl     *template.Template
	gemini  *Gemini
	threads *Threads

	// newToss는 사용자가 등록한 키로 토스 클라이언트를 만든다.
	// 테스트에서 가짜 서버를 가리키게 바꾼다.
	newToss   func(accessKey, secretKey, publisherID string) *Toss
	tossState tossState

	tossLinkClient *http.Client // 테스트에서 토스 공유 링크 따라가기를 가짜로 바꾼다

	session       *session
	appID         string
	appSecret     string
	adminPassword string

	// publicURL은 Threads가 사진을 가져갈 이 서버의 공개 주소다.
	// Render가 RENDER_EXTERNAL_URL로 넣어준다. 로컬에서는 비어 사진 게시가 막힌다.
	publicURL string

	testTpl *template.Template // 테스트에서 전체 템플릿 없이 렌더링하기 위해 쓴다
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL이 설정되지 않았습니다")
	}

	// 제휴 키를 잠그는 열쇠다. 이 값이 바뀌면 저장해 둔 키를 열 수 없어
	// 사용자가 다시 넣어야 하므로, 한번 정하면 바꾸지 않는다.
	encKey := os.Getenv("ENCRYPTION_KEY")
	if encKey == "" {
		log.Fatal("ENCRYPTION_KEY가 설정되지 않았습니다")
	}

	db, err := Open(context.Background(), databaseURL, encKey)
	if err != nil {
		log.Fatalf("DB 연결 실패: %v", err)
	}
	defer db.Close()

	tpl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("템플릿 파싱 실패: %v", err)
	}

	// 쉼표로 여러 개를 넣으면 한도에 걸릴 때 다음 키로 넘어간다.
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY가 설정되지 않았습니다")
	}

	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		log.Fatal("SESSION_SECRET이 설정되지 않았습니다")
	}

	a := &app{
		db:            db,
		tpl:           tpl,
		gemini:        NewGemini(apiKey),
		threads:       NewThreads(),
		session:       &session{secret: []byte(secret)},
		appID:         os.Getenv("THREADS_APP_ID"),
		appSecret:     os.Getenv("THREADS_APP_SECRET"),
		adminPassword: os.Getenv("ADMIN_PASSWORD"),
		newToss:       NewToss,
		publicURL:     os.Getenv("RENDER_EXTERNAL_URL"),
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login/token", a.handleTokenLogin)
	mux.HandleFunc("POST /login/admin", a.handleAdminLogin)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /{$}", a.handleList)
	mux.HandleFunc("POST /new", a.handleManualSubmit)
	mux.HandleFunc("POST /auto", a.handleAuto)
	mux.HandleFunc("POST /auto/one", a.handleAutoOne)
	mux.HandleFunc("POST /drafts/{id}/refresh", a.handleRefresh)
	mux.HandleFunc("POST /drafts/{id}/link", a.handleSaveLink)
	mux.HandleFunc("POST /drafts/{id}/retry", a.handleRetry)
	mux.HandleFunc("GET /drafts/{id}", a.handleDraftEdit)
	mux.HandleFunc("POST /drafts/{id}", a.handleDraftSave)
	mux.HandleFunc("POST /drafts/{id}/regenerate", a.handleRegenerate)
	mux.HandleFunc("POST /drafts/{id}/hold", a.handleHold)
	mux.HandleFunc("POST /drafts/{id}/unhold", a.handleUnhold)
	mux.HandleFunc("POST /drafts/{id}/delete", a.handleDelete)
	mux.HandleFunc("POST /drafts/{id}/publish", a.handlePublish)
	mux.HandleFunc("POST /drafts/{id}/resume", a.handleResumePublish)
	mux.HandleFunc("POST /drafts/{id}/images", a.handleImageUpload)
	mux.HandleFunc("POST /drafts/{id}/images/{imageID}/delete", a.handleImageDelete)
	mux.HandleFunc("GET /media/{token}", a.handleMedia)
	mux.HandleFunc("GET /history", a.handleHistory)
	mux.HandleFunc("GET /stats", a.handleStats)
	mux.HandleFunc("GET /settings", a.handleSettings)
	mux.HandleFunc("POST /disconnect", a.handleDisconnect)
	mux.HandleFunc("POST /settings/toss", a.handleTossKey)
	mux.HandleFunc("POST /settings/toss/delete", a.handleTossKeyDelete)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("http://localhost:%s 에서 대기", port)
	if err := http.ListenAndServe(":"+port, a.requireAuth(a.tokenKeeper(mux))); err != nil {
		log.Fatal(err)
	}
}

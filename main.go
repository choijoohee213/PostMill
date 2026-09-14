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

	// toss는 토스 쉐어링크 Open API다. 키가 없으면 nil이고, 토스 자동 초안은
	// 쿠팡처럼 AI가 상품 종류만 제안한다.
	toss      *Toss
	tossState tossState

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

	db, err := Open(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("DB 연결 실패: %v", err)
	}
	defer db.Close()

	tpl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("템플릿 파싱 실패: %v", err)
	}

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
		publicURL:     os.Getenv("RENDER_EXTERNAL_URL"),
	}

	if key, secret, publisher := os.Getenv("TOSS_ACCESS_KEY"), os.Getenv("TOSS_SECRET_KEY"),
		os.Getenv("TOSS_PUBLISHER_ID"); key != "" && secret != "" && publisher != "" {
		a.toss = NewToss(key, secret, publisher)
	} else {
		log.Print("토스 API 키가 없어 토스 자동 초안은 상품 종류만 제안합니다")
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login/token", a.handleTokenLogin)
	mux.HandleFunc("POST /login/admin", a.handleAdminLogin)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /{$}", a.handleList)
	mux.HandleFunc("GET /new", a.handleNewForm)
	mux.HandleFunc("POST /new", a.handleNewSubmit)
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("http://localhost:%s 에서 대기", port)
	if err := http.ListenAndServe(":"+port, a.requireAuth(a.tokenKeeper(mux))); err != nil {
		log.Fatal(err)
	}
}

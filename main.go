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

	a := &app{db: db, tpl: tpl, gemini: NewGemini(apiKey), threads: NewThreads()}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /{$}", a.handleList)
	mux.HandleFunc("GET /new", a.handleNewForm)
	mux.HandleFunc("POST /new", a.handleNewSubmit)
	mux.HandleFunc("POST /drafts/{id}/retry", a.handleRetry)
	mux.HandleFunc("GET /drafts/{id}", a.handleDraftEdit)
	mux.HandleFunc("POST /drafts/{id}", a.handleDraftSave)
	mux.HandleFunc("POST /drafts/{id}/regenerate", a.handleRegenerate)
	mux.HandleFunc("POST /drafts/{id}/hold", a.handleHold)
	mux.HandleFunc("POST /drafts/{id}/unhold", a.handleUnhold)
	mux.HandleFunc("POST /drafts/{id}/delete", a.handleDelete)
	mux.HandleFunc("POST /drafts/{id}/publish", a.handlePublish)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("http://localhost:%s 에서 대기", port)
	if err := http.ListenAndServe(":"+port, a.tokenKeeper(mux)); err != nil {
		log.Fatal(err)
	}
}

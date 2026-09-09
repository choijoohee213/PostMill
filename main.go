package main

import (
	"context"
	"log"
	"os"
)

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

	log.Println("스키마 초기화 완료")
}

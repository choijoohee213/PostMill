package main

import (
	"context"
	"os"
	"testing"
)

// openTestDB는 테스트 전용 DB를 연다.
//
// TEST_DATABASE_URL이 없으면 테스트를 건너뛴다. DATABASE_URL로 폴백하지
// 않는 것이 핵심이다. 통합 테스트는 app_state의 스레드 토큰을 덮어쓰고
// posts에 행을 만들기 때문에, 운영 DB에 연결되면 실제 연결이 끊기고
// 발행이 실패한다. 실제로 그런 일이 있었다.
func openTestDB(t *testing.T) *DB {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL이 설정되지 않아 건너뜁니다. " +
			"운영 DB를 건드리지 않도록 별도 DB를 지정해야 합니다.")
	}
	if url == os.Getenv("DATABASE_URL") {
		t.Fatal("TEST_DATABASE_URL이 DATABASE_URL과 같습니다. 별도 DB를 지정하세요.")
	}

	db, err := Open(context.Background(), url)
	if err != nil {
		t.Fatalf("테스트 DB 연결 실패: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

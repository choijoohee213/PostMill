# PostMill

스레드(Threads)에 올릴 제휴 마케팅 글을 AI가 초안으로 만들고, 사람이 폰에서 검수한 뒤 발행하는 1인용 웹 도구.

## 스택

| 항목 | 선택 |
|---|---|
| 언어 | Go (1.22+) |
| HTTP | 표준 `net/http` 라우팅 패턴 |
| 뷰 | `html/template` 서버 사이드 렌더링 |
| DB | PostgreSQL (Neon), 드라이버 `pgx` |
| 호스팅 | Render 무료 웹 서비스 |
| 초안 생성 | Gemini API (무료 티어), 표준 `net/http`로 직접 호출 |
| 발행 | Threads Graph API |
| 프론트 | 빌드 파이프라인 없음. CSS 1파일 + 바닐라 JS |

의존성은 `github.com/jackc/pgx/v5` 하나뿐이다. Gemini와 Threads API는 표준 `net/http`로 직접 호출한다.
크론 잡·백그라운드 워커·프론트 프레임워크는 쓰지 않는다.

## 환경변수

```
DATABASE_URL
SESSION_SECRET
APP_PASSWORD
GEMINI_API_KEY
THREADS_APP_ID
THREADS_APP_SECRET
```

Threads 액세스 토큰은 갱신되므로 환경변수가 아니라 `app_state` 테이블에 저장한다.

## 로컬 실행

```bash
go run .
```

기동 시 `schema.sql`을 실행해 테이블을 만든다 (`CREATE TABLE IF NOT EXISTS`). 별도 마이그레이션 도구는 쓰지 않는다.

## 개발 방식

`main`에 직접 푸시하지 않는다. 브랜치를 만들어 풀 리퀘스트로 머지한다.

```bash
git checkout main && git pull
git checkout -b feature/작업이름
# 작업 후
git push -u origin feature/작업이름
gh pr create
```

브랜치 이름은 `feature/*` 를 기본으로 하고, 버그 수정은 `fix/*` 를 쓴다.

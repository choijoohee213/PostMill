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

## 배포 (Render)

`render.yaml` 블루프린트가 있으므로 Render 대시보드에서 **New → Blueprint**로 저장소를 연결하면 된다.

**지역은 Ohio로 둔다.** Neon 프로젝트와 같은 지역이어야 한다. 페이지 한 번을 그리는 데
DB를 여러 번 오가므로, 서버-DB 거리가 사용자-서버 거리보다 체감에 크게 영향을 준다.

환경변수 중 `SESSION_SECRET`은 Render가 자동 생성한다. 나머지는 대시보드에서 직접 입력한다.

무료 플랜은 비활성 시 서비스가 잠들어 첫 접속에 30초~1분이 걸린다. 하루 3~4회
접속하는 도구라 감수한다.

배포 후 `/connect`에서 Threads 계정을 연결한다. Meta 대시보드의 사용자 토큰
생성기에서 받은 1시간짜리 토큰을 붙여넣으면 60일짜리로 교환해 저장한다.

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

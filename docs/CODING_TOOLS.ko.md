# 코딩 도구 연동

<p align="center"><a href="CODING_TOOLS.md">English</a> | <b>한국어</b></p>

OwnGit은 버전이 붙은 JSON 명령줄 인터페이스와 공용 Agent Skill로 코딩 도구에 프로젝트 체크를 제공합니다. 이 연동에는 MCP, 데몬, 세션 실행기가 필요 없습니다. 코딩 도구가 사용자 환경에서 `owngit` 실행 파일을 실행하고 JSON 결과를 읽습니다.

스킬은 지침일 뿐 강제 장치가 아닙니다. 코딩 도구는 스킬을 무시할 수 있으며, 서버는 체크 에이전트(helper)가 실제로 제출한 것만 기록합니다.

## 연동이 제공하는 것

- 리비전이 바뀌어도 유지되는 고정된 작업 식별자
- OwnGit 서버에 기록되는, 리비전에 묶인 체크 결과
- 작업마다 세 라운드로 명시된 수정 라운드 한도
- 진행 중인 코딩 세션에 언제 체크 에이전트를 실행하고 결과를 어떻게 읽을지 알려 주는 스킬

## 준비 사항

- 코딩하는 컴퓨터에서 접속할 수 있는 OwnGit 서버
- 저장소 식별자
- 비공개 파일로 전달된 저장소 범위의 체크 에이전트 토큰
- 코딩하는 컴퓨터에 있는 `owngit` 실행 파일

먼저 이미 전달받은 사실과 프로젝트에서 알 수 있는 사실을 확인하세요. 사용자에게는 사용자가 정해야 하는데 아직 정하지 않은 선택만 묻고, 이미 알 수 있는 사실을 다시 입력하게 하지 마세요.

관리자 비밀번호로 토큰을 만듭니다.

```sh
owngit helper-credential create \
  --server https://owngit.example.test \
  --repository example-project \
  --label laptop \
  --password-file /path/to/admin-password-file \
  --output /path/to/helper-token
```

이 토큰은 OwnGit 범위의 체크 에이전트 토큰이며, 제공자의 구독 토큰이 아닙니다. `--output` 파일에만 쓰이고 서버에는 해시로만 저장됩니다. 명령 인수, 문서, 로그에 넣지 마세요. 파일은 소유자만 읽을 수 있습니다. 파일이나 심볼릭 링크가 이미 있으면 교체하지 않고 알려 줍니다. 파일의 첫 줄에는 토큰을 발급한 서버가 적히고([자격 증명 파일과 서버 줄](#자격-증명-파일과-서버-줄) 참고), 명령은 그 서버를 `token_file_server`로 출력합니다.

일반 HTTP는 전송 내용을 암호화하지 않습니다. 사용자가 해당 요청에 `--accept-insecure-http`를 넘기지 않으면 체크 에이전트는 HTTP를 거부합니다. 사용자를 대신해 이 플래그를 붙이지 마세요.

## 스킬 찾기

공용 스킬은 [integrations/skills/owngit-checks/SKILL.md](../integrations/skills/owngit-checks/SKILL.md)에 있습니다. 릴리스 도구로 만든 결과물에도 스킬과 이 안내 문서(영어판과 한국어판)가 들어 있습니다. [설치 위치](#설치-위치)를 보세요. 모든 `owngit` 실행 파일에는 함께 배포된 스킬이 들어 있으므로, 어떤 방법으로 설치했든 스킬을 설치할 수 있습니다.

```sh
owngit skill --install ~/.agents/skills
```

`--install DIR`는 `DIR/owngit-checks/SKILL.md`를 쓰고 JSON 결과를 출력합니다. `status`는 `installed`, 파일에 이미 같은 바이트가 있으면 `already_current`, 교체했으면 `replaced`입니다. 파일이 이미 있는데 내용이 다르면 사용자가 고친 내용일 수 있으므로 아무것도 바꾸지 않고 `skill_modified`로 실패합니다. 배포된 스킬을 출력하는 `owngit skill --print`로 비교해 보세요. 그다음 `--replace`를 붙이면 지금 파일을 옆에 `SKILL.md.previous-TIMESTAMP`로 남긴 뒤 배포된 스킬을 설치하고, 남긴 파일은 `previous`에 적습니다. `SKILL.md`가 심볼릭 링크이거나 일반 파일이 아니면 `skill_target_invalid`로 거부합니다. 이 명령을 실행하거나 직접 복사하지 않으면 스킬은 설치되지 않습니다.

`owngit-checks` 디렉터리를 링크하지 말고 설치하거나 복사해서, 코딩 도구가 찾아보는 위치에 두세요.

- Codex: 저장소의 `.agents/skills/owngit-checks`, 또는 사용자 전체에 쓰려면 `~/.agents/skills/owngit-checks`. Codex는 현재 디렉터리부터 저장소 루트까지 `.agents/skills`를 찾습니다.
- Pi: 프로젝트의 `.agents/skills/owngit-checks`나 `.pi/skills/owngit-checks`, 또는 사용자 전체에 쓰려면 `~/.agents/skills/owngit-checks`나 `~/.pi/agent/skills/owngit-checks`.

Codex 독립 스킬은 ChatGPT 데스크톱 앱, Codex CLI, IDE 확장에서 쓸 수 있습니다. 앱에서는 `@`로 스킬을 고르고, CLI와 IDE 확장에서는 `/skills`로 목록을 보고 `$`로 스킬을 언급합니다. Pi는 `/skill:owngit-checks`를 등록합니다. 두 도구 모두 설명을 보고 스킬을 저절로 고를 수도 있지만 놓칠 수 있으므로, 직접 호출하는 쪽이 확실합니다. 스킬이 로드되지 않았으면 이 안내의 명령을 직접 실행하세요.

## 클론 안에서 실행하기

OwnGit 저장소의 클론 안에서는 `owngit pr`, `owngit check`, `owngit repo`가 서버와 저장소를 스스로 찾습니다. `--server`나 `--repository`가 없으면 클론의 `origin` 원격을 읽고, OwnGit 클론 주소인 `http(s)://HOST[:PORT]/git/ID.git` 형태만 받아들입니다. `check run`은 `--workdir`가 들어 있는 클론을 읽고, 다른 명령은 현재 디렉터리가 들어 있는 클론을 읽습니다. 직접 넘긴 플래그가 항상 우선하며, `--repository`만 넘기면 서버는 계속 `origin`에서 가져옵니다. `repo list`와 `repo create`는 서버만 가져옵니다.

명령은 무엇을 가져왔는지 표준 오류에 한 줄로 알립니다. 예를 들면 `owngit: using server https://owngit.example.test and repository example-project from the origin remote`입니다. 표준 출력의 JSON은 바뀌지 않습니다.

Git은 사용자 설정과 시스템 설정을 비우고 물려받은 Git 환경 변수 없이 원격을 읽습니다. 그래서 자격 증명 도우미, include, 설정 덮어쓰기가 끼어들지 않습니다. 일반 HTTP에는 여전히 `--accept-insecure-http`가 필요합니다. 다음 경우에는 어떤 서버에도 접속하기 전에 멈춥니다.

- `origin_unavailable`: 디렉터리가 클론 안에 있지 않거나 클론에 `origin` 원격이 없습니다.
- `origin_ambiguous`: `origin`에 URL이 둘 이상 있습니다.
- `origin_unsupported`: `origin`이 GitHub URL, SSH 주소, 로컬 경로 같은 다른 종류의 주소입니다. 메시지에 주소를 다시 적지 않습니다.
- `origin_server_mismatch`: `--server`가 `origin`과 다른 서버를 가리키는데 `--repository`가 없습니다.

### 자격 증명 파일과 서버 줄

클론의 `origin`은 어떤 서버든 가리킬 수 있으므로, `origin`에서 가져온 서버라고 해서 믿을 수 있다는 뜻은 아닙니다. 비밀번호 파일이나 자격 증명 파일은 첫 줄에 그 서버가 적혀 있을 때만 `origin`에서 가져온 서버로 보냅니다.

```text
owngit-server: https://owngit.example.test
SECRET
```

첫 줄은 파일의 맨 처음에서 시작하며 정확히 `owngit-server:`, 공백 하나, 경로 없는 HTTP(S) 오리진 하나로 이루어집니다. 비밀 값은 마지막 줄에 둡니다. 바이트 순서 표시나 빈 줄 뒤에 오거나 대소문자가 다르게 적힌 것처럼 이 줄과 비슷하기만 한 첫 줄은 거부합니다. 이 줄이 없는 파일은 기존 형식이며, 비밀 값을 한 줄에 담아야 하고, `--server`를 직접 넘기면 지금처럼 동작합니다. 이 줄이 있는 파일은 `--server`를 직접 넘긴 경우를 포함해 다른 서버로는 보내지 않으므로, 이 줄은 비밀 값을 항상 한 서버에 묶습니다. 관리자 비밀번호 파일과 러너 토큰 파일도 이 줄을 받아들이며, 이 명령들에는 항상 `--server`를 직접 넘겨야 합니다. 이 줄에는 서버만 적고, 비밀 값의 종류는 파일을 읽는 플래그가 정합니다.

`helper-credential create`는 자신이 사용한 서버로 이 줄을 씁니다. 직접 만든 공유 비밀번호 파일을 묶으려면 텍스트 편집기로 맨 위에 이 줄을 넣으세요. 이렇게 하면 소유자만 읽을 수 있는 권한이 그대로 유지됩니다. macOS나 Linux에서는 본인만 읽을 수 있는 새 파일을 만들 수도 있습니다.

```sh
(umask 077; { printf 'owngit-server: %s\n' https://owngit.example.test; cat password-file; } > bound-password-file)
```

거부 코드는 `credential_origin_required`(서버를 `origin`에서 가져왔는데 파일에 서버가 없음), `credential_origin_mismatch`(파일에 다른 서버가 적혀 있음), `invalid_credential_origin`(첫 줄 형식이 잘못됨)입니다. 어느 경우에도 아무것도 보내지 않습니다. 체크 에이전트 토큰 파일을 직접 읽는 스크립트는 마지막 줄을 읽어야 합니다.

## 작업 흐름

작업 단위마다 고정된 작업을 하나 만듭니다. 작업은 리비전이 바뀌어도 식별자를 유지하며, 새 커밋이 생겨도 수정 라운드 한도가 초기화되지 않습니다.

```sh
owngit check task new \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --title "Fix the failing build"
```

체크를 실행합니다. `--check`를 빼면 테스트하는 리비전, 곧 `--workdir`의 `HEAD`에 커밋된 `.owngit/checks.json`의 체크를 실행합니다. 워킹 트리의 사본이나 다른 리비전에서 서버에 기록된 구성은 쓰지 않으므로, 다른 브랜치의 명령이 내 컴퓨터에서 실행될 일은 없습니다. 그 리비전에 이 파일이 없거나 파일이 올바르지 않으면 아무것도 실행하기 전에 멈춥니다. `--check name=command`를 넘기면 대신 바로 그 체크들을 실행하고 기록합니다.

```sh
owngit check run \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID \
  --check "unit=go test ./..." \
  --check "lint=go vet ./..."
```

`check run`은 실행하기 전에 시도(attempt)를 등록합니다. 그래서 서버가 저장소 전체에 걸친 순번을 매기고, 다시 보낸 요청도 한 번만 처리됩니다. 체크 에이전트는 실행 전후에 워킹 트리를 관찰합니다. 처음에 리비전을 읽지 못하면 등록하기 전에 실행을 멈춥니다. 처음 상태 읽기만 실패하면 상태는 `unknown`입니다. 마지막 관찰에서 리비전을 읽지 못하면 `unknown`이 되고, 리비전이 바뀌었거나 변경이 있거나 상태 읽기가 실패하면 `dirty`로 기록합니다. `dirty`와 `unknown`은 어느 쪽도 깨끗한 커밋을 테스트했다는 증거가 아닙니다.

에이전트에게 수정을 맡기기 전에 수정 라운드를 예약하고, 그 라운드를 확인용 실행에 넘기세요.

```sh
owngit check cycle reserve \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID

owngit check run \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID --cycle CYCLE_ID
```

저장된 상태를 읽습니다.

```sh
owngit check status --task TASK_ID --server URL --repository ID --credential-file PATH
owngit check log --attempt ATTEMPT_ID --server URL --repository ID --credential-file PATH
owngit check config show --server URL --repository ID --credential-file PATH
owngit check cycle list --task TASK_ID --server URL --repository ID --credential-file PATH
```

## 명령 참조

`check task new`는 작업을 만듭니다. 플래그는 `--title`, `--server`, `--repository`, `--credential-file`, `--accept-insecure-http`입니다.

`check run`은 체크를 실행하고, `--no-upload`가 없으면 시도를 기록합니다. 플래그는 `--task`(필수), `--cycle`, `--workdir`(기본값 `.`), `--timeout`(기본값 10분), `--output-limit`(기본값은 체크당 65536바이트), `--no-upload`, 그리고 여러 번 쓸 수 있는 `--check name=command`입니다. `--timeout`과 `--output-limit`은 0보다 커야 합니다. `--no-upload`를 쓰지 않으면 원격 플래그가 필요하며, 클론 안에서는 `--server`와 `--repository`를 `origin`에서 가져올 수 있습니다.

`check cycle reserve`는 수정 라운드 하나를 예약합니다. 플래그는 `--task`(필수)와 원격 플래그입니다. `check cycle list`는 예약한 라운드 목록을 보여 줍니다.

`check status`는 작업과 가장 최근 시도를 읽습니다. `check log`는 `--attempt`로 지정한 원본 로그 하나를 읽습니다. `check config show`는 브랜치와 관계없이 저장소에 가장 최근에 기록된 구성을 읽습니다. `check run`은 이 구성을 쓰지 않습니다.

`helper-credential create`는 토큰을 발급합니다. 플래그는 `--label`, `--output`(필수), `--server`, `--repository`, `--password-file`, `--accept-insecure-http`입니다. `helper-credential list`와 `helper-credential revoke --id ID`로 기존 토큰을 관리합니다.

## 저장소

`owngit repo`는 저장소 목록을 보여 주고, 저장소 하나의 정보를 읽고, 새 저장소를 만든 뒤 JSON 객체 하나를 출력합니다. `owngit pr`과 같이 일반 접근을 사용합니다. 공유 일반 접근 비밀번호를 `--password-file`로 넘기고, 접근이 열려 있으면 생략합니다. 삭제나 이름 변경은 없습니다.

```sh
owngit repo list --server https://owngit.example.test
owngit repo show --server https://owngit.example.test --repository example-project
owngit repo create --server https://owngit.example.test --name example-project \
  --description "Optional description"
```

저장소마다 `id`, `name`, `description`, `created_at`, `clone_url`이 있습니다. `repo show`는 그 순간 저장소의 브랜치를 읽을 수 있으면 `default_branch`도 보여 줍니다. `repo list`는 저장소를 최대 1000개까지 돌려주고, 더 있으면 `truncated`가 true입니다. `repo create`는 브라우저 양식과 같은 이름과 설명 규칙을 적용합니다. 이름이 이미 쓰이고 있으면 `repository_exists`, 양식이 거부할 이름이면 `invalid_repository_name`이나 `reserved_repository_name`, 설명이 500바이트를 넘으면 `invalid_repository_description`으로 실패합니다.

## 풀 리퀘스트 변경 내용

`owngit pr diff --number N`은 풀 리퀘스트가 바꾸는 내용을 JSON 객체 하나로 출력합니다. 비교한 원본과 대상 커밋, 두 커밋의 병합 기준(merge base), 줄 수가 붙은 변경 파일 목록, 패치가 들어 있습니다. `owngit pr`과 같이 일반 접근을 사용하고, 클론 안에서는 서버와 저장소를 `origin`에서 읽습니다.

```sh
owngit pr diff --number 3
owngit pr diff --number 3 --stat
owngit pr diff --number 3 --patch
owngit pr diff --number 3 --source-oid SOURCE_OID --target-oid TARGET_OID
```

변경 내용은 풀 리퀘스트 페이지와 같이 병합 기준에서 원본까지 셉니다. 기본값으로는 풀 리퀘스트의 현재 원본과 대상 커밋을 한 번 읽고 바로 그 두 커밋을 비교합니다. 그래서 읽는 도중에 브랜치가 움직여도 `source.oid`와 `target.oid`는 패치와 일치합니다. 읽은 내용을 리뷰하려면 같은 객체 ID를 `pr review submit`에 넘깁니다. 그사이 브랜치가 움직였으면 리뷰는 `stale_revision`으로 실패합니다. 병합된 풀 리퀘스트의 현재 쌍은 병합한 커밋 쌍입니다.

`--source-oid`와 `--target-oid`는 비교할 커밋 쌍을 고정합니다. 이 쌍은 현재 쌍이거나, 리뷰를 요청한 쌍처럼 그 풀 리퀘스트에 기록된 쌍이어야 합니다. 다른 쌍은 `revision_not_recorded`로 실패하고, 둘 중 하나만 넘기면 CLI에서는 `invalid_arguments`, API에서는 `invalid_revision`으로 실패합니다. 고정한 쌍에서 브랜치가 움직였으면 결과는 여전히 그 쌍을 보여 주면서 `moved`를 true로 두고 현재 쌍을 `current`에 담습니다.

결과에는 크기 제한이 있습니다. 패치에서 빠진 파일이 있으면 `truncated`가 true이고, 파일 목록에서도 빠진 파일이 있으면 `incomplete`가 true입니다. 패치는 언제나 파일 경계에서 끝납니다. `reason`은 빠진 이유를 알려 줍니다. `output_limit`는 비교 결과가 8 MiB 제한에 닿은 경우, `time_limit`는 Git의 시간이 다 된 경우(나중에 다시 시도하면 더 읽을 수 있습니다), `response_limit`는 4 MiB 응답에 맞추려고 잘라 낸 경우입니다. 두 브랜치에 공통 커밋이 없거나 병합 기준이 둘 이상이면 `unavailable`이 `no_merge_base` 또는 `multiple_merge_bases`이고, 파일 목록과 패치가 없습니다.

`--stat`은 `patch`를 뺀 같은 객체를 출력합니다. `--patch`는 패치 텍스트만 출력하고, 비교한 커밋과 브랜치 이동이나 잘림 여부는 표준 오류에 씁니다. API 경로는 `GET /api/v1/repositories/ID/pull-requests/N/diff`이며, 선택 쿼리 매개변수로 `source_oid`와 `target_oid`를 받습니다.

## 결과 읽기

`check run`은 JSON 객체 하나를 출력하고 다음 코드로 끝납니다.

- `0`: 모든 체크가 통과했습니다. 시도가 기록되었는지는 `registered`와 `uploaded`로 확인하세요. 로컬 `--no-upload` 실행도 0으로 끝납니다.
- `1`: 통과하지 못한 체크가 하나 이상 있습니다.
- `2`: 이 클라이언트가 시도가 기록되었는지 확인하지 못했습니다.
- `130`: 실행이 취소되었습니다.

`check run`이 체크를 하나도 실행하기 전에 멈추면, 결과 대신 오류 객체 `{"ok":false,"error":{"code":...,"message":...}}`를 출력하고 1로 끝납니다. 인수가 잘못되었을 때, 커밋된 구성이 없거나 올바르지 않을 때(`checks_not_configured`, `invalid_check_configuration`), 그리고 예약하지 않은 `--cycle`처럼 서버가 등록을 거부했을 때입니다. 이때는 아무것도 실행되지 않았고 아무것도 기록되지 않았습니다.

JSON 객체에는 `ok`, `registered`, `uploaded`, `attempt_id`, `cycle_id`, `task`, `attempt`, `correction_cycles_remaining`, `results`, `upload_error`가 들어 있습니다. `attempt` 객체에는 `status`, `revision_oid`, `worktree_state`, `summary`, `cleanup_failed`, `log_truncated`와 실행 한도가 들어 있습니다. `results`의 각 항목에는 `name`, `command`, `status`, `exit_code`, `duration_ms`, `output_excerpt`, `truncated`, `cleanup_error`가 들어 있습니다.

체크별 상태는 `passed`, `failed`, `error`, `cancelled`, `incomplete`, `unavailable`입니다. 서버는 체크 에이전트가 보낸 종합 결과를 믿지 않고, 개별 결과로 시도 상태를 다시 계산합니다. 정리 오류가 있으면 명령의 종료 코드가 보이더라도 결과는 `error`입니다. 출력이 체크의 출력 한도를 넘으면 결과는 `incomplete`입니다. 줄인 발췌나 로그는 잘렸다고 표시하며 상태는 바꾸지 않습니다. 구성된 체크가 하나도 없으면 `passed`가 아니라 `unavailable`입니다. 등록된 뒤 완료를 보고하지 않은 시도는 `pending`으로 계속 보입니다.

`upload_error`는 이 클라이언트가 등록이나 완료를 확인하지 못했다는 뜻입니다. 응답을 받지 못했더라도 서버에는 등록이나 완료가 받아들여져 있을 수 있습니다. 그러니 예약이나 실행을 되풀이하기 전에 `check status`를 확인하고, 시도가 없다고 단정하지 마세요. 이는 기록된 실패 체크와 다릅니다. 기록된 실패 체크에는 `failed` 결과를 가진 저장된 시도가 있습니다. 서버에 아예 연결하지 못해도 체크는 실행되고, 명령은 2로 끝나며, `upload_error`에 연결 거부나 TLS 오류 같은 원인이 나옵니다.

### 최상위 수정 횟수가 항상 측정값은 아닙니다

최상위 `correction_cycles_remaining`은 서버 응답이 있을 때만 채워집니다. 따로 "알 수 없음"을 나타내는 값이 없어서, 응답이 값을 채우지 않았어도 이 필드는 `0`으로 출력됩니다. 이 `0`은 한도를 다 썼다는 뜻이 아니라 클라이언트가 한도를 읽지 않았다는 뜻입니다. 실행 결과에 `task` 객체가 함께 있을 때만 측정된 한도가 담깁니다. 그 값은 `task.correction_cycles_remaining`에서 읽으세요.

측정되지 않은 `0`이 출력되는 경우는 두 가지입니다.

- `--no-upload`는 서버에 접속하지 않으므로 서버의 작업은 바뀌지 않습니다.
- 등록이 실패해 `registered`가 false이고 `upload_error`가 설정된 경우는 시도가 없다는 뜻이 아니라 확인되지 않았다는 뜻입니다. 요청이 받아들여졌다면 저장된 시도가 있고 순번이 올라갔습니다. 받아들여지지 않았다면 아무것도 바뀌지 않았습니다. 어느 쪽이라고도 가정하지 마세요.

확인되지 않은 시도가 있으면 고정된 작업 식별자를 그대로 쓰고, 출력된 `attempt_id`를 진단 근거로 보관한 뒤 `check status --task TASK_ID`를 확인하세요. 불확실함을 풀려고 체크를 다시 실행하거나 라운드를 예약하지 마세요. `check run`은 실행할 때마다 새 시도 식별자를 만들고 기존 식별자를 다시 제출할 수 없으므로, 다시 실행하면 별개의 시도가 시작됩니다. 응답을 받지 못한 요청을 클라이언트가 스스로 재시도할 때는 같은 본문을 보내므로 안전하지만, 셸에서 명령을 다시 실행하는 것은 안전하지 않습니다. `check status`로 바로 그 시도를 확인할 수 없으면 확인되지 않았다고 보고하세요. 측정되지 않은 `0`을 근거로 한도를 다 썼다고 보고하거나 승인된 수정을 멈추지 마세요.

## 수정 라운드 한도

작업의 한도는 자동 수정 라운드 세 번입니다. 에이전트에게 수정을 맡기기 전에 라운드를 예약하고, 예약 응답의 `cycle.id`를 확인용 실행에 `--cycle`로 넘기세요. 진행 중인 작업에서 이미 승인된 수정은 사용자에게 다시 묻지 않고 한도 안에서 계속할 수 있습니다. 요청받지 않은 수정은 시작하지 마세요. 새 식별자를 만들지 말고 고정된 작업 식별자와 라운드 식별자를 다시 쓰세요. 예약한 라운드는 이어지는 체크가 통과하든 실패하든 한 번으로 세며, 라운드 안의 재시도는 그 라운드를 다시 씁니다. 첫 체크와 직접 다시 실행한 체크는 라운드를 쓰지 않으며, 사용할 수 없거나 취소된 실행만으로는 라운드가 생기지 않습니다. 한도를 다 쓰면 예약 명령이 `correction_budget_exhausted`를 돌려줍니다. 자동으로 계속하지 말고 해결되지 않은 작업을 보고하세요. 한도를 다 쓴 뒤에도 직접 실행한 체크는 기록할 수 있습니다.

한도를 다 썼는지는 `check status`나, `correction_budget_exhausted`를 돌려준 예약 호출로만 알 수 있습니다. 실행 결과의 최상위 `correction_cycles_remaining`이 `0`이어도, 같은 실행 결과에 `task` 객체가 없으면 한도를 다 썼다는 뜻이 아닙니다.

## 제한

- 체크는 참고용입니다. 병합을 막지 않으며, 체크를 통과했다고 코드가 옳다는 증거가 되지는 않습니다. 프로젝트나 팀이 더 엄격한 리뷰나 체크 규칙을 요구할 수 있으며, 이 연동은 그 규칙보다 우선하지 않습니다.
- 체크 에이전트는 사용자의 환경과 권한을 물려받습니다. 샌드박스가 아니며, 체크는 사용자 계정이 접근할 수 있는 파일과 인증 정보를 읽을 수 있습니다.
- 변경이 있거나 상태를 알 수 없는 워킹 트리는 테스트한 커밋이 아닙니다. 그 리비전을 테스트했다고 말하지 말고 기록된 워킹 트리 상태를 보고하세요.
- `--no-upload`는 로컬에서 실행되며 서버에 기록되지 않습니다. 서버에 기록된 근거라고 설명하지 마세요. 출력에 `task` 객체가 없으며, 최상위 `correction_cycles_remaining`의 `0`은 측정한 한도가 아니라 읽지 않은 필드입니다.
- 실패한 체크를 무작정 다시 시도하지 말고, 체크를 통과시키려고 커밋된 체크 구성을 약하게 바꾸거나 다른 것으로 바꾸지 마세요.
- 코딩 세션을 시작하거나 재개하지 말고, 모델을 바꾸거나, 읽기 전용 리뷰어에게 도구를 주거나, 팀을 다시 불러오거나, 인증 파일이나 대화 기록을 살펴보지 마세요.
- 스킬이 반드시 로드되어 쓰인다는 보장은 없습니다. 직접 호출과 이 안내의 수동 명령이 확실한 방법입니다.
- 읽기 전용 권한만 있는 리뷰어는 체크를 실행할 수 없습니다. 실행 권한이 있고 승인된 참여자가 체크를 실행해 그 출처와 함께 결과를 전달합니다. 이것이 도구를 주거나, 역할을 바꾸거나, 팀 정책을 정하는 것은 아닙니다.

## 설치 위치

GitHub Releases의 포터블 압축 파일은 이 자료를 소스 기준 상대 경로 그대로 담고 있습니다. 그래서 압축을 푼 뒤에도 안내 문서의 스킬 링크가 동작합니다.

- `docs/CODING_TOOLS.md`
- `docs/CODING_TOOLS.ko.md`
- `integrations/skills/owngit-checks/SKILL.md`

서명되지 않은 macOS 앱 프로토타입은 같은 상대 경로로 `OwnGit.app/Contents/Resources/` 아래에 담고 있고, Debian 프로토타입 패키지는 `/usr/share/doc/owngit/` 아래에 설치합니다.

Homebrew와 npm 패키지는 `owngit` 명령, 라이선스, 고지 사항만 설치하며 이 안내 문서와 스킬 파일은 설치하지 않습니다. 이 방법으로 설치했다면 `owngit skill --install DIR`를 실행하세요. 명령 안에 스킬이 들어 있습니다.

압축을 푼 포터블 압축 파일에서는 복사할 수도 있습니다.

```sh
mkdir -p ~/.agents/skills
cp -R integrations/skills/owngit-checks ~/.agents/skills/
```

### 체크 에이전트 실행 파일 찾기

이 안내의 명령은 `owngit`을 호출합니다. 소스 빌드, 포터블 압축 파일, macOS 앱은 이 실행 파일을 `PATH`에 추가하지 않습니다.

- 소스 빌드: README에서 빌드한 대로 체크아웃 안의 `bin/owngit`
- 포터블 압축 파일: 압축을 푼 디렉터리에서 `./owngit`을 실행하거나 전체 경로를 씁니다.
- macOS 프로토타입 앱: 앱 번들 안의 `OwnGit.app/Contents/Resources/bin/owngit`
- Debian 프로토타입 패키지: 보통 `PATH`에 들어 있는 `/usr/bin/owngit`

실행 파일이 `PATH`에 없으면 코딩 도구를 대신해 셸 시작 파일을 고치지 말고, 코딩 도구에 전체 경로를 알려 주세요.

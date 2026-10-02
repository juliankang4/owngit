# 코딩 도구 연동

<p align="center"><a href="CODING_TOOLS.md">English</a> | <b>한국어</b></p>

이 안내는 Codex, Claude Code, Pi 같은 코딩 도구를 OwnGit에 연결하는 사람과 그 코딩 도구가 읽는 문서입니다. 코딩 도구는 다음 두 방법 중 하나로 OwnGit을 씁니다.

- 사용자 환경에서 `owngit` 명령을 실행하고 버전이 붙은 JSON 결과를 읽습니다. 이 역할을 하는 명령을 체크 에이전트(helper)라고 부릅니다. 어떤 명령을 실행할지는 공용 Agent Skill이 알려 줍니다.
- MCP를 지원하는 도구는 같은 명령을 도구로 제공하는 로컬 [MCP 서버](#mcp-서버) `owngit mcp`를 실행합니다.

어느 쪽이든 코딩 도구는 풀 리퀘스트를 읽고 리뷰하고, 저장소 목록을 보고 새로 만들고, 프로젝트 체크를 기록할 수 있습니다. 체크를 기록할 때 OwnGit은 작업 단위마다 작업을 하나 두고 리비전이 바뀌어도 그 작업을 그대로 씁니다. 체크 결과는 테스트한 바로 그 리비전에 묶어 서버에 저장하며 수정 라운드는 작업마다 세 번까지 허용합니다. 스킬은 지침일 뿐입니다. 코딩 도구는 스킬을 무시할 수 있고 서버는 체크 에이전트가 실제로 제출한 것만 기록합니다.

## 준비 사항

코딩하는 컴퓨터에서 접속할 수 있는 OwnGit 서버, 저장소 식별자, 코딩하는 컴퓨터에 있는 `owngit` 실행 파일([체크 에이전트 실행 파일 찾기](#체크-에이전트-실행-파일-찾기) 참고), 비공개 파일로 전달된 저장소 범위의 체크 에이전트 토큰이 필요합니다. 먼저 이미 전달받은 사실과 프로젝트에서 알 수 있는 사실을 확인하고 사용자에게는 사용자가 정해야 하는데 아직 정하지 않은 선택만 물으세요.

관리자 비밀번호로 토큰을 만듭니다.

```sh
owngit helper-credential create \
  --server https://owngit.example.test \
  --repository example-project \
  --label laptop \
  --password-file /path/to/admin-password-file \
  --output /path/to/helper-token
```

토큰은 소유자만 읽을 수 있는 `--output` 파일에만 쓰이고 서버에는 해시로만 저장되므로 명령 인수, 문서, 로그에 넣지 마세요. `--output` 위치에 파일이나 심볼릭 링크가 이미 있으면 교체하지 않고 알려 줍니다. 파일의 첫 줄에는 토큰을 발급한 서버가 적히고([자격 증명 파일과 서버 줄](#자격-증명-파일과-서버-줄) 참고), 명령은 그 서버를 `token_file_server`로 출력합니다.

사용자가 해당 요청에 `--accept-insecure-http`를 넘기지 않으면 체크 에이전트는 일반 HTTP를 거부합니다. 사용자를 대신해 이 플래그를 붙이지 마세요.

## 스킬 찾기

공용 스킬은 [integrations/skills/owngit-checks/SKILL.md](../integrations/skills/owngit-checks/SKILL.md)에 있습니다. GitHub Releases의 포터블 압축 파일은 이 스킬을 같은 경로에 `docs/CODING_TOOLS.md`, `docs/CODING_TOOLS.ko.md`와 함께 담고 있습니다. Arch Linux 패키지는 같은 파일을 `/usr/share/doc/owngit-bin/` 아래에 설치합니다. 스킬은 `/usr/share/doc/owngit-bin/integrations/skills/owngit-checks/SKILL.md`에 있습니다. Homebrew와 npm 패키지는 `owngit` 명령만 설치하지만 모든 실행 파일에는 함께 배포된 스킬이 들어 있습니다.

```sh
owngit skill --install ~/.agents/skills
```

`--install DIR`는 `DIR/owngit-checks/SKILL.md`를 쓰고 `status`가 `installed`, `already_current`, `replaced` 중 하나인 JSON 결과를 출력합니다. 내용이 다른 파일은 사용자가 고친 내용일 수 있으므로 아무것도 바꾸지 않고 `skill_modified`로 실패합니다. `owngit skill --print`로 비교한 뒤 `--replace`를 붙이면 배포된 스킬을 설치하고 이전 파일을 옆에 `SKILL.md.previous-TIMESTAMP`로 남깁니다(`previous`에 이름이 나옵니다). 심볼릭 링크이거나 일반 파일이 아닌 `SKILL.md`는 `skill_target_invalid`로 거부합니다. 압축을 푼 포터블 압축 파일에서는 복사할 수도 있습니다.

```sh
mkdir -p ~/.agents/skills
cp -R integrations/skills/owngit-checks ~/.agents/skills/
```

`owngit-checks` 디렉터리를 링크하지 말고 설치하거나 복사해서 코딩 도구가 찾아보는 위치에 두세요.

- Codex: 저장소의 `.agents/skills/owngit-checks`, 또는 사용자 전체에 쓰려면 `~/.agents/skills/owngit-checks`. Codex는 현재 디렉터리부터 저장소 루트까지 `.agents/skills`를 찾습니다.
- Pi: 프로젝트의 `.agents/skills/owngit-checks`나 `.pi/skills/owngit-checks`, 또는 사용자 전체에 쓰려면 `~/.agents/skills/owngit-checks`나 `~/.pi/agent/skills/owngit-checks`.

설명으로 고르게 두면 놓칠 수 있으니 스킬은 직접 호출하세요. Codex 앱은 `@`로 스킬을 고르고, Codex CLI와 IDE 확장은 `/skills`로 스킬 목록을 보여 주며 `$`로 스킬을 지정할 수 있고, Pi는 `/skill:owngit-checks`를 등록합니다. 스킬이 로드되지 않았으면 이 안내의 명령을 직접 실행하세요.

### 체크 에이전트 실행 파일 찾기

Homebrew, npm, Arch Linux 패키지는 `owngit`을 `PATH`에 넣습니다. 소스 빌드와 포터블 압축 파일은 넣지 않으므로 소스 체크아웃에서는 `bin/owngit`, 압축을 푼 디렉터리에서는 `./owngit`을 쓰세요. 한 줄 설치 스크립트는 Linux와 macOS에서는 `~/.local/bin/owngit`이나 `/usr/local/bin/owngit`에, Windows에서는 `%LOCALAPPDATA%\Programs\OwnGit` 아래의 릴리스 폴더에 프로그램을 두며 `PATH` 설정은 바꾸지 않습니다. 실행 파일이 `PATH`에 없으면 코딩 도구를 대신해 셸 시작 파일을 고치지 말고, 코딩 도구에 전체 경로를 알려 주세요.

## 코딩 도구 화면

대시보드 사이드바에 코딩 도구(`/coding-tools`)가 있습니다. 서버 주소와 이 서버에 맞춘 명령을 보여 주며 명령마다 복사 버튼이 있습니다.

- `owngit skill --install ~/.agents/skills`는 스킬을 설치합니다.
- `claude mcp add --scope user owngit -- owngit mcp --server ORIGIN`은 Claude Code에, `codex mcp add owngit -- owngit mcp --server ORIGIN`은 Codex에 MCP 서버를 추가합니다. stdio 방식을 쓰는 다른 클라이언트에는 `owngit mcp --server ORIGIN`을 설정합니다.

주소가 일반 HTTP이면 명령에 `--accept-insecure-http`가 들어가고 화면에 경고가 나옵니다. 서버가 공유 비밀번호를 요구하면 명령에 `--password-file PASSWORD_FILE`이 들어갑니다. 비밀번호를 나만 읽을 수 있는 파일에 저장하고 그 경로를 넣으세요. 체크 도구를 쓰려면 `--credential-file`과 저장소(`--repository`, 또는 클론을 가리키는 `--workdir`)도 필요합니다. 코딩 도구의 `PATH`에 `owngit`이 없으면 `--` 뒤에 전체 경로를 쓰세요.

화면에는 모든 저장소의 체크 에이전트 토큰이 이름, 저장소, 발급 시각, 마지막 사용(또는 "사용한 적 없음"), 폐기 여부와 함께 나옵니다. 단, 관리자로 확인한 브라우저이거나 관리자 비밀번호 확인이 꺼져 있을 때만 나오고, 그 밖에는 관리자 확인 링크만 보입니다. 이름은 발급할 때 붙인 것이며 누가 썼는지를 증명하지 않습니다. 토큰 자체는 어디에도 보이지 않습니다. 발급과 폐기는 저장소의 체크 에이전트 토큰 화면에서 합니다.

또 모든 저장소에서 가장 최근에 바뀐 체크 작업 10개(작업 자체가 바뀐 때와 가장 최근 시도 중 늦은 쪽 기준)를 보여 주고, 더 있으면 그렇게 알립니다.

## 클론 안에서 실행하기

OwnGit 저장소의 클론 안에서는 `owngit pr`, `owngit check`, `owngit repo`가 `--server`나 `--repository`가 없을 때 서버와 저장소를 스스로 찾습니다. 클론의 `origin` 원격을 읽고 OwnGit 클론 주소인 `http(s)://HOST[:PORT]/git/NAME.git` 형태만 받아들입니다. `check run`은 `--workdir`가 들어 있는 클론을 읽고, 다른 명령은 현재 디렉터리가 들어 있는 클론을 읽습니다. 직접 넘긴 플래그가 항상 우선하고, `--repository`만 넘기면 서버는 계속 `origin`에서 가져오며, `repo list`와 `repo create`는 서버만 가져옵니다. 명령은 무엇을 가져왔는지 표준 오류에 한 줄로 알립니다. 예를 들면 `owngit: using server https://owngit.example.test and repository example-project from the origin remote`이며, 표준 출력의 JSON은 바뀌지 않습니다. 일반 HTTP에는 여전히 `--accept-insecure-http`가 필요합니다.

다음 경우에는 어떤 서버에도 접속하기 전에 멈춥니다. `origin_unavailable`(클론 안이 아니거나 `origin`이 없음), `origin_ambiguous`(`origin`에 URL이 둘 이상), `origin_unsupported`(GitHub URL, SSH 주소, 로컬 경로 같은 다른 종류의 주소), `origin_server_mismatch`(`--server`가 `origin`과 다른 서버를 가리키는데 `--repository`가 없음)입니다.

저장소 [이름을 바꾸면](OPERATIONS.ko.md#저장소-이름-바꾸기) 클론의 `origin`에는 예전 주소가 그대로 남습니다. 이때 `owngit pr`, `owngit repo`처럼 일반 접근이나 관리자 비밀번호를 쓰는 명령은 `repository_moved`로 멈추고 `details.address`에 새 이름이 나옵니다. 체크 에이전트 명령은 체크 에이전트 토큰으로 90일 동안 예전 주소에서 계속 동작하다가 그 뒤에 멈춥니다. `git remote set-url origin`으로 클론의 주소를 새 클론 주소로 바꾸세요.

### 자격 증명 파일과 서버 줄

클론의 `origin`은 어떤 서버든 가리킬 수 있으므로, `origin`에서 가져온 서버라고 해서 믿을 수 있다는 뜻은 아닙니다. 비밀번호 파일이나 자격 증명 파일은 첫 줄에 그 서버가 적혀 있을 때만 `origin`에서 가져온 서버로 보냅니다.

```text
owngit-server: https://owngit.example.test
SECRET
```

첫 줄은 파일의 맨 처음에서 시작하며 정확히 `owngit-server:`, 공백 하나, 경로 없는 HTTP(S) 오리진 하나로 이루어지고, 비밀 값은 마지막 줄에 둡니다. 바이트 순서 표시나 빈 줄 뒤에 오거나 대소문자가 다른 줄처럼 비슷하기만 한 첫 줄은 거부합니다. 이 줄이 없는 파일은 비밀 값을 한 줄에 담아야 하며 `--server`를 직접 넘기면 지금처럼 동작하고, 이 줄이 있는 파일은 `--server`를 직접 넘겨도 다른 서버로는 보내지 않습니다. 관리자 비밀번호 파일과 러너 토큰 파일도 이 줄을 받아들이며 이 명령들에는 항상 `--server`를 직접 넘겨야 합니다. 거부 코드는 `credential_origin_required`(서버를 `origin`에서 가져왔는데 파일에 서버가 없음), `credential_origin_mismatch`(파일에 다른 서버가 적혀 있음), `invalid_credential_origin`(첫 줄 형식이 잘못됨)이며, 어느 경우에도 아무것도 보내지 않습니다. 체크 에이전트 토큰 파일이나 러너 토큰 파일을 직접 읽는 스크립트는 마지막 줄을 읽어야 합니다.

`helper-credential create`와 `runner-credential issue`는 자신이 사용한 서버로 이 줄을 씁니다. 직접 만든 공용 비밀번호 파일을 묶으려면 텍스트 편집기로 맨 위에 이 줄을 넣거나(파일 권한이 그대로 유지됩니다), macOS나 Linux에서 본인만 읽을 수 있는 새 파일을 만드세요.

```sh
(umask 077; { printf 'owngit-server: %s\n' https://owngit.example.test; cat password-file; } > bound-password-file)
```

Windows에서 메모장이나 `echo`로 만든 파일은 폴더의 접근 항목을 상속하므로 비공개가 아니라는 이유로 거부됩니다. PowerShell에서 파일을 만들고 본인 계정으로 접근을 제한한 뒤 비밀번호를 적고 서버 줄을 넣으세요.

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
[IO.File]::WriteAllText($file, "owngit-server: https://owngit.example.test`n" + [IO.File]::ReadAllText($file))
```

이 명령은 Windows PowerShell 5.1과 PowerShell 7에서, 일반 창과 관리자 권한으로 실행한 창 모두 동작합니다. 관리자 창에서는 Administrators 그룹이 파일 소유자가 되는데, 본인 계정만 접근할 수 있으면 OwnGit은 이 소유자를 받아들입니다. `Read-Host -AsSecureString`은 비밀번호를 화면과 기록에 남기지 않습니다.

## 작업 흐름

작업 단위마다 고정된 작업을 하나 만듭니다. 작업은 리비전이 바뀌어도 식별자를 유지하며 새 커밋이 생겨도 수정 라운드 한도가 초기화되지 않습니다.

```sh
owngit check task new \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --title "Fix the failing build"
```

체크를 실행합니다. `--check`를 빼면 `--workdir`의 `HEAD`에 커밋된 `.owngit/checks.json`의 체크를 실행합니다. 워킹 트리의 사본이나 다른 리비전에서 서버에 기록된 구성은 쓰지 않으며 파일이 없거나 올바르지 않으면 아무것도 실행하기 전에 멈춥니다. `--check name=command`를 넘기면 대신 바로 그 체크들을 실행하고 기록합니다.

```sh
owngit check run \
  --server https://owngit.example.test \
  --repository example-project \
  --credential-file /path/to/helper-token \
  --task TASK_ID \
  --check "unit=go test ./..." \
  --check "lint=go vet ./..."
```

`check run`은 실행하기 전에 시도(attempt)를 등록합니다. 그래서 서버가 저장소 전체에 걸친 순번을 매기고 다시 보낸 요청도 한 번만 처리됩니다. 체크 에이전트는 실행 전후에 Git과 사용자의 Git 설정으로 워킹 트리를 관찰합니다. 처음에 리비전을 읽지 못하면 등록하기 전에 멈추고, 처음 상태 읽기만 실패하면 상태는 `unknown`입니다. 마지막 관찰에서 리비전을 읽지 못해도 `unknown`이며, 리비전이 바뀌었거나 변경이 있거나 상태 읽기가 실패하면 `dirty`로 기록합니다. `dirty`도 `unknown`도 깨끗한 커밋을 테스트했다는 증거가 아닙니다.

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
owngit check task list --server URL --repository NAME --credential-file PATH
owngit check status --task TASK_ID --server URL --repository NAME --credential-file PATH
owngit check log --attempt ATTEMPT_ID --server URL --repository NAME --credential-file PATH
owngit check config show --server URL --repository NAME --credential-file PATH
owngit check cycle list --task TASK_ID --server URL --repository NAME --credential-file PATH
```

## 명령 참조

모든 명령은 `--server`, `--repository`, `--credential-file`, `--accept-insecure-http`(원격 플래그)를 받으며 클론 안에서는 `--server`와 `--repository`를 `origin`에서 가져올 수 있습니다.

- `check task new`는 작업을 만들고(`--title`), `check task list`는 저장소의 작업과 각 작업의 수정 라운드 한도를 보여 줍니다.
- `check run`은 체크를 실행하고, `--no-upload`가 없으면 시도를 기록합니다. 플래그는 `--task`(필수), `--cycle`, `--workdir`(기본값 `.`), `--timeout`(기본값 10분), `--output-limit`(기본값 체크당 65536바이트, 둘 다 0보다 커야 하며 어느 쪽이든 넘은 체크는 멈추고 완료되지 않은 것으로 끝납니다), `--no-upload`(이때 원격 플래그는 선택 사항), 여러 번 쓸 수 있는 `--check name=command`입니다.
- `check cycle reserve`는 수정 라운드 하나를 예약하고(`--task` 필수), `check cycle list`는 예약한 라운드 목록을 보여 줍니다.
- `check status`는 작업과 가장 최근 시도를 읽고, `check log`는 `--attempt`로 지정한 원본 로그 하나를 읽으며, `check config show`는 브랜치와 관계없이 저장소에 가장 최근에 기록된 구성을 읽습니다. `check run`은 이 구성을 쓰지 않습니다.
- `helper-credential create`는 토큰을 발급하고(`--label`, `--output` 필수, `--credential-file` 대신 `--password-file`), `helper-credential list`와 `helper-credential revoke --id ID`로 기존 토큰을 관리합니다. `create`의 출력에는 저장소의 지금 주소가 `repository_address`로 나옵니다. `--repository` 없이 `helper-credential list`를 실행하면 모든 저장소의 토큰을 보여 주며, 토큰마다 저장소의 지금 주소가 `repository_address`로 나옵니다(API `GET /api/v1/helper-credentials`).
- `owngit tasks`는 대시보드에 보이는 대로 체크 작업을 출력합니다. 일반 접근을 쓰므로 체크 에이전트 토큰 없이 공유 비밀번호가 든 `--password-file`로 실행합니다. `--repository`가 없으면 모든 저장소에서 가장 최근에 바뀐 작업 10개와 `truncated`를, `--repository NAME`을 주면 그 저장소의 작업을 체크 탭과 같은 순서로, `--task TASK`까지 주면 그 작업과 최신 시도 100개, `attempts_truncated`를 출력합니다. 목록의 각 작업에는 `latest_attempt`(시도가 없으면 null)와 저장소의 지금 주소인 `repository_address`가 들어 있습니다. 오류에는 `repository_not_found`, `repository_moved`(이름을 바꾼 저장소의 예전 이름), `task_not_found`, `invalid_arguments`(`--repository` 없이 `--task`를 준 경우)가 있습니다. API 경로는 `GET /api/v1/tasks`, `GET /api/v1/tasks/NAME`, `GET /api/v1/tasks/NAME/TASK`입니다.

## 결과 읽기

`check run`은 JSON 객체 하나를 출력하고, 모든 체크가 통과했으면 `0`(로컬 `--no-upload` 실행도 마찬가지이며, 시도가 기록되었는지는 `registered`와 `uploaded`로 확인하세요), 통과하지 못한 체크가 있으면 `1`, 이 클라이언트가 시도가 기록되었는지 확인하지 못했으면 `2`, 실행이 취소되었으면 `130`으로 끝납니다. 인수가 잘못되었거나, 커밋된 구성이 없거나 올바르지 않거나(`checks_not_configured`, `invalid_check_configuration`), 예약하지 않은 `--cycle`처럼 서버가 등록을 거부해서 체크를 하나도 실행하기 전에 멈추면, 오류 객체 `{"ok":false,"error":{"code":...,"message":...}}`를 출력하고 1로 끝납니다. 이때는 아무것도 실행되지 않았고 아무것도 기록되지 않았습니다.

JSON 객체에는 `ok`, `registered`, `uploaded`, `attempt_id`, `cycle_id`, `task`, `attempt`, `correction_cycles_remaining`, `results`, `upload_error`가 들어 있습니다. `attempt`에는 `status`, `revision_oid`, `worktree_state`, `summary`, `cleanup_failed`, `log_truncated`와 실행 한도가, `results`의 각 항목에는 `name`, `command`, `status`, `exit_code`, `duration_ms`, `output_excerpt`, `truncated`, `cleanup_error`가 들어 있습니다.

체크별 상태는 `passed`, `failed`, `error`, `cancelled`, `incomplete`, `unavailable`입니다. 서버는 체크 에이전트가 보낸 종합 결과를 믿지 않고 개별 결과로 시도 상태를 다시 계산합니다. 정리 오류가 있으면 종료 코드가 보이더라도 결과는 `error`이고, 출력이 한도를 넘으면 `incomplete`이며, 줄인 발췌나 로그는 잘렸다고만 표시합니다. 구성된 체크가 하나도 없으면 `passed`가 아니라 `unavailable`이고, 등록된 뒤 완료를 보고하지 않은 시도는 `pending`으로 계속 보입니다.

`upload_error`가 나오면 이 클라이언트는 등록이나 완료를 확인하지 못한 상태입니다. 응답을 받지 못했더라도 서버에는 등록이나 완료가 받아들여져 있을 수 있으니, 예약이나 실행을 되풀이하기 전에 `check status`를 확인하고 시도가 없다고 단정하지 마세요. 기록된 실패 체크와는 다릅니다. 기록된 실패 체크에는 `failed` 결과가 담긴 저장된 시도가 있습니다. 서버에 아예 연결하지 못해도 체크는 실행되고, 명령은 2로 끝나며, `upload_error`에 연결 거부나 TLS 오류 같은 원인이 나옵니다.

## 수정 라운드 한도

작업의 한도는 자동 수정 라운드 세 번입니다. 에이전트에게 수정을 맡기기 전에 라운드를 예약하고 예약 응답의 `cycle.id`를 확인용 실행에 `--cycle`로 넘기세요. 진행 중인 작업에서 이미 승인된 수정은 사용자에게 다시 묻지 않고 한도 안에서 계속할 수 있습니다. 요청받지 않은 수정은 시작하지 말고, 새 식별자를 만들지 말고 고정된 작업 식별자와 라운드 식별자를 다시 쓰세요. 예약한 라운드는 이어지는 체크가 통과하든 실패하든 한 번으로 세며 라운드 안의 재시도는 그 라운드를 다시 씁니다. 첫 체크와 직접 다시 실행한 체크는 라운드를 쓰지 않으며 사용할 수 없거나 취소된 실행만으로는 라운드가 생기지 않습니다. 한도를 다 쓰면 예약 명령이 `correction_budget_exhausted`를 돌려줍니다. 자동으로 계속하지 말고 해결되지 않은 작업을 보고하세요. 한도를 다 쓴 뒤에도 직접 실행한 체크는 기록할 수 있습니다.

한도를 다 썼는지는 `check status`나, `correction_budget_exhausted`를 돌려준 예약 호출로만 알 수 있습니다. 실행 결과의 최상위 `correction_cycles_remaining`은 서버 응답이 있을 때만 채워지고 "알 수 없음"을 나타내는 값이 따로 없어서, 응답이 값을 채우지 않았어도 `0`으로 출력됩니다. 이 `0`은 한도를 다 썼다는 뜻이 아니라 클라이언트가 한도를 읽지 않았다는 뜻입니다. 측정된 한도는 실행 결과에 `task` 객체가 함께 있을 때만 담기므로 `task.correction_cycles_remaining`에서 읽으세요. 측정되지 않은 `0`이 출력되는 경우는 두 가지입니다.

- `--no-upload`는 서버에 접속하지 않으므로 서버의 작업은 바뀌지 않습니다.
- 등록이 실패해 `registered`가 false이고 `upload_error`가 설정된 경우는 시도가 없다는 뜻이 아니라 확인되지 않았다는 뜻입니다. 요청이 받아들여졌다면 저장된 시도가 있고 순번이 올라갔으며, 받아들여지지 않았다면 아무것도 바뀌지 않았습니다. 어느 쪽이라고도 가정하지 마세요.

확인되지 않은 시도가 있으면 고정된 작업 식별자를 그대로 쓰고 출력된 `attempt_id`를 진단 근거로 보관한 뒤 `check status --task TASK_ID`를 확인하세요. 불확실함을 풀려고 체크를 다시 실행하거나 라운드를 예약하지 마세요. `check run`은 실행할 때마다 새 시도 식별자를 만들고 기존 식별자를 다시 제출할 수 없으므로 다시 실행하면 별개의 시도가 시작됩니다. 응답을 받지 못한 요청을 클라이언트가 스스로 재시도할 때는 같은 본문을 보내므로 안전하지만 셸에서 명령을 다시 실행하는 것은 안전하지 않습니다. `check status`로 바로 그 시도를 확인할 수 없으면 확인되지 않았다고 보고하세요. 측정되지 않은 `0`을 근거로 한도를 다 썼다고 보고하거나 승인된 수정을 멈추지 마세요.

## 저장소

`owngit repo`는 저장소 목록을 보여 주고, 저장소 하나의 정보를 읽고, 새 저장소를 만들고, 이전 커밋의 파일을 되살리며 결과를 JSON 객체 하나로 출력합니다. `owngit pr`과 같이 일반 접근을 쓰므로 공용 비밀번호를 `--password-file`로 넘기고 접근이 열려 있으면 생략합니다.

```sh
owngit repo list --server https://owngit.example.test
owngit repo show --server https://owngit.example.test --repository example-project
owngit repo create --server https://owngit.example.test --name example-project \
  --description "Optional description"
```

저장소마다 `id`, `name`, `address`, `description`, `created_at`, `clone_url`이 있습니다. `address`는 저장소에 접속하는 주소로, 지금 이름의 소문자입니다. 이름을 바꾸기 전에는 ID와 같습니다. `repo show`는 그 순간 브랜치를 읽을 수 있으면 `default_branch`도 보여 주며 아직 이 저장소로 안내하는 예전 주소를 `aliases`에 안내가 끝나는 시각(`until`)과 함께 보여 줍니다. 브랜치를 읽지 못하면 `default_branch` 대신 `default_branch_error`가 나옵니다. 저장소 폴더가 없거나 쓸 수 없으면 `The repository folder is missing or unusable; see the server log.`이고 그 밖의 읽기 실패는 `OwnGit could not read the branches; see the server log.`입니다. 저장소가 사용 중이거나 준비 중이거나 마지막으로 읽은 내용이 오래되었을 수 있으면 두 필드 모두 나오지 않습니다. 푸시로 바꿀 수 있는 ref 이름공간은 `push_ref_namespaces`에 나옵니다([다른 ref 이름공간](OPERATIONS.ko.md#다른-ref-이름공간) 참고). `repo list`는 저장소를 최대 1000개까지 돌려주고 더 있으면 `truncated`가 true입니다. `repo create`는 브라우저 양식과 같은 규칙을 적용하며 `repository_exists`, `invalid_repository_name`, `reserved_repository_name`, `invalid_repository_description`(500바이트 초과), `repository_name_busy`(그 이름의 가져오기가 아직 실행 중이거나 복구가 필요함. 끝난 뒤 다시 시도), `repository_storage_in_use`(실행 중인 다른 OwnGit 서버가 저장소 폴더를 사용 중임. 그 서버를 멈추거나 다른 폴더를 선택), `repository_create_kept`(저장소를 기록하지 못해 폴더가 남음. 내용을 확인하고 다른 곳으로 옮긴 뒤 같은 이름으로 다시 시도), `repository_create_failed`(저장소를 만들지 못함. 예를 들어 폴더를 비공개로 만들 수 없는 경우. 서버 컴퓨터에서 `owngit doctor`를 실행하고 서버 로그를 확인)로 실패합니다.

`repo` 명령 가운데 다음 명령은 소유자 작업이라 `--password-file`에 공용 비밀번호 대신 관리자 비밀번호를 넣어야 합니다.

- `owngit repo settings show`와 `owngit repo settings set`은 저장소 하나의 [보관된 기록과 기본 브랜치 보호](OPERATIONS.ko.md#보관된-기록), [다른 ref 이름공간](OPERATIONS.ko.md#다른-ref-이름공간) 설정을 읽고 바꿉니다.
- `owngit repo default-branch --branch BRANCH`는 기존 브랜치를 [기본 브랜치](OPERATIONS.ko.md#기본-브랜치-바꾸기)로 정합니다. 이름이 두 브랜치에 해당하면 `ambiguous_branch`(HTTP 422)로 실패하며 메시지에 두 전체 ref와 각각 보낼 값이 나옵니다.
- `owngit repo rename NAME NEW-NAME`은 저장소 이름을 바꾸고 바뀐 저장소를 JSON으로 출력합니다([저장소 이름 바꾸기](OPERATIONS.ko.md#저장소-이름-바꾸기) 참고). 실패하면 `repository_name_taken`, `repository_busy`, `invalid_repository_name`, `reserved_repository_name` 중 하나가 나옵니다.
- `owngit repo delete --repository NAME --files keep|delete`는 저장소를 삭제합니다([명령줄에서 삭제하기](OPERATIONS.ko.md#명령줄에서-삭제하기) 참고).
- `owngit repo share list`, `create`, `revoke`는 저장소의 읽기 전용 [공유 링크](OPERATIONS.ko.md#공유-링크)를 관리합니다. `create`는 링크의 비밀값을 한 번만 출력하고 `list`는 비밀값을 출력하지 않습니다.

이름을 바꾼 뒤 90일 동안 예전 주소로 저장소를 가리키는 `repo`나 `pr` 명령은 아무것도 바꾸지 않고 `repository_moved`로 실패하고 `details.address`에 새 이름이 나옵니다. 90일이 지나면 예전 주소는 `repository_not_found`로 답합니다.

클론 안에서는 이 명령들이 `--server`를 `origin`에서 가져옵니다. 다만 `repo rename`에는 `--server`를 늘 넘겨야 합니다. `repo settings`, `repo default-branch`, `repo share`는 `--repository`도 `origin`에서 가져옵니다. 이때 비밀번호 파일에 그 서버가 적혀 있어야 합니다([자격 증명 파일과 서버 줄](#자격-증명-파일과-서버-줄) 참고).

`owngit repo kept-history`와 `owngit repo restore`는 대시보드의 되돌리기 화면처럼 이전 커밋의 파일을 되살리며 일반 접근도 그 화면과 같습니다([저장소 파일 되돌리기](OPERATIONS.ko.md#저장소-파일-되돌리기) 참고). 되돌리기는 두 단계입니다. 먼저 미리 보고, 미리 보기가 돌려준 `expected_head`를 넣어 적용합니다.

```sh
owngit repo kept-history
owngit repo restore preview --source OID --target main --path src/app.go
owngit repo restore apply --source OID --target main --path src/app.go --expected-head OID
```

`repo kept-history`는 강제 푸시, 가져오기, 삭제로 바뀐 브랜치와 태그의 이전 값을 최신순으로 보여 줍니다. 항목마다 어느 ref에서 보관했는지(`source_ref`), 되돌릴 때 쓸 커밋(`commit_oid`), 대시보드가 되돌릴 곳으로 제안하는 새 브랜치(`restore_target`)가 들어 있습니다.

`--source`에는 되돌릴 때 쓸 커밋의 전체 ID를 넣습니다. `--target`에는 `main` 같은 브랜치 이름을 넣습니다. `source_ref`가 쓰는 `refs/heads/main` 같은 전체 ref 이름은 받지 않습니다. 되돌릴 파일마다 `--path`를 한 번씩 붙입니다. `--path`가 없으면 트리 전체를 되돌리고 원본 커밋에 없는 파일은 지워집니다. 없는 브랜치에 트리 전체를 되돌리면 원본 커밋에서 그 브랜치를 만듭니다.

미리 보기는 아무것도 바꾸지 않습니다. 바뀌는 경로마다 `status`(`added`, `modified`, `deleted`), `old_mode`와 `new_mode`(`120000`은 심볼릭 링크), `additions`, `deletions`, `binary`를 보여 줍니다. `creates_branch`는 적용하면 브랜치를 새로 만드는지 알려 줍니다. 새로 만들지 않으면 적용할 때 `expected_head` 위에 트리가 `result_tree`인 커밋 하나를 추가합니다. 브랜치에 이미 같은 파일이 있으면 `can_apply`가 false입니다. 되돌리기는 기록을 다시 쓰지 않습니다.

적용에 성공하면 `commit_oid`를 돌려줍니다. 거부된 요청은 다음 코드 가운데 하나로 실패합니다.

- `stale_revision`: 미리 본 뒤 브랜치가 움직였습니다. 바뀐 것은 없으니 다시 미리 봅니다.
- `restore_no_changes`: 브랜치에 이미 같은 파일이 있습니다.
- `restore_unsupported`: 선택에 서브모듈이 들어 있거나, 선택한 경로 아래의 선택하지 않은 파일이 지워지거나, 없는 브랜치에 일부 파일만 되돌리려고 했습니다.
- `invalid_restore`: 요청이 잘못됐습니다. 대상에 전체 ref 이름을 넣었거나, 원본이 이 저장소 커밋의 전체 ID가 아닌 경우가 여기에 듭니다. `main` 옆의 `Main`처럼 일부 파일 시스템이 다른 브랜치와 같은 이름으로 보는 대상 브랜치도 거부됩니다.
- `restore_failed`: 되돌리기를 끝내지 못했지만 실제로는 적용됐을 수도 있습니다. 다시 시도하기 전에 대상 브랜치를 읽어 확인합니다.

API 경로는 `GET /api/v1/repositories/ID/kept-history`, `POST /api/v1/repositories/ID/restore/preview`, `POST /api/v1/repositories/ID/restore`입니다.

## 풀 리퀘스트 변경 내용

`owngit pr diff --number N`은 풀 리퀘스트가 바꾸는 내용을 JSON 객체 하나로 출력합니다. 비교한 원본과 대상 커밋, 두 커밋의 병합 기준(merge base), 줄 수가 붙은 변경 파일 목록, 패치가 들어 있습니다. `owngit pr`과 같이 일반 접근을 쓰고 클론 안에서는 서버와 저장소를 `origin`에서 읽습니다.

```sh
owngit pr diff --number 3
owngit pr diff --number 3 --stat
owngit pr diff --number 3 --patch
owngit pr diff --number 3 --source-oid SOURCE_OID --target-oid TARGET_OID
```

변경 내용은 풀 리퀘스트 페이지와 같이 병합 기준에서 원본까지 셉니다. 기본값으로는 풀 리퀘스트의 현재 원본과 대상 커밋을 한 번 읽고 바로 그 두 커밋을 비교하므로, 읽는 도중에 브랜치가 움직여도 `source.oid`와 `target.oid`는 패치와 일치합니다. 읽은 내용을 리뷰하려면 같은 객체 ID를 `pr review submit`에 넘기며 그사이 브랜치가 움직였으면 리뷰는 `stale_revision`으로 실패합니다. 병합된 풀 리퀘스트의 현재 쌍은 병합한 커밋 쌍입니다. `--source-oid`와 `--target-oid`는 비교할 커밋 쌍을 고정하며 이 쌍은 현재 쌍이거나 리뷰를 요청한 쌍처럼 그 풀 리퀘스트에 기록된 쌍이어야 합니다. 다른 쌍은 `revision_not_recorded`로 실패하고, 둘 중 하나만 넘기면 CLI에서는 `invalid_arguments`, API에서는 `invalid_revision`으로 실패합니다. 고정한 쌍에서 브랜치가 움직였으면 결과는 여전히 그 쌍을 보여 주면서 `moved`를 true로 두고 현재 쌍을 `current`에 담습니다.

결과에는 크기 제한이 있습니다. 패치에서 빠진 파일이 있으면 `truncated`가 true이고, 파일 목록에서도 빠진 파일이 있으면 `incomplete`가 true이며, 패치는 언제나 파일 경계에서 끝납니다. `reason`은 빠진 이유를 알려 줍니다. `output_limit`는 비교 결과가 8 MiB 제한에 닿은 경우, `time_limit`는 Git의 시간이 다 된 경우(나중에 다시 시도하면 더 읽을 수 있습니다), `response_limit`는 4 MiB 응답에 맞추려고 잘라 낸 경우입니다. 두 브랜치에 공통 커밋이 없거나 병합 기준이 둘 이상이면 `unavailable`이 `no_merge_base` 또는 `multiple_merge_bases`이고 파일 목록과 패치가 없습니다. `--stat`은 `patch`를 뺀 같은 객체를 출력하고, `--patch`는 패치 텍스트만 출력하며 비교한 커밋과 브랜치 이동이나 잘림 여부는 표준 오류에 씁니다. API 경로는 `GET /api/v1/repositories/ID/pull-requests/N/diff`이며 선택 쿼리 매개변수로 `source_oid`와 `target_oid`를 받습니다.

## 풀 리퀘스트 병합 가능 여부

`owngit pr mergeability --number N`은 열려 있는 풀 리퀘스트를 현재 원본과 대상 커밋으로 지금 병합할 수 있는지 확인해 JSON 객체 하나로 출력합니다. 아무것도 바꾸지 않으며 ref, 기록, 저장소 안의 객체 어느 것도 만들지 않습니다. `owngit pr diff`처럼 일반 접근을 씁니다.

```sh
owngit pr mergeability --number 3
owngit pr mergeability --number 3 --source-oid SOURCE_OID --target-oid TARGET_OID
```

`source`와 `target`은 답이 가리키는 커밋입니다. `status`는 다음 가운데 하나입니다.

- `clean`: 병합할 수 있습니다. `method`는 `fast_forward`, `merge_commit`, `up_to_date` 가운데 하나입니다.
- `conflict`: `conflict_paths`에 충돌한 경로가 최대 100개 들어 있고 더 있으면 `conflict_paths_truncated`가 true입니다. Git이 충돌한 파일을 알려 주지 않으면 `conflict_paths`가 없습니다. 두 브랜치에 공통 기록이 없으면 대신 `reason`이 `no_merge_base`입니다.
- `unavailable`: OwnGit이 알아낼 수 없었습니다. `reason`에는 `unsupported_git`(Git 2.38 미만), `source_branch_missing`, `repository_unavailable` 같은 이유가, `message`에는 설명이 들어 있습니다.
- `stale`: `--source-oid`와 `--target-oid`로 준 쌍에서 브랜치가 움직였습니다. 이때 `source`와 `target`에는 현재 쌍이 들어 있습니다.

이전 답의 커밋을 `--source-oid`와 `--target-oid`로 넘기면 같은 쌍을 다시 확인합니다. 답은 저장하지 않고 병합을 예약하지도 않으며 `pr merge`는 실행할 때 다시 확인합니다. API 경로는 `GET /api/v1/repositories/ID/pull-requests/N/mergeability`이며 선택 쿼리 매개변수로 `source_oid`와 `target_oid`를 받습니다.

## MCP 서버

`owngit mcp`는 MCP를 지원하는 코딩 도구를 위한 [Model Context Protocol](https://modelcontextprotocol.io) 서버입니다. 프로토콜 리비전 `2025-11-25`를 표준 입력과 표준 출력(stdio 전송)으로 구현하며 한 줄에 JSON-RPC 메시지 하나를 주고받고 네트워크 포트는 열지 않습니다. 각 도구는 `owngit` 명령 하나를 감싸고 그 명령이 출력하는 JSON을 돌려줍니다. 셸 명령을 실행할 수 있는 코딩 도구는 명령줄을 그대로 써도 됩니다. 도구 설명 목록보다 명령줄 쪽이 대개 토큰을 덜 씁니다.

### 서버 시작

코딩 도구가 서버를 자식 프로세스로 실행합니다. 도구 호출이 다른 곳에 닿는 데 쓸 수 있는 값은 모두 시작 플래그로 정해집니다.

- `--workdir DIR`(기본값은 코딩 도구가 서버를 시작한 디렉터리): [클론 안에서 실행하기](#클론-안에서-실행하기)에서처럼 `origin`으로 서버와 저장소를 알려 주는 클론이며, `check_run`이 체크를 실행하는 곳입니다. 코딩 도구마다 서버를 시작하는 디렉터리가 다르므로 절대 경로를 주세요.
- `--server`와 `--repository`는 `origin`보다 우선합니다. 알려진 저장소가 없으면 저장소 도구와 풀 리퀘스트 도구가 대신 `repository` 인수를 받습니다.
- `--password-file`: 저장소 도구와 풀 리퀘스트 도구에 쓰는 공용 비밀번호입니다. 접근이 열려 있으면 생략합니다.
- `--credential-file`: 체크 에이전트 토큰입니다. 체크 도구를 추가하며, 저장소가 정해져 있어야 합니다.
- `--accept-insecure-http`: 일반 HTTP 서버에 필요합니다. 위험을 받아들인 연결에만 붙이세요.
- `--no-run-check`: `check_run`을 뺍니다.
- `--result-limit BYTES`(기본값 65536, 4096부터 4194304까지): 도구 결과 하나의 최대 크기입니다.

비밀번호 파일과 자격 증명 파일에는 [자격 증명 파일과 서버 줄](#자격-증명-파일과-서버-줄)의 규칙이 그대로 적용됩니다. 서버를 `origin`에서 가져왔으면 파일에 그 서버가 적혀 있어야 합니다. 파일은 시작할 때 한 번 읽으며 비밀 값은 결과에 나오지 않습니다. `credential_origin_required`나 `insecure_http_confirmation_required`처럼 시작에 실패하면 오류 객체를 표준 오류에 쓰고 종료 상태 1로 끝납니다. 코딩 도구는 이 내용을 MCP 서버 로그에 보여 주며 `origin`에서 무엇을 가져왔는지 알리는 줄도 거기에 나옵니다.

도구 인수는 풀 리퀘스트 번호, 커밋 ID, 작업 ID, 제목, 브랜치 이름 같은 값입니다. 서버, 경로, 명령처럼 도구의 입력 스키마에 없는 인수는 `invalid_arguments`로 실패하고 시작할 때 정한 저장소가 아닌 `repository`는 `repository_not_allowed`로 실패합니다. 올바른 UTF-8이 아닌 텍스트도 `invalid_arguments`로 실패합니다. 짝 없이 홀로 쓴 `\ud800`처럼 서로게이트 쌍의 절반만 적은 `\u` 이스케이프도 마찬가지입니다. OwnGit은 읽을 수 없는 텍스트를 다른 문자로 바꾸지 않기 때문입니다.

저장소는 `repository_list`에 나오는 주소로 가리킵니다. 저장소 [이름을 바꾸면](OPERATIONS.ko.md#저장소-이름-바꾸기) 예전 주소에서 저장소 도구와 풀 리퀘스트 도구는 `repository_moved`로 실패하고 `details.address`에 새 주소가 나옵니다. 체크 도구는 90일 동안 그 주소에서 계속 동작합니다. `repository`에 새 주소를 넘기세요. 시작할 때 저장소를 정했다면 `--repository`나 클론의 `origin`을 바꾼 뒤 서버를 다시 시작합니다.

### 클라이언트 설정

경로는 자신의 것으로 바꾸세요. 자격 증명은 플래그가 가리키는 파일에만 두고 클라이언트 설정이나 환경 변수에는 넣지 마세요.

Claude Code는 프로젝트의 `.mcp.json`을 읽습니다. `claude mcp add --scope project owngit -- owngit mcp ...`도 이 파일에 씁니다.

```json
{
  "mcpServers": {
    "owngit": {
      "command": "owngit",
      "args": ["mcp", "--workdir", "/path/to/clone", "--credential-file", "/path/to/helper-token"]
    }
  }
}
```

Codex는 `~/.codex/config.toml`을 읽습니다. Codex는 기본으로 도구를 60초 기다린 뒤 호출을 취소하며 이때 실행 중인 `check_run`도 멈추므로, `tool_timeout_sec`을 체크에 걸리는 시간보다 길게 잡으세요.

```toml
[mcp_servers.owngit]
command = "owngit"
args = ["mcp", "--workdir", "/path/to/clone", "--credential-file", "/path/to/helper-token"]
tool_timeout_sec = 1800
```

그 밖의 MCP 클라이언트에서는 stdio 전송, 명령 `owngit`(또는 전체 경로, [체크 에이전트 실행 파일 찾기](#체크-에이전트-실행-파일-찾기) 참고), 인수 `mcp`와 위의 플래그를 설정합니다.

### 도구

읽기 도구는 아무것도 바꾸지 않습니다.

| 도구 | 명령 |
|---|---|
| `repository_list`, `repository_show` | `repo list`, `repo show` |
| `repository_kept_history`, `repository_restore_preview` | `repo kept-history`, `repo restore preview`. `source_oid`, `target_branch`, `paths`는 `--source`, `--target`, `--path`에 해당하고 미리 보기는 적용에 필요한 `expected_head`를 돌려줍니다 |
| `pull_request_list`, `pull_request_show` | `pr list`, `pr show`. 설명과 리뷰 메모는 show에만 들어 있습니다 |
| `pull_request_diff` | `pr diff`. `patch: false`는 `--stat`과 같고, `source_oid`와 `target_oid`를 함께 넘기면 커밋 쌍을 고정합니다. |
| `pull_request_mergeability` | `pr mergeability`. `source_oid`와 `target_oid`를 함께 넘기면 그사이 브랜치가 움직였을 때 `stale`로 답합니다. |
| `check_task_list`, `check_status` | `check task list`, `check status`(작업 하나와 가장 최근 시도) |
| `check_log`, `check_cycle_list`, `check_config_show` | `check log`, `check cycle list`, `check config show` |
| `backup_status` | 해당 명령 없음. `backup status`의 요약이며 아래에서 설명합니다 |
| `activity` | `activity`. `year`와 `date`는 `--year`, `--date`에 해당합니다([전체 활동](OPERATIONS.ko.md#전체-활동)) |

`backup_status`는 위험한 변경을 하기 전처럼 OwnGit의 백업 기록이 어떤지 알고 싶을 때 씁니다. 저장소 도구처럼 일반 접근을 씁니다. 기록만 읽고 백업 폴더는 보지 않으므로, OwnGit 밖에서 지운 백업도 다음 백업이 알아챌 때까지는 남아 있는 것으로 나옵니다. 돌려주는 요약은 다음과 같습니다.

- `schedule`: `not_configured`, `off`, `on` 중 하나
- `last_run`: 마지막으로 끝난 백업. `kind`(`scheduled` 또는 `manual`), `status`(`succeeded`, `failed`, `interrupted`), `verification`(`passed`, `failed`, `not_run`), `finished_at`이 들어 있습니다. 없으면 null입니다.
- `last_verified_at`: 검사를 통과했고 OwnGit 기록상 아직 남겨 둔 가장 새 백업이 끝난 시각. 없으면 null입니다.
- `next_run`: 다음 예약 백업 시각. 없으면 null입니다.

요약에는 폴더, 저장소, 오류 메시지가 나오지 않습니다. 관리자는 `owngit backup status`로 이 내용을 봅니다. [백업 상태 확인하기](OPERATIONS.ko.md#백업-상태-확인하기)를 참고하세요.

쓰기 도구와 그 효과는 다음과 같습니다.

| 도구 | 명령 | 효과 |
|---|---|---|
| `pull_request_create` | `pr create` | 풀 리퀘스트를 추가합니다. 마크다운 설명(`body`)은 선택 사항입니다. 브랜치는 움직이지 않습니다. |
| `pull_request_edit` | `pr edit` | `edit_revision`이 그대로일 때 제목이나 `body`, 또는 둘 다를 바꿉니다. 그사이 누가 수정했다면 `stale_edit`으로 거부됩니다. 브랜치, 리뷰, 체크는 바뀌지 않습니다. |
| `pull_request_review` | `pr review submit` | 정확한 커밋 ID에 내린 결정과 호출자가 준 리뷰어 표시, 선택 사항인 메모(`note`)를 기록합니다. 참고용입니다. |
| `pull_request_review_request`, `pull_request_review_skip` | `pr review request`, `pr review skip` | 정확한 커밋 ID의 리뷰 상태를 pending이나 skipped로 바꿉니다. 누구에게도 알리지 않습니다. 참고용입니다. |
| `pull_request_close`, `pull_request_reopen` | `pr close`, `pr reopen` | 풀 리퀘스트 상태를 바꿉니다. 브랜치는 움직이지 않습니다. |
| `pull_request_merge` | `pr merge` | 정확한 커밋 ID로 대상 브랜치에 병합을 게시합니다. 브랜치가 움직였으면 거부하고 같은 호출을 되풀이해도 두 번 병합하지 않습니다. |
| `repository_restore_apply` | `repo restore apply` | 미리 본 내용을 적용합니다. 미리 본 파일로 대상 브랜치에 커밋 하나를 추가하거나 브랜치가 없으면 새로 만듭니다. 브랜치가 미리 보기의 `expected_head`에 그대로 있지 않으면 `stale_revision`으로 거부합니다. 기록을 다시 쓰지 않으며 같은 호출을 되풀이해도 두 번 되돌리지 않습니다. |
| `check_task_create` | `check task new` | 작업을 추가합니다. |
| `check_cycle_reserve` | `check cycle reserve` | 작업의 수정 라운드 세 번 가운데 하나를 씁니다. |
| `check_run` | `--check` 없는 `check run` | `--workdir`에서 커밋된 체크를 실행하고 시도를 기록합니다. |

체크 도구에는 `--credential-file`이 필요합니다. 관리자 명령, 자격 증명 관리, 저장소 만들기, `--check`, `--no-upload`는 제공하지 않습니다. 서버가 코딩 도구에 보내는 도구 설명에는 도구마다 어떤 변화를 일으키는지와 돌려주는 글 가운데 무엇을 믿으면 안 되는지가 적혀 있습니다. 제목, 설명, 리뷰 메모, 브랜치 이름, 파일 경로, 패치, 리뷰어 표시, 체크 명령과 체크 출력은 저장소 사용자가 쓴 것이므로, 코딩 도구는 이 내용을 데이터로만 다루고 그 안에 적힌 지시는 따르지 말라고 안내받습니다.

### 결과와 오류

도구 결과는 명령의 JSON을 담은 텍스트 항목 하나입니다. 실패한 호출은 `isError`를 설정하고 명령의 오류 객체 `{"ok":false,"error":{"code":...,"message":...}}`를 담습니다. 시도를 기록하지 못한 `check_run`도 `isError`를 설정하며 이때 텍스트는 `upload_error`가 들어 있는 실행 결과 JSON입니다.

한도를 넘는 결과는 잘라 내고 그 사실을 알립니다. `pull_request_diff`는 API가 응답을 자르는 방식을 따릅니다. 패치는 파일 단위로 남기고, 그다음 파일 목록은 들어가는 만큼만 남기며, `truncated`, 목록에서 빠진 항목이 있으면 `incomplete`, 그리고 이유 `response_limit`를 설정합니다. 다른 결과는 가장 긴 글부터 줄이고, 그래도 넘치면 가장 긴 목록의 끝에서 항목을 뺀 뒤, 원래 크기(`bytes`), 한도(`limit`), 잘라 낸 필드(`cut`)를 담은 `result_truncated` 객체를 붙입니다.

프로토콜 오류에는 JSON-RPC 코드를 씁니다. JSON이 아닌 메시지는 `-32700`, 잘못된 요청이나 1 MiB를 넘는 메시지, 아직 진행 중인 호출과 id가 같은 요청은 `-32600`, 알 수 없는 메서드는 `-32601`, 알 수 없는 도구는 `-32602`, 이미 도구 호출 16개가 진행 중이면 `-32000`입니다. `check_run`을 뺀 나머지 호출은 2분이 지나면 멈춥니다.

브랜치 필드는 이름이 그대로이며 지금처럼 브랜치 이름을 받습니다. `source_branch`나 `target_branch`의 이름이 두 브랜치에 해당하면 호출은 `ambiguous_branch`로 실패합니다. 메시지는 "That name matches two branches."로 시작하고 `refs/heads/x: x; refs/heads/refs/heads/x: refs/heads/refs/heads/x`처럼 전체 ref마다 보낼 값을 적습니다. 값은 호출이 받아들이는 것만 나옵니다. 값이 없는 전체 ref는 브라우저에서 골라야 합니다. 풀 리퀘스트에서는 이 필드의 255바이트 한도를 넘는 값도 여기에 해당합니다.

### 체크 실행

`check_run`은 `--check` 없이 실행한 `owngit check run`과 똑같이, `--workdir`의 `HEAD`에 커밋된 `.owngit/checks.json`의 체크를 체크당 10분과 출력 65536바이트라는 기본 한도로 실행합니다. 인수로는 작업과, 확인용 실행이라면 예약한 라운드만 넘깁니다. 체크는 사용자의 권한과 환경으로 실행되며 샌드박스 안에서 실행되지 않습니다. 한 번에 하나만 실행할 수 있으며 실행 중에 다시 호출하면 `check_run_busy`로 실패합니다. 코딩 도구가 호출을 취소하거나 입력을 닫으면 체크와 그 자식 프로세스를 멈춥니다. 시도는 취소된 것으로 기록되며, 취소된 호출에는 응답하지 않습니다.

체크는 저장소에 커밋된 명령이므로, 클론에 커밋할 수 있는 사람은 `check_run`이 프로그램을 실행하게 만들 수 있습니다. 에이전트가 파일은 고쳐도 되지만 명령을 실행하면 안 된다면 서버를 `--no-run-check`로 시작하세요. 그러면 도구 목록에서 `check_run`이 빠지고 호출해도 아무것도 실행하기 전에 거부하며 다른 체크 도구는 남아 있으므로 에이전트는 기록된 결과를 읽고, 작업을 만들고, 라운드를 예약할 수 있습니다.

## 제한

- 체크는 참고용입니다. 병합을 막지 않으며, 체크를 통과했다고 코드가 옳다는 증거가 되지는 않습니다. 프로젝트나 팀이 더 엄격한 리뷰나 체크 규칙을 요구할 수 있으며, 이 연동은 그 규칙보다 우선하지 않습니다.
- 체크 에이전트는 사용자의 환경과 권한을 물려받습니다. 샌드박스가 아니며 체크는 사용자 계정이 접근할 수 있는 파일과 인증 정보를 읽을 수 있습니다.
- 변경이 있거나 상태를 알 수 없는 워킹 트리는 테스트한 커밋이 아닙니다. 그 리비전을 테스트했다고 말하지 말고 기록된 워킹 트리 상태를 보고하세요.
- `--no-upload`는 로컬에서 실행되며 서버에 기록되지 않습니다. 서버에 기록된 근거라고 설명하지 마세요.
- 실패한 체크를 무작정 다시 시도하지 말고, 체크를 통과시키려고 커밋된 체크 구성을 약하게 바꾸거나 다른 것으로 바꾸지 마세요.
- 읽기 전용 권한만 있는 리뷰어는 체크를 실행할 수 없습니다. 실행 권한이 있고 승인된 참여자가 체크를 실행해 그 출처와 함께 결과를 전달합니다.

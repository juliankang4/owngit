# 저장소와 가져오기

OwnGit 관리자를 위한 안내서입니다. 저장소를 옮겨 오는 방법, 기록을 보호하고 되돌리는 방법, 이름 바꾸기, 공유, 삭제, 그리고 다른 Git 호스트에서 저장소를 가져오는 방법을 다룹니다.

대부분의 작업은 대시보드 버튼과 `owngit` 명령 두 가지로 할 수 있습니다. 저장소를 바꾸는 명령에는 관리자 비밀번호를 담은 소유자 전용 파일을 `--password-file`로 넘기고([비밀번호 파일과 토큰 파일](OPERATIONS.ko.md) 참고), 서버 주소를 `--server`로 넘깁니다. 저장소 클론 안에서 실행하면 `--server`와 `--repository`는 `origin` 리모트에서 가져옵니다. `owngit repo delete`만은 예외입니다.

## 저장소 이름 규칙

저장소를 만들거나 가져오거나 이름을 바꿀 때 같은 규칙을 따릅니다.

- ASCII 영문자, 숫자, `.`, `_`, `-`로 1~100자이며 영문자나 숫자로 시작합니다.
- `.git`으로 끝날 수 없습니다. 첫 `.` 앞부분이 `CON`, `NUL`, `COM1` 같은 Windows 장치 이름이어도 안 됩니다.
- `new`와 `new-import`는 쓸 수 없습니다.
- 대소문자만 다른 이름도 포함해 다른 저장소가 쓰는 이름은 쓸 수 없습니다. 아직 새 주소로 리디렉션되는 예전 이름도 마찬가지입니다.

주소에는 이름이 소문자로 들어갑니다. 예를 들어 `Tools-2`의 주소는 `tools-2`입니다.

## 기존 저장소를 OwnGit으로 옮기기

대시보드나 `owngit repo create --name PROJECT`로 빈 저장소를 만듭니다. 그다음 기존 저장소의 클론에서 푸시하고 양쪽을 비교합니다.

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

푸시로 바꿀 수 있는 것은 브랜치와 태그, 그리고 그 저장소에 등록한 [다른 ref 이름공간](#다른-ref-이름공간)뿐입니다. 나머지 ref는 모두 거부하므로 다른 호스트의 미러에서 `git push --mirror`를 하면 `refs/pull/*` 같은 ref에서 실패합니다. `main` 옆의 `Main`처럼 일부 파일 시스템이 기존 이름과 같게 보는 브랜치나 태그 이름도 거부합니다. 이유와 해결 방법은 Git 화면에 나옵니다.

OwnGit은 푸시로 들어오는 객체를 모두 Git의 객체 검사로 확인합니다. 손상되었거나 형식이 잘못된 객체가 있으면 푸시를 거부하고, Git이 `remote: error:` 줄에 이유를 보여 줍니다. 오래된 프로젝트 기록에 흔한 0으로 채운 파일 모드나 예전 날짜 형식은 경고로만 남으므로 이런 저장소도 그대로 푸시할 수 있습니다.

다른 OwnGit 서버로 푸시해도 보관된 기록, 풀 리퀘스트, 체크는 넘어가지 않습니다. 이것까지 옮기려면 백업을 쓰세요([백업](BACKUPS.ko.md)). 계속 쓰는 호스트를 따라가려면 푸시 대신 [가져오기](#다른-git-호스트에서-가져오기)를 쓰세요.

## 다른 호스트에 사본 두기

OwnGit은 다른 호스트로 푸시하지 않습니다. 미러는 직접 만듭니다.

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

사본을 갱신하려면 같은 폴더에서 `git fetch --prune`과 `git push --mirror`를 다시 실행합니다. `--mirror`는 여기서 지운 것을 다른 호스트에서도 지웁니다.

## 기본 브랜치와 보호

기본 브랜치는 `git clone`이 체크아웃하는 브랜치입니다. 저장소의 설정 탭에서 바꾸거나 다음 명령을 실행합니다.

```sh
owngit repo default-branch --repository NAME --branch BRANCH
```

새 저장소는 `main`으로 시작합니다. `trunk` 같은 다른 이름을 쓰려면 설정의 저장소 탭에서 첫 브랜치를 바꾸거나 `owngit settings set --initial-branch trunk`를 실행합니다. 가져온 저장소는 원본의 기본 브랜치를 따릅니다.

저장소 설정 탭의 "기본 브랜치 보호"를 켜면 기본 브랜치를 다시 쓰거나 지우는 푸시를 막습니다. 기본값은 꺼짐입니다. 명령으로 켜려면 다음을 실행합니다.

```sh
owngit repo settings set --repository NAME --protect-default-branch on
```

커밋을 더하는 푸시, 병합, 파일 되돌리기는 보호 중에도 됩니다. 보호된 브랜치를 다시 쓰게 되는 가져오기는 `protected_default_branch`로 실패하고 아무것도 바꾸지 않습니다.

## 보관된 기록과 파일 되돌리기

강제 푸시, 가져오기, 삭제로 브랜치나 태그의 커밋이 바뀌면 OwnGit은 이전 커밋을 보관된 기록으로 남깁니다. 보관된 기록은 둘러보고 되돌릴 수 있습니다. 처음부터 켜져 있습니다.

모든 저장소에서 끄려면 설정의 저장소 탭에서 보관된 기록을 바꾸거나 `owngit settings set --kept-history off`를 실행합니다. 저장소 하나만 끄려면 그 저장소의 설정 탭을 쓰거나 `owngit repo settings set --repository NAME --kept-history off`를 실행합니다. 값은 `on`, `off`, 서버 설정을 따르는 `default` 중 하나입니다. 꺼도 이미 보관된 기록은 지워지지 않습니다. 다만 그 뒤에 바뀐 커밋은 보관된 기록에서 되돌릴 수 없습니다.

파일을 되돌리려면 저장소 개요의 "되돌리기 시작", 브랜치나 태그나 보관된 기록 항목의 "여기서 파일 되돌리기", 파일 페이지의 "이 파일 되돌리기" 가운데 하나를 고릅니다. 원본 커밋과 대상 브랜치를 고르고 미리보기를 확인한 뒤 적용합니다. OwnGit은 대상 브랜치에 새 커밋을 더하거나, 지워진 브랜치를 다시 만듭니다. 기록을 다시 쓰지 않고 누구의 작업 사본도 건드리지 않습니다.

명령줄에서는 먼저 미리 보고 그 내용을 그대로 적용합니다.

```sh
owngit repo kept-history --repository NAME
owngit repo restore preview --repository NAME --source OID --target main
owngit repo restore apply --repository NAME --source OID --target main --expected-head OID
```

`--source`에는 전체 커밋 ID를 씁니다. 일부 파일만 되돌리려면 파일마다 `--path FILE`을 붙입니다. `--expected-head`에는 미리보기의 `expected_head` 값을 넣습니다. 미리 본 뒤에 브랜치가 움직였으면 적용은 `stale_revision`으로 실패하고 아무것도 바꾸지 않습니다. 다시 미리 보세요.

## 다른 ref 이름공간

푸시는 기본적으로 브랜치(`refs/heads/`)와 태그(`refs/tags/`)만 바꿀 수 있습니다. Git 노트처럼 다른 ref도 받으려면 저장소 설정 탭의 다른 ref 이름공간에 이름공간을 적거나 다음 명령을 실행합니다.

```sh
owngit repo settings set --repository NAME --extra-ref-prefixes refs/notes/,refs/meta/
```

이름공간은 `refs/`로 시작해 `/`로 끝납니다. 저장소마다 32개까지 적을 수 있고 빈 값을 주면 모두 지워집니다. 이 이름공간의 ref에는 보관된 기록도 보호도 없습니다. 푸시로 덮어쓰거나 지우면 되돌릴 수 없습니다.

## 저장소 이름 바꾸기

저장소 설정 탭의 이름과 주소에서 바꾸거나 다음 명령을 실행합니다.

```sh
owngit repo rename NAME NEW-NAME
```

저장소 안의 내용은 그대로입니다. 90일 동안 예전 주소는 새 주소로 리디렉션되므로 기존 클론도 계속 동작합니다. 이때 Git은 `warning: redirecting to` 줄을 보여 줍니다. 90일 안에 클론마다 주소를 바꾸세요.

```sh
git remote set-url origin http://HOST:7654/git/NEW-NAME.git
```

90일이 지나면 예전 주소는 404를 돌려주고 다른 저장소가 그 이름을 가져갈 수 있습니다. 그러면 예전 주소를 그대로 쓰는 클론은 그 저장소에서 받고 그 저장소로 푸시하게 됩니다.

- `owngit` 명령과 MCP 도구는 리디렉션을 따라가지 않고 `repository_moved`로 멈춥니다. `origin`이나 `--repository`를 바꾸세요.
- 러너와 체크 에이전트는 90일 동안 예전 주소에서도 동작합니다. 그 전에 `owngit runner --repository NEW-NAME`처럼 새 이름으로 바꾸세요.
- 가져오기, 체크, 푸시, 백업이 저장소를 쓰는 동안에는 이름을 바꿀 수 없습니다. 끝난 뒤 다시 하세요.
- 새 이름도 [저장소 이름 규칙](#저장소-이름-규칙)을 따릅니다.

## 공유 링크

공유 링크를 받은 사람은 계정 없이 저장소 하나를 읽을 수 있습니다. 링크는 저장소 설정 탭의 공유 링크에서 만들고 폐기합니다. 링크마다 다음을 정합니다.

- 관리자만 보는 이름
- 권한: "파일과 기록 보기", 또는 "보기와 Git 복제"
- 만료: 1일, 7일, 30일(기본), 90일, 또는 "폐기할 때까지"
- 방문자가 입력해야 하는 추가 비밀번호(선택, 8~1024자)

새 링크의 주소 `https://HOST/share/SECRET`은 한 번만 보여 줍니다. 잃어버렸다면 새 링크를 만들고 예전 링크를 폐기하세요. 폐기하면 링크는 바로 멈춥니다.

방문자는 파일, README, 커밋, 브랜치, 태그를 봅니다. 다른 저장소, 풀 리퀘스트, 체크, 설정, 보관된 기록은 볼 수 없고 아무것도 바꿀 수 없습니다. 복제 권한이 있는 링크에는 `https://HOST/share/ID.git` 형태의 Git 주소가 붙습니다. Git이 사용자 이름과 비밀번호를 물으면 이렇게 입력합니다.

- 추가 비밀번호가 없으면 사용자 이름은 아무것이나 넣고 비밀번호에 링크의 `/share/` 뒤 부분을 넣습니다.
- 추가 비밀번호가 있으면 사용자 이름에 `/share/` 뒤의 부분을, 비밀번호에 추가 비밀번호를 넣습니다.

이 Git 주소는 저장소의 HEAD가 브랜치나 태그의 끝 커밋(주석 태그라면 그 태그가 가리키는 커밋)일 때만 HEAD를 알려 줍니다. 그렇지 않으면 클론해도 파일이 체크아웃되지 않을 수 있습니다. 이때는 `--branch BRANCH`를 붙여 클론하거나, 클론한 뒤 브랜치를 체크아웃하세요. OwnGit이 HEAD를 확인하지 못하면 HTTP 503으로 응답하니 다시 시도하세요.

링크는 비밀번호처럼 다루세요. 이미 복제한 사본은 링크를 폐기해도 회수할 수 없습니다. OwnGit 앞에 리버스 프록시가 있으면 비밀값이 담긴 첫 요청이 프록시 로그에 남을 수 있으니 접근 로그를 확인하세요. 공유 링크는 백업에 들어가지 않으므로 복원한 뒤에는 새로 만들어야 합니다.

명령줄에서는 다음과 같이 합니다.

```sh
owngit repo share list   --repository NAME
owngit repo share create --repository NAME --label "Reviewer" --scope clone --days 7
owngit repo share revoke --repository NAME --id ID
```

`create`는 `--until-revoked`와, 추가 비밀번호 파일을 받는 `--link-password-file PATH`도 받습니다. 비밀값은 한 번만 출력합니다.

### 공유 링크용 공개 주소

OwnGit 자체는 비공개로 두어야 합니다. 그래도 Tailscale Funnel 같은 방법으로 공유 링크에만 두 번째 공개 주소를 줄 수 있습니다. 이 주소는 공유 링크 페이지와 복제에만 답합니다. 나머지 요청에는 모두 `404 page not found`를 돌려줍니다.

기본값은 꺼짐입니다. 비어 있는 로컬 포트와 방문자가 쓸 주소를 정해 설정의 네트워크 탭, 공유 링크용 공개 주소에 둘 다 저장하거나 다음 명령을 실행합니다.

```sh
owngit network set --public-share-listen 127.0.0.1:7655 --public-share-url https://box.tail1234.ts.net:8443
```

OwnGit을 다시 시작하면 적용됩니다. 그다음 터널이 그 포트를 가리키게 직접 설정합니다.

```sh
tailscale funnel --bg --https=8443 http://127.0.0.1:7655
```

터널의 주소를 신뢰하는 프록시로 추가하세요. 이 컴퓨터에서 Funnel을 쓴다면 `127.0.0.1`입니다. 추가하지 않으면 추가 비밀번호를 틀린 횟수를 셀 때 모든 방문자가 한 주소로 묶입니다. 켜고 나면 인터넷의 누구나 그 주소에 접속할 수 있습니다. 링크를 가진 사람은 링크가 만료되거나 폐기될 때까지 그 주소에서 링크를 쓸 수 있습니다. 끄려면 `owngit network set --public-share-off`를 실행합니다.

## 저장소 삭제하기

저장소 화면의 탭 줄 맨 끝에 있는 "저장소 삭제"를 고른 뒤 파일을 어떻게 할지 정합니다.

- OwnGit에서만 제거하고 파일은 남기기: bare 저장소를 저장소 폴더 안의 `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git`로 옮깁니다(`ID`는 저장소를 처음 만들 때의 이름을 소문자로 쓴 것입니다). 직접 지울 때까지 남아 있으며 백업에는 들어가지 않습니다.
- 파일까지 영구 삭제: 보관된 기록까지 함께 지웁니다. 이전 백업에는 남아 있습니다.

어느 쪽이든 OwnGit은 그 저장소의 풀 리퀘스트, 체크, 인증 정보, 공유 링크, 가져오기 설정을 지웁니다. 저장소 이름은 다시 쓸 수 있게 됩니다.

명령줄에서는 `--repository`를 항상 적어야 합니다.

```sh
owngit repo delete --repository NAME --files keep --confirm-name NAME
```

삭제 페이지는 저장소 이름을 입력하라고 합니다. 이 확인을 없애려면 설정의 저장소 탭, 저장소 삭제에서 "묻지 않음"을 고르거나 `owngit settings set --delete-requires-name off`를 실행합니다.

남겨 둔 저장소를 되살리려면 같은 이름으로 빈 저장소를 만듭니다. 그다음 OwnGit이 실행되는 컴퓨터에서 남은 폴더를 그 저장소로 푸시합니다.

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

돌아오는 것은 브랜치와 태그뿐입니다. 보관된 기록, 풀 리퀘스트, 체크는 돌아오지 않습니다.

### 삭제가 거부되거나 중간에 멈췄을 때

- 가져오기, 체크, Git 작업이 진행 중이면 끝난 뒤 다시 하세요.
- 체크 컨테이너를 아직 정리하는 중이면 Docker를 켠 뒤 OwnGit을 다시 시작하세요.
- 자동 체크 페이지의 남은 체크 컨테이너에 컨테이너가 보이면 먼저 그 컨테이너를 실행한 Docker 데몬에서 지웁니다. 그다음 확인란을 체크하고 컨테이너 기록 지우기를 고릅니다. 대시보드를 쓸 수 없으면 OwnGit 컴퓨터에서 `owngit forget-check-container --job JOB --confirm-container-removed`를 실행합니다.

삭제 도중 OwnGit이 멈추면 다음에 시작할 때 마저 끝냅니다. 저장소 폴더의 `.owngit-deletion-ID` 파일은 지우지 마세요. OwnGit은 이 파일로 올바른 저장 장치가 연결되었는지 확인합니다.

## 압축 파일 내려받기

코드 탭에서는 고른 브랜치나 태그를 ZIP이나 tar.gz로 받을 수 있습니다. 커밋 페이지에서는 그 커밋을 받을 수 있습니다. 브라우저 없이 받으려면 다음을 실행합니다.

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

`ref`에는 브랜치, 태그, 전체 커밋 ID를 씁니다. 빼면 기본 브랜치를 받습니다. `--user`를 주면 curl이 공용 비밀번호를 묻습니다. 받는 도중 끊기면 curl이 오류를 알립니다. 이때 받은 파일은 올바른 압축 파일이 아닙니다.

OwnGit은 압축 파일을 만들기 전에 커밋 안에 같은 경로를 쓰는 파일이나 폴더가 둘 있는지 확인합니다. 그런 항목이 있거나, 파일이 너무 많아 확인할 수 없으면 HTTP 409로 거부합니다. 확인 작업 자체가 실패하면 HTTP 502로 응답하고 압축 파일을 만들지 않습니다.

## 전체 활동

사이드바의 전체 활동은 모든 저장소의 한 해 커밋을 보여 주고 그해 또는 하루의 최신 커밋을 1,000개까지 나열합니다. `owngit activity`는 같은 내용을 JSON으로 출력하며 `--year 2025`나 `--date 2026-09-29`를 받습니다.

## 저장소에 준비 중이나 읽지 못함이 보일 때

OwnGit은 시작할 때 저장소를 준비한 뒤에 제공합니다. 저장소 폴더를 읽지 못했다가 다시 읽게 되었을 때도 마찬가지입니다. 준비하는 동안 Git은 HTTP 503과 `repository is being prepared; try again later`를 받고 대시보드에는 "준비 중"이 표시됩니다. OwnGit은 알아서 다시 시도합니다. 계속 이 상태라면 서버 로그에서 연결되지 않은 디스크나 잘못된 권한 같은 원인을 찾으세요. 원인을 고친 뒤 OwnGit을 다시 시작합니다. 로그에 `hooks` 항목이 나오면 [`owngit doctor`](OPERATIONS.ko.md)를 실행하세요. 그 항목을 옆으로 옮기는 명령을 알려 줍니다.

읽지 못함은 폴더는 읽었지만 Git 데이터는 읽지 못했다는 뜻입니다. 이때는 Git이 직접 오류를 보여 줍니다.

OwnGit이 푸시 객체를 검사하기 전부터 있던 저장소에는 한 경로에 항목이 둘인 커밋이 있을 수 있습니다. OwnGit은 그 데이터를 지우지 않습니다. 다만 그 항목을 읽는 페이지에는 Git 데이터를 읽지 못했다고 나오고, 언어 패널에는 "지금은 세지 못했습니다"가 보입니다. 그 커밋은 압축 파일로도 받을 수 없습니다.

## 다른 Git 호스트에서 가져오기

가져오기는 다른 Git 호스트의 저장소를 새 OwnGit 저장소로 복사합니다. 나중에 직접, 또는 예약에 따라 새로고침할 수도 있습니다. OwnGit은 원본을 읽기만 하고 원본에 쓰지 않습니다. Git LFS 파일은 받지 않습니다.

### 가져오기 시작하기

대시보드에서 "저장소 가져오기"를 고릅니다. 그 뒤로는 저장소의 가져오기 탭에서 원본, 인증 정보, 설정, 예약을 바꾸고 실행 기록을 봅니다. 원본 주소, 인증 정보, 설정은 관리자만 볼 수 있습니다.

명령줄에서는 다음과 같이 합니다.

```sh
owngit import add PROJECT https://git.example.test/team/project.git \
  --token-file /path/to/owner-only-token
```

`import add`는 저장소를 만들고 실행이 끝날 때까지 기다립니다. 토큰 대신 사용자 이름과 비밀번호를 두 줄로 적은 파일은 `--basic-file`로 넘깁니다. 원본용 인증 기관은 `--ca-file`로 넘깁니다. OwnGit은 인증 정보를 소유자 전용 파일이나 입력 프롬프트로만 받습니다.

| 명령 | 하는 일 |
|---|---|
| `owngit import refresh PROJECT` | 원본을 다시 받고 끝날 때까지 기다림 |
| `owngit import status PROJECT` | 실행, 설정, 한도, 원본과 다른 ref를 보여 줌 |
| `owngit import history PROJECT` | 지난 실행 목록 |
| `owngit import cancel PROJECT` | 게시하기 전의 실행을 멈춤 |
| `owngit import schedule PROJECT --enable --interval 6h` | OwnGit이 실행 중일 때 60초~168시간 간격으로 새로고침 |
| `owngit import credentials PROJECT --token-file FILE` | 인증 정보를 바꿈. `--clear`는 지움 |
| `owngit import configure PROJECT ...` | 원본 주소, 방식, 동의, 연결 설정, 한도, 새로고침 설정을 바꿈([자세히](#원본-바꾸기)) |
| `owngit import resolve PROJECT` | [미해결 게시](#미해결-게시) 뒤 저장소 상태를 인정 |

`add`와 `refresh`는 성공하면 0, 원본과 다른 로컬 ref를 남겼으면 3, 취소되면 130, 그 밖에는 1로 끝납니다. 모든 명령이 `--json`을 받습니다.

다른 Git 작업이 저장소를 쓰고 있으면 `import add`와 원본, 인증 정보, 설정 변경은 그 작업이 끝나기를 기다립니다. 그 전에 요청 시간이 다 되면 HTTP 409 `another Git operation holds the repository; nothing was changed`를 받고, 아무것도 바뀌지 않습니다. 다시 시도하세요. `import add`의 첫 실행이 실패하면 OwnGit은 저장해 둔 원본과 인증 정보를 지웁니다. 이 정리에서 남은 것은 OwnGit을 다음에 시작할 때 지웁니다.

### 원본 바꾸기

`owngit import configure`는 지정한 항목만 바꿉니다. 지정하지 않은 항목은 저장된 값을 그대로 씁니다.

```sh
owngit import configure PROJECT \
  --url https://git.example.test/new-team/project.git --mode coexistence
```

| 옵션 | 정하는 것 |
|---|---|
| `--url URL` | 원본 주소 |
| `--mode standalone` | 독립. 앞으로 OwnGit을 주 저장소로 씀 |
| `--mode coexistence` | 공존. 다른 호스트가 계속 주 저장소이고 이 복사본은 거기서 새로고침함 |
| `--git-only-consent` | Git 내용만 받기([Git LFS](#git-lfs)) |
| `--allow-private-network` | 사설망 원본 허용([원본 주소와 네트워크](#원본-주소와-네트워크)) |

동의를 거두려면 `--git-only-consent=false`나 `--allow-private-network=false`를 씁니다. OwnGit은 대시보드에서와 같은 기준으로 변경을 확인하고, 거부하면 명령이 그 이유를 그대로 보여 줍니다.

원본이 없는 기존 저장소에 원본을 붙이려면 `--url`을 지정합니다. 같은 명령에서 따로 정하지 않으면 방식은 독립이고 두 동의는 꺼진 상태입니다. `import add`는 새 저장소를 만들 때만 씁니다.

### 원본 주소와 네트워크

원본은 HTTPS를 써야 합니다. 다음 주소는 그 원본에 허용해 주어야 연결합니다.

| 원본 | 필요한 설정 |
|---|---|
| `http://` 주소 | 이 원본에 암호화되지 않은 HTTP 허용(`--allow-plain-http`). 코드와 인증 정보가 암호화되지 않은 채 오갑니다. |
| 사설 LAN, tailnet, 루프백 주소 | 사설망 원본 허용(`--allow-private-network`) |
| 문서용이나 벤치마크용 주소 범위 | 예외 대상 주소 허용(`--allow-exceptional-destination`) |
| 169.254.169.254 같은 링크 로컬, 멀티캐스트 | 허용할 수 없음 |

리디렉션은 기본적으로 거부합니다. 리디렉션 설정에서 같은 출처 안의 리디렉션만 따르게 하거나(`--redirects same_origin`), 승인한 출처 하나까지 따르게 할 수 있습니다(`--redirects approved --approved-origin https://mirror.example`). 인증 정보는 원본 자신의 출처에만 보냅니다.

이런 이유로 실행이 멈추면 가져오기 탭이 켜야 할 설정을 알려 줍니다. 원본 주소를 바꾸면 암호화되지 않은 HTTP, 예외 대상 주소, 리디렉션, 두 [새로고침 설정](#새로고침-설정)이 다시 꺼집니다. 새 주소에 필요한 것을 다시 고르거나 같은 `import configure` 명령에 함께 지정하세요. 한도와 추가 ref 네임스페이스는 그대로입니다. 이전 주소용으로 저장한 인증 정보는 더 이상 쓰지 않으므로 `owngit import credentials`로 새 주소용 인증 정보를 저장하세요.

### 한도

한도는 연결과 한도에서 바꾸거나 `owngit import configure PROJECT --limit run_seconds=2h --limit pack_bytes=32GiB`로 바꿉니다. 가져오기가 어떤 한도에 걸려 실패하면 그 한도를 높이세요. 한도를 높이면 디스크와 시간을 더 씁니다.

| 한도 | 기본값 | 범위 |
|---|---|---|
| `pack_bytes`(가장 큰 팩) | 16 GiB | 1 MiB~1 TiB |
| `run_seconds`(실행 시간) | 1시간 | 1분~24시간 |
| `fetch_seconds`(색인을 포함한 다운로드) | 30분 | 1분~24시간 |
| `index_seconds` | 20분 | 1분~24시간 |
| `verify_seconds` | 10분 | 1분~24시간 |
| `refs`(원본이 나열하는 ref) | 50,000 | 1~200,000 |
| `advertisement_bytes`(ref 목록 크기) | 16 MiB | 64 KiB~64 MiB |
| `tls_handshake_seconds` | 15초 | 1초~10분 |
| `response_header_seconds` | 30초 | 1초~1시간 |
| `lfs_objects`(LFS 검사 객체 수) | 200,000 | 1~1,000,000 |

다운로드 시간과 검증 시간은 실행 시간 안에, 색인 시간은 다운로드 시간 안에 들어가야 합니다. 다른 Git 작업이 저장소를 쓰는 동안 기다린 시간도 실행 시간에 들어갑니다.

### 새로고침이 ref를 바꾸는 방식

가져오기는 브랜치와 태그를 받습니다. 새 저장소의 기본 브랜치는 원본을 따릅니다. 기본적으로 새로고침은 OwnGit에서 한 작업을 덮어쓰지 않습니다.

- 브랜치는 지난 새로고침 뒤 여기서 바뀌지 않았거나 여기서 더한 커밋을 원본 브랜치가 모두 담고 있을 때 원본을 따릅니다.
- 태그는 여기서 바뀌지 않았을 때만 원본을 따릅니다.
- 그 밖의 차이는 그대로 두고 달라진 ref로 알립니다.
- 원본에서 지운 ref는 여기 남고 원본에서 삭제됨으로 표시됩니다.

보관된 기록이 켜져 있으면 바뀐 브랜치와 태그의 커밋은 보관된 기록에 남습니다.

### 새로고침 설정

연결과 한도 안의 ref와 새로고침에 있는 세 설정으로 이 동작을 바꿉니다. 다음 새로고침부터 적용됩니다.

| 설정 | 명령줄 | 효과 |
|---|---|---|
| 추가 ref 네임스페이스 | `--extra-ref-prefixes refs/notes/` | 이 이름공간의 ref도 가져옴 |
| 원본과 달라진 브랜치 덮어쓰기 | `--overwrite-diverged` | OwnGit에서 바뀐 ref를 원본 값으로 바꿈 |
| 원본의 삭제 따르기 | `--follow-upstream-deletions` | 원본에서 지운 ref를 지움 |

뒤의 두 설정은 여기서 한 작업을 바꾸거나 지울 수 있습니다. 켜기 전에 가져오기 탭의 "지금 이 설정이 바꿀 ref" 목록이나 `owngit import status`의 같은 목록을 확인하세요. 끄려면 `--overwrite-diverged=false`나 `--follow-upstream-deletions=false`를 씁니다.

둘 다 켜도 새로고침은 보호된 기본 브랜치를 다시 쓰지 않고 HEAD가 가리키는 브랜치를 지우지 않습니다. 가져올 ref를 원본이 하나도 나열하지 않으면 아무것도 지우지 않습니다. 추가 이름공간의 ref에는 보관된 기록이 없습니다.

### Git LFS

원본에 Git LFS 포인터 파일이 있으면 실행은 `git_lfs_required`로 멈춥니다. Git 내용만 받기(`--git-only-consent`)를 고르면 LFS 내용 없이 포인터 파일만 가져옵니다.

### 백업에서 복원한 뒤

백업에는 가져오기마다 원본 주소와 새로고침 설정이 들어갑니다. 인증 정보, 연결 설정, 한도, 예약은 들어가지 않습니다. 복원한 뒤에는 인증 정보를 다시 입력하세요. 암호화되지 않은 HTTP나 예약처럼 원본마다 필요한 설정도 다시 해야 합니다. 복원하거나 인증 정보를 바꾼 뒤의 새로고침은 새 인증 정보로 한 번 이상 확인한 ref만 지웁니다.

### 미해결 게시

ref를 쓰는 도중 멈춘 경우처럼 실행이 어떻게 끝났는지 OwnGit이 확인하지 못하면 그 실행은 미해결로 남습니다. 그러면 저장소 상태를 인정할 때까지 새로고침이 멈춥니다.

1. 브랜치, 태그, HEAD와 마지막 실행의 이유를 확인합니다.
2. 남기고 싶지 않은 것은 일반 Git으로 고칩니다.
3. 실행 중인 가져오기가 없는지 확인합니다.
4. 가져오기 탭에서 "현재 저장소 상태 인정"을 고르거나 `owngit import resolve PROJECT`를 실행합니다.

이유에 잠금 파일을 확인하지 못했다고 나오면 먼저 OwnGit과 그 저장소에 쓰는 모든 Git 프로그램을 멈춥니다. 이름이 나온 `.lock` 파일은 지우지 말고 저장소 밖으로 옮긴 뒤 OwnGit을 시작하고 상태를 인정합니다.

첫 가져오기가 미해결인데 저장소가 아직 없으면 OwnGit을 다시 시작합니다. 그래도 그대로라면 그 가져오기의 `.owngit-create-*` 폴더를 저장소 폴더 밖으로 옮기고 다시 시작합니다.

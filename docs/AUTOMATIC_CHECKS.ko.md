# 자동 체크

<p align="center"><a href="AUTOMATIC_CHECKS.md">English</a> | <b>한국어</b></p>

이 문서는 푸시와 풀 리퀘스트마다 OwnGit이 저장소의 체크를 알아서 실행하게 하려는 소유자를 위한 안내입니다. 저장소가 체크 파일을 커밋하고 소유자가 실행 정책을 저장해 켜면, OwnGit이 호스트에서 자기 계정으로, 제한된 로컬 Docker 컨테이너에서, 또는 별도로 연결한 러너에서 체크를 실행합니다. 결과는 참고용이며 병합을 막지 않습니다.

## 워크플로 파일

저장소는 `.owngit/checks.json`을 커밋해 자동 체크를 쓰겠다고 알리며 OwnGit은 체크하는 바로 그 커밋에서 이 파일을 읽습니다(`--check` 없이 직접 실행하는 체크 에이전트도 같은 파일을 실행합니다. [코딩 도구 연동](CODING_TOOLS.ko.md) 참고).

```json
{
  "version": 1,
  "events": {
    "push": {"branches": ["main", "release/*"]},
    "pull_request": {}
  },
  "checks": [{"name": "unit", "command": "go test ./..."}],
  "limits": {"timeout_ms": 60000, "output_limit_bytes": 65536}
}
```

- `events`로 `push`와 `pull_request`를 켤 수 있습니다. 이벤트 객체가 비어 있으면 모든 브랜치가 대상입니다. `branches`에는 정확한 이름이나 `*` 하나로 끝나는 이름을 쓰며 이벤트마다 200바이트 이하의 패턴을 최대 64개까지 쓸 수 있습니다.
- `checks`에는 이름을 붙인 셸 명령을 1개에서 50개까지 넣습니다. 이름은 최대 100바이트, 명령은 최대 24000바이트입니다.
- `limits`는 선택 사항입니다. OwnGit은 기본값을 적용하고 소유자 정책보다 큰 값은 낮춥니다.

잘못된 UTF-8, 알 수 없는 필드, 중복 키, 빠진 필수 필드, 형식이 틀린 브랜치 패턴, 64 KiB를 넘는 파일은 거부합니다. 실행 방식, 컨테이너 이미지, 네트워크, 자원 한도, 소스 한도, 러너 인증 정보, 동의는 저장소 내용으로 정할 수 없으며 소유자 정책에서 옵니다.

## 소유자 정책과 동의

정책은 실행 방식(`host`, `container`, `external_runner`), 허용할 이벤트, 실행 한도, 대기열과 동시 실행 한도, 임대(lease) 기간, [소스 한도](#소스-한도)를 정합니다. 컨테이너 정책은 여기에 더해 `sha256:<64 lowercase hex digits>`나 `registry.example/checks@sha256:<64 lowercase hex digits>` 같은 불변 이미지, `none` 또는 `bridge` 네트워크, CPU, 메모리, 프로세스, 임시 공간 한도와 소유자가 고른 [컨테이너 설정](#컨테이너-설정)을 지정합니다. 호스트나 외부 러너용 최소 정책은 `execution.source`를 비워 두고 기본 소스 한도를 씁니다.

```json
{
  "executor": "host",
  "allowed_events": ["push", "pull_request"],
  "max_timeout_ms": 600000,
  "max_output_limit_bytes": 1048576,
  "queue_limit": 32,
  "max_active_jobs": 1,
  "max_lease_ms": 60000,
  "execution": {"source": {}}
}
```

저장하고 켭니다.

```sh
owngit check-policy set \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json

owngit check-policy enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password
```

파일의 정책을 저장하면서 바로 그 정책으로 켤 수도 있습니다.

```sh
owngit check-policy set --enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json
```

`check-policy enable`은 저장된 바로 그 정책 버전을 승인하므로, 다른 정책을 저장하면 다시 켤 때까지 실행이 꺼집니다. `check-policy set --enable`은 파일의 정책을 저장하고 한 트랜잭션 안에서 바로 그 정책만 승인하므로, 바뀐 설정으로 명령이 곧바로 실행될 수 있습니다. API로는 `POST .../check-policy/save-and-enable`에 `{"policy": {...}}`를 보냅니다. 선택 항목인 `"expected": {"version": N, "digest": "..."}`는 변경을 검토할 때 비교한 저장된 정책을 가리키며(버전 0과 빈 digest는 정책이 없었다는 뜻), 저장된 정책이 다르면 `check_policy_stale`로 거부합니다. `check-policy disable`은 새 작업을 멈춥니다. 복원해도 실행이 꺼집니다. 조건에 맞는 워크플로 파일과 유효한 동의가 모두 있어야만 무엇이든 실행됩니다. `check-policy show`는 정책과 체크 런타임을 쓸 수 있는지를 출력합니다. OwnGit이 비공개 체크 작업 공간을 준비하지 못하거나 시작할 때의 복구를 마치지 못하면 Git, 설정, 병합은 계속 동작하게 두고 자동 실행과 러너 실행만 멈춘 뒤 `workspace_unavailable` 또는 `restart_reconciliation_unavailable`을 보고합니다. 원인을 고치고 OwnGit을 다시 시작하세요.

## 브라우저 화면

관리자 화면 두 곳에서 같은 작업을 할 수 있습니다. 변경할 때 관리자 비밀번호를 묻는지는 [관리자 비밀번호 확인](OPERATIONS.ko.md#관리자-비밀번호-확인) 설정을 따릅니다.

저장소의 체크 탭과 설정 탭에서 이어지는 **자동 체크** 화면(`/repositories/{id}/configured-checks`)에서는 정책을 편집하고, 실행을 켜거나 끄고, 작업 목록을 봅니다. 화면 맨 위의 상태 요약은 체크가 켜져 있는지, 어디서 언제 실행되는지, 기본 브랜치에 올바른 `.owngit/checks.json`이 있는지, 정책이 저장되어 있고 실행 환경을 쓸 수 있는지, 다음에 할 일이 무엇인지 보여 주고 이어서 다섯 단계로 안내합니다. 체크를 실행할 곳, 실행할 때, 체크 파일(복사해 쓸 수 있는 최소 예시 포함), 저장, 체크 켜기입니다. 켜기는 화면에 표시된 정책 버전을 승인하며 그사이 누군가 다른 정책을 저장했다면 거부됩니다. 저장 버튼 옆의 **저장하고 체크 켜기**는 먼저 바뀔 설정마다 현재 저장값과 새 값을 보여 주고, 바뀐 설정으로 명령이 바로 실행될 수 있다고 경고하며, 아무것도 저장하지 않습니다. 확인하면 검토한 설정 그대로 저장하고 그 설정으로 체크를 켭니다. 검토한 뒤 양식을 고쳤거나 그사이 누군가 저장했다면 저장하지 않고 검토 화면을 다시 보여 줍니다. 일반 저장은 체크를 켜지 않습니다.

한도는 **고급 한도** 아래에 있으며 바로 쓸 수 있는 값이 채워져 있습니다. 시간은 초, 분, 시간 단위로, 크기는 바이트, KB, MB, GB 단위로 입력합니다(1 KB는 1024바이트). 필드마다 허용 범위와 기본값이 표시됩니다. 범위의 최댓값은 그 필드에 대한 이 컴퓨터의 [검사 상한](OPERATIONS.ko.md#검사-상한)이며 관리자가 올릴 수 있습니다. 시간 한도와 출력 한도는 최댓값입니다. 체크 파일의 `limits`에서 이 최댓값 안의 다른 값을 요청하지 않으면 체크의 시간 한도는 10분이고 출력은 64 KiB까지 할 수 있습니다. 출력 한도보다 많이 출력한 체크는 시간이 다 된 체크처럼 멈추고 `incomplete`로 끝나며, 출력 첫 줄에 그 사실이 적힙니다.

작업 페이지에는 커밋, 실행 방식, 워크플로 경로, 설정과 정책 버전, 받아들인 시각이 나옵니다. 작업이 대기 중이거나 가져감 상태이거나 실행 중일 때는 취소를(기록된 취소는 요청일 뿐 프로세스가 멈췄다는 증거가 아닙니다), 끝난 뒤에는 다시 실행을 제공하며, 시도마다 직접 실행한 체크 에이전트인지 자동 작업인지 표시합니다.

`/repositories/{id}/runner-tokens`에서는 정책을 저장한 뒤에 러너 토큰을 발급하고 취소합니다. 토큰 값은 발급 응답에 한 번만 나타나며 URL, 로그, 브라우저 저장소에는 남지 않습니다. 취소한 토큰도 누가 접근할 수 있었는지 보여 주는 기록으로 목록에 남습니다.

## 작업

OwnGit은 푸시, 풀 리퀘스트 갱신, 병합을 감지하고 바로 그 커밋에서 워크플로 파일을 읽어 조건에 맞는 이벤트마다 작업을 받아들입니다. Git 쓰기는 체크를 기다리지 않으며 같은 이벤트로 작업을 두 번 만들지 않습니다. 체크를 처음 켤 때는 조건에 맞는 브랜치 헤드 가운데 작업이 한 번도 없었던 것을 대기열 한도 안에서 한 번씩 넣습니다. 정책의 새 버전을 저장하거나 켜도 이미 작업이 있었던 것은 다시 넣지 않고, 아직 시작하지 않고 기다리던 작업은 `interrupted`로 표시합니다. 기존 헤드를 새 정책으로 다시 확인하려면 그 작업을 다시 실행하세요.

작업은 `pending`에서 `claimed`, `started`를 거쳐 결과 상태가 됩니다. 결과는 `passed`, `failed`, `error`, `cancelled`, `incomplete`, `unavailable`, `ambiguous`, `interrupted` 중 하나입니다. 작업이 한 번 시작되면 명령이 이미 실행되었을 수 있으므로, 다시 시작하거나 임대가 끊기더라도 OwnGit은 그 작업을 스스로 다시 대기열에 넣지 않습니다. 대신 재실행을 요청하세요.

```sh
owngit check-job list --server https://git.example.test --repository project --password-file ./admin-password
owngit check-job show --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job log --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job cancel --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job rerun --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
```

`check-job list`는 가장 최근 작업 100개를 돌려줍니다. `check-job log`는 원본 로그를 읽는데, 원본 로그는 설정에서 정한 기간(기본 30일, [프로젝트 체크](OPERATIONS.ko.md#프로젝트-체크) 참고) 동안만 보관하고 결과는 계속 남습니다. 이미 끝난 작업에 `check-job cancel`을 쓰면 아무것도 바뀌지 않고 `check_job_finished`로 실패합니다. 시작 전에 실패하면 `unavailable`, `error`, `interrupted`로 기록합니다. 작업이 소스를 복사할 때 푸시가 저장소를 붙잡고 있으면 늦어질 뿐이며(OwnGit이 직접 실행하는 체크는 최대 10분, 러너의 소스 요청은 요청마다 20초), 그래도 끝나지 않을 때 `unavailable`로 기록합니다. 명령이 실행된 뒤 OwnGit은 추적되는 파일을 모두 다시 확인합니다. 새로 생긴 추적되지 않은 파일은 괜찮지만 추적되는 파일이 바뀌거나 지워지거나 모드가 바뀌면 깨끗한 결과가 되지 않으며 컨테이너나 프로세스가 정리되었는지 OwnGit이 확인하지 못해도 마찬가지입니다. 끝난 작업의 컨테이너를 OwnGit이 지우지 못하면 저장소의 자동 체크 페이지에 있는 남은 체크 컨테이너 목록에 나옵니다([삭제가 거부될 때](OPERATIONS.ko.md#삭제가-거부될-때) 참고).

## 체크가 보는 소스

작업을 실행하기 전에 OwnGit은 커밋된 파일을 그대로 새 비공개 디렉터리에 복사하며 이 디렉터리는 체크를 실행하는 계정만 읽을 수 있습니다. 러너는 인증된 연결로 같은 파일을 받습니다.

- 바이트까지 정확히 같습니다. Git 속성, 필터, 훅, 줄 끝 변환은 적용하지 않으며 모든 파일을 Git 객체 ID와 대조합니다.
- `.git` 디렉터리, 인덱스, 빈 디렉터리가 없으므로 Git 메타데이터가 필요한 명령은 동작하지 않습니다.
- 심볼릭 링크와 서브모듈은 거부하므로 둘을 추적하는 저장소는 자동 체크를 쓸 수 없으며, 작업은 명령을 실행하기 전에 그 사실을 보고합니다. 안전하지 않은 경로(`..`, 절대 경로, 다르게 표기한 `.git`), 중복 경로, 대소문자를 구분하지 않거나 유니코드를 정규화하는 파일 시스템에서 충돌하는 이름도 거부합니다.
- Git 파일 모드는 기록하지만, Windows에서는 실행 비트를 적용할 수 없습니다.
- Git LFS 포인터 파일은 그대로 복사하며 대용량 파일 내용은 가져오지 않습니다.

### 소스 한도

정책의 `execution.source` 객체가 복사할 범위를 제한하며 어느 한도라도 넘는 작업은 파일을 쓰기 전에 거부합니다. 0이거나 빠진 값은 기본값을 쓰고, `max_total_bytes`는 `max_file_bytes`보다 작을 수 없습니다. 자동 체크 화면에 필드마다 허용 범위와 기본값이 표시됩니다.

| 필드 | 기본값 |
| --- | --- |
| `max_entries` | 트리 항목 20000개(거부된 항목 포함) |
| `max_file_bytes` | 파일당 64 MiB |
| `max_total_bytes` | 전체 256 MiB |
| `max_path_depth` | 경로 구성 요소 64개 |
| `max_path_bytes` | 경로당 1024바이트 |
| `max_name_bytes` | 이름당 255바이트 |
| `metadata_limit_bytes` | 트리 목록 16 MiB |

## 실행 방식

### 호스트

호스트 모드는 OwnGit 계정으로 플랫폼 셸에서 명령을 실행합니다. 샌드박스가 아니므로 명령은 OwnGit의 상태와 인증 정보를 포함해 그 계정이 접근할 수 있는 모든 것에 접근할 수 있습니다. 완전히 믿을 수 있는 저장소에만 쓰세요.

### 제한된 로컬 Docker

컨테이너 모드는 로컬 Linux Docker 데몬만 쓰며 `DOCKER_HOST`, `DOCKER_CONTEXT`, TLS 재정의 없이 데몬을 고르고, 정책이 허용할 때만 이미지를 내려받습니다(pull). 작업마다 이미지를 한 번 이미지 ID로 정하고, 그 작업의 모든 명령을 그 ID로 다음 조건에서 실행합니다.

- root가 아닌 OwnGit 자신의 사용자, 그리고 정책이 쓰기 가능한 루트를 허용하지 않으면 읽기 전용 루트 파일 시스템
- 모든 Linux capability 제거와 `no-new-privileges`
- 선택한 `none`, `bridge` 또는 이름을 지정한 네트워크
- CPU, 메모리, 프로세스 한도(스왑 없음)
- 크기가 제한되고 실행할 수 있는 `/tmp`
- 작업의 소스 디렉터리만 `/workspace`에 읽기와 쓰기가 가능하게 마운트(호스트 디스크를 함께 쓰며 별도의 크기 한도는 없습니다)
- Docker 로깅 끔

정책에서 따로 고르지 않으면 볼륨을 선언한 이미지와, 메모리, 스왑, CPU, 프로세스 한도를 적용하지 않는 데몬은 거부합니다.

#### 컨테이너 설정

각 설정은 소유자가 켜기 전까지 꺼져 있고, 컨테이너 모드에만 적용되며, 무엇을 허용하는지 함께 표시됩니다. 하나라도 바꾸면 새 정책 버전으로 저장되므로 다시 켤 때까지 체크가 꺼집니다. 모두 `execution`의 필드입니다.

| 필드 | 대시보드 | 허용하는 것 |
|---|---|---|
| `container_allow_tags` | 이미지 태그 허용 | `registry.example/checks:1` 같은 태그를 이미지로 쓸 수 있습니다. 태그는 다음번에 다른 코드를 가리킬 수 있으므로, 작업마다 시작할 때 태그를 이미지 ID로 정하고 그 ID를 기록합니다. |
| `container_pull_missing` | 없는 이미지 내려받기 | 이 컴퓨터에 이미지가 없으면 작업 전에 레지스트리에서 내려받습니다(최대 15분). 저장된 레지스트리 로그인, 인증 도우미, `DOCKER_AUTH_CONFIG` 값을 쓰지 않으므로 누구에게나 제공되는 이미지만 받을 수 있습니다. `sha256:` ID만으로는 안 되고 이미지 이름이 필요합니다. |
| `container_network` | 직접 만든 Docker 네트워크 사용 | `none`, `bridge`가 아닌 이름은 소유자가 만든 Docker 네트워크를 뜻합니다. 체크는 그 네트워크의 모든 서비스에 접근할 수 있습니다. `host`는 받아들이지 않습니다. 체크가 OwnGit을 포함해 이 컴퓨터의 서비스에 접근할 수 있게 되기 때문입니다. 네트워크 ID나 `host` 드라이버를 쓰는 네트워크는 작업을 시작할 때 거부합니다. |
| `container_image_volumes` | 이미지 볼륨에 임시 공간 주기 | 이미지가 볼륨으로 선언한 경로마다 임시 공간 한도만큼의 메모리 공간을 주고, 컨테이너와 함께 버립니다. Docker가 따로 볼륨을 만들지 않았는지 확인합니다. `/workspace`, `/tmp`, `/proc`, `/sys`, `/dev`, Docker가 쓰는 `/etc` 파일과 겹치는 경로가 있거나, 경로 자체나 그 경로에 이르는 폴더가 이미지 안의 링크이면 이미지를 계속 거부합니다. |
| `container_writable_root` | 명령이 컨테이너 파일을 바꿀 수 있게 하기 | 루트 파일 시스템을 쓸 수 있게 합니다. 명령은 여전히 OwnGit 자신의 사용자로 실행되므로 그 사용자가 바꿀 수 있는 이미지 파일만 바꿀 수 있고, 바뀐 내용은 컨테이너와 함께 버려집니다. |
| `container_missing_enforcement` | 이 컴퓨터의 Docker가 적용하지 못할 수 있는 한도 | `memory`, `swap`, `cpu`, `pids` 목록입니다. 목록에 있는 한도를 Docker가 적용하지 못해도 체크를 멈추지 않으며, 그러면 체크가 그 자원을 한도보다 많이 쓸 수 있습니다. 목록에 없는 한도는 여전히 체크를 멈추고 그 이름을 알려 줍니다. |

작업이 태그를 쓰거나, 이미지를 내려받거나, 허용한 한도 없이 실행되면 첫 체크의 출력 맨 앞에 이미지 ID와 적용되지 않은 한도가 적히므로, 작업 페이지와 `check-job show`에서 실제로 무엇이 실행되었는지 알 수 있습니다.

명령은 이미지의 사용자가 아니라 항상 OwnGit 자신의 사용자로 실행됩니다(OwnGit이 root로 실행되면 고정된 root가 아닌 사용자). 그래야 명령이 작업 공간에 쓴 파일을 체크가 끝난 뒤 OwnGit이 지울 수 있습니다. privileged 모드, 호스트 네트워크, Docker 소켓이나 호스트 마운트, 임의의 Docker 옵션은 없습니다.

### 외부 러너

외부 러너 작업은 직접 연결한 러너에서만 실행됩니다. 저장소 범위의 토큰을 소유자만 읽을 수 있는 새 파일로 발급한 뒤 러너를 시작하세요.

```sh
owngit runner-credential issue \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --label build-host \
  --token-file ./runner-token

owngit runner \
  --server https://git.example.test \
  --repository project \
  --token-file ./runner-token \
  --workspace-root /srv/owngit-runner/project
```

토큰 파일의 첫 줄에는 토큰을 발급한 서버가 `owngit-server: ORIGIN` 형식으로 적히며, `runner`는 다른 `--server`에는 이 파일을 쓰지 않습니다([자격 증명 파일과 서버 줄](CODING_TOOLS.ko.md#자격-증명-파일과-서버-줄) 참고). 러너 토큰은 `owngit runner-credential list`로 확인하고 `owngit runner-credential revoke --credential ID`로 취소합니다. 서버는 해시만 저장하며 토큰을 취소하면 더는 쓸 수 없고 그 러너가 가져갔지만 아직 시작하지 않은 작업은 중단됩니다.

저장소 이름을 바꾸면 예전 이름으로 시작한 러너는 90일 동안 계속 동작합니다. 그 전에 `--repository`를 새 이름으로 바꿔 러너를 다시 시작하세요([이름을 바꾼 뒤의 러너와 체크 에이전트](OPERATIONS.ko.md#이름을-바꾼-뒤의-러너와-체크-에이전트) 참고).

`runner`와 `runner-credential`은 OwnGit의 HTTPS 주소가 필요합니다. OwnGit을 tailnet에 공유하고 같은 tailnet에 있는 러너에서 `https://NAME.TAILNET.ts.net` 주소를 쓰거나([tailnet에서 HTTPS로 공유하기](OPERATIONS.ko.md#tailnet에서-https로-공유하기) 참고), OwnGit 앞에 TLS를 처리하는 리버스 프록시를 두세요([리버스 프록시 뒤에서 운영하기](OPERATIONS.ko.md#리버스-프록시-뒤에서-운영하기) 참고). `--ca-file /path/to/private-ca.pem`을 쓰면 시스템 루트에 더해 비공개 인증 기관도 신뢰합니다.

리버스 프록시는 OwnGit이 받아들이는 Host를 보내야 하고(`--allowed-host`나 `approve-host`로 승인한 이름이거나, 같은 컴퓨터에서 실행되면 `localhost`, `127.0.0.1`, `::1`도 됩니다. [다른 기기에서 서버에 접속하기](OPERATIONS.ko.md#다른-기기에서-서버에-접속하기) 참고), 큰 본문을 크기 제한 없이 전달해야 합니다. OwnGit이 프록시를 신뢰하도록 설정하면 브라우저 화면도 같은 주소로 쓸 수 있습니다. tailnet 공유는 이 설정을 알아서 합니다. `--accept-insecure-http`를 붙인 일반 HTTP는 루프백 주소에서만 받아들입니다.

러너는 자기 저장소의 작업만 가져가서 정확한 소스 파일을 내려받고, 명령을 자기 계정으로 실행하고(샌드박스가 아닙니다), 작업 공간을 정리한 뒤 결과를 보고합니다. `--workspace-root`는 비어 있거나 전에 OwnGit 러너가 쓰던 폴더의 절대 경로여야 하고, 러너를 실행하는 계정의 소유여야 합니다. 없으면 러너는 그 계정의 캐시 폴더(Linux에서는 `~/.cache/owngit` 또는 `$XDG_CACHE_HOME/owngit`, macOS에서는 `~/Library/Caches/owngit`, Windows에서는 `%LOCALAPPDATA%\owngit`) 안에 서버와 저장소별 폴더를 만들어 쓰며, OwnGit 1.1.0 이하가 임시 폴더에 만든 작업 공간은 남아 있는 동안 계속 씁니다. 러너는 루트가 비어 있지 않은데 OwnGit이 소유하지 않았거나, 다른 러너가 쓰고 있거나, 다른 계정의 소유이거나, Linux와 macOS에서 다른 계정이 그 위의 폴더를 바꾸거나 바꿔치기할 수 있으면 거부하고 해결 방법을 알려 줍니다. root로 실행하면 경고가 나오니 아래 서비스 예시처럼 전용 계정으로 실행하세요.

러너는 OwnGit이 다시 시작되거나 네트워크가 끊겨도 멈추지 않습니다. 간격을 최대 1분까지 늘려 가며 다시 시도하고(OwnGit이 `Retry-After`를 보낸 경우에만 더 길게), 장애와 복구를 각각 한 번씩 기록합니다. 장애 중에 실행되던 작업은 결과가 확인되지 않은 채 끝날 수 있습니다. 그러면 러너가 작업 ID와 이유를 담은 줄을 한 번 기록하고, OwnGit은 임대가 만료될 때 그 작업을 `ambiguous`로 표시합니다. 러너는 다시 시도해도 소용없을 때만 종료 코드 1로 멈춥니다. 토큰을 알 수 없거나 취소된 경우, 토큰이 `--repository`와 다른 저장소의 것인 경우, 서버가 요청 자체를 잘못되었다고 거부한 경우입니다. `--once`를 쓰면 작업을 최대 하나만 가져와 한 번만 시도하며, 서버에 연결할 수 없는 경우를 포함해 실패하면 종료 코드 1로 끝납니다.

재부팅 뒤에도 러너가 계속 동작하게 하려면 시스템의 서비스 관리자로 실행하세요. 예를 들어 systemd를 쓰는 Linux에서는 다음과 같습니다.

```ini
[Unit]
Description=OwnGit runner for project
Wants=network-online.target
After=network-online.target

[Service]
User=owngit-runner
ExecStart=/usr/local/bin/owngit runner --server https://git.example.test --repository project --token-file /etc/owngit-runner/project-token --workspace-root /srv/owngit-runner/project
Restart=on-failure
RestartSec=60

[Install]
WantedBy=multi-user.target
```

토큰 파일은 서비스 계정만 읽을 수 있어야 합니다. macOS에서는 launchd 에이전트나 데몬으로, Windows에서는 서비스 래퍼로 같은 명령을 실행하세요. 토큰이 취소된 뒤에는 `Restart=on-failure`가 같은 오류만 되풀이하므로 먼저 새 토큰을 발급하세요.

## 백업과 복원

오프라인 백업에는 정책, 작업, 결과가 들어가지만 러너 토큰은 들어가지 않습니다. 복원한 뒤에는 소유자가 다시 켤 때까지 실행이 꺼져 있고, 러너 토큰을 다시 발급해야 하며, 끝나지 않은 작업은 다시 실행하지 않고 `interrupted`로 표시합니다. 이전 OwnGit 버전의 정책과 작업은 기록으로 남으며 소유자가 현재 정책을 저장하고 켜기 전에는 실행할 수 없습니다.

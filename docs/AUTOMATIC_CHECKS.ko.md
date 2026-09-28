# 자동 체크

<p align="center"><a href="AUTOMATIC_CHECKS.md">English</a> | <b>한국어</b></p>

이 문서는 푸시와 풀 리퀘스트마다 OwnGit이 저장소의 체크를 알아서 실행하게 하려는 소유자를 위한 안내입니다. 저장소가 체크 파일을 커밋하고, 소유자가 실행 정책을 저장해 켜면, OwnGit이 호스트에서 자기 계정으로, 제한된 로컬 Docker 컨테이너에서, 또는 별도로 연결한 러너에서 체크를 실행합니다. 결과는 참고용이며 병합을 막지 않습니다.

## 워크플로 파일

저장소는 `.owngit/checks.json`을 커밋해 자동 체크를 쓰겠다고 알리며, OwnGit은 체크하는 바로 그 커밋에서 이 파일을 읽습니다(`--check` 없이 직접 실행하는 체크 에이전트도 같은 파일을 실행합니다. [코딩 도구 연동](CODING_TOOLS.ko.md) 참고).

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

- `events`로 `push`와 `pull_request`를 켤 수 있습니다. 이벤트 객체가 비어 있으면 모든 브랜치가 대상입니다. `branches`에는 정확한 이름이나 `*` 하나로 끝나는 이름을 쓰며, 이벤트마다 200바이트 이하의 패턴을 최대 64개까지 쓸 수 있습니다.
- `checks`에는 이름을 붙인 셸 명령을 1개에서 50개까지 넣습니다. 이름은 최대 100바이트, 명령은 최대 24000바이트입니다.
- `limits`는 선택 사항입니다. OwnGit은 기본값을 적용하고, 소유자 정책보다 큰 값은 낮춥니다.

잘못된 UTF-8, 알 수 없는 필드, 중복 키, 빠진 필수 필드, 형식이 틀린 브랜치 패턴, 64 KiB를 넘는 파일은 거부합니다. 실행 방식, 컨테이너 이미지, 네트워크, 자원 한도, 소스 한도, 러너 인증 정보, 동의는 저장소 내용으로 정할 수 없으며 소유자 정책에서 옵니다.

## 소유자 정책과 동의

정책은 실행 방식(`host`, `container`, `external_runner`), 허용할 이벤트, 실행 한도, 대기열과 동시 실행 한도, 임대(lease) 기간, [소스 한도](#소스-한도)를 정합니다. 컨테이너 정책은 여기에 더해 `sha256:<64 lowercase hex digits>`나 `registry.example/checks@sha256:<64 lowercase hex digits>` 같은 불변 이미지, `none` 또는 `bridge` 네트워크, CPU, 메모리, 프로세스, 임시 공간 한도를 지정합니다. 호스트나 외부 러너용 최소 정책은 `execution.source`를 비워 두고 기본 소스 한도를 씁니다.

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

`check-policy enable`은 저장된 바로 그 정책 버전을 승인하므로, 다른 정책을 저장하면 다시 켤 때까지 실행이 꺼집니다. `check-policy disable`은 새 작업을 멈추며, 복원해도 실행이 꺼집니다. 조건에 맞는 워크플로 파일과 유효한 동의가 모두 있어야만 무엇이든 실행됩니다. `check-policy show`는 정책과 체크 런타임을 쓸 수 있는지를 출력합니다. OwnGit이 비공개 체크 작업 공간을 준비하지 못하거나 시작할 때의 복구를 마치지 못하면 Git, 설정, 병합은 계속 동작하게 두고 자동 실행과 러너 실행만 멈춘 뒤 `workspace_unavailable` 또는 `restart_reconciliation_unavailable`을 보고합니다. 원인을 고치고 OwnGit을 다시 시작하세요.

## 브라우저 화면

관리자 화면 두 곳에서 같은 작업을 할 수 있습니다. 변경할 때 관리자 비밀번호를 묻는지는 [관리자 비밀번호 확인](OPERATIONS.ko.md#관리자-비밀번호-확인) 설정을 따릅니다.

저장소의 체크 탭과 설정 탭에서 이어지는 **자동 체크** 화면(`/repositories/{id}/configured-checks`)에서는 정책을 편집하고, 실행을 켜거나 끄고, 작업 목록을 봅니다. 화면 맨 위의 상태 요약은 체크가 켜져 있는지, 어디서 언제 실행되는지, 기본 브랜치에 올바른 `.owngit/checks.json`이 있는지, 정책이 저장되어 있고 실행 환경을 쓸 수 있는지, 다음에 할 일이 무엇인지 보여 주고, 이어서 다섯 단계로 안내합니다. 체크를 실행할 곳, 실행할 때, 체크 파일(복사해 쓸 수 있는 최소 예시 포함), 저장, 체크 켜기입니다. 켜기는 화면에 표시된 정책 버전을 승인하며, 그사이 누군가 다른 정책을 저장했다면 요청은 409로 거부됩니다.

한도는 **고급 한도** 아래에 있으며 바로 쓸 수 있는 값이 채워져 있습니다. 시간은 초, 분, 시간 단위로, 크기는 바이트, KB, MB, GB 단위로 입력합니다(1 KB는 1024바이트). 시간은 밀리초, 크기는 바이트 단위의 정수로 떨어져야 하고, CPU는 소수점 아래 세 자리까지만 쓸 수 있으며, 필드마다 허용 범위와 기본값이 표시됩니다. 시간 한도와 출력 한도는 최댓값입니다. 체크 파일의 `limits`에서 이 최댓값 안의 다른 값을 요청하지 않으면 체크의 시간 한도는 10분이고 출력은 64 KiB까지 보관합니다.

작업 페이지에는 커밋, 실행 방식, 워크플로 경로, 설정과 정책 버전, 받아들인 시각이 나옵니다. 작업이 대기 중이거나 가져감 상태이거나 실행 중일 때는 취소를(기록된 취소는 요청일 뿐 프로세스가 멈췄다는 증거가 아닙니다), 끝난 뒤에는 다시 실행을 제공하며, 시도마다 직접 실행한 체크 에이전트인지 자동 작업인지 표시합니다.

`/repositories/{id}/runner-tokens`에서는 정책을 저장한 뒤에 러너 토큰을 발급하고 취소합니다. 토큰 값은 발급 응답에 한 번만 나타나며 URL, 로그, 브라우저 저장소에는 남지 않습니다. 취소한 토큰도 누가 접근할 수 있었는지 보여 주는 기록으로 목록에 남습니다.

## 작업

OwnGit은 푸시, 풀 리퀘스트 갱신, 병합을 감지하고, 바로 그 커밋에서 워크플로 파일을 읽어 조건에 맞는 이벤트마다 작업을 받아들입니다. Git 쓰기는 체크를 기다리지 않으며, 같은 이벤트로 작업을 두 번 만들지 않습니다. 체크를 처음 켤 때는 조건에 맞는 브랜치 헤드 가운데 작업이 한 번도 없었던 것을 대기열 한도 안에서 한 번씩 넣습니다. 정책의 새 버전을 저장하거나 켜도 이미 작업이 있었던 것은 다시 넣지 않고, 아직 시작하지 않고 기다리던 작업은 `interrupted`로 표시합니다. 기존 헤드를 새 정책으로 다시 확인하려면 그 작업을 다시 실행하세요.

작업은 `pending`에서 `claimed`, `started`를 거쳐 결과 상태가 됩니다. 결과는 `passed`, `failed`, `error`, `cancelled`, `incomplete`, `unavailable`, `ambiguous`, `interrupted` 중 하나입니다. 작업이 한 번 시작되면 명령이 이미 실행되었을 수 있으므로, 다시 시작하거나 임대가 끊기더라도 OwnGit은 그 작업을 스스로 다시 대기열에 넣지 않습니다. 대신 재실행을 요청하세요.

```sh
owngit check-job list --server https://git.example.test --repository project --password-file ./admin-password
owngit check-job show --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job log --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job cancel --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
owngit check-job rerun --server https://git.example.test --repository project --password-file ./admin-password --job JOB_ID
```

`check-job list`는 가장 최근 작업 100개를 돌려주고, `check-job log`는 원본 로그를 읽으며(로그는 만료되지만 결과는 남습니다), 이미 끝난 작업에 `check-job cancel`을 쓰면 아무것도 바뀌지 않고 `check_job_finished`로 실패합니다. 시작 전에 실패하면 `unavailable`, `error`, `interrupted`로 기록합니다. 작업이 소스를 복사할 때 푸시가 저장소를 붙잡고 있으면 늦어질 뿐이며(OwnGit이 직접 실행하는 체크는 최대 10분, 러너의 소스 요청은 요청마다 20초), 그래도 끝나지 않을 때 `unavailable`로 기록합니다. 명령이 실행된 뒤 OwnGit은 추적되는 파일을 모두 다시 확인합니다. 새로 생긴 추적되지 않은 파일은 괜찮지만, 추적되는 파일이 바뀌거나 지워지거나 모드가 바뀌면 깨끗한 결과가 되지 않으며, 컨테이너나 프로세스가 정리되었는지 OwnGit이 확인하지 못해도 마찬가지입니다.

## 체크가 보는 소스

작업을 실행하기 전에 OwnGit은 커밋된 파일을 그대로 새 비공개 디렉터리에 복사하며, 이 디렉터리는 체크를 실행하는 계정만 읽을 수 있습니다. 러너는 인증된 연결로 같은 파일을 받습니다.

- 바이트까지 정확히 같습니다. Git 속성, 필터, 훅, 줄 끝 변환은 적용하지 않으며, 모든 파일을 Git 객체 ID와 대조합니다.
- `.git` 디렉터리, 인덱스, 빈 디렉터리가 없으므로 Git 메타데이터가 필요한 명령은 동작하지 않습니다.
- 심볼릭 링크와 서브모듈은 거부하므로 둘을 추적하는 저장소는 자동 체크를 쓸 수 없으며, 작업은 명령을 실행하기 전에 그 사실을 보고합니다. 안전하지 않은 경로(`..`, 절대 경로, 다르게 표기한 `.git`), 중복 경로, 대소문자를 구분하지 않거나 유니코드를 정규화하는 파일 시스템에서 충돌하는 이름도 거부합니다.
- Git 파일 모드는 기록하지만, Windows에서는 실행 비트를 적용할 수 없습니다.
- Git LFS 포인터 파일은 그대로 복사하며 대용량 파일 내용은 가져오지 않습니다.

### 소스 한도

정책의 `execution.source` 객체가 복사할 범위를 제한하며, 어느 한도라도 넘는 작업은 파일을 쓰기 전에 거부합니다. 0이거나 빠진 값은 기본값을 쓰고, `max_total_bytes`는 `max_file_bytes`보다 작을 수 없습니다. 자동 체크 화면에 필드마다 허용 범위와 기본값이 표시됩니다.

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

컨테이너 모드는 로컬 Linux Docker 데몬만 쓰며, `DOCKER_HOST`, `DOCKER_CONTEXT`, TLS 재정의 없이 데몬을 고르고, 이미지를 내려받지(pull) 않습니다. 소유자가 고정한 이미지를 다음 조건으로 실행합니다.

- root가 아닌 사용자와 읽기 전용 루트 파일 시스템
- 모든 Linux capability 제거와 `no-new-privileges`
- 선택한 `none` 또는 `bridge` 네트워크
- CPU, 메모리, 프로세스 한도(스왑 없음)
- 크기가 제한되고 실행할 수 있는 `/tmp`
- 작업의 소스 디렉터리만 `/workspace`에 읽기와 쓰기가 가능하게 마운트(호스트 디스크를 함께 쓰며 별도의 크기 한도는 없습니다)
- Docker 로깅 끔

볼륨을 선언한 이미지와, 메모리, 스왑, CPU, 프로세스 한도를 적용하지 않는 데몬은 거부합니다.

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

토큰 파일의 첫 줄에는 토큰을 발급한 서버가 `owngit-server: ORIGIN` 형식으로 적히며, `runner`는 다른 `--server`에는 이 파일을 쓰지 않습니다([자격 증명 파일과 서버 줄](CODING_TOOLS.ko.md#자격-증명-파일과-서버-줄) 참고). 러너 토큰은 `owngit runner-credential list`로 확인하고 `owngit runner-credential revoke --credential ID`로 취소합니다. 서버는 해시만 저장하며, 토큰을 취소하면 더는 쓸 수 없고 그 러너가 가져갔지만 아직 시작하지 않은 작업은 중단됩니다.

`runner`와 `runner-credential`은 HTTPS가 필요하므로 OwnGit 앞에 TLS를 처리하는 프록시를 두세요(`--ca-file /path/to/private-ca.pem`을 쓰면 시스템 루트에 더해 비공개 인증 기관도 신뢰합니다). 프록시는 OwnGit이 받아들이는 Host를 보내야 하고(`--allowed-host`나 `approve-host`로 승인한 이름이거나, 같은 컴퓨터에서 실행되면 `localhost`, `127.0.0.1`, `::1`도 됩니다. [다른 기기에서 서버에 접속하기](OPERATIONS.ko.md#다른-기기에서-서버에-접속하기) 참고), 큰 본문을 크기 제한 없이 전달해야 합니다. OwnGit은 이런 프록시를 거쳐 들어온 브라우저 변경을 거부하므로, 프록시는 러너용으로 쓰고 브라우저 화면은 직접 여세요. `--accept-insecure-http`를 붙인 일반 HTTP는 루프백 주소에서만 받아들입니다.

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

오프라인 백업에는 정책, 작업, 결과가 들어가지만 러너 토큰은 들어가지 않습니다. 복원한 뒤에는 소유자가 다시 켤 때까지 실행이 꺼져 있고, 러너 토큰을 다시 발급해야 하며, 끝나지 않은 작업은 다시 실행하지 않고 `interrupted`로 표시합니다. 이전 OwnGit 버전의 정책과 작업은 기록으로 남으며, 소유자가 현재 정책을 저장하고 켜기 전에는 실행할 수 없습니다.

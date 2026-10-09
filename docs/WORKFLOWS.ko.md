# GitHub Actions 워크플로

<p align="center"><a href="WORKFLOWS.md">English</a> | <b>한국어</b></p>

OwnGit은 저장소에 있는 GitHub Actions 워크플로 파일(`.github/workflows/*.yml`, `*.yaml`)을 자체 [자동 체크](AUTOMATIC_CHECKS.ko.md)로 실행합니다. GitHub에는 아무것도 보내지 않으며 액션도 내려받지 않습니다. 이 안내는 OwnGit 저장소에 푸시하는 사람과 서버를 운영하는 관리자를 위한 것입니다.

워크플로를 실행하려면 OwnGit 1.1.8 이상이 필요합니다. 지원하는 `run` 단계로 빌드하고 테스트하는 워크플로는 GitHub 없이 실행할 수 있습니다. 다른 사람이 만든 액션을 쓰는 단계는 `run` 단계로 바꿔야 합니다. [액션을 run 단계로 바꾸기](#액션을-run-단계로-바꾸기)를 보세요.

시작하는 순서입니다.

1. 관리자가 저장소의 자동 체크를 설정합니다. 작업을 실행할 곳, 한도, 실행을 시작할 수 있는 이벤트를 정합니다. [자동 체크](AUTOMATIC_CHECKS.ko.md#정책과-체크-켜기)를 보세요.
2. 관리자가 저장소의 워크플로를 켭니다. OwnGit 1.1.8 이상에서 체크 정책을 처음 저장하면 명시적으로 끄지 않는 한 워크플로가 기본으로 켜집니다. [워크플로 켜기](#워크플로-켜기)를 보세요.
3. 워크플로 파일이 든 커밋을 푸시합니다. OwnGit은 그 커밋에서 파일을 읽고 조건에 맞는 실행을 시작합니다.

결과는 저장소의 체크 탭, 각 풀 리퀘스트, 저장소 페이지에 나옵니다. 다른 체크처럼 병합을 막지는 않습니다.

## 워크플로 켜기

워크플로를 실행할지는 `.owngit/checks.json`과 마찬가지로 체크 정책이 정합니다. 다음 세 가지가 모두 맞아야 실행됩니다.

- 정책에서 `run_workflows`가 켜져 있습니다.
- 바로 그 정책으로 체크가 켜져 있습니다(관리자 승인).
- 이벤트가 정책의 `allowed_events`에 들어 있습니다. 이벤트는 `push`, `pull_request`, `workflow_dispatch`, `schedule`입니다.

체크 탭에는 작업, 워크플로, 실행 기록 화면이 있습니다. 작업에서는 체크 작업을, 워크플로에서는 파일을, 실행 기록에서는 워크플로 실행 내역을 봅니다. 워크플로 화면은 브랜치에서 찾은 파일을 기본 브랜치부터 보여 줍니다. 파일마다 트리거와 작업, 실행될 작업, 거부되거나 표시만 되는 작업, 대기열에 들어갈 수 없는 실행을 알려 줍니다. 워크플로가 꺼져 있으면 이 목록을 확인한 뒤 버튼 하나로 켤 수 있습니다. 관리자 확인은 설정의 접근 권한 탭에서 정한 방식에 따릅니다.

명령줄에서는 정책 파일에 `"run_workflows": true`와 쓸 이벤트를 넣은 뒤 저장과 켜기를 한 번에 합니다.

```json
{
  "executor": "host",
  "allowed_events": ["push", "pull_request", "workflow_dispatch", "schedule"],
  "run_workflows": true,
  "max_timeout_ms": 600000,
  "max_output_limit_bytes": 1048576,
  "queue_limit": 32,
  "max_active_jobs": 1,
  "max_lease_ms": 60000,
  "execution": {"source": {}}
}
```

```sh
owngit check-policy set --enable \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --policy-file ./check-policy.json
```

- 1.1.8 이상에서 처음 저장한 정책은 파일에 `false`라고 적지 않는 한 `run_workflows`가 켜집니다. 이전 버전에서 저장한 정책은 업그레이드한 뒤에도 꺼진 채로 남습니다. GitHub용으로 쓴 파일이 저절로 실행되지 않게 하려는 것입니다.
- `run_workflows`를 바꾸면 정책이 바뀌므로 다시 켤 때까지 체크가 꺼집니다. `--enable`을 붙이면 저장과 켜기를 한 번에 합니다.
- 워크플로를 켜는 것만으로는 아무것도 실행되지 않습니다. 그다음 푸시, 풀 리퀘스트 갱신, 예약 시각, 직접 실행이 실행을 시작합니다.
- `owngit workflow list`와 `owngit workflow show --path FILE`은 워크플로 화면과 같은 내용을 출력합니다.

## 실행되는 것

### 이벤트

실행 하나는 워크플로 파일 하나와 이벤트 하나에 대응합니다.

| 이벤트 | 시작하는 때 |
| --- | --- |
| `push` | OwnGit이 Git 클라이언트에서 받아들인 브랜치 푸시 |
| `pull_request` | 새로 열리거나 갱신된 풀 리퀘스트. 소스 커밋이 그런 푸시로 들어온 경우만 해당합니다. |
| `workflow_dispatch` | 누군가 워크플로를 직접 실행할 때. [워크플로 직접 실행하기](#워크플로-직접-실행하기)를 보세요. |
| `schedule` | 기본 브랜치에 적힌 cron 시각. [예약 실행](#예약-실행)을 보세요. |

조건에 맞는 파일마다 실행이 따로 생깁니다. OwnGit이 실행할 수 없는 파일은 그 파일의 실행만 거부합니다. `release`나 `issues` 같은 다른 GitHub 이벤트는 워크플로 화면에 OwnGit이 실행하지 않는 이벤트로 표시될 뿐 같은 파일의 다른 트리거는 그대로 동작합니다.

워크플로를 실행할 권한은 OwnGit이 인증하고 받아들인 푸시에만 있습니다. 가져오기 갱신이나 복원, 그 밖의 방법으로 브랜치가 바뀌어도 실행이 저절로 시작되지 않으며 저장소의 시크릿도 읽지 않습니다. 풀 리퀘스트의 소스 커밋이 가져오기로 들어왔다면 실행은 시작되지 않고 OwnGit이 새로 푸시해야 한다고 알려 줍니다. 직접 실행과 예약 실행에도 해당 저장소, 브랜치 ref, 현재 커밋에 대해 받아들인 푸시 기록이 남아 있어야 합니다. 가져오거나 복원한 브랜치에 이 기록이 없으면 직접 실행할 수 없습니다. 먼저 새 커밋을 OwnGit에 푸시하세요.

### 필터

- `branches`, `branches-ignore`, `paths`, `paths-ignore`는 `**`와 `!` 패턴을 포함해 GitHub의 패턴 규칙을 따릅니다. `pull_request`의 브랜치 필터는 대상 브랜치와 비교합니다.
- `tags`, `tags-ignore`는 읽기는 하지만 OwnGit은 태그 푸시로 워크플로를 실행하지 않습니다. 태그 필터만 있는 푸시 트리거로는 실행이 시작되지 않습니다.
- `pull_request`의 `types`: 풀 리퀘스트의 첫 리비전은 `opened`, 그 뒤 리비전은 모두 `synchronize`입니다. `reopened`, `closed` 같은 다른 유형은 보내지 않습니다.
- `paths`에 쓰는 바뀐 파일 목록은 GitHub처럼 푸시는 두 점 diff, 풀 리퀘스트는 세 점 diff로 구합니다. 새 브랜치는 기본 브랜치와의 병합 기준 커밋과 비교합니다. 첫 푸시이거나 기본 브랜치와 공통 커밋이 없으면 모든 파일을 바뀐 것으로 봅니다.

바뀐 파일 목록을 끝까지 읽지 못하면(파일 3,000개 초과, 파일 이름 1 MiB 초과, 10초 초과, 쓸 수 있는 기준 커밋 없음) 이미 읽은 파일만으로 판단할 수 있을 때만 판단합니다. 판단할 수 없으면 그 실행은 이유와 함께 실행하지 않음으로 기록됩니다. 필터 없이 실행하려면 다시 실행하세요.

### 작업

- 작업 하나, 그리고 매트릭스 조합 하나가 각각 체크 작업 하나가 됩니다. 실행 하나에는 작업이 최대 16개까지 들어갑니다. 더 큰 실행은 나누는 방법을 알려 주며 거부됩니다.
- `needs`가 있는 작업은 지정한 작업의 모든 조합이 끝날 때까지 `waiting` 상태로 기다립니다.
- 작업과 단계의 `if`는 GitHub와 같습니다. 상태 함수가 없으면 `success() && (...)`로 판단합니다. `always()`, `failure()`, `cancelled()`도 GitHub처럼 동작합니다.
- `continue-on-error`, `timeout-minutes`(작업 기본값 360분), `strategy.fail-fast`(기본값 켜짐), `strategy.max-parallel`, `concurrency`(`cancel-in-progress`, `queue` 포함)를 적용합니다. 동시 실행 그룹 이름은 저장소의 모든 워크플로가 함께 쓰며 대소문자를 구분하지 않습니다.
- 작업은 `continue-on-error`로 허용하지 않은 단계가 처음 실패하면 멈춥니다. 그 뒤 단계는 `if: failure()`나 `if: always()`처럼 `if`로 요청한 경우에만 실행됩니다.
- `runs-on`은 표시만 하고 실행할 컴퓨터를 고르지 않습니다. 모든 작업을 어디서 실행할지는 정책이 정합니다([체크를 실행할 곳](AUTOMATIC_CHECKS.ko.md#체크를-실행할-곳) 참고). 운영체제 매트릭스(`os: [ubuntu-latest, windows-latest]`)는 같은 컴퓨터에서 같은 일을 되풀이할 뿐이므로 그 축은 지우세요.
- `permissions`, `cache-mode`, 작업 `outputs`는 받아들이지만 적용하지 않는다고 표시합니다.

서버는 로컬 작업(호스트, 컨테이너)을 모든 저장소를 통틀어 한 번에 하나씩 실행합니다. 그래서 작업 16개짜리 매트릭스는 하나씩 차례로 돕니다. 외부 러너는 정책의 `max_active_jobs`까지 동시에 실행할 수 있습니다.

### 단계와 셸

단계는 `run` 스크립트이거나 아래의 기본 제공 액션입니다. 기본 셸은 Linux, macOS, 컨테이너에서 `bash -e {0}`이고(bash가 없으면 `sh -e {0}`), Windows에서는 `pwsh`(없으면 `powershell`)입니다. Windows에서 `runs-on`이 Linux나 macOS를 가리키는 작업은 Git for Windows의 bash가 있으면 그것을 씁니다. `shell`에는 GitHub처럼 `bash`, `sh`, `pwsh`, `powershell`, `cmd`, `python`, 또는 스크립트 파일 자리에 `{0}`을 넣은 명령을 쓸 수 있습니다.

단계마다 정책의 `max_timeout_ms`만큼 실행하고 `max_output_limit_bytes`만큼 출력할 수 있습니다. 정책의 시간 한도에 걸린 단계는 `max_timeout_ms` 때문에 멈췄다고 알려 줍니다. 관리자는 자동 체크 화면에서 이 값을 늘릴 수 있습니다.

`GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`은 GitHub와 같이 동작합니다. 한 단계에서 같은 작업의 다음 단계로 변수, `PATH` 항목, `steps.<id>.outputs`를 넘길 수 있습니다. `GITHUB_ENV`로는 `GITHUB_*`, `RUNNER_*`, `NODE_OPTIONS`를 설정할 수 없습니다. 그런 줄은 무시하고 작업에 그 사실을 남깁니다. `GITHUB_STEP_SUMMARY`에 쓴 작업 요약은 OwnGit에 표시되지 않습니다.

### 기본 제공 액션

| 액션 | OwnGit의 처리 |
| --- | --- |
| `actions/checkout` | 통과합니다. 커밋은 이미 작업 공간에 있고 `.git` 폴더는 없습니다. `ref`, `repository`, `path`는 이 실행의 커밋, 저장소, 작업 공간을 가리켜야 하고 `submodules`, `lfs`는 꺼져 있어야 합니다. |
| `actions/setup-go`, `setup-node`, `setup-python`, `setup-java` | 실행하지 않습니다. 작업은 컴퓨터나 이미지에 이미 있는 도구를 씁니다. 요청한 버전은 표시하지만 설치하거나 확인하거나 고르지는 않습니다. |
| `actions/cache`, `actions/cache/restore`, `actions/cache/save` | 실행하지 않습니다. OwnGit은 캐시를 보관하지 않으며 `cache-hit`은 `'false'`입니다. |
| `actions/upload-artifact` | 실행하지 않습니다. OwnGit은 산출물을 보관하지 않으므로 파일은 그 작업의 작업 공간에만 남습니다. |

아무것도 내려받지 않으므로 버전(`@v4`, `@main`, 커밋)은 무엇이든 받아들입니다. 액션 문서에 없는 입력을 쓰면 그 작업을 거부합니다.

## 거부하는 것과 그 이유

OwnGit은 워크플로 파일을 엄격하게 읽습니다. 지킬 수 없는 키나 기능이 있으면 아무것도 실행하기 전에 파일이나 작업을 멈춥니다. 워크플로 화면과 실행 화면에 해당 줄과 고치는 방법이 나옵니다. 모르는 키를 건너뛰고 실행하는 일은 없습니다.

| 거부하는 것 | 이유 | 고치는 방법 |
| --- | --- | --- |
| 그 밖의 `uses:` 액션, `docker://` 액션, 로컬 `./` 액션 | OwnGit은 액션을 내려받거나 빌드하지 않습니다. | `run` 단계로 바꿉니다. 다음 절을 보세요. |
| `actions/download-artifact` | 산출물을 보관하지 않으므로 파일이 없습니다. | 파일을 같은 작업 안에서 만드세요. |
| 재사용 워크플로(작업의 `uses`, `with`, `secrets`) | 실행되지 않습니다. | 그 작업들을 이 파일로 옮겨 적으세요. |
| 작업의 `container`, `services` | 작업을 실행할 곳과 쓸 컨테이너는 관리자의 정책이 정합니다. | 키를 지우세요. 서비스가 필요하면 `run` 스크립트 하나 안에서 시작해 쓰거나, 정책의 네트워크로 접속할 수 있는 서비스를 쓰세요. |
| 작업의 `environment` | OwnGit에는 배포 환경과 승인 절차가 없고 일반 접근 권한이 있으면 누구나 워크플로를 직접 실행할 수 있습니다. | 승인 없이 이 작업을 실행해도 될 때만 지우세요. |
| 작업의 `snapshot`, 단계의 `background`, `wait`, `wait-all`, `cancel`, `parallel` | OwnGit은 러너 이미지를 만들지 않으며 단계를 백그라운드로 실행하지 않습니다. | `run` 스크립트 하나 안에서 프로세스를 시작하고 쓰세요. |
| GitHub에 없는 이벤트 이름 | 파일이 뜻하는 바가 분명하지 않습니다. | 철자를 확인하세요. `pull_request_target` 같은 다른 GitHub 이벤트는 실행하지 않는다고 표시만 합니다. 대신 풀 리퀘스트 자체의 커밋으로 실행하는 `pull_request`를 쓰세요. |
| `on.schedule[].timezone` | 예약 실행은 UTC로만 합니다. | cron 시각을 UTC로 쓰세요. |
| `on.workflow_dispatch.inputs.*.type: environment` | 배포 환경이 없습니다. | `string`이나 `choice`를 쓰세요. |
| `github.token`, `secrets.GITHUB_TOKEN` | OwnGit은 워크플로에 GitHub 토큰을 주지 않습니다. | 직접 만든 토큰을 다른 이름의 시크릿으로 저장하세요. |
| `vars`, `needs.<id>.outputs`, `hashFiles()`, [식](#식)에 없는 `github` 값 | 제공하지 않습니다. | `env`나 시크릿을 쓰세요. 값은 그 값이 필요한 작업에서 계산하고 해시는 `run` 단계에서 계산하세요. |
| YAML 태그, 병합 키(`<<`), 문서 여러 개, 중복 키, 모르는 키 | 파일의 뜻이 하나로 정해져야 합니다. | 값을 풀어 쓰고 GitHub 워크플로 문법과 철자를 비교하세요. |

한도를 넘는 파일도 거부합니다. [한도](#한도)를 보세요.

## 액션을 run 단계로 바꾸기

액션을 그 액션이 실행할 명령으로 바꾸고 필요한 도구는 컨테이너 이미지나 호스트, 러너에 설치합니다.

| 원래 단계 | 바꿀 단계 |
| --- | --- |
| `golangci/golangci-lint-action@v6` | `run: golangci-lint run` |
| `pnpm/action-setup@v4` | `run: pnpm install --frozen-lockfile`(이미지에 pnpm 설치) |
| `actions/setup-dotnet@v4` | `run: dotnet test`(이미지에 .NET 설치) |
| `ruby/setup-ruby@v1`과 `bundler-cache: true` | `run: bundle install && bundle exec rake` |
| `dtolnay/rust-toolchain@stable` | `run: cargo test`(이미지에 Rust 설치) |
| `docker/build-push-action@v6` | Docker가 있는 호스트나 러너에서 `run: docker build -t app .`. 컨테이너 작업 안에는 Docker가 없습니다. |
| `codecov/codecov-action@v4` | 지우거나, 토큰을 시크릿으로 저장하고 Codecov의 명령줄 업로더를 실행하세요. |
| `actions/github-script@v7` | `run: node scripts/task.js`. OwnGit은 GitHub 토큰을 주지 않습니다. |

흔한 Node.js 작업은 그대로 실행됩니다.

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-node@v4
    with:
      node-version: 20
      cache: npm
  - run: npm ci
  - run: npm test
```

### 작업에 주어지는 것

도구가 CI에서처럼 동작하도록 OwnGit은 `CI=true`와 `GITHUB_ACTIONS=true`를 설정합니다. 흔히 쓰는 `GITHUB_*`, `RUNNER_*` 변수도 설정합니다. `GITHUB_WORKSPACE`, `GITHUB_SHA`, `GITHUB_REF`, `GITHUB_REF_NAME`, `GITHUB_REF_TYPE`, `GITHUB_HEAD_REF`, `GITHUB_BASE_REF`, `GITHUB_EVENT_NAME`, `GITHUB_EVENT_PATH`, `GITHUB_REPOSITORY`, `GITHUB_RUN_ID`, `GITHUB_RUN_NUMBER`, `GITHUB_RUN_ATTEMPT`, `GITHUB_JOB`, `GITHUB_WORKFLOW`, `RUNNER_OS`, `RUNNER_ARCH`, `RUNNER_TEMP`입니다.

OwnGit이 주지 않는 것도 있습니다.

- GitHub 토큰, GitHub API, GitHub 호스트 러너에 깔린 도구는 없습니다.
- 작업 공간에 `.git` 폴더가 없으므로 `git describe`나 `git diff --exit-code`는 동작하지 않습니다. Git LFS 파일은 포인터 파일입니다.
- 심볼릭 링크나 서브모듈을 추적하는 저장소는 체크용 파일을 아예 준비할 수 없습니다([체크가 보는 파일](AUTOMATIC_CHECKS.ko.md#체크가-보는-파일) 참고).

호스트 작업은 모든 호스트 체크가 받는 짧은 서버 변수 목록([환경 변수](AUTOMATIC_CHECKS.ko.md#환경-변수) 참고)에 위의 변수와 워크플로의 `env`를 더해 받습니다.

컨테이너에서는 단계마다 새 컨테이너가 뜹니다. 작업 공간(`/workspace`)과 작업 전용 홈, 임시, 캐시 폴더는 작업이 끝날 때까지 바뀐 내용이 남습니다. 그래서 한 단계에서 `pip install --user`를 하고 다음 단계에서 `pytest`를 실행할 수 있습니다. 그 밖의 곳에서 바꾼 내용은 단계가 끝나면 사라집니다.

## 식

`${{ }}` 식은 GitHub의 문법, 연산자, 형 변환을 따릅니다. OwnGit은 실행을 받아들일 때 모든 식을 검사합니다. 계산할 수 없는 식이 있으면 아무것도 실행하기 전에 그 작업을 거부합니다. 모르는 값을 빈 문자열로 바꾸지 않습니다.

- 함수: `success()`, `failure()`, `always()`, `cancelled()`, `contains()`, `startsWith()`, `endsWith()`, `format()`, `join()`, `toJSON()`, `fromJSON()`
- 컨텍스트: `github`, `inputs`, `matrix`, `strategy`, `needs.<id>.result`, `env`, `secrets`, `runner`, `steps`, `job.status`. GitHub가 허용하는 키에서만 쓸 수 있습니다.
- `github`에서 쓸 수 있는 값: `sha`, `ref`, `ref_name`, `ref_type`, `head_ref`, `base_ref`, `event_name`, `event.action`, `event.inputs`, `event.number`, `event.pull_request`(`number`, `head.ref`, `head.sha`, `base.ref`, `base.sha`), `repository`, `actor`, `triggering_actor`, `run_id`, `run_number`, `run_attempt`, `workflow`, `job`, 그리고 단계 안에서는 `workspace`
- 풀 리퀘스트는 병합 커밋이 아니라 풀 리퀘스트의 소스 커밋으로 실행합니다. `github.sha`는 그 커밋이고 `github.ref`는 `refs/pull/<number>/head`, `github.ref_name`은 `<number>/head`입니다. `GITHUB_REF`와 `GITHUB_REF_NAME`도 같은 값을 담습니다. 브랜치 필터는 여전히 대상 브랜치와 비교합니다.
- `steps.<id>.outcome`은 원래 결과를 그대로 담습니다. `continue-on-error`로 허용한 실패 단계의 `conclusion`은 `success`입니다.
- `continue-on-error`가 있는 작업이 실패하면 `needs.<id>.result`는 `success`이며 매트릭스의 다른 조합도 취소하지 않습니다.

식에는 한도가 있습니다. 식 길이 4 KiB, 중첩 32단계, 계산하며 만드는 값마다 64 KiB, 계산 단계 10,000번입니다.

## 시크릿

시크릿은 배포 토큰처럼 워크플로가 `${{ secrets.NAME }}`으로 읽는 값입니다. 관리자가 저장소마다 저장합니다.

```sh
owngit workflow-secret set \
  --server https://git.example.test \
  --repository project \
  --password-file ./admin-password \
  --name DEPLOY_TOKEN \
  --value-file ./deploy-token
```

- `--value-file`은 내 계정만 읽을 수 있는 파일이어야 합니다. 표준 입력에서 읽으려면 대신 `--value-stdin`을 씁니다. 값을 명령 인수로 넘기는 방법은 없습니다.
- `owngit workflow-secret list`는 이름과 바꾼 시각만 보여 주고 값은 보여 주지 않습니다. 하나를 지우려면 `owngit workflow-secret remove --name NAME`을 씁니다. 브라우저에서는 워크플로나 자동 체크 화면의 워크플로 시크릿 링크를 열면 같은 일을 할 수 있습니다.
- 셋 다 관리자 비밀번호가 필요합니다. MCP로 연결한 코딩 도구는 시크릿을 읽거나 바꿀 수 없습니다.
- 이름에는 영문자, 숫자, 밑줄만 쓰고 숫자나 `GITHUB_`로 시작할 수 없습니다. 대소문자는 구분하지 않습니다. 저장소마다 시크릿을 100개까지, 하나에 48 KiB까지 저장합니다.

시크릿은 `env`로 명령에 넘기세요. 그러면 값이 스크립트 본문에 들어가지 않습니다.

```yaml
  - run: ./deploy.sh
    env:
      DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
```

시크릿은 `secrets.NAME`처럼 이름을 직접 씁니다. `if`에는 시크릿을 쓸 수 없습니다. 시크릿이 있는지 확인하려면 `env`에 넣고 그 변수를 확인하세요. 예: `if: env.DEPLOY_TOKEN != ''`. 작업은 워크플로에 이름이 적힌 시크릿만 받습니다. 어떤 이벤트로 시작한 실행이든 마찬가지입니다. 이름은 적혔지만 저장되지 않은 시크릿은 빈 값이 되며 작업에 그 사실이 남습니다.

### 시크릿이 막지 못하는 것

- 저장소에 푸시하거나 워크플로를 직접 실행할 수 있는 사람은 시크릿을 읽는 코드를 실행할 수 있습니다. 열린 접근이면 OwnGit에 접속할 수 있는 누구나 해당합니다.
- OwnGit은 로그와 결과에서 각 시크릿의 정확한 텍스트와 단계가 `::add-mask::`로 등록한 값을 가립니다. 바꾸거나 인코딩한 값은 가리지 못합니다. 여러 줄 시크릿에서 8바이트보다 짧은 줄도 따로 가리지 않습니다. 워크플로가 시크릿을 다른 곳으로 보내는 것은 가리기로 막을 수 없습니다.
- 호스트 작업은 샌드박스가 아닙니다. OwnGit 계정이 읽을 수 있는 파일은 다른 자격 증명을 포함해 모두 읽을 수 있습니다.
- 시크릿은 상태 디렉터리의 `workflow-secrets/`에 저장소마다 파일 하나로 암호화하지 않고 저장합니다. OwnGit을 실행하는 계정만 읽을 수 있습니다. 데이터베이스나 백업에는 들어가지 않으므로, 복원한 뒤나 새 컴퓨터에서는 다시 입력해야 합니다.
- 시크릿 파일을 읽을 수 없으면 작업은 시작하지 않고 그 사실을 남깁니다. 빈 값으로 대신 실행하는 일은 없습니다.

## 워크플로 직접 실행하기

`on: workflow_dispatch`가 있는 워크플로는 일반 접근 권한이 있는 누구나 실행할 수 있습니다. 체크 탭의 워크플로 실행 양식, 명령줄, MCP로 연결한 코딩 도구에서 실행합니다. 정책이 `workflow_dispatch`를 허용해야 합니다.

```sh
owngit workflow dispatch \
  --server https://git.example.test \
  --repository project \
  --path .github/workflows/deploy.yml \
  --ref main \
  --input environment_name=staging \
  --expected-oid 0123456789abcdef0123456789abcdef01234567
```

- `--ref`에는 브랜치를 주며 생략하면 기본 브랜치입니다. 해당 저장소와 브랜치의 현재 커밋에 대해 받아들인 푸시 기록이 남아 있어야 합니다. 없으면 HTTP 409와 `note.push_required`로 거부합니다.
- OwnGit이 실행을 준비하는 동안 브랜치가 바뀌면 HTTP 409와 `workflow.moved`로 거부합니다. 브랜치를 다시 확인한 뒤 실행하세요.
- `--expected-oid`를 주면 그사이 브랜치가 다른 커밋으로 바뀌었을 때 `workflow.moved`로 거부합니다. 워크플로 실행 양식은 늘 이 값을 보내므로, 양식에 보였던 커밋이 실행됩니다.
- `--input name=value`는 선언된 입력 하나를 정하며 입력마다 한 번씩 씁니다. OwnGit은 그 커밋의 파일을 기준으로 `required`, `type`(`string`, `boolean`, `choice`, `number`), `options`를 확인합니다. 선언하지 않았거나 올바르지 않은 입력은 `workflow.dispatch_input`으로 거부합니다.
- 열린 접근이 아니면 공용 비밀번호가 든 `--password-file`을 붙입니다. 열린 접근일 때 워크플로 실행 양식은 OwnGit에 접속할 수 있는 누구나 워크플로를 실행할 수 있고 그 워크플로가 저장소의 시크릿을 읽을 수 있다고 경고합니다.

## 예약 실행

`on.schedule`은 cron 시각에 맞춰 워크플로를 UTC 기준으로 실행합니다. OwnGit은 GitHub처럼 기본 브랜치에 있는 워크플로 파일에서 일정을 읽습니다.

- cron은 다섯 필드로 씁니다. 숫자, 목록, 범위, 간격, 월과 요일 이름을 쓸 수 있고 일요일은 `0`이나 `7`입니다. 날짜 필드 두 개를 모두 제한하면 둘 중 하나만 맞아도 됩니다. 파일 하나에 일정은 10개까지입니다.
- `timezone`이 있으면 파일을 거부합니다. 시각을 UTC로 바꿔 쓰세요.
- 기본 브랜치의 최신 커밋이 받아들인 푸시로 들어왔을 때만 예약 실행이 시작됩니다. 가져오기나 복원 뒤, 또는 그 푸시 기록이 더는 남아 있지 않으면 다음에 받아들인 푸시가 올 때까지 기다립니다. 워크플로 화면에도 그 사실이 나옵니다.
- 한 일정의 실행은 적어도 5분 간격으로 시작합니다. 그보다 자주 실행하라는 cron이면 그 사이 시각은 건너뛰며 실행 기록에 그 사실이 남습니다.
- OwnGit이 꺼져 있었거나 그 일정의 이전 실행이 아직 끝나지 않아 놓친 시각은 한 번만 실행합니다. 이 실행에는 원래 예정이던 시각이 적힙니다.
- 예약 실행의 결과를 알 수 없게 되면([결과를 알 수 없는 작업](#결과를-알-수-없는-작업) 참고) 누군가 그 실행을 다시 실행하거나 워크플로를 직접 실행할 때까지 그 일정은 멈춥니다.
- 이벤트를 허용하거나, 워크플로를 켜거나, 정책을 다시 승인해 예약 실행을 켜면 각 일정은 다음 시각부터 시작합니다. 꺼져 있던 동안의 시각은 실행하지 않습니다.
- 대기열이 차 있으면 그 예약 실행은 실행하지 않음으로 기록되고 그 시각은 지나간 것으로 칩니다.
- 2월 30일처럼 어떤 날짜와도 맞지 않는 cron은 거부합니다. 다음 날짜가 5년보다 먼 2월 29일 일정은 예정 시각에 한 번 실행한 뒤, 그 날짜가 5년 안으로 들어오면 다시 돌아옵니다.

## 실행, 취소, 다시 실행

체크 탭에서 실행 기록을 열면 워크플로 실행 목록이 최신순으로 나옵니다. 실행 페이지는 작업과 단계를 결과, 안내와 함께 보여 줍니다. 작업 페이지는 단계마다 명령과 가린 출력 발췌(단계마다 최대 8 KiB), 원본 로그 링크를 보여 줍니다.

명령줄에서는 일반 접근으로 다음을 씁니다.

```sh
owngit workflow-run list   --server https://git.example.test --repository project
owngit workflow-run show   ... --run RUN_ID
owngit workflow-run show   ... --run RUN_ID --job JOB_ID
owngit workflow-run log    ... --run RUN_ID --job JOB_ID
owngit workflow-run cancel ... --run RUN_ID
owngit workflow-run rerun  ... --run RUN_ID
```

`...`는 같은 `--server`, `--repository` 플래그이고, 공용 비밀번호가 필요하면 `--password-file`도 붙입니다. `list`는 최근 실행 50개를 보여 주며 `--limit`에는 1부터 999까지 줄 수 있습니다. JSON으로 받으려면 `--json`을 붙입니다.

### 취소

일반 접근 권한이 있으면 다른 사람의 배포를 포함해 어떤 실행이든 취소할 수 있습니다. 아직 시작하지 않은 작업은 바로 취소됩니다. 실행 중인 작업은 되도록 빨리 멈추고 그 뒤 단계는 `if: always()` 단계를 포함해 실행되지 않습니다. 취소된 작업이 필요한 작업은 시작하지 않습니다.

### 다시 실행

다시 실행하면 같은 커밋에서 같은 이벤트와 같은 직접 실행 입력으로 워크플로 전체를 다시 돌립니다. 워크플로 파일은 지금 정책과 한도로 다시 읽습니다. 같은 실행의 다시 실행이 아직 끝나지 않았으면 다시 요청해도 그 실행을 돌려줍니다. 대기열이 찼거나 바뀐 파일 목록을 읽지 못해 실행하지 않음으로 기록된 실행도 같은 방법으로 다시 실행할 수 있습니다. 작업 하나만 다시 실행할 수는 없습니다.

### 결과

실행은 결과 하나로 끝나며 저장소 페이지와 풀 리퀘스트, 작업 목록이 모두 같은 규칙을 씁니다.

| 결과 | API 값 | 뜻 |
| --- | --- | --- |
| 실패 | `failed` | 작업 하나가 실패했습니다. 나중에 통과한 작업이 이를 가리지 않습니다. |
| 완료되지 않음 | `incomplete` | 오류, 시간이나 출력 한도, 도구나 셸 없음, 결과를 알 수 없는 실행 때문에 작업을 끝내지 못했습니다. |
| 취소됨 | `cancelled` | 작업이 취소되었습니다. |
| 일부만 실행 | `partial` | 일부 작업이 거부되었고 나머지는 실패하지 않았습니다. 통과가 아닙니다. |
| 통과 | `passed` | 실행된 작업이 모두 통과했습니다. `continue-on-error`가 있는 작업의 실패는 통과로 세고 따로 표시합니다. |
| 건너뜀 | `skipped` | 실행된 것이 없습니다. 모든 작업을 건너뛰었거나 기본 제공 단계만 있었습니다. 통과가 아닙니다. |
| 실행하지 않음 | `not_run` | 실행을 기록했지만 시작하지 않았습니다. 이유가 함께 나옵니다. |
| OwnGit에서 지원하지 않음 | `refused` | 파일에서 실행할 수 있는 것이 없습니다. 이 결과는 중립입니다. |

작업이 기다리거나 실행 중일 때 결과는 `queued`나 `running`입니다.

작업 공간에서 추적 중인 파일을 바꾼 작업은 단계가 모두 통과해도 완료되지 않음으로 끝납니다. 그 결과가 해당 커밋의 근거로 쓰이기 때문입니다. 바뀐 사실은 그 작업의 마지막 `run` 단계에 나옵니다.

리비전 하나의 결과는 워크플로 파일마다 가장 최근 실행과 `.owngit/checks.json`의 결과를 합쳐 정합니다. 어디서든 실패가 있으면 실패입니다. OwnGit이 전혀 실행할 수 없는 파일은 목록에는 나오지만 리비전을 실패로도 통과로도 만들지 않습니다.

### 결과를 알 수 없는 작업

작업이 시작된 뒤 OwnGit이 멈추거나 러너와 연결이 끊기면 그 작업은 `ambiguous`가 됩니다. 명령이 이미 실행되었을 수 있으므로 OwnGit은 그 작업을 스스로 다시 시작하지 않습니다.

- 그 작업이 직접 또는 다른 작업을 거쳐 필요한 작업은 `if: always()`나 `if: failure()`가 있어도 모두 건너뜁니다. 그 작업과 상관없는 작업은 그대로 실행됩니다.
- 실행은 완료되지 않음으로 끝납니다.
- 그런 작업이 있는 예약 실행의 일정은 누군가 그 실행을 다시 실행하거나 워크플로를 직접 실행할 때까지 멈춥니다.

다시 실행해도 안전할 때 다시 실행하세요.

## 이전 러너

OwnGit 1.1.7 이하의 외부 러너는 워크플로 작업을 실행하지 못합니다. 이런 러너는 `.owngit/checks.json` 작업만 계속 맡습니다. 워크플로 작업은 러너를 업데이트하라는 안내와 함께 기다립니다. 러너 컴퓨터의 `owngit`을 업데이트하고 러너를 다시 시작하세요.

지금 버전의 러너는 작업이 시작될 때 한 번, 러너의 HTTPS 연결로 작업 계획과 워크플로에 이름이 적힌 시크릿을 받습니다. 이 응답을 잃어버리면 작업은 두 번 실행되지 않고 결과를 알 수 없는 상태가 됩니다.

## GitHub와 다른 점

차이는 대부분 위에 적은 거부입니다. 다음은 OwnGit이 받아들인 워크플로의 동작이 달라지는 경우입니다.

- 추적 중인 파일을 바꾼 작업은 단계가 모두 통과해도 완료되지 않음으로 끝납니다.
- 필요한 작업의 실행 결과를 알 수 없으면 `if: always()`나 `if: failure()`가 있어도 그 작업을 건너뜁니다.
- 풀 리퀘스트 실행을 다시 실행하면 풀 리퀘스트 이벤트와 그 동작(action)을 그대로 씁니다.
- 가져오기와 복원은 실행을 시작하지 않습니다. `push`, `pull_request`, `workflow_dispatch`, `schedule`로 워크플로를 실행할 권한은 OwnGit이 받아들인 푸시에만 있습니다.
- `paths`에 쓸 바뀐 파일 목록을 끝까지 읽지 못하면 실행하지 않음으로 기록합니다. GitHub는 이때 실행합니다.
- 풀 리퀘스트는 병합 커밋이 아니라 소스 커밋으로 실행합니다.
- 취소되거나 작업 시간 한도에 걸리면 `if: always()` 단계를 포함해 그 뒤 단계는 실행되지 않습니다.
- 작업 사이에 출력값(`needs.<id>.outputs`)을 넘기지 않으며 작업 공간에 `.git` 폴더가 없습니다.
- `runs-on`은 실행할 컴퓨터를 고르지 않으며 `actions/setup-*`는 도구를 설치하지 않습니다.
- 예약 실행은 UTC로만 하고 5분에 한 번까지만 시작합니다.
- 로그는 단계가 아니라 작업마다 남습니다. 작업의 원본 로그는 앞부분과 끝부분을 합쳐 256 KiB까지 남으므로, 뒤 단계가 많이 출력하면 앞서 실패한 단계의 출력이 잘릴 수 있습니다. 단계마다 8 KiB짜리 발췌는 따로 남습니다.

## 한도

| 항목 | 한도 |
| --- | --- |
| 커밋마다 읽는 워크플로 파일 | 32개, 하나에 128 KiB, 모두 합쳐 1 MiB, 이름 100바이트까지 |
| 실행 하나의 작업 | 매트릭스 조합 포함 16개 |
| 작업 하나의 단계 | 50개 |
| `run` 스크립트 | 적힌 그대로 24,000바이트 |
| 식 | 길이 4 KiB, 중첩 32단계, 값마다 64 KiB, 계산 10,000단계 |
| 단계마다 환경 변수 | 200개, 값 하나에 48 KiB, 모두 합쳐 256 KiB |
| 단계마다 `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT` | 각각 64 KiB |
| `::add-mask::` 값 | 작업마다 256개, 하나에 8 KiB |
| `paths`에 쓰는 바뀐 파일 | 3,000개, 이름 1 MiB, 10초 |
| 직접 실행 입력 | 25개, 값 하나에 1 KiB |
| 일정 | 파일마다 10개 |
| 시크릿 | 저장소마다 100개, 하나에 48 KiB |

실행은 모든 작업과 함께 정책의 `queue_limit` 안에 들어가야 합니다. 지금 들어가지 않는 실행은 실행하지 않음으로 기록되므로 나중에 다시 실행하세요. `queue_limit`보다 많은 작업이 필요한 워크플로는 아예 시작할 수 없고 워크플로 화면이 이벤트가 오기 전에 미리 알려 줍니다.

## 참고: 명령, API, MCP

모든 명령은 `--server`와 `--repository`를 받으며 `--json`을 붙이면 JSON으로 출력합니다. 일반 접근 명령의 `--password-file`에는 공용 비밀번호를, 시크릿 명령에는 관리자 비밀번호를 넣습니다. 공유 링크로는 이 명령을 쓸 수 없습니다.

| 할 일 | 명령 | API(`/api/v1/repositories/{repository}` 아래) | MCP 도구 |
| --- | --- | --- | --- |
| 브랜치의 워크플로 파일 목록 보기, 파일 보기 | `owngit workflow list [--ref]`, `owngit workflow show --path [--ref]` | `GET /workflows?ref=BRANCH[&path=FILE]` | `workflow_list`, `workflow_show` |
| 워크플로 실행 | `owngit workflow dispatch --path [--ref] [--input k=v] [--expected-oid]` | `POST /workflows/dispatch` | `workflow_dispatch` |
| 실행 목록 | `owngit workflow-run list [--limit]` | `GET /workflow-runs?limit=N` | `workflow_run_list` |
| 실행이나 작업 하나 보기 | `owngit workflow-run show --run [--job]` | `GET /workflow-runs/{run}`, `GET /workflow-runs/{run}/jobs/{job}` | `workflow_run_show` |
| 작업의 원본 로그 읽기 | `owngit workflow-run log --run --job` | `GET /workflow-runs/{run}/jobs/{job}/log` | `workflow_job_log` |
| 실행 취소, 다시 실행 | `owngit workflow-run cancel --run`, `rerun --run` | `POST /workflow-runs/{run}/cancel`, `POST /workflow-runs/{run}/rerun` | `workflow_run_cancel`, `workflow_run_rerun` |
| 시크릿 관리(관리자) | `owngit workflow-secret list`, `set --name (--value-file \| --value-stdin)`, `remove --name` | `GET /workflow-secrets`, `PUT`, `DELETE /workflow-secrets/{name}` | 없음 |
| 워크플로 켜기(관리자) | `run_workflows`를 넣은 `owngit check-policy set --enable` | `POST /check-policy/save-and-enable` | 없음 |

API로 값을 바꾸는 요청은 `Content-Type: application/json`과 JSON 객체를 보냅니다. 직접 실행에는 `path`를 넣고 필요하면 `ref`, `expected_oid`, `inputs` 객체를 넣습니다. 취소, 다시 실행, 시크릿 삭제에는 `{}`가 필요합니다. 시크릿 설정은 관리자 연결로 `{"value":"YOUR_SECRET_VALUE"}`를 보냅니다. 배열, `null`, 단일 값, 모르는 필드는 거부합니다. 체크 정책의 `enable`과 `disable`은 기존 빈 본문도 받습니다. 새 연동은 `{}`를 쓰세요.

워크플로 메시지는 `code`, `detail`과, 값이 있으면 `path`, `line`, 구조화된 `args`를 담습니다. API, CLI, MCP는 기록된 영어 `detail`을 그대로 돌려줍니다. 대시보드는 아는 메시지에 필요한 인수가 모두 있으면 영어와 한국어로 표시합니다. 모르는 코드이거나 필요한 인수가 없으면 `detail`을 그대로 보여 줍니다. 이 문장을 분석해 인수를 추측하거나 성공으로 해석하지 마세요.

실행 응답에는 작업과 단계 결과만 있고 출력 발췌는 없습니다. 작업 응답에는 그 작업의 단계 명령과 발췌가 더해집니다. 작업 목록과 리비전 응답에서 `workflows_total`은 실행이 있는 워크플로 파일 수 전체이며 앞의 22개만 담았을 때 `workflows_truncated`가 true입니다.

# 운영 안내

<p align="center"><a href="OPERATIONS.md">English</a> | <b>한국어</b></p>

셀프 호스팅 Git 서버인 OwnGit을 설치하고 운영하는 사람을 위한 안내입니다. 설치, 처음 설정, 서비스로 실행하기, 설정 화면, 다른 기기에서 접속하기, 설치 호스트에서 복구하기, 서버를 지키는 한도를 다룹니다.

명령은 `owngit`으로 적습니다. 압축 파일을 풀었다면 `./owngit`, 소스에서 빌드했다면 `./bin/owngit`으로 실행하세요([Build and run](../CONTRIBUTING.md#build-and-run) 참고).

다른 작업은 따로 안내합니다.

- 저장소 관리, 이름 바꾸기, 공유 링크, 삭제, 다른 Git 호스트에서 가져오기: [저장소](REPOSITORIES.ko.md)
- 저장 공간, 백업, 백업 확인, 복원: [백업](BACKUPS.ko.md)
- 명령줄 풀 리퀘스트, 코딩 도구가 실행하는 체크: [코딩 도구](CODING_TOOLS.ko.md)
- 푸시 뒤 자동 체크와 체크 원본 로그: [자동 체크](AUTOMATIC_CHECKS.ko.md)

## 한 줄 설치

설치 스크립트는 최신 릴리스를 내려받아 SHA-256을 확인하고 설치한 뒤 서비스로 실행합니다. 중간에 묻는 것은 없습니다. Linux와 macOS에서는 다음 명령을 실행합니다.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

Windows에서는 PowerShell에서 실행합니다.

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

터미널에서 실행하면 마지막에 설정 링크가 나옵니다. 다시 실행해도 상태와 저장소는 그대로 남습니다.

프로그램은 `~/.local/bin/owngit`(root라면 `/usr/local/bin/owngit`)에 설치됩니다. Windows에서는 `%LOCALAPPDATA%\Programs\OwnGit` 아래에 릴리스마다 폴더를 따로 만듭니다. 설치 스크립트는 PATH를 바꾸지 않습니다. 그 폴더가 PATH에 없으면 실행 방법을 알려 줍니다.

| Linux와 macOS | Windows | 하는 일 |
| --- | --- | --- |
| `--version 1.1.3` | `-Version 1.1.3` | 최신 릴리스 대신 그 릴리스를 설치합니다. |
| `--no-service` | `-NoService` | 프로그램만 설치합니다. `owngit serve`나 `owngit service install`로 시작하세요. |
| `--to PATH` | `-Dir FOLDER` | 다른 경로나 폴더에 설치합니다. |

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh -s -- --version 1.1.3 --no-service
```

```powershell
& ([scriptblock]::Create((irm -MaximumRedirection 0 https://owngit.app/install.ps1))) -Version 1.1.3 -NoService
```

설치 스크립트는 다음 경우에 아무것도 내려받기 전에 멈춥니다.

- 프로그램 경로가 npm의 `owngit` 같은 심볼릭 링크일 때. 그 설치는 원래 방법으로 업데이트하거나 `--to`로 다른 경로를 고르세요.
- 다른 계정이 프로그램 폴더, 그 위의 폴더, 임시 폴더를 바꿀 수 있을 때. 나만(또는 root나 Windows 관리자만) 바꿀 수 있는 폴더를 고르세요.

### 실행 전에 스크립트 확인하기

한 줄 명령은 스크립트를 확인하지 않고 바로 실행합니다. `SHA256SUMS`는 압축 파일만 다루고 같은 릴리스에서 나오므로 독립된 서명이 아닙니다. 스크립트를 직접 확인하려면 다음과 같이 합니다.

1. [GitHub Releases](https://github.com/juliankang4/owngit/releases)의 릴리스에서 `install.sh`, `install.ps1`, `proxmox.sh` 중 필요한 파일을 내려받습니다.
2. 그 파일의 SHA-256을 릴리스의 `manifest.json`과 비교하거나, 릴리스 태그의 `packaging/installer/`와 비교합니다.
3. 내용을 읽은 뒤 `/bin/sh install.sh`나 `& .\install.ps1`로 실행합니다. `proxmox.sh`는 Proxmox VE 호스트에서 root로 `/bin/sh proxmox.sh`를 실행합니다.

### 다른 설치 방법

어느 방법이든 호스트에 `git-http-backend`가 포함된 Git이 있어야 합니다. Homebrew와 Arch Linux 패키지는 Git도 함께 설치합니다.

- Homebrew(Apple silicon macOS, Linux x64와 ARM64): `brew install juliankang4/tap/owngit`
- npm(Apple silicon macOS, Linux x64와 ARM64, Windows x64, Node.js 필요): `npm install -g owngit`
- Arch Linux와 Omarchy(x64, ARM64): 릴리스마다 첨부된 `PKGBUILD`로 빌드합니다.
- 위 플랫폼 모두: [GitHub Releases](https://github.com/juliankang4/owngit/releases)에서 압축 파일을 받아 `SHA256SUMS`로 확인합니다.

## 처음 설정하기

OwnGit을 포그라운드로 시작하거나 [서비스로 실행](#서비스로-실행하기)합니다.

```sh
owngit serve
```

기본 주소는 `http://127.0.0.1:7654`입니다. 설정에서는 다음을 묻습니다.

- 저장소 폴더
- 일반 접근을 OwnGit에 접속할 수 있는 누구에게나 열지, 공용 비밀번호 하나로 보호할지
- 관리 작업 전에 대시보드가 묻는 별도의 관리자 비밀번호([묻는 빈도](#관리자-비밀번호-확인))

설정을 끝내기 전에는 아무것도 저장되지 않습니다. 설정이 끝나면 빈 대시보드가 나옵니다. 새 저장소를 만들면 `http://HOST:7654/git/PROJECT.git` 형태의 클론 주소가 생깁니다.

인터넷의 공인 주소에서 설정 화면을 열면 공용 비밀번호 방식이 미리 선택되고 그 이유가 표시됩니다.

### 터미널에서 설정하기

터미널에서 `owngit serve`를 실행하면 언어를 먼저 묻고 "이 터미널에서 계속"과 "웹 대시보드 열기" 중 하나를 고르게 합니다.

- 터미널에서 계속하면 웹 화면과 같은 질문을 터미널에서 묻습니다. Ctrl-C를 누르면 OwnGit은 아무것도 저장하지 않고 멈춥니다.
- 브라우저에서는 "터미널에 승인 요청"을 누릅니다. 브라우저와 터미널에 같은 짧은 코드가 나옵니다. 내 브라우저에 그 코드가 보일 때만 터미널에서 `y`를 입력하세요. 다른 기기나 프록시를 거친 요청에는 경고가 붙습니다.

Tailscale이 실행 중이면 "다른 기기에서 접속" 단계에서 `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --accept-insecure-http` 같은 명령을 보여 줍니다. 설정이 끝난 뒤 이 명령을 실행하고 OwnGit을 다시 시작하세요. 암호화된 주소가 필요하면 [tailnet에서 HTTPS로 공유하기](#tailnet에서-https로-공유하기)를 쓰세요.

### 터미널 없이 설정하기

서비스는 브라우저를 열지 않습니다. OwnGit은 상태 디렉터리에 소유자만 읽을 수 있는 설정 파일을 만들고 링크가 아닌 그 파일의 경로만 로그에 남깁니다. 설치 호스트에서 다음 명령을 실행하면 링크를 받을 수 있습니다.

```sh
owngit setup-link
```

링크는 15분 안에 한 번만 쓸 수 있습니다. 명령을 다시 실행하면 이전 링크는 무효가 됩니다. 설정을 마치기 전에 다른 기기에서 설정하려면 그 기기에서 쓸 주소를 지정하세요. 예: `owngit setup-link --base-url http://192.168.1.20:7654`

### 화면이 없는 컴퓨터

화면이 없는 컴퓨터(컨테이너, 디스플레이 없는 SSH 세션, 화면에 로그인하지 않은 Mac)에서는 첫 시작 때 다른 기기에서 설정할 수 있도록 모든 주소(`0.0.0.0:7654`)에서 연결을 받습니다. 설정을 마칠 때까지 다른 기기에는 설정 화면만 보입니다.

- 설정 링크는 사설 주소(`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, IPv6 고유 로컬 주소)로 된 것만 출력합니다.
- 공인 주소만 있는 컴퓨터에서는 `ssh -L 7654:127.0.0.1:7654 USER@HOST` 같은 SSH 터널 명령을 대신 출력합니다. 이 명령을 내 컴퓨터에서 실행한 뒤 링크를 거기서 여세요.
- 이 컴퓨터에서 오는 연결만 받으려면 `owngit network set --listen 127.0.0.1:7654`를 실행하고 다시 시작하세요.

## 서비스로 실행하기

`owngit service install`은 OwnGit을 백그라운드에서 실행하고 멈추면 다시 시작합니다. OwnGit이 응답할 때까지 기다린 뒤 설정 링크를 출력합니다.

| 명령 | 하는 일 |
| --- | --- |
| `owngit service install` | 서비스를 설치하고 시작합니다. 프로그램을 새 버전으로 바꾼 뒤 다시 실행하면 같은 상태로 새 버전을 다시 시작합니다. |
| `owngit service status` | 실행과 응답 여부, 실행 계정, 로그, 상태 디렉터리, 주소를 보여 줍니다. |
| `owngit service start`, `stop`, `restart` | 서비스를 시작, 중지, 재시작합니다. |
| `owngit service uninstall` | 서비스를 멈추고 제거합니다. 상태 디렉터리와 저장소는 남습니다. |

다른 계정이 `owngit` 프로그램을 바꿀 수 있으면 명령은 그 프로그램을 거부하고 고칠 경로를 알려 줍니다. 프로그램을 `~/.local/bin`처럼 나나 root만 바꿀 수 있는 폴더로 옮기거나 [한 줄 설치](#한-줄-설치)를 쓰세요.

Homebrew로 설치했다면 서비스는 Homebrew가 맡습니다(`brew services restart owngit`). 로그는 `$(brew --prefix)/var/log/owngit.log`입니다.

macOS와 Windows, Homebrew에서는 서비스 로그가 파일로 남습니다. 파일이 10 MB 가까이 되면 오래된 부분은 이름 끝에 `.1`을 붙인 파일로 옮겨집니다. 옮기지 못하면 OwnGit은 지금 파일에 계속 쓰고 실패를 한 번 알린 뒤 1분마다 다시 시도합니다. 파일이 한도의 두 배가 되면 그 뒤의 줄은 서비스의 표준 오류로 갑니다(macOS에서는 `owngit.stderr.log`). Windows 서비스는 표준 오류를 버리므로 이 줄은 남지 않습니다. 실패를 알리는 안내에도 그렇게 적힙니다.

### 중지에 걸리는 시간

멈추라는 요청을 받으면 OwnGit은 하던 일을 최대 45초 동안 마무리합니다. 하는 일이 없으면 바로 멈춥니다. 진행 중인 Git 전송은 그 45초 가운데 40초쯤까지 이어질 수 있습니다. 가져오기, 체크, 백업, 유지 관리도 같은 45초 안에 끝나야 합니다.

그때까지 끝나지 않은 일이 있으면 OwnGit은 그 일을 로그에 남기고 자기가 시작한 Git, 체크, 보조 프로세스를 끝낸 뒤 종료합니다. 무엇이 중단됐는지는 다음에 시작할 때 기록합니다.

서비스 관리자는 OwnGit을 강제로 끝내기 전에 70초를 기다립니다.

- systemd 유닛(`TimeoutStopSec`)
- macOS LaunchAgent(`ExitTimeOut`). 다만 launchd는 최대 60초까지만 기다립니다. 45초는 그 안에 들어갑니다.
- Windows 작업. `owngit service` 명령이 작업을 멈추거나 다시 시작할 때 기다립니다.
- Homebrew 서비스(포뮬러의 `stop_timeout`)
- Compose 파일(`stop_grace_period`). Compose 없이 Docker를 쓴다면 [컨테이너로 실행하기](#컨테이너로-실행하기)를 보세요.

### Linux

| 명령을 실행한 곳 | 서비스 | 상태 디렉터리 | 로그 |
| --- | --- | --- | --- |
| 데스크톱 | systemd 사용자 서비스, 부팅 때 시작(lingering) | 평소 상태 디렉터리 | `journalctl --user -u owngit.service -f` |
| SSH 접속이나 그래픽 세션 없음 | 내 계정으로 실행되는 시스템 서비스, `sudo`를 한 번 요청 | 평소 상태 디렉터리 | `sudo journalctl -u owngit.service -f` |
| root | 새로 만든 `owngit` 계정으로 실행되는 시스템 서비스 | `/var/lib/owngit/state` | `sudo journalctl -u owngit.service -f` |

`sudo`를 쓸 수 없으면 관리자가 실행할 스크립트를 출력합니다.

root로 설치할 때는 다음을 지켜야 합니다.

- `--state-dir`은 `/var/lib/owngit` 안에 있어야 합니다. 프로그램은 `/usr/local/bin/owngit`처럼 root만 바꿀 수 있는 곳에 있어야 합니다.
- 다른 명령은 `/etc/owngit/state-dir`로 상태를 찾고 `owngit` 계정으로 실행됩니다. `backup --output`처럼 넘기는 파일은 그 계정이 쓸 수 있는 절대 경로여야 합니다.
- `owngit` 계정은 `/home`, `/root`, `/run/user`를 볼 수 없으므로 저장소 폴더는 그 계정 소유여야 합니다. 설정 화면이 `sudo install -d -o owngit -g owngit -m 0700 /srv/git` 같은 명령을 보여 줍니다.

### Windows

`owngit service install`은 작업 스케줄러에 `OwnGit` 작업을 등록합니다. 로그는 상태 디렉터리의 `logs\service.log`입니다.

- 관리자 계정에서 설치하면 부팅 때 시작합니다. 사용자 계정 컨트롤 승인을 한 번 받아 프로그램을 `%ProgramFiles%\OwnGit`에 복사하고 개인 네트워크용 Windows 방화벽 규칙 `OwnGit`을 추가합니다. Git이 없으면 `winget`으로 설치합니다. OwnGit 자체는 관리자 권한 없이 실행됩니다.
- 표준 계정에서 설치하면 로그인할 때 시작합니다. 방화벽 규칙은 추가하지 않습니다.

새로 설치한 Windows에서는 누군가 화면에서 처음 로그인할 때까지 부팅 작업이 "Queued" 상태로 남습니다.

업데이트하려면 새 릴리스를 `%ProgramFiles%\OwnGit` 밖에 설치하고 그 `owngit service install`을 실행하세요. 데스크톱이 있는 컴퓨터에서는 두 번째 작업 `OwnGit icon`이 [OwnGit 아이콘](#owngit-아이콘과-알림)을 시작합니다.

### macOS

`owngit service install`은 LaunchAgent `~/Library/LaunchAgents/app.owngit.server.plist`를 만듭니다. launchd가 로그인할 때(자동 로그인이면 재시동 직후) OwnGit을 시작하고 멈추면 다시 시작합니다.

- 상태는 `~/Library/Application Support/owngit`에 있습니다.
- 로그는 `~/Library/Logs/owngit`에 있습니다([macOS의 로그 파일](#macos의-로그-파일)).
- 시스템 설정의 일반, 로그인 항목 및 확장 프로그램에서 OwnGit을 끄면 에이전트가 시작되지 않습니다.
- `npm update -g owngit` 뒤에는 `owngit service install`을 다시 실행하세요.

### macOS의 로그 파일

로그 폴더는 `~/Library/Logs/owngit`입니다. Homebrew로 설치했다면 `$(brew --prefix)/var/log`입니다.

- `owngit.log`는 서버 로그입니다. 10 MB 가까이 되면 오래된 부분이 `owngit.log.1`로 옮겨집니다([옮기지 못할 때](#서비스로-실행하기)).
- `owngit.stderr.log`에는 충돌 출력처럼 서버 로그에 담지 못한 내용만 남습니다.
- `.owngit-staging`은 OwnGit이 새 로그 파일을 만드는 빈 숨김 폴더입니다. 그대로 두세요.

OwnGit의 로그 파일은 생기는 순간부터 내 계정만 읽을 수 있습니다. OwnGit은 내 계정만 열 수 있는 `.owngit-staging`에서 새 로그 파일을 만들고 거기서 접근 목록(ACL)을 지운 다음 제 이름으로 옮깁니다. `owngit.log`를 비공개로 만들지 못하면 시작하지 않고 `chmod -N PATH` 같은 해결 명령을 알려 줍니다.

다른 계정이 읽을 수 있는 로그 파일이나 이전 버전의 OwnGit이 쓴 로그 파일은 내용이 같은 비공개 사본으로 한 번 바뀝니다. `owngit.log`와 `owngit.log.1`은 OwnGit이 시작할 때, LaunchAgent의 `owngit.stderr.log`는 `owngit service install`을 실행할 때 바뀝니다. 바뀌기 전에 옛 파일을 열어 둔 프로그램은 그 뒤로 새 줄을 보지 못합니다.

물려받은 접근 목록은 OwnGit이 소유한 로그 폴더에서만 지웁니다. `~/Library/Logs/owngit`과 OwnGit이 로그를 위해 만든 폴더가 여기에 해당합니다. `$(brew --prefix)/var/log`처럼 다른 프로그램과 함께 쓰는 폴더는 그대로 둡니다. 이런 폴더의 접근 목록이 다른 계정에게 새 파일을 읽도록 허용하면 로그 첫 줄에 그 사실이 나옵니다. OwnGit의 로그 파일은 계속 비공개입니다. 하지만 폴더의 다른 파일은 다른 계정도 읽을 수 있습니다. 폴더의 접근 목록을 지우려면 다음을 실행하세요.

```sh
chmod -N "$(brew --prefix)/var/log"
```

한 OwnGit이 로그를 쓰고 있는 폴더에 두 번째 OwnGit이 로그를 쓰면 두 번째 OwnGit은 그 폴더의 파일을 바꾸지 않고 로그 첫 줄에 이유를 남깁니다.

Homebrew 서비스가 쓰는 `owngit.stderr.log`는 OwnGit이 건드리지 않습니다. `$(brew --prefix)/var/log`의 파일을 다른 계정이 읽을 수 있다면 그 파일을 직접 비공개로 바꾸세요.

```sh
chmod -N "$(brew --prefix)/var/log/owngit.stderr.log"
chmod 600 "$(brew --prefix)/var/log/owngit.stderr.log"
```

### 서비스 계정이 할 수 있는 일

서비스와 푸시된 커밋으로 실행되는 체크는 그 계정이 할 수 있는 일을 모두 할 수 있습니다. 다른 사람도 푸시할 수 있다면 Linux에서 root로 설치해 별도의 `owngit` 계정으로 실행하세요.

Linux 시스템 서비스에는 systemd 보안 설정이 걸립니다. 서비스가 시작한 프로그램은 권한을 높일 수 없어 체크에서 `sudo`를 쓸 수 없습니다. `/usr`, `/boot`, `/efi`, `/etc`는 읽기 전용이고 새 파일은 서비스 계정만 읽을 수 있습니다.

### 상태 확인

`GET /healthz`는 OwnGit이 HTTP를 제공하는 동안 `200 OK`로 응답합니다. 다른 기기의 모니터링 도구는 OwnGit이 받아들이는 호스트 이름을 써야 합니다([호스트 이름](#호스트-이름)).

이 컴퓨터에서 `owngit health`는 이 상태 디렉터리의 서버가 응답하고 자신이 그 서버임을 증명할 때만 `OwnGit answers at http://ADDRESS`를 출력하고 0으로 끝납니다. 서버는 시작할 때마다 새 키를 상태 디렉터리의 `health-run.json`에 씁니다. 이 파일은 서버 계정만 읽을 수 있고, 서버가 멈추면 지워집니다. `owngit health`는 매번 새 확인값을 보내고 그 키로 만든 응답만 받아들입니다. 그래서 같은 주소에서 다른 프로그램이 응답해도 통과하지 못합니다. `owngit service install`, `start`, `restart`도 서비스가 실행 중이라고 알리기 전에 같은 방식으로 확인합니다.

`owngit health`는 상태 디렉터리를 읽기만 하고 아무것도 쓰지 않습니다. 따라서 상태 디렉터리가 읽기 전용이어도 동작합니다. OwnGit을 실행하는 계정으로 실행하세요. 이 파일은 백업에 들어가지 않습니다.

서버를 확인하지 못하면 0이 아닌 값으로 끝나고 이유를 알려 줍니다.

| 메시지 | 뜻 |
| --- | --- |
| `it published no health key; restart OwnGit` | 실행 중인 서버가 1.1.5보다 먼저 시작했거나 키를 쓰지 못했습니다. OwnGit을 한 번 다시 시작하세요. |
| `another program answers at http://ADDRESS, or OwnGit restarted` | 응답에 올바른 증명이 없습니다. 다른 프로그램이 그 주소를 쓰고 있거나, 확인하는 사이에 OwnGit이 다시 시작했을 수 있습니다. 다시 실행해 보고, 같은 메시지가 나오면 그 주소에서 듣고 있는 프로그램을 찾으세요. |
| `OwnGit is still starting` | 잠시 뒤에 다시 실행하세요. |
| `OwnGit is not running` | 이 상태 디렉터리로 실행 중인 서버가 없습니다. |
| `another program holds the state directory` | 어떤 프로그램이 이 상태 디렉터리로 실행 중인지 OwnGit이 알 수 없습니다. |

## 점검

`owngit doctor`는 이 컴퓨터의 상태 디렉터리를 쓰는 OwnGit 설치를 점검하고 문제마다 고치는 명령 하나를 함께 출력합니다. `--json`을 붙이면 JSON으로 출력합니다. 설정 화면의 일반 탭에서도 관리자로 확인한 브라우저에 같은 점검 결과를 보여 줍니다.

점검하는 항목은 다음과 같습니다.

- 서버가 실행 중이고 응답하는지, 설정을 마쳤는지
- 다른 계정이 저장소 폴더나 각 저장소의 폴더를 바꿀 수 있는지
- 각 저장소에서 OwnGit이 관리하는 `hooks` 항목
- Windows에서 Administrators 그룹이 소유한 폴더(고치는 명령: `owngit service install`)
- OwnGit이 다른 기기의 접속을 받을 때 이 컴퓨터의 방화벽

OwnGit은 고치는 명령을 스스로 실행하지 않습니다. root 권한이 있어야 읽을 수 있는 ufw나 firewalld 규칙처럼 확인하지 못한 항목은 "Could not check" 아래에 나옵니다.

## 업데이트와 제거

`owngit update`는 OwnGit을 설치한 방법을 알아내 업데이트 명령을 출력합니다. 실행할 때 GitHub에 최신 릴리스를 묻습니다. `--json`을 붙이면 JSON으로 출력합니다. 이 명령도 대시보드도 직접 설치하지는 않습니다.

| 설치 방법 | 업데이트 | 프로그램 제거 |
| --- | --- | --- |
| Homebrew | `brew upgrade owngit` | `brew uninstall owngit` |
| npm | `npm install -g owngit@X.Y.Z` | `npm uninstall -g owngit` |
| Arch Linux 패키지 `owngit-bin` | 새 릴리스의 `PKGBUILD`로 `makepkg -si` | `sudo pacman -R owngit-bin` |
| 압축 파일이나 한 줄 설치 | 새 릴리스의 설치 스크립트로 이 프로그램 갱신 | 프로그램 파일 삭제 |

컨테이너는 [컨테이너로 실행하기](#컨테이너로-실행하기)를 보세요.

내 계정의 서비스가 이 프로그램을 실행하고 있으면 출력된 명령이 `owngit service install`까지 실행해 새 버전으로 서비스를 다시 시작합니다. 서비스가 없으면 OwnGit을 직접 다시 시작하세요.

Homebrew에서 `brew upgrade owngit` 뒤에 `owngit service install`을 실행하면, 상태를 먼저 열지 않고 바로 `brew services restart owngit`에 재시작을 맡깁니다. 새 버전은 상태를 백업한 뒤 업그레이드합니다.

`owngit uninstall`은 서비스를 제거하고 프로그램을 지우는 방법을 알려 줍니다. 상태 디렉터리와 저장소는 지우지 않고 위치만 알려 줍니다. 나중에 다시 설치하면 그대로 이어서 씁니다.

### 백업 버전

업그레이드 뒤 이전 버전으로 돌아갈 때처럼 예전 OwnGit으로 백업을 복원하려면 먼저 아래 내용을 확인하세요. 복원 절차는 [백업](BACKUPS.ko.md)에 있습니다.

- OwnGit은 백업 버전 1, 2, 9, 10, 11을 복원하고 그 밖의 버전은 거부합니다.
- 새 백업은 버전 10으로 만들어지며, OwnGit 1.0.3부터 1.1.2까지 복원할 수 있습니다. OwnGit 기록이 64 MiB를 넘거나 버전 10에 담을 수 없는 기록이 있으면 버전 11로 만들어지고, 이때는 1.1.3 이상이 필요합니다. OwnGit 1.1.3 이후 만들거나 고친 풀 리퀘스트와 리뷰, 이름을 바꾼 저장소의 이름, 저장소별 ref 설정, 가져오기 새로고침 설정, 체크 컨테이너 옵션이 그런 기록입니다.
- OwnGit 1.1.3은 브랜치나 태그 이름에 유니코드 공백 문자가 든 백업, 거부되거나 중단된 병합 기록이 풀 리퀘스트의 이후 병합과 다른 백업, 1.1.3이 받아들이지 않는 시간 순서의 체크 기록이 든 백업을 거부합니다. 컴퓨터 시계를 되돌린 뒤 이런 순서가 생길 수 있습니다. 이런 백업은 1.1.4 이상으로 복원하세요.

## 컨테이너로 실행하기

Docker Engine과 Docker Compose가 있는 Linux에서 [`compose.yaml`](../packaging/container/compose.yaml)을 새 폴더에 저장한 뒤 그 폴더에서 다음을 실행합니다.

```sh
docker compose up -d
docker compose exec -it owngit owngit setup-link
```

이미지는 x64와 ARM64용 `ghcr.io/juliankang4/owngit`이고 태그는 `X.Y.Z`, `X.Y`, `latest`입니다. `owngit` 계정(ID 10001)으로 실행되며 특별한 권한이 필요 없습니다.

링크 `http://localhost:7654/setup#...`은 15분 안에 여세요. 기본값인 열린 접근으로 설정한다면 `http://nas.local:7654/setup#...`처럼 컴퓨터 이름이나 주소로 설정을 마치세요. 열린 접근인 컨테이너는 설정 뒤 컨테이너 밖에서 오는 `localhost`를 거부합니다. 이 컴퓨터와 다른 기기를 주소로 구분할 수 없기 때문입니다. 나중에 다른 이름을 허용하려면 다음을 실행합니다.

```sh
docker compose exec owngit owngit network set --allowed-host nas.local
docker compose restart
```

이 컴퓨터에서만 쓰려면 포트를 `"127.0.0.1:7654:7654"`로 게시하세요. 이때 `localhost`는 공용 비밀번호를 쓸 때만 동작합니다.

상태, 저장소, 업그레이드 전 백업은 모두 볼륨 `owngit-data`의 `/data`에 있습니다.

- `docker compose down`은 볼륨을 남깁니다. **`docker compose down -v`는 볼륨을 저장소와 함께 지웁니다.**
- `/data`는 로컬 디스크에 두세요. 저장소를 네트워크 공유에 두려면 공유를 `/repositories` 같은 다른 경로에 마운트하고 설정에서 그 폴더를 고르세요.
- 다른 계정으로 실행하려면 `compose.yaml`에 `user:`를 지정하세요. 그리고 그 계정이 소유하고 다른 계정은 바꿀 수 없는 폴더를 마운트하세요.

`compose.yaml`은 OwnGit이 멈출 때까지 70초를 기다립니다([중지에 걸리는 시간](#중지에-걸리는-시간)). Docker의 기본 대기 시간은 10초라서 Git 전송 중에 멈추면 OwnGit이 정리를 마치기 전에 끝나 버릴 수 있습니다. Compose 없이 이미지를 실행한다면 `docker run --stop-timeout 70`으로 시작하거나 `docker stop -t 70`으로 멈추세요.

업데이트는 `compose.yaml`이 있는 폴더에서 `docker compose pull && docker compose up -d`를 실행합니다. OwnGit은 상태를 업그레이드하기 전에 백업합니다. 먼저 직접 백업하려면 다음과 같이 합니다.

```sh
docker compose stop
docker compose run --rm owngit owngit backup --output /data/backup-before-update
docker compose pull
docker compose up -d
docker compose exec owngit owngit backup verify /data/backup-before-update
```

볼륨 안의 백업은 볼륨과 함께 지워집니다. 다른 곳에 두는 방법은 [백업](BACKUPS.ko.md)에 있습니다.

## Proxmox VE에서 실행하기

Proxmox VE 호스트에서 다음 명령을 실행하면 비특권 Debian 13 컨테이너(LXC)를 만들고 그 안에 OwnGit을 서비스로 설치합니다. 호스트 셸에서 root로 실행하세요.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh
```

컨테이너 이름은 `owngit`이고 비어 있는 다음 ID를 씁니다. 코어 2개, 메모리 1024 MB, `local-lvm`(없으면 `local-zfs`)의 8 GB 디스크, `vmbr0`의 DHCP로 만들어지며 호스트와 함께 시작합니다. 스크립트는 컨테이너 주소와 설정 링크를 출력합니다. 중간에 실패하면 만든 컨테이너를 지웁니다. 이미 있는 컨테이너는 바꾸지 않습니다.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh -s -- --repositories /tank/owngit
```

| 옵션 | 하는 일 |
| --- | --- |
| `--id N`, `--hostname NAME` | 컨테이너 ID와 이름 |
| `--storage NAME`, `--disk GB`, `--cores N`, `--memory MB` | 디스크 저장소와 크기, CPU 코어, 메모리 |
| `--bridge NAME`, `--ip ADDRESS/PREFIX`, `--gateway ADDRESS` | 네트워크 브리지, 고정 IPv4 주소와 게이트웨이 |
| `--repositories FOLDER` | 저장소를 호스트 폴더에 둡니다. |
| `--template VOLUME` | 이미 있는 Debian 13 템플릿을 씁니다. |
| `--version X.Y.Z` | 그 릴리스(1.1.3 이상)를 설치합니다. |

`--repositories` 폴더는 새 폴더이거나 비어 있어야 하고 링크를 거쳐서도 안 됩니다. 그 폴더와 위의 모든 폴더는 root 소유여야 하며 다른 계정이 쓸 수 없어야 합니다(`chown root:root FOLDER && chmod go-w FOLDER`). Proxmox 백업(`vzdump`)에는 이 폴더가 들어가지 않으므로 [OwnGit 백업](BACKUPS.ko.md)이나 호스트 자체 백업으로 따로 백업하세요.

컨테이너 안의 명령은 스크립트가 출력한 ID와 프로그램 전체 경로로 실행합니다.

```sh
pct exec 105 -- /usr/local/bin/owngit service status
```

업데이트하려면 `pct exec 105 -- /usr/local/bin/owngit update`를 실행한 다음 출력된 명령을 컨테이너 셸(`pct enter 105`)에서 실행하세요. `pct destroy 105`는 컨테이너를 그 안의 상태, 저장소와 함께 지웁니다. 먼저 백업하세요.

## 설정 화면

대시보드 사이드바에서 여는 설정 화면에는 탭이 다섯 개 있습니다.

| 탭 | 내용 |
| --- | --- |
| 일반(`/settings`) | 이 브라우저의 언어, 화면 모드, 목록 순서, [새 릴리스 확인](#새-릴리스-알림), [점검](#점검), [OwnGit 아이콘](#owngit-아이콘과-알림) |
| 접근 권한(`/settings/access`) | 열린 접근이나 공용 비밀번호, [로그인 유지 시간](#로그인-유지-시간), [다른 사이트의 링크](#다른-사이트의-링크), [로그인 시도 한도](#로그인-시도-한도), 관리자 비밀번호와 [묻는 빈도](#관리자-비밀번호-확인) |
| 네트워크(`/settings/network`) | [네트워크 설정](#네트워크-설정), [tailnet 공유](#tailnet에서-https로-공유하기) |
| 저장소(`/settings/repositories`) | 기본 브랜치, 기록 보관, 삭제 확인([저장소](REPOSITORIES.ko.md)), [Git 전송 제한](#git-전송-제한), [보기 한도](#보기-한도), [체크 상한](#체크-상한) |
| 보관과 복구(`/settings/storage`) | 저장소 폴더, 백업, 유지 관리([백업](BACKUPS.ko.md)), 체크 원본 로그([자동 체크](AUTOMATIC_CHECKS.ko.md)) |

부분마다 저장 버튼이 따로 있습니다. 이 브라우저에서 관리자 확인이 아직 유효하지 않으면 저장할 때 관리자 비밀번호를 묻습니다. 네트워크 설정은 다음 시작 때, 나머지는 저장하는 즉시 적용됩니다. 공용 비밀번호를 켜거나 바꾸면 그 비밀번호로 로그인한 사람은 모두 로그아웃됩니다.

### 명령줄에서 설정 바꾸기

`owngit settings`는 관리자 API로 일반, 접근 권한, 저장소, 보관과 복구 탭의 설정을 바꿉니다. 명령마다 `--server`와 관리자 비밀번호가 든 `--password-file`이 필요하며 결과는 JSON으로 출력합니다.

```sh
owngit settings show --server http://127.0.0.1:7654 --accept-insecure-http --password-file /path/to/admin-password
owngit settings set --server http://127.0.0.1:7654 --accept-insecure-http --password-file /path/to/admin-password --update-check off
```

- `settings set`은 옵션으로 지정한 설정만 바꿉니다. 옵션 목록은 `owngit settings set --help`로 볼 수 있습니다.
- `settings access --mode password`나 `--mode open`은 일반 접근 방식을 바꿉니다. `settings admin-password`는 관리자 비밀번호를 바꿉니다. `settings confirmation --choice CHOICE`는 [대시보드가 관리자 비밀번호를 묻는 빈도](#관리자-비밀번호-확인)를 정합니다.
- 새 비밀번호는 소유자만 읽을 수 있는 파일(`--access-password-file`, `--new-password-file`)에서 읽거나, 입력이 보이지 않는 프롬프트에서 두 번 묻습니다. 명령줄에 비밀번호를 적는 방법은 없습니다.
- `--accept-insecure-http`는 그 명령 한 번에 한해 암호화되지 않은 HTTP를 허용합니다. `https://` 주소에는 붙이지 마세요.

네트워크 설정, tailnet 공유, 아이콘은 설치 호스트에서 실행하는 별도 명령 `owngit network`, `owngit tailscale`, `owngit tray`로 바꿉니다.

`settings set`과 `settings confirmation`으로 바꾸는 설정은 이 호스트에 속하고 백업에 들어가지 않습니다. 복원한 설치는 기본값으로 시작합니다. 접근 방식과 두 비밀번호는 백업에 들어갑니다.

### 로그인 유지 시간

공용 비밀번호 로그인은 기본으로 12시간 유지됩니다. 접근 권한 탭에서 1시간, 8시간, 12시간, 1일, 7일, 30일 중에 고르거나 `owngit settings set --session 7d`를 실행하세요. 새 시간은 이후 로그인부터 적용됩니다. 지금 모든 로그인을 끝내려면 공용 비밀번호를 바꾸세요.

### 다른 사이트의 링크

기본값에서는 채팅, 웹메일, 다른 사이트에서 연 OwnGit 링크에 공용 비밀번호 로그인이 적용되지 않습니다. OwnGit 안에서 다시 열면 로그인 상태로 보입니다. 이런 링크에서도 로그인을 유지하려면 접근 권한 탭에서 "로그인 유지"를 고르거나 `owngit settings set --cross-site-links lax`를 실행하세요(기본값은 `strict`). 관리자 확인은 이런 링크에서 절대 유지되지 않습니다.

### 로그인 시도 한도

기본값에서는 한 주소에서 10분 안에 비밀번호를 4번 틀리면 그 주소를 15분 동안 막습니다. 그동안은 맞는 비밀번호도 받지 않으며 시간이 지나면 저절로 풀립니다. 공용 비밀번호와 관리자 비밀번호는 따로 셉니다. 대시보드, Git, API 모두에 적용됩니다.

```sh
owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m
```

횟수는 1에서 100까지, 집계 시간과 차단 시간은 1분에서 24시간까지 정할 수 있습니다. 한도를 끌 수는 없습니다. 막힌 동안 Git과 API는 `Retry-After`와 함께 HTTP 429를 받습니다.

신뢰하지 않은 프록시 뒤에서는 모두가 프록시 주소로 들어오므로 함께 막힙니다. 먼저 [프록시를 신뢰하도록](#리버스-프록시-뒤에서-운영하기) 설정하세요.

### 관리자 비밀번호 확인

접근 권한 탭의 "관리자 비밀번호 묻기"는 대시보드가 관리 작업 전에 비밀번호를 언제 물을지 정합니다.

- 매번 묻기
- 30분(기본값), 1시간, 8시간, 하루, 7일, 30일 뒤에 다시 묻기. 비밀번호를 입력한 때부터 세며 이 브라우저에만 적용됩니다. 사이드바에 확인이 끝나는 시각이 나오며 "종료"를 누르면 바로 끝납니다.
- 묻지 않기. 대시보드를 열 수 있는 사람은 누구나 설정을 바꾸고, 저장소를 지우고, 자격 증명을 발급할 수 있습니다. 이때는 모든 페이지에 "관리자 비밀번호 확인 꺼짐"이 표시됩니다.

명령줄에서는 `owngit settings confirmation --choice every`처럼 `every`, `30m`, `1h`, `8h`, `1d`, `7d`, `30d`, `never` 중 하나를 지정합니다. `never`에는 `--acknowledge-no-ask`도 필요합니다. 명령줄과 API는 선택과 관계없이 항상 비밀번호를 묻습니다.

더 이상 접근할 수 없는 브라우저의 관리자 확인을 끝내려면 관리자 비밀번호를 바꾸거나, 설치 호스트에서 `owngit reset-admin`을 실행하세요.

## OwnGit 아이콘과 알림

데스크톱이 있는 컴퓨터에서는 OwnGit 아이콘이 macOS 메뉴 막대, Windows 알림 영역, Linux 데스크톱 패널에 나타납니다. 아이콘 패널에는 실행 여부, 클론 주소, 최근 푸시 세 건이 보입니다. 손볼 일이 있으면 실행할 명령도 보입니다. 아이콘을 숨기거나 종료해도 OwnGit은 멈추지 않습니다.

- `owngit tray off`는 `owngit tray on`을 실행할 때까지 아이콘을 숨깁니다. `owngit tray status`는 아이콘이 보이는지, 안 보인다면 왜인지 알려 줍니다. 설정 화면의 일반 탭에도 같은 스위치가 있습니다.
- macOS: 아이콘은 프로그램 옆에 설치되는 OwnGit.app입니다. `owngit service install`이 앱을 열고 로그인할 때 열리도록 등록합니다.
- Windows: `OwnGit icon` 작업이 로그인할 때 아이콘을 시작합니다. 클릭하면 대시보드가, 오른쪽 클릭하면 패널이 열립니다.
- Linux: 데스크톱의 터미널에서 `owngit service install`을 실행하세요. 이 명령이 `~/.config/autostart/owngit-icon.desktop`을 만듭니다. 데스크톱이 StatusNotifierItem 아이콘을 보여 줘야 하고(Omarchy, AppIndicator 확장을 켠 GNOME 48에서 확인), GTK 4가 포함된 `gjs`가 필요합니다. 화면이 작으면 패널의 제목, 상태 줄, 대시보드 열기, 숨기기, 종료 버튼은 그대로 보이고 그 사이 내용만 스크롤됩니다. 데스크톱의 패널 프로그램이 30초 안에 응답하지 않으면 `owngit tray icon`이 이유를 알리고 시작을 포기합니다. 시작하는 도중에 멈춘 아이콘은 정상적으로 끝납니다.

Linux의 root 설치와 화면이 없는 컴퓨터에는 아이콘이 없습니다.

아이콘은 푸시, 새 풀 리퀘스트, 실패한 체크, 정상적으로 끝나지 않은 가져오기와 백업, 새 OwnGit 버전을 데스크톱 알림으로 알려 줍니다. 종류마다 패널이나 `owngit tray notifications`로 끌 수 있습니다.

```sh
owngit tray notifications push off
owngit tray notifications only_others on
```

`only_others`를 켜면 이 컴퓨터에서 한 푸시, 풀 리퀘스트, 가져오기는 알리지 않습니다. 다른 상태 표시줄이나 스크립트는 `owngit tray read --json`으로 패널 내용을 읽고 `owngit tray open --json`으로 대시보드를 열 수 있습니다.

알림은 편의 기능입니다. 알림이 떴든 안 떴든 푸시, 체크, 백업 결과는 모두 대시보드에 남습니다. 알림을 클릭하면 그 알림이 가리키는 페이지가 열립니다. 여러 푸시를 묶은 알림이 한 저장소의 것이면, 클릭할 때 마지막으로 푸시된 브랜치나 태그의 커밋 목록이 열립니다. 그 마지막 푸시가 브랜치나 태그를 지웠다면 저장소 페이지가 열립니다.

- Windows: 한꺼번에 생긴 알림은 약 10초 간격으로 하나씩 뜹니다(패널이 열려 있으면 5초). 한 번에 네 개 이상이면 "OwnGit 새 알림 4개"처럼 개수를 알려 주는 알림 하나로 뜨고, 이 알림을 클릭하면 대시보드가 열립니다. 알림이 화면에서 사라진 뒤에는 클릭해도 아무 페이지도 열리지 않습니다.
- Linux: 데스크톱에 알림 서비스가 없으면 패널의 알림 항목과 아이콘 메뉴에 그 사실이 표시됩니다. OwnGit은 최대 5분까지 간격을 늘려 가며 다시 시도하고, 이유는 알림이 다시 표시될 때까지 로그에 한 번만 남깁니다. 알림 서비스가 시작되면 기다리던 알림이 중복 없이 한 번씩 뜹니다.

## 다른 기기에서 서버에 접속하기

OwnGit은 암호화되지 않은 HTTP로 응답합니다. 다른 기기에서 접속하는 방법을 하나 고르세요.

- 암호화: [tailnet에서 HTTPS로 공유](#tailnet에서-https로-공유하기)하거나 [리버스 프록시 뒤에](#리버스-프록시-뒤에서-운영하기) 둡니다.
- 암호화되지 않은 HTTP: Tailscale, 직접 운영하는 VPN, LAN에서 OwnGit이 [네트워크 주소에서 연결을 받게](#네트워크-설정) 합니다. 이 방식은 한 번만 승인하면 되며 페이지 머리글에 연결이 암호화되었는지 항상 표시됩니다.

**OwnGit을 인터넷에 공개하지 마세요.** 공개 주소를 따로 가질 수 있는 것은 공유 링크뿐입니다([저장소](REPOSITORIES.ko.md)).

### 이 컴퓨터의 방화벽

`owngit doctor`가 이 컴퓨터에서 무엇을 허용해야 하는지 알려 줍니다.

- Windows: 관리자 계정에서 `owngit service install`을 실행하면 개인 네트워크용 규칙 `OwnGit`이 추가됩니다. Windows 설정에서 집 네트워크를 개인 네트워크로 지정하세요.
- macOS: 애플리케이션 방화벽이 켜져 있으면 macOS가 물을 때 OwnGit을 허용하세요.
- Linux: ufw나 firewalld가 켜져 있으면 사설 네트워크에서 오는 접속만 그 포트로 허용하세요. 예: `sudo ufw allow from 192.168.1.0/24 to any port 7654 proto tcp`

### 호스트 이름

OwnGit은 Host가 승인된 이름이거나, 이 컴퓨터에서 온 `localhost`, `127.0.0.1`, `::1`인 요청에만 응답합니다. 다른 이름에는 "unrecognized host"로 답합니다. 이름을 승인하려면 설치 호스트에서 다음을 실행하고 OwnGit을 다시 시작하세요.

```sh
owngit approve-host gitbox.internal
```

### 네트워크 설정

설치 호스트에서 주소를 저장합니다. 다음 시작 때 적용됩니다.

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal --accept-insecure-http
owngit network show
```

| 옵션 | 정하는 것 |
| --- | --- |
| `--listen HOST:PORT` | OwnGit이 연결을 받는 주소. `0.0.0.0`이나 `::`는 모든 인터페이스입니다. 기본값은 `127.0.0.1:7654`입니다. |
| `--accept-insecure-http` | 다른 기기가 접속하는 주소에 암호화되지 않은 HTTP를 허용합니다. 한 번만 필요하며 없으면 `set`이 그런 주소를 거부합니다. |
| `--base-url URL` | 다른 기기가 쓰는 주소. 클론 주소에 표시됩니다. |
| `--allowed-host`, `--remove-allowed-host` | 승인된 Host 이름 |
| `--trusted-proxy`, `--remove-trusted-proxy` | 전달 헤더를 믿을 리버스 프록시 |

`--base-url=`처럼 빈 값을 주면 저장된 값을 지웁니다. `owngit serve`에 준 옵션은 그 실행에서 저장된 값보다 우선하므로, 서비스 정의에는 `--listen`과 `--base-url`을 넣지 마세요. `owngit network show`와 네트워크 탭에서 저장된 값, 실행 중인 값, 재시작이 필요한지를 볼 수 있습니다.

저장된 주소 때문에 접속할 수 없게 되면 설치 호스트에서 초기화하고 다시 시작하세요.

```sh
owngit network reset
```

`reset`은 listen 주소, base URL, 공유 링크용 공개 주소를 지웁니다. `--clear-allowed-hosts`나 `--clear-trusted-proxies`를 붙이면 그것도 지웁니다. 네트워크 설정은 백업에 들어가지 않습니다.

### tailnet에서 HTTPS로 공유하기

OwnGit을 실행하는 컴퓨터에서 Tailscale이 돌고 있으면 이 컴퓨터의 Tailscale 이름으로 들어오는 HTTPS를 Tailscale이 받아 OwnGit에 넘기게 할 수 있습니다. 그러면 tailnet의 기기는 `https://NAME.TAILNET.ts.net/`을 열고 `https://NAME.TAILNET.ts.net/git/project.git`에서 클론합니다. tailnet 밖의 기기는 접속할 수 없습니다.

필요한 것은 다음과 같습니다.

- 이 컴퓨터에 로그인된 Tailscale 1.50 이상
- Tailscale 관리 콘솔의 DNS 페이지에서 켠 MagicDNS와 HTTPS Certificates
- Linux에서는 `sudo tailscale set --operator=$USER`를 한 번 실행

네트워크 탭에서 "내 tailnet에 OwnGit 공유"를 켜거나, 설치 호스트에서 다음 명령을 씁니다.

```sh
owngit tailscale on
owngit tailscale status
owngit tailscale off
```

설정 화면에서 켜면 바로 적용됩니다. 명령줄에서 켰다면 OwnGit을 다시 시작해야 합니다.

- 네트워크 탭에서는 Tailscale이 멈춰 있거나, 로그아웃됐거나, 시작하는 중이어도 공유를 끌 수 있습니다. OwnGit은 자기가 만든 주소를 지워 보고 Tailscale이 거부하면 아무것도 바꾸지 않은 채 화면에 Tailscale의 답을 보여 줍니다. 공유를 켜려면 Tailscale이 실행 중이어야 합니다.
- 공유 변경은 한 번에 하나씩만 진행됩니다. 다른 변경이 진행 중일 때 시작한 변경은 정해진 시간 동안 기다립니다. 그때까지 다른 변경이 끝나지 않으면 OwnGit이 그렇다고 알리고 아무것도 바꾸지 않습니다. 잠시 뒤에 다시 시도하세요.
- Tailscale은 443 포트로 HTTPS를 제공하고 443이 사용 중이면 8443이나 10000을 씁니다. 포트를 직접 고르려면 HTTPS 포트에서 "직접 지정"을 고르거나 `owngit tailscale on --https-port 8443`을 실행하세요. 예전 주소를 쓰던 클론은 `git remote set-url origin https://NAME.TAILNET.ts.net:8443/git/project.git`으로 바꿔야 합니다.
- OwnGit은 다른 서비스가 쓰는 포트를, 사용자가 내용을 확인하고 바꾸기로 할 때까지 건드리지 않습니다. `owngit tailscale status`가 그 포트의 내용과 바꾸는 명령을 보여 줍니다.
- "홈 네트워크에서도 허용 (암호화되지 않음)"을 체크하거나 `--home-network`를 붙이지 않으면 홈 네트워크에는 열지 않습니다.
- 처음 HTTPS로 접속할 때는 Tailscale이 인증서를 받는 동안 1분쯤 걸릴 수 있습니다.
- 인증서를 받으면 `gitbox.tail0000.ts.net`처럼 이 컴퓨터와 tailnet의 이름이 공개 Certificate Transparency 로그에 남습니다. 남는 것은 이름뿐이고 내용은 남지 않습니다.
- OwnGit은 Tailscale Funnel을 켜지 않으며 Funnel을 거쳐 온 요청은 모두 거부합니다.
- Tailscale에서 컴퓨터 이름을 바꿨다면 공유를 다시 켜세요.
- macOS용 Tailscale 앱은 누군가 로그인해 있을 때만 실행됩니다. 자동 로그인을 켜거나 Homebrew의 `tailscaled`를 쓰세요.

### 다른 비공개 네트워크

NetBird, Headscale, WireGuard에서는 그 네트워크에서 이 컴퓨터가 쓰는 주소에서 OwnGit이 연결을 받게 하고 다시 시작합니다.

```sh
owngit network set --listen 100.64.0.7:7654 --base-url http://gitbox.netbird.selfhosted:7654 --accept-insecure-http
```

이 네트워크들은 트래픽을 암호화하지만 OwnGit은 그것을 알 수 없어 암호화되지 않은 HTTP로 표시합니다. HTTPS 주소가 필요하면 아래처럼 이 컴퓨터에서 리버스 프록시를 실행하세요. tailnet 공유는 Tailscale에서만 됩니다.

### 리버스 프록시 뒤에서 운영하기

리버스 프록시를 쓰면 `https://git.example.internal`처럼 전용 호스트 이름의 루트에 HTTPS 주소를 둘 수 있습니다. 다른 사이트 아래의 경로에는 둘 수 없습니다. 주소와 프록시를 저장한 뒤 OwnGit을 다시 시작하세요.

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
```

`--trusted-proxy`에는 프록시가 접속해 오는 주소를 적습니다. 이 컴퓨터의 프록시면 `127.0.0.1`, 이 컴퓨터의 Docker 안이면 `172.18.0.0/16` 같은 Docker 네트워크 대역, 다른 컴퓨터면 그 컴퓨터의 주소입니다. 프록시를 신뢰하기 전에는 한 기기가 비밀번호를 틀려도 모든 기기가 막히고 HTTPS로 보낸 양식이 실패합니다.

- 신뢰하는 대역은 작게 잡으세요. IPv4는 `/8`, IPv6는 `/32`보다 넓은 대역은 거부합니다. `127.0.0.1`을 신뢰하면 `ssh -L` 터널처럼 이 컴퓨터에서 요청을 전달하는 모든 프로그램도 신뢰하게 됩니다.
- 프록시가 이 컴퓨터에 있으면 OwnGit을 `127.0.0.1:7654`에 두어 다른 기기가 프록시를 거치지 않고 들어오지 못하게 하세요.
- 프록시는 원래 Host를 넘기고 `X-Forwarded-Proto`를 설정해야 합니다. 클라이언트 주소도 `X-Forwarded-For`에 추가해야 합니다. 클라이언트가 보낸 `X-Forwarded-For`를 그대로 넘기면 안 됩니다.
- 프록시의 본문 크기 한도와 시간 제한은 OwnGit의 [Git 전송 제한](#git-전송-제한)(기본 4 GB, 30분) 이상이어야 합니다.

Caddy는 따로 설정하지 않아도 위 조건을 모두 지킵니다. `tls internal`은 Caddy의 로컬 인증 기관으로 인증서에 서명하므로, 각 기기가 그 인증 기관을 신뢰해야 합니다.

```caddyfile
git.example.internal {
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

nginx는 다음과 같습니다.

```nginx
server {
    listen 443 ssl;
    server_name git.example.internal;
    ssl_certificate     /etc/ssl/git.example.internal.crt;
    ssl_certificate_key /etc/ssl/git.example.internal.key;

    client_max_body_size 4g;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_read_timeout 30m;
    proxy_send_timeout 30m;

    location / {
        proxy_pass http://127.0.0.1:7654;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Host "";
    }
}
```

다른 프록시는 다음을 확인하세요.

- Traefik은 기본으로 60초가 지나면 요청 읽기를 멈추므로 긴 푸시가 끊깁니다. 엔트리 포인트의 `transport.respondingTimeouts` 아래에 `readTimeout: 30m`을 설정하세요.
- Nginx Proxy Manager: "Trust Upstream Forwarded Proto Headers"는 끈 채로 둡니다. 그리고 Custom Nginx Configuration에 `client_max_body_size 4g;`, `proxy_request_buffering off;`, `proxy_read_timeout 30m;`, `proxy_send_timeout 30m;`, `set_real_ip_from 127.0.0.1;`을 추가하세요. `proxy_http_version`은 넣지 마세요.

## 새 릴리스 알림

설정을 마치면 OwnGit은 하루에 한 번 GitHub에 새 릴리스가 있는지 묻습니다. 새 릴리스가 있으면 대시보드에 알림을 띄웁니다. 관리자로 확인한 브라우저에는 이 설치를 업데이트하는 명령도 보여 줍니다. OwnGit이 직접 내려받거나 설치하지는 않습니다.

확인은 `https://api.github.com/repos/juliankang4/owngit/releases/latest`에 보내는 HTTPS 요청 하나입니다. 저장소 데이터는 보내지 않으며 GitHub에는 서버 주소가 보입니다. 일반 탭이나 `owngit settings set --update-check off`로 끌 수 있습니다. 아예 확인하지 않게 하려면 다음과 같이 시작합니다.

```sh
owngit serve --no-update-check
```

## 설치 호스트에서 복구하기

OwnGit에는 이메일이나 계정 복구 기능이 없습니다. 아래 두 방법 모두 설치 호스트에서 실행합니다.

설정을 마치기 전이라면 새 설정 링크를 발급합니다.

```sh
owngit setup-link --no-open
```

관리자 비밀번호를 잊었다면 새 비밀번호를 소유자만 읽을 수 있는 파일에 넣고([비밀번호 파일과 토큰 파일](#비밀번호-파일과-토큰-파일) 참고) 다음을 실행합니다.

```sh
owngit reset-admin --password-file /path/to/owner-only-password-file
```

모든 브라우저의 관리자 확인이 끝나며 저장소는 바뀌지 않습니다.

### 비밀번호 파일과 토큰 파일

비밀번호나 토큰 파일을 읽는 명령(`reset-admin`, `settings`, `import`, `pr`, `repo` 등)은 내 계정만 읽을 수 있는 일반 파일만 받습니다. 명령줄 값으로 비밀번호를 받지 않습니다. 파일에는 비밀번호를 한 줄로 적습니다. 파일을 거부할 때는 그 파일을 읽을 수 있는 다른 계정과 고치는 명령을 알려 줍니다.

macOS, Linux와 그 밖의 Unix에서는 파일이 다음 조건을 모두 갖춰야 합니다.

- 내 계정이나 root가 소유한 파일
- 소유자만 접근할 수 있는 파일(그룹과 다른 사용자에게는 권한 없음)
- 폴더, 파이프, 장치가 아닌 일반 파일
- 토큰 파일과 가져오기 인증 파일은 1 MiB 이하

OwnGit은 파일을 한 번 열고, 열린 파일을 검사한 뒤 그대로 읽습니다. 그래서 검사와 읽기 사이에 파일을 바꿔치기할 수 없습니다. 심볼릭 링크는 따라가며, 링크가 가리키는 파일도 같은 조건을 갖춰야 합니다.

`umask 077` 상태에서 파일을 만들거나 `chmod 600 FILE`로 고치세요. macOS에서는 `chmod -N FILE`로 접근 목록(ACL)도 지우세요.

Windows에서 메모장이나 `echo`로 만든 파일은 폴더의 권한을 물려받습니다. PowerShell에서 파일을 만들고 내 계정만 접근하게 한 다음 비밀번호를 쓰세요.

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

Windows PowerShell 5.1과 PowerShell 7에서 모두 동작합니다. 비밀번호는 화면과 명령 기록에 남지 않습니다.

## Git 전송 제한

Git 요청(클론, 페치, 푸시, 압축 파일)마다 다음 제한이 걸립니다. 저장소 탭의 "Git 전송"에서 바꾸거나 `owngit settings set`을 쓰세요.

| 제한 | 기본값 | 범위 | 옵션 |
|---|---|---|---|
| 한 방향 최대 전송량 | 4 GB | 1 MB ~ 64 GB | `--transfer-size` |
| 최대 전송 시간 | 30분 | 1분 ~ 24시간 | `--transfer-time` |
| 클라이언트가 아무것도 보내지 않을 때 끊는 시간 | 1분 | 10초 ~ 1시간 | `--transfer-idle` |
| 저장소당 동시 전송 | 4 | 1 ~ 32 | `--transfer-per-repository` |
| 전송이 없는 저장소용 추가 자리 | 1 | 0 ~ 32 | `--transfer-extra-slots` |
| 빈 자리를 기다리는 시간 | 90초 | 5초 ~ 10분 | `--transfer-queue` |

다른 전송이 저장소를 쓰고 있으면 요청은 그 저장소가 풀리기를 기다리기도 합니다. 이렇게 기다린 시간도 최대 전송 시간에 들어갑니다. 다만 푸시, 클론, 페치가 저장소를 기다리는 시간은 빈 자리를 기다리는 시간(`--transfer-queue`)을 넘지 않습니다. 그때까지 저장소가 풀리지 않으면 아래의 503 응답을 받습니다. 이 시간보다 오래 걸리는 전송이 있다면 `--transfer-queue`를 늘리세요. 그래야 다른 푸시와 페치가 실패하지 않고 기다립니다.

클라이언트에서는 다음과 같이 보입니다.

- 크기 제한을 넘는 푸시는 HTTP 413을 받습니다. Git은 이를 `fatal: the remote end hung up unexpectedly`로만 보여 줄 수도 있습니다. 제한을 넘는 클론과 페치는 중간에 끊깁니다.
- 기다리는 시간 안에 빈 자리나 저장소를 얻지 못한 요청은 HTTP 503 `Git service is busy with other transfers; try again shortly`를 받습니다. 데이터가 오가기 전에 거부되면 Git은 `remote: Git service is busy with other transfers; try again shortly`와 `fatal: unable to access '...': The requested URL returned error: 503`을 보여 줍니다. 데이터를 주고받는 단계에서 거부되면 `error: RPC failed; HTTP 503`만 보입니다. 어느 쪽이든 명령을 다시 실행하세요.
- OwnGit은 Git LFS를 제공하지 않습니다. 기록이 크기 제한보다 큰 저장소는 클론할 수 없으니, 큰 바이너리 파일은 Git 기록에 넣지 마세요.

가져오기가 저장소에 `objects/pack/pack-<hash>.keep` 파일을 남길 때가 있습니다. 가져오기가 끝난 뒤에도 다른 Git 작업이 저장소를 계속 쓰고 있었거나, 가져오기 도중 OwnGit이 멈춘 경우입니다. 앞의 경우에는 로그에 그 파일 경로가 남습니다. 이 파일이 있는 동안 유지 관리는 그 팩을 다시 묶지 않습니다. 실행 중인 가져오기가 없을 때 이 파일을 지우세요.

### 메모리가 작은 Linux 호스트

Linux에서는 OwnGit이 컴퓨터나 컨테이너가 허용하는 메모리를 읽고 그 안에 맞춰 일합니다. macOS와 Windows에서는 이 값을 읽지 못하므로 저장된 제한을 그대로 씁니다.

- OwnGit 자신의 메모리는 한도의 절반 근처로 유지합니다. `GOMEMLIMIT`을 정하면 OwnGit의 이 값만 바뀌고 Git에는 영향이 없습니다.
- 저장된 제한이 더 크더라도 동시에 도는 Git 전송은 메모리 한도가 512 MiB면 3개쯤, 1 GiB면 6개쯤으로 줄어듭니다. 팩을 만드는 클론, 페치, 압축 파일 내려받기는 512 MiB면 한 번에 1개, 1 GiB면 2개만 실행됩니다. 푸시는 이 제한을 받지 않습니다. 이 수를 넘는 요청은 빈 자리를 기다리는 시간만큼 기다린 뒤 위의 503 응답을 받습니다. 설정 화면에는 저장된 값이 그대로 보입니다.
- 메모리 한도가 512 MiB면 약 16 MiB, 1 GiB면 약 32 MiB보다 큰 파일은 새 델타 압축 없이 저장됩니다. 그래서 이런 파일은 새 버전마다 압축된 전체 크기만큼 저장소, 백업, 클론에서 공간을 차지합니다.
- 대시보드는 이 크기보다 큰 파일을 보여 주지 않습니다. 페이지에 "이 파일은 너무 커서 여기에 보여 줄 수 없습니다. 저장소를 클론해서 받으세요."가 나오고, 원본 내려받기도 같은 메시지로 거부됩니다.

메모리 한도를 찾으면 OwnGit은 시작 로그에 그 크기를 남깁니다.

## 보기 한도

보기 한도는 파일, diff, 풀 리퀘스트 비교를 보여 줄 때 한 페이지가 읽는 양을 제한합니다. 저장소 탭의 "보기 한도"에서 바꾸거나 `owngit settings set`을 쓰세요.

| 한도 | 기본값 | 범위 | 옵션 |
|---|---|---|---|
| 원본 파일 내려받기 | 10 MB | 1 MB ~ 256 MB | `--browse-raw` |
| 파일 보기 | 2 MB | 64 KB ~ 64 MB | `--browse-file` |
| 커밋 페이지의 diff | 2 MB | 64 KB ~ 64 MB | `--browse-commit-diff` |
| 파일 하나의 diff 페이지 | 8 MB | 64 KB ~ 64 MB | `--browse-file-diff` |
| 커밋 페이지 안의 파일 하나 | 256 KB | 16 KB ~ 16 MB | `--browse-commit-file` |
| 풀 리퀘스트 비교 | 8 MB | 64 KB ~ 64 MB | `--browse-compare` |
| 비교 시간 | 20초 | 5초 ~ 1분 | `--browse-compare-time` |

- 한도보다 큰 원본 파일은 브라우저에서 내려받을 수 없습니다. 저장소를 클론해서 받으세요.
- 파일 보기는 한도 안에 드는 부분만 보여 줍니다. diff가 너무 커서 커밋 페이지에 못 들어가는 파일은 따로 diff 페이지 링크를 보여 줍니다.
- 한 페이지에는 파일이나 diff는 10,000줄까지, 폴더는 1,000개 항목까지 보이며, "첫 페이지"와 "다음 페이지" 링크로 넘깁니다.
- `owngit pr diff`, API, MCP 도구는 기본값과 같은 고정 한도를 따로 씁니다.

## 체크 상한

체크 상한은 저장소의 [체크 정책](AUTOMATIC_CHECKS.ko.md)이 요구할 수 있는 양에 이 컴퓨터가 두는 상한입니다. 저장소 탭의 "체크 상한"에서 바꾸거나 `owngit settings set`을 쓰세요.

| 상한 | 기본값 | 범위 | 옵션 |
|---|---|---|---|
| 체크 하나의 시간 | 24시간 | 1초 ~ 168시간 | `--check-time` |
| 체크 하나의 출력 | 64 MB | 1 KB ~ 1024 MB | `--check-output` |
| 저장소당 대기 체크 | 1000 | 1 ~ 10000 | `--check-queue` |
| 저장소당 동시 실행 체크 | 100 | 1 ~ 1000 | `--check-active` |
| 컨테이너 CPU | 64 | 0.1 ~ 1024 | `--check-cpus` |
| 컨테이너 메모리 | 64 GB | 64 MB ~ 1024 GB | `--check-memory` |
| 컨테이너 프로세스 | 4096 | 16 ~ 65536 | `--check-processes` |
| 컨테이너 작업 공간 | 16 GB | 1 MB ~ 1024 GB | `--check-scratch` |
| 체크용으로 복사하는 소스 | 4 GB | 최대 1024 GB | `--check-source` |

- 상한을 넘는 정책은 저장할 수 없습니다. 상한을 낮추면 그보다 큰 저장된 정책은 상한을 올리거나 정책을 낮출 때까지 새 체크를 대기열에 넣지 않습니다. 해당 저장소는 "체크 상한"에 표시됩니다. 이미 대기 중인 체크는 원래 한도로 실행됩니다.
- 출력 한도보다 많이 출력한 체크는 멈추고 `incomplete`로 끝납니다.
- 상한은 이 컴퓨터에 속하며 백업에 들어가지 않습니다.

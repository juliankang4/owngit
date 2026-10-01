# 운영 안내

<p align="center"><a href="OPERATIONS.md">English</a> | <b>한국어</b></p>

이 문서는 셀프 호스팅 Git 서버인 OwnGit을 설치하고 운영하는 사람을 위한 안내입니다. 절마다 작업 하나를 다룹니다. 설치, 처음 설정, 서비스로 실행하기, 다른 기기에서 접속하기, 저장소를 다루는 일상 작업, 가져오기, 명령줄 풀 리퀘스트와 체크, 백업과 복구가 있습니다.

명령은 `owngit`으로 적습니다. 압축 파일을 풀어서 쓴다면 `./owngit`, 소스에서 빌드했다면 `./bin/owngit`으로 실행하세요([Build and run](../CONTRIBUTING.md#build-and-run) 참고).

## 한 줄 설치

설치 스크립트는 릴리스를 내려받아 확인하고 설치한 뒤 서비스로 실행하는 일을 명령 하나로, 아무것도 묻지 않고 처리합니다. Linux와 macOS에서는 다음과 같이 실행합니다.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh
```

Windows에서는 PowerShell에서 실행합니다.

```powershell
irm -MaximumRedirection 0 https://owngit.app/install.ps1 | iex
```

끝나면 OwnGit이 백그라운드에서 돌고, 터미널에서 실행했다면 설정 링크가 출력됩니다. 나중에 다시 실행해도 상태와 저장소는 그대로입니다.

스크립트는 다음 순서로 진행합니다.

1. 최신 릴리스나 지정한 릴리스에서 이 컴퓨터에 맞는 압축 파일(Linux x64나 ARM64, Apple silicon Mac, Windows x64)을 고릅니다.
2. 그 릴리스의 `SHA256SUMS`와 압축 파일을 HTTPS로 내려받고(리디렉션도 HTTPS로만 따라갑니다) 압축 파일의 SHA-256을 대조합니다. 내려받기에 실패하거나 값이 맞지 않으면 그 자리에서 멈추며 컴퓨터에는 아무것도 바뀌지 않습니다.
3. 프로그램을 제자리에 둡니다.
   - Linux와 macOS에서는 `~/.local/bin/owngit`에 두고, root가 실행하면 `/usr/local/bin/owngit`에 둡니다. `sudo`는 내 계정이 쓸 수 없는 폴더일 때만 쓰므로 root가 소유한 프로그램은 계속 root 소유로 남습니다.
   - Windows는 실행 중인 프로그램 파일을 바꿀 수 없으므로 릴리스마다 `%LOCALAPPDATA%\Programs\OwnGit\owngit_X.Y.Z_windows_amd64` 폴더를 따로 만들어 풉니다. `%ProgramFiles%\OwnGit`은 `owngit service install`이 쓰는 폴더라서 쓰지 않습니다.
4. 그 프로그램으로 `owngit service install`을 실행합니다([서비스로 실행하기](#서비스로-실행하기) 참고).

| Linux와 macOS | Windows | 하는 일 |
| --- | --- | --- |
| `--version 1.1.3` | `-Version 1.1.3` | 최신 릴리스 대신 지정한 릴리스를 설치합니다. |
| `--no-service` | `-NoService` | 프로그램만 설치하고 서비스 등록이나 실행은 하지 않습니다. 다음에 할 일 두 가지를 알려 줍니다. 지금 바로 실행하려면 `owngit serve`, 서비스로 실행하려면 `owngit service install`입니다. |
| `--to PATH` | `-Dir FOLDER` | 프로그램을 `PATH`에 두거나 릴리스 폴더를 `FOLDER` 안에 만듭니다. |

옵션은 `/bin/sh -s --` 뒤에 붙이고 PowerShell에서는 스크립트 블록에 넘깁니다.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/install.sh | /bin/sh -s -- --version 1.1.3 --no-service
```

```powershell
& ([scriptblock]::Create((irm -MaximumRedirection 0 https://owngit.app/install.ps1))) -Version 1.1.3 -NoService
```

다시 실행했을 때 이미 같은 릴리스가 있으면 프로그램을 건드리지 않습니다. 업그레이드한 뒤에는 `owngit service install`이 같은 방식과 같은 상태 디렉터리로 서비스를 새 버전으로 다시 시작합니다. PATH 설정은 바꾸지 않으며, 프로그램 폴더가 PATH에 없으면 어떻게 실행하는지 알려 줍니다. Windows에서 예전 릴리스 폴더는 직접 지울 때까지 남습니다.

### 설치 스크립트가 멈추는 경우

다음 경우에는 아무것도 내려받기 전에 멈추고 이유를 알려 줍니다.

- Linux나 macOS에서 프로그램 경로가 심볼릭 링크일 때(예를 들어 npm이 `/usr/local/bin`에 둔 `owngit`). 다른 설치 방법이 관리하는 링크이므로 그 설치는 원래 방법대로 업데이트하거나 `--to`로 다른 경로를 고르세요.
- 프로그램 폴더, 그 폴더로 가는 길의 폴더나 링크(와 링크가 가리키는 곳), 임시 폴더(`TMPDIR`나 `TEMP`)를 다른 계정이 바꿀 수 있을 때. 그 계정이 확인을 마친 프로그램을 실행 직전에 바꿔치기할 수 있기 때문입니다. 나만 바꿀 수 있는 폴더를 고르세요. root와 Windows의 관리자 그룹은 괜찮습니다.

### 내려받기를 보호하는 방법

curl 옵션은 모든 요청과 리디렉션을 HTTPS로만 하게 하고 `-MaximumRedirection 0`은 PowerShell이 리디렉션을 따라가지 않게 합니다. 그래서 스크립트가 평문 HTTP로 오는 일은 없습니다. `curl`과 `sh`는 시스템 경로로 부르고 설치 스크립트도 시스템 폴더의 도구만 쓰므로, `PATH` 앞쪽 폴더에 있는 같은 이름의 프로그램이 대신 실행되지 않습니다.

설치 스크립트는 [Proxmox VE 스크립트](#proxmox-ve에서-실행하기) `proxmox.sh`와 함께 소스의 `packaging/installer/`에 있고, 모든 릴리스에 같은 세 파일이 압축 파일과 함께 올라갑니다. `SHA256SUMS`는 압축 파일과 같은 릴리스에서 옵니다. 그래서 망가졌거나 덜 받았거나 잘못 받은 파일은 걸러 내지만 독립된 서명은 아닙니다. 릴리스 파일을 바꿀 수 있는 사람이라면 두 파일을 함께 바꿀 수 있습니다. macOS 실행 파일은 이와 별도로 Apple의 서명과 공증을 받았습니다.

한 줄 명령은 owngit.app이 HTTPS로 내주는 스크립트를 확인 없이 바로 실행합니다. `SHA256SUMS`에는 압축 파일만 들어 있고, 설치 스크립트와 `proxmox.sh`의 SHA-256은 릴리스의 `manifest.json`에 기록됩니다. 실행하기 전에 스크립트를 확인하려면 이렇게 합니다.

1. 릴리스에서 `install.sh`, `install.ps1`, `proxmox.sh` 중 필요한 파일을 내려받습니다.
2. 그 SHA-256을 `manifest.json`이나, 릴리스 페이지에서 GitHub가 그 파일에 보여 주는 값과 비교합니다. 그 릴리스 태그의 `packaging/installer/`와 파일을 비교해도 됩니다.
3. 내용을 읽어 본 뒤 `/bin/sh install.sh`나 `& .\install.ps1`로 실행합니다. `proxmox.sh`는 Proxmox VE 호스트에서 root로 `/bin/sh proxmox.sh`를 실행합니다.

## 처음 설정하기

먼저 OwnGit 릴리스를 설치합니다. 어느 방법으로 설치하든 호스트에는 `git-http-backend`가 들어 있는 Git이 있어야 합니다. Homebrew와 Arch Linux 패키지는 Git을 함께 설치합니다.

- macOS(Apple silicon)나 Linux(x64, ARM64)의 Homebrew: `brew install juliankang4/tap/owngit`
- macOS(Apple silicon), Linux(x64, ARM64), Windows(x64)의 npm: `npm install -g owngit`. 이 방법은 설치할 때와 OwnGit을 시작할 때 Node.js가 필요합니다.
- Arch Linux(x64, ARM64)나 Omarchy: 1.0.3부터 각 릴리스에 첨부된 `PKGBUILD`로 패키지를 만들어 설치합니다.
- 위 플랫폼 어디서나: [GitHub Releases](https://github.com/juliankang4/owngit/releases)에서 압축 파일을 내려받아 `SHA256SUMS`로 확인합니다.

방법별 자세한 절차는 README의 [설치](../README.ko.md#설치)에 있습니다. 처음부터 백그라운드에서 실행하려면 [서비스로 실행하기](#서비스로-실행하기)로 가세요. 터미널에서 바로 실행하려면 다음과 같이 시작합니다.

```sh
owngit serve
```

기본 주소는 `http://127.0.0.1:7654`입니다. 설정에서는 세 가지를 정합니다.

- 저장소를 둘 폴더(경로를 입력하거나 브라우저에서 [목록으로 고를 수 있습니다](#브라우저에서-저장소-폴더-고르기))
- 일반 접근을 열어 둘지, 공용 비밀번호 하나로 보호할지
- 별도의 관리자 비밀번호. 대시보드에서 관리자 작업을 할 때 이 비밀번호를 묻습니다([얼마나 자주 묻는지](#관리자-비밀번호-확인)).

공인 인터넷 주소에서 설정 화면을 열었거나, 원래 주소를 넘겨주지 않은 신뢰하는 프록시를 거쳐 열었다면 설정 페이지는 일반 접근 방식으로 공용 비밀번호를 골라 두고 그 이유를 알려 줍니다. 프록시를 거친 경우에는 "프록시를 거쳐 온 접속, 원래 주소 확인 불가"라는 경고가 나옵니다.

설정을 마치면 빈 대시보드가 나옵니다. 새 저장소로 만든 저장소의 clone 주소는 `http://HOST:7654/git/PROJECT.git` 형식입니다.

대시보드는 기본 브랜치의 최근 커밋 작성 시간을 기준으로 최근에 수정한 저장소부터 보여 줍니다. 목록 옆의 정렬에서 오래전에 수정한 순이나 이름순으로 바꿀 수 있고 고른 정렬은 이 브라우저에 저장됩니다.

설정이 어떻게 시작되는지는 OwnGit을 어떻게 실행했는지에 따라 다릅니다. 아래 네 절에서 경우마다 설명합니다.

### 터미널에서 설정하기

아직 설정하지 않은 OwnGit을 터미널의 포그라운드에서 `owngit serve`로 시작하면 그 터미널에서 설정을 진행합니다. 먼저 언어(English 또는 한국어)를 묻습니다. Enter만 누르면 로캘의 언어를 쓰고, 나중에 L 키로 바꿀 수 있습니다. 이어서 "이 터미널에서 계속"과 "웹 대시보드 열기" 중 하나를 고릅니다.

터미널에서는 웹 페이지와 같은 내용을 묻습니다.

1. 저장소 폴더
2. 저장소를 읽고 쓸 수 있는 사람
3. 관리자 비밀번호(두 번 입력하며 화면에 나타나지 않습니다)
4. "다른 기기에서 접속"
5. OwnGit이 네트워크 주소에서 연결을 받는다면, 암호화되지 않는 HTTP로 계속할지

확인 카드에서 "설정 완료"를 고르기 전에는 아무것도 저장하지 않습니다. Ctrl-C를 누르면 아무것도 저장하지 않고 서버가 멈춥니다. 다시 하려면 `owngit serve`를 다시 실행하세요. 키로 동작하는 것은 Enter, Backspace, Ctrl-U, Ctrl-C, Escape뿐이고, 그 밖의 문자는 답에 그대로 들어갑니다.

OwnGit이 이 컴퓨터에서만 연결을 받으면 일반 HTTP 확인을 묻지 않습니다. 나중에 다른 기기에서 접속하면 설정 화면이 그때 묻습니다. 일반 HTTP 질문에 아니요라고 답하면 OwnGit을 이 컴퓨터에서만 쓰는 방법을 알려 줍니다. `--listen 127.0.0.1:PORT` 옵션으로 다시 시작하거나, 주소가 저장된 [네트워크 설정](#네트워크-설정)에서 왔다면 `owngit network set --listen 127.0.0.1:PORT --base-url=`를 실행한 뒤 다시 시작하면 됩니다.

"다른 기기에서 접속" 단계는 이 컴퓨터에서 Tailscale이 실행 중인지 확인합니다. Tailscale의 상태를 읽기만 합니다.

- Tailscale을 쓸 수 없으면 그 이유를 알려 줍니다.
- 실행 중이면 이 컴퓨터의 Tailscale 주소와 MagicDNS 이름을 보여 주고, 이 값을 [네트워크 설정](#네트워크-설정)으로 저장하는 명령을 출력합니다. 예를 들면 `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --accept-insecure-http`입니다. 명령의 `--accept-insecure-http` 옵션은 다른 기기가 접속하는 주소에서 일반 HTTP를 받아들인다는 확인을 기록합니다([네트워크 설정](#네트워크-설정) 참고). PATH의 `owngit`이 지금 실행 중인 OwnGit이 아니면(압축 파일에서 푼 사본을 실행할 때처럼) 명령은 실행 중인 OwnGit의 경로로 시작합니다.

설정 과정에서 이 명령을 실행하지는 않으므로, 설정을 마친 뒤 직접 실행하고 OwnGit을 다시 시작하세요. Tailscale이 연결을 암호화하더라도 OwnGit은 그 사실을 확인할 수 없어 이 주소를 계속 일반 HTTP로 표시합니다. OwnGit이 암호화된 연결로 표시하는 주소를 쓰려면 대신 [tailnet에서 HTTPS로 공유](#tailnet에서-https로-공유하기)하세요.

### 브라우저에서 설정하고 터미널에서 승인하기

"웹 대시보드 열기"를 고르면 브라우저에서 `http://127.0.0.1:7654/setup`을 엽니다. `--no-open`을 붙였다면 터미널에 주소만 출력합니다.

1. 브라우저에서 "터미널에 승인 요청"을 누릅니다. 페이지에 짧은 코드가 나옵니다.
2. 터미널에는 "브라우저가 OwnGit 설정을 요청했습니다"라는 카드와 함께 같은 코드와 요청을 보낸 주소가 나옵니다. 다른 기기에서 온 요청과 신뢰하는 프록시를 거쳐 온 요청은 모두 경고와 함께 표시합니다. 프록시가 원래 주소를 넘겨주지 않았다면 주소 대신 "프록시를 거쳐 온 접속, 원래 주소 확인 불가"라고 나옵니다.
3. 내 브라우저에 그 코드가 보일 때만 `y`로 답하세요.

승인은 그 브라우저 하나에만 적용되고, 그 브라우저가 웹 페이지에서 설정을 이어 갑니다. 설정을 마치면 터미널에 저장된 답을 보여 줍니다.

승인 요청에는 다음 제한이 있습니다.

- 승인을 기다릴 수 있는 브라우저는 한 번에 하나입니다.
- 거절한 브라우저는 1분이 지나야 다시 요청할 수 있고, 한 주소에서는 10분에 다섯 번까지만 요청할 수 있습니다.
- 답하지 않은 요청과 쓰지 않은 승인은 10분 뒤에 만료됩니다.
- 기다리는 동안 T를 누르면 터미널에서 설정하며 이미 승인한 브라우저의 설정 세션은 끝납니다. 두 번째 브라우저를 승인해도 첫 브라우저의 설정 세션이 끝납니다.

### 설정 파일로 설정 시작하기

터미널 없이 시작하면 OwnGit은 상태 디렉터리 안에 소유자만 읽을 수 있는 설정 파일을 만들고 그 파일의 경로를 서버 로그에 남깁니다. 링크 자체는 로그에 남기지 않습니다. 설치 호스트의 브라우저에서 그 파일을 열거나 그 컴퓨터의 터미널에서 `owngit setup-link`를 실행하세요.

서비스는 브라우저를 열지 않습니다. `owngit service install`이 만드는 서비스와 Homebrew의 `brew services`는 모두 `--no-open`으로 OwnGit을 시작하므로 경로는 로그에서만 볼 수 있습니다. Homebrew라면 로그는 `$(brew --prefix)/var/log/owngit.log`입니다. OwnGit이 설치한 소유자의 브라우저에서 파일을 직접 여는 경우는 따로 있습니다. 누군가 보고 있는 화면이 있는 세션에서 `--no-open` 없이 백그라운드 작업으로 실행했고 브라우저를 열 수 있을 때입니다. OwnGit이 어떤 세션을 화면이 없다고 보는지는 [화면이 없는 컴퓨터](#화면이-없는-컴퓨터)에서 설명합니다. Windows에서 SSH 밖의 백그라운드로 시작한 프로그램도 콘솔을 읽는 사람이 없으므로 설정 파일을 씁니다.

`owngit setup-link`는 이전 링크를 대신하는 새 링크를 발급합니다.

- 출력이 터미널이면 15분 안에 한 번만 쓸 수 있는 링크를 출력합니다. 출력이 파이프, 파일, 저널로 가면 설정 파일의 경로만 출력합니다.
- `--base-url`을 주지 않으면 서버가 연결을 받는 주소로 링크를 만듭니다. 모든 주소에서 받는다면 이 컴퓨터의 주소를 쓸 가능성이 높은 순서로 나열합니다.
- 설정을 마치기 전에는 OwnGit을 시작할 때 알려 주지 않은 주소로도 다른 기기에서 링크를 쓸 수 있습니다. `owngit setup-link --base-url http://192.168.1.20:7654`는 그 주소용 링크를 만들고, 그 주소는 설정 페이지만 보여 줍니다. 링크를 쓴 뒤에는 그 주소의 그 브라우저만 설정을 이어 갈 수 있으며, 설정 화면은 설정을 마친 뒤에도 이 주소를 계속 받아들일지 묻습니다.

### 화면이 없는 컴퓨터

아무도 브라우저를 열 수 없는 컴퓨터에서는 다른 기기에서 설정해야 합니다. 그래서 설정 전에 저장된 연결 주소도 `--listen` 옵션도 없이 처음 시작하면, OwnGit은 모든 주소(`0.0.0.0:7654`)에서 연결을 받고 이를 연결 주소로 저장합니다. 설정을 마치기 전까지 다른 기기에는 설정 페이지로만 응답합니다.

설정 링크는 사설 주소로만 출력합니다. `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, tailnet 대역 `100.64.0.0/10`, IPv6 고유 로컬 주소가 여기에 해당합니다. 많은 클라우드 서버처럼 공인 주소만 있는 컴퓨터에서는 SSH 명령(`ssh -L 7654:127.0.0.1:7654 USER@HOST`)과 `http://127.0.0.1:7654`의 링크를 대신 출력합니다. 내 컴퓨터에서 그 명령을 실행해 열어 둔 채 그 링크를 여세요.

설정 페이지는 일반 HTTP를 받아들일지 묻고 사용한 주소를 계속 받아들일지 묻습니다. 이 항목은 공인 주소에서 설정 화면을 열었거나 원래 주소를 넘겨주지 않은 신뢰하는 프록시를 거쳐 연 경우가 아니면 처음부터 선택되어 있습니다. 선택을 풀면 설정을 마친 뒤 그 주소를 거부하고 다음 시작부터 `127.0.0.1:7654`에서만 연결을 받습니다. 처음부터 이 컴퓨터에서만 쓰려면 `owngit network set --listen 127.0.0.1:7654`를 실행하고 다시 시작하세요. 화면이 있는 컴퓨터는 다른 주소를 고를 때까지 `127.0.0.1:7654`에서만 연결을 받습니다.

OwnGit은 다음 경우를 화면이 없는 컴퓨터로 봅니다.

- root가 컨테이너 안에서 실행할 때
- 디스플레이 없는 SSH 세션에서 실행할 때(`DISPLAY`와 `WAYLAND_DISPLAY`가 모두 없음, Windows에서는 모든 SSH 세션)
- systemd-logind에 그래픽 세션이 없고 두 변수도 모두 없을 때
- Mac에서 OwnGit을 실행하는 사용자가 화면에 로그인해 있지 않을 때. 화면에 로그인한 Mac은 SSH로 실행해도 화면이 있는 컴퓨터로 봅니다.

서비스는 설치할 때 정한 `--headless=true` 또는 `false`를 넘기므로 부팅할 때 다시 판단하지 않습니다.

### 브라우저에서 저장소 폴더 고르기

브라우저로 설정할 때는 설정 페이지의 저장소 폴더 입력란 옆에 있는 "폴더 선택"을 눌러 경로를 입력하는 대신 목록에서 폴더를 고를 수 있습니다. 목록에 나오는 폴더는 브라우저를 연 기기가 아니라 OwnGit이 실행되는 컴퓨터의 폴더이며, OwnGit을 실행하는 계정이 볼 수 있는 폴더입니다. 경로를 직접 입력해도 됩니다. JavaScript가 꺼져 있으면 이 버튼은 나타나지 않습니다.

폴더 선택 창은 입력란에 적힌 경로에서 열립니다. 입력란이 비어 있으면 그 계정의 홈 폴더에 있는 제안 폴더 `OwnGit-Repositories`에서 열립니다. 그 폴더가 아직 없으면 가장 가까운 상위 폴더를 열고 없는 폴더의 이름을 "새 폴더 이름"에 채워 둡니다.

- 목록에는 폴더와 심볼릭 링크가 이름순으로 나오고 파일은 나오지 않습니다. "상위 폴더"를 누르면 한 단계 위로 가고 "숨김 폴더 표시"를 켜면 이름이 점으로 시작하는 폴더 같은 숨김 폴더도 나옵니다.
- 링크는 따라가지 않고 목록에 올립니다. 대상이 없는 링크, 파일을 가리키는 링크, 계정이 읽을 수 없는 폴더를 가리키는 링크는 열 때 오류를 보여 줍니다.
- "폴더 만들기"는 지금 열린 폴더 안에 폴더 하나를 만들고 그 폴더를 엽니다. 이미 있는 항목은 바꾸지 않습니다.
- 없는 폴더, 폴더가 아닌 경로, 계정이 읽을 수 없는 폴더, 3초 안에 응답하지 않는 폴더는 빈 목록 대신 오류를 보여 줍니다. 상위 폴더로 가거나 다른 경로를 입력하세요.
- 항목이 아주 많은 폴더에서는 처음 2,000개 항목 안에 있는 폴더만 보여 주고 목록이 일부라고 알려 줍니다.
- "이 폴더 선택"은 입력란에 경로를 채우기만 합니다. 직접 입력한 경로와 마찬가지로 양식을 제출할 때 설정이 경로를 확인합니다.

폴더 선택은 설정을 진행하는 브라우저에서, 설정을 마치기 전까지만 쓸 수 있습니다. 다른 사람의 요청은 거부하며 설정을 마친 뒤에는 이 기능이 없습니다.

## 서비스로 실행하기

`owngit service install`은 OwnGit을 백그라운드에서 실행하고 알아서 다시 켜지게 합니다.

- Linux에서는 systemd가 부팅할 때마다 켭니다.
- macOS에서는 launchd가 로그인할 때마다 켭니다.
- Windows에서는 작업 스케줄러가 관리자 계정이면 부팅할 때마다, 표준 계정이면 로그인할 때마다 켭니다.

누가 서비스를 실행할지는 명령을 어떻게 실행했는지에 따라 정해집니다. 명령은 서비스가 응답할 때까지 기다린 뒤 설정 링크를 출력합니다. 기다리는 동안 다른 프로그램이 포트를 쓰는 등의 이유로 서비스가 오류로 멈추면 그 오류와 다음에 할 일을 바로 출력합니다.

`owngit service install`은 자체 서비스를 등록하기 전에 내 계정, root, 관리자가 아닌 다른 계정이 `owngit` 실행 파일을 바꿔치기할 수 없는지 확인합니다. 파일을 직접 바꾸는 경우뿐 아니라 경로 중간의 폴더나 링크를 거쳐 바꾸는 경우도 확인합니다. 다른 계정이 바꿀 수 있으면 명령은 아무것도 바꾸기 전에 멈추고 고쳐야 할 경로를 알려 줍니다. Windows 관리자 계정의 설치는 이 대신 Program Files에 보호된 복사본을 만들어 등록합니다. Homebrew로 설치했다면 서비스는 Homebrew가 맡습니다.

| 명령 | 하는 일 |
| --- | --- |
| `owngit service status` | OwnGit이 실행 중이고 응답하는지, 누가 실행하는지, 유닛, 에이전트 또는 작업, 로그 위치, 상태 디렉터리, 주소를 보여 줍니다. |
| `owngit service start`, `stop`, `restart` | 서비스를 시작하거나 멈추거나 다시 시작합니다. 멈춘 서비스는 다음 부팅 또는 로그인 조건에 맞으면 다시 켜집니다. |
| `owngit service uninstall` | 서비스를 멈추고 유닛, 에이전트 또는 작업을 지웁니다. Linux에서 멈출 수 없는 서비스는 그대로 둡니다([Linux](#linux)). 상태 디렉터리와 저장소, Linux에서는 `owngit` 계정도 남으며 데이터가 어디 있는지 알려 줍니다. Linux 사용자 서비스라면 lingering이 켜진 채 남는다고 알려 줍니다(`loginctl disable-linger`로 끕니다). |
| `owngit uninstall` | 위와 같이 한 뒤 프로그램 자체를 지우는 방법을 알려 줍니다. [업데이트와 제거](#업데이트와-제거)를 보세요. |

실행 파일을 새 릴리스로 바꾼 뒤에는 `owngit service install`을 다시 실행하세요. 같은 방식과 같은 상태 디렉터리로 유닛을 다시 쓰고 서비스를 다시 시작합니다.

모든 유닛과 에이전트는 `owngit serve --state-dir DIR --no-open --headless=true` 또는 `--headless=false`를 실행합니다. `--listen`이나 `--base-url`은 넘기지 않으므로 저장된 [네트워크 설정](#네트워크-설정)이 적용됩니다. 처음 설치할 때 정한 화면 없음(headless) 여부는 유지되며 `--headless=true`나 `--headless=false`로 바꿀 수 있습니다. 화면 없는 컴퓨터로 시작한 뒤 이 컴퓨터에서만 쓰도록 되돌리려면 `owngit network set --listen 127.0.0.1:7654`도 실행하세요.

### Linux

어떤 서비스가 되는지는 명령을 어디서 실행하느냐에 따라 다릅니다.

- **데스크톱에서** 실행하면 내 계정의 systemd 사용자 서비스(`~/.config/systemd/user/owngit.service`)가 되고, 상태는 평소 쓰던 상태 디렉터리에 둡니다. 부팅할 때 켜지도록 lingering(`loginctl enable-linger`)을 켭니다. Debian 13, Ubuntu 24.04와 26.04, Arch Linux는 비밀번호 없이 이를 허용합니다. lingering에 비밀번호가 필요한 곳에서는 대신 시스템 서비스로 설치합니다.
- **SSH로 접속했거나 그래픽 세션이 없으면** 내 계정으로 실행되는 시스템 서비스(`User=`와 `Group=`이 있는 `/etc/systemd/system/owngit.service`)가 되고, 상태는 평소 쓰던 상태 디렉터리에 둡니다. 명령은 root가 할 일을 한 줄로 알려 준 뒤 유닛 쓰기, systemd 다시 읽기, 서비스 켜기와 시작을 `sudo` 한 번으로 처리합니다. `sudo`가 없거나 끝나지 않으면 관리자가 root 셸에 붙여 넣을 수 있도록 스크립트 전체를 출력합니다.
- **root로** 실행하면(예: LXC 컨테이너나 클라우드 서버) 시스템 계정 `owngit`을 만들고, 상태를 `/var/lib/owngit/state`에 두며, 서비스를 그 계정으로 실행합니다. [root로 설치하기](#root로-설치하기)를 보세요.
- **Homebrew로 설치했다면** 명령은 `brew services restart owngit`을 실행합니다. 그래서 OwnGit을 업그레이드하는 Homebrew가 계속 서비스를 관리합니다.

사용자 서비스와 시스템 서비스는 명령을 실행할 때 쓴 `owngit`을 실행합니다. 다른 계정이 그 파일을 직접 바꾸거나 경로 중간의 폴더나 링크로 바꿔치기할 수 있으면 명령은 멈추고 그 경로를 알려 줍니다. `~/.local/bin`처럼 나나 root만 바꿀 수 있는 폴더로 `owngit`을 옮기거나 [한 줄 설치](#한-줄-설치)를 쓰세요. 데스크톱에서는 아이콘의 로그인 항목이 시작하는 프로그램도 같은 확인을 거칩니다.

로그는 systemd 저널에 남습니다. 사용자 서비스는 `journalctl --user -u owngit.service -f`, 시스템 서비스는 `sudo journalctl -u owngit.service -f`로 봅니다.

`owngit service uninstall`과 `owngit uninstall`은 서비스를 멈춘 뒤에 지웁니다. systemd가 서비스를 멈추지 못하면 명령은 `Could not stop the OwnGit service. Stop it manually, then rerun "owngit service uninstall". The unit has not been removed.`라는 내용의 오류로 끝나고, 유닛과 로그인이나 부팅 때 시작하는 설정은 그대로 남습니다. `systemctl --user stop owngit.service`나 `sudo systemctl stop owngit.service`처럼 직접 멈춘 뒤 명령을 다시 실행하세요. 이미 멈춘 서비스나 systemd가 더 이상 알지 못하는 서비스는 평소처럼 지웁니다.

#### root로 설치하기

- `--state-dir`도 `/var/lib/owngit` 안이어야 합니다.
- 실행 파일은 `/usr/local/bin/owngit`처럼 root만 바꿀 수 있는 곳에 있어야 합니다. 홈 폴더에 있는 실행 파일은 거부합니다.
- 상태 디렉터리 경로를 `/etc/owngit/state-dir`에 적어 두므로 `owngit setup-link`, `owngit network` 등 상태 디렉터리를 쓰는 명령은 `--state-dir` 없이 찾습니다. root가 실행하면 이 명령들은 `owngit` 계정으로 실행됩니다. 그래서 `backup --output`처럼 넘기는 파일은 `/var/lib/owngit/backup`처럼 그 계정이 쓸 수 있는 절대 경로여야 합니다. `reset-admin --password-file`은 root만 읽을 수 있는 파일도 읽습니다.

root가 이미 `/root/.config/owngit`의 상태로 OwnGit을 쓰고 있었다면(예: 1.1.0), root의 `owngit service install`은 그 상태를 그대로 둡니다. 새 상태를 아직 설정하지 않은 동안에는 기존 설치를 서비스로 옮기는 명령을 알려 줍니다. 그 OwnGit을 멈춘 뒤 백업하고, `owngit` 계정으로 복원하고, 서비스가 그 사본을 쓰게 하는 순서입니다.

```sh
sudo owngit backup --state-dir /root/.config/owngit --output /var/lib/owngit-root-backup && \
sudo chown -R owngit: /var/lib/owngit-root-backup && \
sudo runuser -u owngit -- owngit restore --input /var/lib/owngit-root-backup --state-dir /var/lib/owngit/state-from-root --repository-root /var/lib/owngit/repositories && \
sudo owngit service install --state-dir /var/lib/owngit/state-from-root
```

다른 복원과 마찬가지로 백업에 들어 있지 않은 것은 기본값으로 시작합니다([복원한 뒤에 할 일](#복원한-뒤에-할-일)). 로그인 세션, 허용한 Host, 네트워크 설정도 여기에 들어갑니다. 다시 로그인하고, 다른 기기에서 접속하려면 `sudo owngit network set --listen 0.0.0.0:7654 --allowed-host 주소 --accept-insecure-http`와 `sudo owngit service restart`를 실행하세요. root의 옛 상태, 백업, 쓰지 않는 `/var/lib/owngit/state`는 직접 지우기 전까지 남습니다. 이후에 다시 설치해도 복원한 상태를 계속 씁니다.

### Windows에서

`owngit service install`은 `OwnGit`이라는 작업 스케줄러 작업을 등록하고 이 작업이 내 계정으로 OwnGit을 백그라운드에서 실행합니다. 어느 계정에서 실행하느냐에 따라 OwnGit이 켜지는 때가 달라집니다.

- **관리자 계정**(Windows 컴퓨터의 첫 계정)에서 설치하면 작업은 아무도 로그인하기 전인 부팅 때 시작하며 비밀번호를 저장하지 않습니다("암호를 저장하지 않음" 로그온). 등록하려면 사용자 계정 컨트롤 승인이 한 번 필요합니다. 명령은 그 승인으로 무엇을 하는지 미리 알려 주고 승인을 거절하면 아무것도 바뀌지 않습니다. "관리자 권한으로 실행"으로 연 터미널이나 관리자로 접속한 SSH에서는 묻는 창이 뜨지 않습니다. 관리자 권한 없는 SSH에서는 대신 할 일을 알려 줍니다.
- **표준 계정**에서 설치하면 작업은 로그인할 때 시작하고 설치하면서 아무것도 묻지 않습니다. Windows는 표준 계정이 부팅 작업이나 로그인 없이 도는 작업을 만들지 못하게 하므로, 부팅할 때 시작하게 하려면 관리자 계정에서 설치하세요. 방화벽 규칙은 추가하지 않습니다([다른 기기에서 서버에 접속하기](#다른-기기에서-서버에-접속하기) 참고).

관리자 계정의 승인 한 번으로 다음 일을 합니다.

1. 프로그램을 `%ProgramFiles%\OwnGit\owngit.exe`에 복사하고 Administrators와 SYSTEM만 바꿀 수 있게 그 폴더를 보호합니다.
2. 그 복사본을 실행하는 작업을 등록합니다.
3. 개인 네트워크용 Windows 방화벽 규칙 `OwnGit`을 추가합니다. 공용 네트워크는 계속 막혀 있습니다. 이 규칙에는 OwnGit의 설명이 붙으며, OwnGit은 그 설명이 붙은 `owngit.exe`용 규칙만 바꾸거나 지웁니다. OwnGit이 추가하지 않은 `OwnGit` 규칙이 있으면 그 이름의 규칙은 추가하지도 지우지도 않습니다. 설치는 아무것도 바꾸기 전에 멈추고 그 사실을 알려 주며, 제거할 때는 규칙을 그대로 둡니다.
4. 컴퓨터와 사용자 PATH에 Git이 없으면 `winget`으로 Git for Windows를 설치합니다. Git이 설치되어 있는데 그 PATH에 없으면 설치하는 대신 Git의 `cmd` 폴더(예: `C:\Program Files\Git\cmd`)를 PATH에 넣고 다시 실행하라고 알려 줍니다.
5. 예전에 관리자 권한으로 돌던 OwnGit이 상태 디렉터리와 저장소 폴더에 Administrators 그룹 소유로 남긴 파일을 내 계정에 돌려줍니다. 이런 저장소는 Git이 "dubious ownership"으로 거부합니다. 명령은 몇 개를 바꿨는지 알려 줍니다. Administrators 그룹이 소유한 항목만 바꾸고, 링크는 따라가지 않으며, 다른 계정의 폴더, 드라이브 전체, Windows나 프로그램 폴더는 바꾸지 않습니다.

표준 계정에서도 `owngit service install`이 관리자 승인을 받아 5단계만 처리합니다. Windows가 관리자 암호를 한 번 묻고, 명령을 실행한 계정에 폴더를 돌려줍니다. 이 단계는 그 설치 명령이 요청한 경우에만, 그 계정의 사용자 폴더 안에 있는 OwnGit 상태 디렉터리와 저장소 폴더만 바꿉니다. 사용자 폴더 밖의 폴더는 관리자가 바꿔야 합니다. SSH에서는 그 확인 창을 띄울 데스크톱이 없으니 컴퓨터 앞에서 실행하세요.

상태 디렉터리나 저장소 폴더를 `C:\OwnGit`처럼 사용자 폴더 밖에 두어도 됩니다.

데스크톱이 있는 컴퓨터에서는 [OwnGit 아이콘](#windows의-아이콘)을 위한 두 번째 작업 `OwnGit icon`도 등록합니다.

#### Windows 서비스가 실행되는 방식

관리자 계정의 작업은 `%ProgramFiles%\OwnGit\owngit.exe serve --state-dir DIR --no-open --log-file DIR\logs\service.log --service --headless=true`(또는 `false`)를 실행합니다. 표준 계정의 작업은 설치할 때 쓴 `owngit.exe`를 실행합니다. 이 작업은 로그인할 때 그 경로에 있는 프로그램을 그대로 실행하므로, 표준 계정의 `owngit service install`은 이 컴퓨터의 다른 계정이 바꿀 수 있는 `owngit.exe`를 거부합니다. `D:\tools`처럼 다른 계정이 바꿀 수 있는 폴더나 그 아래에 있는 경우입니다. `owngit.exe`를 사용자 프로필 안의 폴더처럼 나만 바꿀 수 있는 폴더로 옮기거나 [한 줄 설치](#한-줄-설치)로 설치한 뒤 `owngit service install`을 다시 실행하세요.

- 첫 프로세스는 감독만 합니다. `--service`가 붙으면 내 계정의 평범한 창과 같은 권한으로 서버 복사본을 시작하므로 Git, hook, 체크, 새 파일은 내 계정의 것이 됩니다. 서버가 실패하면 5초 뒤 다시 시작합니다.
- 작업 스케줄러는 출력을 남기지 않으므로 로그는 상태 디렉터리의 `logs\service.log`에 씁니다. 10MB 아래로 유지되고 바로 전 파일 하나가 옆에 남습니다. 서버가 시작하지 못하면 `owngit service install`, `start`, `status`가 그 이유를 보여 주고, 로그도 같은 오류로 끝납니다.
- `owngit service stop`은 Ctrl-C처럼 서버가 하던 일을 마치고 멈추게 합니다. 150초가 지나도 서버가 멈추지 않았을 때만 작업을 끝냅니다.
- 새로 설치한 Windows에서는 누군가 화면에서 처음 로그인하기 전까지 부팅 작업이 "큐에 대기됨" 상태로 남습니다(SSH 로그인은 해당하지 않습니다). 그 뒤로는 바로, 그리고 부팅할 때마다 시작합니다. `owngit service install`과 `status`는 작업이 대기 중이면 알려 줍니다.

#### Windows 서비스 업데이트와 제거

업데이트하려면 새 릴리스를 `%ProgramFiles%\OwnGit` 밖에 설치하거나 압축을 푼 뒤 그 `owngit service install`을 실행하세요. `owngit service status`는 자기 버전이 보호된 복사본과 다르면 알려 주고, 보호 경로에서 실행한 `service install`은 거부합니다.

관리자 계정에서는 예전 서비스를 멈추고, 예전 폴더를 `OwnGit.old-TIMESTAMP`로 옮기고, 새 복사본을 설치하고, 방화벽 규칙을 새로 쓰고, 새 버전을 시작합니다. 예전 폴더에 `owngit.exe`, `installed-from.txt`, `temp`만 남았으면 그 폴더를 지웁니다. 표준 계정은 새 `owngit.exe`로 같은 명령을 실행합니다. 보호된 복사본 옆의 `installed-from.txt`에는 어느 `owngit.exe`를 복사했는지 적어 두므로, 실행 중인 서비스가 그 프로그램의 업데이트 방법을 보여 줄 수 있습니다.

`owngit service uninstall`은 승인 한 번으로 작업, 방화벽 규칙, 보호된 복사본을 지우며 상태 디렉터리와 저장소는 남습니다. 설치가 중간에 멈춰 작업은 없이 보호된 복사본이나 규칙만 남았을 때도 관리자 계정에서 같은 명령을 실행하면 함께 지웁니다. OwnGit이 만들지 않은 파일이 함께 든 폴더는 남기고 그 사실을 알려 줍니다.

### macOS

`owngit service install`은 내 계정의 LaunchAgent `~/Library/LaunchAgents/app.owngit.server.plist`를 쓰고 관리자 비밀번호 없이 시작합니다. launchd는 로그인할 때마다 OwnGit을 켜고, 멈추면 다시 켭니다. 부팅할 때가 아니라 로그인할 때이며, 자동 로그인을 켜 두었다면 다시 시작한 직후입니다. 상태는 `~/Library/Application Support/owngit`에, 로그는 `~/Library/Logs/owngit/owngit.log`에 있습니다.

모든 릴리스 설치처럼 프로그램과 함께 OwnGit.app이 있으면 macOS는 시스템 설정의 일반 > 로그인 항목 및 확장 프로그램 > 백그라운드에서 허용에 이 에이전트를 OwnGit으로 표시하고, 앱이 없는 프로그램은 `owngit`으로 표시합니다. 그곳에서 끄면 켜지지 않으며, 명령은 그 사실을 알리고 원래 있던 에이전트를 되돌려 놓습니다.

- **SSH로 실행할 때** Mac 화면에 로그인해 있다면 그 Mac의 터미널 창에서 실행한 것과 같습니다. 로그인하지 않은 상태라면 OwnGit이 바로 시작하고 Mac을 다시 시작할 때까지 계속 실행되고 그 뒤에는 다음 로그인 때 켜집니다. 이런 Mac은 [화면이 없는 컴퓨터](#화면이-없는-컴퓨터)로 칩니다. root로 실행하면 거부합니다.
- **Homebrew로 설치했고** 화면에 로그인해 있다면 명령은 `brew services restart owngit`을 실행합니다. `status`, `start`, `stop`, `restart`, `uninstall`도 `brew services`를 쓰며, 로그는 `$(brew --prefix)/var/log/owngit.log`에 있습니다. Homebrew 설치는 항상 기본 상태 디렉터리를 쓰므로 `--state-dir`와 `--headless`는 받지 않고, 주소는 `owngit network set --listen`으로 바꿉니다. 화면에 아무도 로그인하지 않은 상태에서 SSH로 실행하면 Homebrew 서비스가 켜질 수 없으므로, 대신 `$(brew --prefix)/opt/owngit/bin/owngit`을 실행하는 OwnGit LaunchAgent를 설치합니다. 나중에 데스크톱에서 `owngit service install`이나 `brew services start owngit`을 실행하면 Homebrew가 서비스를 넘겨받고 그 에이전트를 지웁니다.
- **npm으로 설치했다면** 에이전트는 Node.js 실행기 대신 플랫폼 패키지의 실행 파일(예: `/opt/homebrew/lib/node_modules/owngit/node_modules/owngit-darwin-arm64/bin/owngit`)을 실행하므로 [실행기의 신호 처리 한계](../packaging/README.md#homebrew-winget-npm-and-arch-linux)가 해당하지 않습니다. `npm update -g owngit`을 실행했거나 Node.js를 바꿨다면 `owngit service install`을 다시 실행하세요.

에이전트는 명령을 실행한 경로(예: `/usr/local/bin/owngit`)로 OwnGit을 시작합니다. 본인이나 root가 아닌 계정이 바꿀 수 있는 실행 파일은 거부합니다. macOS의 `wheel`과 `admin` 그룹에 쓰기 권한이 있는 것은 받아들입니다. `owngit service`가 만들지 않은 launchd 작업이 이미 `owngit serve`를 실행하고 있으면 명령은 그 작업을 알려 주고 아무것도 바꾸지 않습니다. 먼저 그 작업을 내리고 지우세요. 예를 들어 `launchctl bootout gui/$(id -u)/LABEL`로 내릴 수 있습니다.

#### macOS의 로그 파일

서비스는 로그 폴더(`~/Library/Logs/owngit`, Homebrew는 `$(brew --prefix)/var/log`)에 파일 두 개를 둡니다.

- `owngit.log`는 서버 로그입니다. OwnGit이 직접 쓰며(`--service`와 함께 준 `--log-file`) 내 계정만 읽을 수 있게 둡니다. 10MB 아래로 유지하며 그 크기에 이르면 `owngit.log.1`로 옮기고 이전 `.1` 파일을 대신합니다.
- `owngit.stderr.log`에는 launchd가 OwnGit의 출력에서 모은 내용이 들어갑니다. 로그가 담지 못하는 것(예를 들어 비정상 종료 보고나 OwnGit이 로그를 열지 못한 오류)뿐이라 작게 유지됩니다. 이 파일의 크기는 launchd가 제한하지 않습니다.

이전 버전으로 설치한 서비스는 `owngit service install`을 다시 실행하기 전까지(Homebrew는 업그레이드한 뒤 `brew services restart owngit`을 실행하기 전까지) 모든 내용을 크기 제한 없는 `owngit.log` 하나에 계속 씁니다.

### 서비스가 할 수 있는 일

내 계정으로 실행하는 서비스는 내 계정이 할 수 있는 일을 모두 할 수 있고, 푸시된 커밋에서 실행하는 체크도 마찬가지입니다. 다른 사람이 푸시할 수 있다면 Linux에서 OwnGit을 root로 설치해 별도의 `owngit` 계정으로 실행하세요.

Linux의 시스템 서비스에는 systemd 강화 설정이 걸리지만 이 컴퓨터나 Docker에서 실행하는 Git, hook, 체크를 막지는 않습니다. 눈에 띄는 것은 다음과 같습니다.

- OwnGit이 시작하는 어떤 프로그램도 권한을 얻지 못하므로 체크는 `sudo`를 쓸 수 없습니다(`NoNewPrivileges`, `RestrictSUIDSGID`).
- `/usr`, `/boot`, `/efi`, `/etc`는 읽기 전용입니다(`ProtectSystem=full`). 그 밖의 곳은 서비스 계정의 파일 권한을 따르므로, 내 계정으로 실행하는 서비스는 내 홈 폴더와 내 계정 소유의 저장소 폴더를 쓸 수 있습니다.
- `owngit` 계정은 `/home`, `/root`, `/run/user`를 보지 못하므로(`ProtectHome=yes`) 저장소 폴더가 이 계정의 것이어야 합니다. 새 폴더라면 설정 화면이 명령(`sudo install -d -o owngit -g owngit -m 0700 /srv/git`)을 보여 주고, `/opt`처럼 이미 있는 폴더라면 `/opt/owngit-repos`처럼 그 안의 새 폴더를 권합니다.
- OwnGit은 따로 `/tmp`를 씁니다(`PrivateTmp`). 체크 작업 공간은 상태 디렉터리에 있으므로 Docker 체크도 그대로 봅니다.
- 새 파일은 서비스 계정만 읽을 수 있습니다(`UMask=0077`).

유닛은 이 밖에도 `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `LockPersonality`, `RestrictRealtime`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, 빈 `CapabilityBoundingSet=`, `SystemCallArchitectures=native`, `ProtectProc=invisible`을 설정하는데, 모두 OwnGit이 쓰지 않는 것입니다. 사용자 서비스에는 `NoNewPrivileges`, `RestrictSUIDSGID`, `LockPersonality`, `RestrictRealtime`, `UMask=0077`만 겁니다. 어떤 설정을 허용하지 않는 컨테이너에서는 systemd가 그 설정을 빼고 OwnGit을 시작합니다.

### 상태 확인

`GET /healthz`는 OwnGit이 HTTP를 제공하는 동안 설정 전후와 관계없이 빈 본문의 `200 OK`로 응답하며 상태를 읽지 않습니다. 다른 경로와 마찬가지로 OwnGit이 받아들이는 Host 이름에만 응답하므로, 다른 기기의 모니터는 승인된 이름이나 주소를 써야 합니다. `owngit health`는 이 컴퓨터의 상태 디렉터리를 쓰는 서버를 확인하고 응답하면 종료 코드 0으로 끝납니다. `owngit service status`와 `install`도 같은 확인을 씁니다.

## 점검

`owngit doctor`는 이 컴퓨터에서 한 상태 디렉터리를 쓰는 OwnGit을 살펴봅니다. 찾은 문제마다 고치는 명령을 하나씩 출력하거나, 문제가 없다고 알려 줍니다. 프로그램, 상태 디렉터리, 저장소 폴더, 연결을 받는 주소, 서비스 로그 위치도 함께 보여 주며, `--json`을 붙이면 같은 내용을 JSON으로 출력합니다.

확인하는 항목은 다음과 같습니다.

- 이 상태 디렉터리의 서버가 실행 중이고 응답하는지(`owngit service start`, 서비스가 없으면 `owngit service install`), 그 주소에서 다른 프로그램이 대신 응답하고 있지 않은지, 설정을 마쳤는지(`owngit setup-link`)
- Windows에서 상태 디렉터리나 저장소 폴더의 소유자가 Administrators 그룹인지. OwnGit은 관리자 권한 없이 실행되므로 이런 폴더를 쓸 수 없습니다. 고치는 명령은 어느 계정이든 `owngit service install`입니다. 관리자 계정에서는 승인 한 번으로, 표준 계정에서는 관리자 암호를 한 번 입력하면 폴더를 돌려받습니다.
- OwnGit이 다른 기기의 연결을 받을 때 이 컴퓨터의 방화벽([다른 기기에서 서버에 접속하기](#다른-기기에서-서버에-접속하기) 참고). OwnGit은 Windows 방화벽 규칙과 네트워크 종류, "모두 차단" 설정, macOS 애플리케이션 방화벽을 읽습니다. ufw와 firewalld의 규칙은 root만 읽을 수 있어서 "확인하지 못함"으로 분류하고 OwnGit이 연결을 받는 사설 네트워크에서만 포트를 허용하는 명령을 함께 보여 줍니다. 그런 네트워크를 찾지 못하면 명령 대신 무엇을 허용할지 글로 알려 줍니다.

OwnGit이 직접 고치지는 않으니 명령은 이 컴퓨터에서 실행하세요. 점검 결과는 이 컴퓨터의 설정이 무엇을 하는지만 알려 줍니다. 공유기나 상대 기기는 볼 수 없습니다. 실행하지 못한 확인이나 OwnGit이 읽을 수 없는 설정은 문제가 없다고 하지 않고 "확인하지 못함"으로 표시합니다.

설정의 일반 탭도 같은 점검 결과를 보여 줍니다. 다만 이 컴퓨터의 경로가 드러나므로 관리자로 확인한 사람에게만 보입니다.

## 업데이트와 제거

`owngit update`는 이 설치를 업데이트하는 명령을 알려 줍니다. OwnGit이 어떻게 설치됐는지는 짐작하지 않고 이 컴퓨터에서 확인할 수 있는 사실로 판단합니다.

| 설치 방법 | 판단 근거 | 업데이트 명령 | 프로그램 지우기 |
| --- | --- | --- | --- |
| Homebrew | 프로그램이 Homebrew의 `Cellar/owngit` 안에 있습니다 | `brew upgrade owngit` | `brew uninstall owngit` |
| npm | 프로그램이 `node_modules` 안 `owngit-<플랫폼>` 패키지의 `bin/owngit`입니다 | `npm install -g owngit@X.Y.Z`. 내 계정이 전역 `node_modules` 폴더에 쓸 수 없으면 `sudo npm`으로 실행합니다 | `npm uninstall -g owngit`(쓸 수 없으면 `sudo`를 붙입니다) |
| Arch Linux 패키지 | root만 바꿀 수 있는 `/usr/bin/pacman`의 `-Qo`가 프로그램이 든 패키지를 알려 줍니다 | `owngit-bin`이면 새 임시 폴더에서 새 릴리스의 `PKGBUILD`를 `makepkg -si`로 빌드합니다. 다른 패키지라면 릴리스 `PKGBUILD`가 그 패키지를 바꿔 버리므로 명령을 보여 주지 않습니다. 설치한 방법 그대로 업데이트하세요 | `sudo pacman -R`과 패키지 이름 |
| 릴리스 압축 파일이나 [한 줄 설치](#한-줄-설치) | 위 어디에도 해당하지 않습니다 | 새 릴리스의 설치 스크립트를 이 프로그램에 대해 실행합니다. `/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/juliankang4/owngit/vX.Y.Z/packaging/installer/install.sh \| /bin/sh -s -- --version X.Y.Z --to <프로그램>`이고, Windows에서는 그 릴리스의 `install.ps1`을 `-Version X.Y.Z -Dir <폴더>`로 실행합니다. [업데이트 명령 자세히](#업데이트-명령-자세히)를 보세요 | 파일을 지웁니다(따로 만든 폴더에 풀었다면 그 폴더도) |

`owngit update`는 실행할 때마다 GitHub에 최신 릴리스를 묻습니다. 하루 한 번 하는 확인을 꺼 두었어도 마찬가지입니다. 결과로 릴리스, 설치 방법, 프로그램 경로, 명령을 출력하며 `owngit update --json`은 같은 내용을 JSON으로 출력합니다. 이 명령과 대시보드는 아무것도 직접 실행하지 않습니다.

출력된 명령은 서비스를 다음과 같이 다룹니다.

- 내 계정의 서비스가 이 프로그램을 실행하고 있으면 명령이 `owngit service install`도 실행해 서비스를 새 버전에 맞게 다시 쓰고 다시 시작합니다. 압축 파일이라면 이 명령은 설치 스크립트가 실행합니다. 그런 서비스가 없으면 명령에 `--no-service`(Windows에서는 `-NoService`)가 붙습니다.
- Homebrew 서비스는 `brew services restart owngit`으로 다시 시작합니다.
- 서비스가 다른 OwnGit을 실행하고 있다면(예를 들어 서비스는 압축 파일로 받은 것을 돌리는데 npm 쪽을 업데이트할 때) 명령은 이 프로그램만 업데이트하고 서비스는 건드리지 않습니다. `owngit update`가 그렇다고 알려 줍니다.
- 서비스 없이 실행 중이라면 업데이트한 뒤 OwnGit을 직접 다시 시작하세요. Windows에서 압축 파일로 업데이트했다면 명령이 알려 주는 새 폴더의 `owngit.exe`로 시작합니다.

### 업데이트 명령 자세히

- 릴리스 압축 파일이라면 설치 스크립트가 무엇이든 바꾸기 전에 압축 파일을 `SHA256SUMS`와 대조합니다. 내 계정이 프로그램 폴더에 쓸 수 없을 때만 `sudo`를 쓰므로 root가 소유한 프로그램은 계속 root 소유로 남습니다. root가 설치한 서비스에는 그래야 합니다. Windows에서는 새 릴리스를 지금 폴더 옆에 릴리스 이름으로 된 폴더에 풉니다.
- Windows에서 로그인 작업이 npm 프로그램을 직접 실행하고 있으면 명령이 `owngit service stop`으로 시작합니다. Windows에서는 실행 중인 프로그램 파일을 npm이 바꿀 수 없기 때문입니다. 그 뒤 npm이 실패했다면 `owngit service start`로 예전 버전을 다시 시작할 수 있습니다. npm 명령의 각 단계는 앞 단계가 성공했을 때만 실행되며 Windows PowerShell과 PowerShell 7 모두 같습니다.
- 릴리스 압축 파일이 없는 플랫폼용으로 빌드한 프로그램에는 명령이 없으며 `owngit update`가 새 릴리스를 소스에서 빌드하라고 알려 줍니다.
- `makepkg`는 root로 실행되지 않으므로 root에게도 Arch Linux 명령을 보여 주지 않습니다. 평소 쓰는 계정으로 `owngit update`를 실행하세요.

### 제거

`owngit uninstall`은 OwnGit이 `owngit service install`로 직접 만든 것만 지웁니다.

- 서비스 유닛, LaunchAgent나 예약 작업
- Homebrew로 설치했다면 `brew services`에 등록된 서비스(`brew services stop`으로)
- Windows에서는 `%ProgramFiles%\OwnGit`의 보호된 복사본과 방화벽 규칙

상태 디렉터리와 저장소는 절대 지우지 않고 위치를 알려 주므로, 나중에 다시 설치하면 그대로 이어서 씁니다. 데이터를 지우는 단계는 따로 없습니다. 서비스를 지우지 못하면 `owngit uninstall`은 그 오류로 끝나며 프로그램을 지우는 방법은 알려 주지 않습니다. 패키지 관리자가 설치한 파일은 그 관리자가 지울 몫이라 OwnGit은 남겨 두고 해당 명령을 알려 줍니다. 직접 가져다 둔 프로그램도 남겨 두고 지우는 명령을 알려 줍니다. `owngit service install`을 다시 실행하거나 같은 방법으로 다시 설치해도 상태와 저장소는 그대로 남습니다.

## 컨테이너로 실행하기

Docker Engine과 Docker Compose가 있는 Linux 컴퓨터에서는 OwnGit을 서비스 대신 컨테이너로 실행할 수 있습니다. 이미지는 x64와 ARM64용 `ghcr.io/juliankang4/owngit`입니다. 릴리스마다 `X.Y.Z`, `X.Y`, `latest` 태그가 붙고 빌드 출처 증명(provenance)이 함께 올라갑니다.

[`compose.yaml`](../packaging/container/compose.yaml)을 새 폴더에 저장하고 그 폴더에서 OwnGit을 시작한 뒤 설정 링크를 확인하세요.

```sh
docker compose up -d
docker compose exec -it owngit owngit setup-link
```

일회용 설정 링크는 터미널에서만 보이므로 `-it`로 터미널을 붙여 실행합니다. 터미널이 없으면 명령은 컨테이너 안 설정 파일의 경로만 알려 줍니다. `docker compose logs`로 보는 로그에도 그 경로만 남고 링크는 남지 않습니다. 링크 `http://localhost:7654/setup#...`는 15분 안에 한 번만 쓸 수 있습니다. 컨테이너를 실행하는 컴퓨터의 브라우저에서 여세요. 다른 기기에서 열 때는 `localhost` 자리에 그 컴퓨터의 이름이나 주소를 넣으세요(예: `http://nas.local:7654/setup#...`). 설정 페이지가 그 이름을 계속 받아들일지 묻습니다. 받아들이면 이후 다른 기기에서도 그 이름으로 접속합니다.

설정은 기본으로 공개 접속을 제안합니다. 공개 접속에서는 OwnGit에 접속할 수 있는 누구나 비밀번호 없이 들어옵니다. 공개 접속으로 쓴다면 컨테이너를 실행하는 컴퓨터에서도 그 컴퓨터의 이름이나 주소로 설정을 마치세요. 설정을 마친 뒤에는 컨테이너 안의 OwnGit이 컨테이너 밖에서 온 `localhost`를 거부합니다. 그 이유는 [localhost와 다른 주소](#localhost와-다른-주소)에 있습니다.

컨테이너 안에서 `owngit health`가 응답을 받으면 `docker compose ps`에 `healthy`로 표시됩니다. 다른 명령도 같은 방식으로 실행합니다. 예: `docker compose exec owngit owngit doctor`

이미지에는 Debian 13 위에 릴리스의 `owngit` 프로그램과 Git이 들어 있습니다. OwnGit은 root가 아닌 `owngit` 계정(사용자와 그룹 ID 10001)으로 실행됩니다. 특권 모드, Linux 권한(capability), Docker 소켓, 호스트 네트워크는 필요하지 않으며, `compose.yaml`은 모든 권한을 빼고 새 권한을 얻지 못하게 막습니다.

### 데이터 위치

모든 데이터는 `/data`에 연결된 볼륨 `owngit-data`에 있습니다.

- 상태: `/data/owngit`
- 저장소: `/data/OwnGit-Repositories`(설정에서 제안하는 폴더)
- [업그레이드 전 백업](#업그레이드-전-백업): `/data/owngit-backups`

컨테이너를 다시 시작하거나 새로 만들거나 업데이트해도 볼륨은 그대로입니다. `docker compose down`은 컨테이너만 지우고 볼륨은 남깁니다. `docker compose down -v`는 볼륨을 지우며 그 안의 저장소도 모두 사라집니다.

[상태 디렉터리](#상태-디렉터리)는 로컬 디스크에 있어야 하므로 `/data`도 로컬 디스크에 두세요. 저장소를 네트워크 공유 폴더에 두려면 공유 폴더를 컨테이너 안의 다른 경로(예: `/repositories`)에 연결하고 설정에서 그 폴더를 고르세요.

### localhost와 다른 주소

OwnGit은 컨테이너 안의 모든 주소에서 연결을 받습니다. 누가 접속할 수 있는지는 `compose.yaml`의 `ports` 줄이 정합니다. `"7654:7654"`는 컨테이너를 실행하는 컴퓨터의 모든 주소에 포트를 엽니다. `"127.0.0.1:7654:7654"`로 쓰면 그 컴퓨터에서만 접속할 수 있는데, 이때 `localhost`는 아래 설명처럼 접속에 공용 비밀번호가 필요할 때만 쓸 수 있습니다. 다른 포트를 쓰려면 앞의 숫자를 바꾸고(예: `"8080:7654"`) 설정 링크에서도 그 포트를 쓰세요.

컨테이너 안의 OwnGit은 컨테이너 밖에서 온 `localhost`, `127.0.0.1`, `::1`을 접속에 공용 비밀번호가 필요할 때만 받아들입니다. 이 경우에는 어차피 모든 방문자가 로그인해야 합니다. 기본값인 공개 접속에서는 이 이름을 거부하고, 그 컴퓨터의 이름이나 주소를 쓰라는 페이지를 보여 줍니다. 컨테이너를 실행하는 컴퓨터에서 접속해도 마찬가지입니다. 컨테이너 안에서는 접속한 주소만으로 그 컴퓨터와 다른 기기를 구별할 수 없기 때문입니다. Docker는 IPv6 연결을 컨테이너 네트워크 안의 주소에서 전달하고 rootless Docker는 모든 연결을 그렇게 전달합니다.

공개 접속으로 쓰려면 그 컴퓨터의 이름이나 주소로 설정을 마치고 그 이름을 계속 받아들이도록 하세요. 나중에 이름을 허용하려면 저장한 뒤 컨테이너를 다시 시작합니다.

```sh
docker compose exec owngit owngit network set --allowed-host nas.local
docker compose restart
```

설정을 마치기 전에는 다른 OwnGit과 마찬가지로 설정 링크만 쓸 수 있습니다.

OwnGit은 틀린 비밀번호를 주소별로 세며 기본값으로는 한 주소에서 10분 안에 4번 틀리면 그 주소를 15분 동안 멈춥니다([로그인 시도 한도](#로그인-시도-한도)). 일반 Docker의 IPv4 연결은 기기마다 주소가 유지되므로 기기별로 따로 셉니다. Docker의 프록시를 거치는 IPv6 연결과 rootless Docker의 모든 연결은 한 주소에서 들어옵니다. 이때는 이 기기들의 틀린 비밀번호를 합쳐서 셉니다. 그래서 네트워크의 누구든 4번 틀리면 올바른 비밀번호를 아는 사람까지 모두 15분 동안 로그인할 수 없습니다. 멈춤은 시간이 지나면 저절로 풀립니다.

### 업데이트

새 릴리스가 나오면 대시보드 알림과 `owngit update`가 컨테이너용 명령을 보여 줍니다.

```sh
docker compose pull && docker compose up -d
```

컨테이너를 실행하는 컴퓨터의 `compose.yaml`이 있는 폴더에서 실행하세요. 새 이미지를 내려받아 그 이미지로 컨테이너를 새로 만들며 볼륨은 그대로 남습니다. OwnGit은 [업그레이드 전 백업](#업그레이드-전-백업)에 나온 대로 상태를 업그레이드하기 전에 백업합니다.

직접 만든 백업도 남기려면 OwnGit을 멈추고 백업한 뒤 업데이트하고 백업을 검증하세요.

```sh
docker compose stop
docker compose run --rm owngit owngit backup --output /data/backup-before-update
docker compose pull
docker compose up -d
docker compose exec owngit owngit backup verify /data/backup-before-update
```

출력 폴더는 아직 없어야 하므로 매번 새 이름을 쓰세요. 볼륨 안의 백업은 볼륨을 지우면 함께 지워집니다. 다른 곳에 남기려면 `owngit` 계정(ID 10001)이 소유하고 다른 계정은 바꿀 수 없는 폴더를 연결해 그곳에 백업하세요.

### 다른 계정으로 실행하기

컴퓨터에 있는 폴더의 소유자에 맞추는 경우처럼 OwnGit을 다른 계정으로 실행하려면 `compose.yaml`에 `user:`를 지정하고 `/data`를 그 계정이 소유하며 다른 계정은 바꿀 수 없는 폴더로 두세요.

```yaml
services:
  owngit:
    user: "1000:1000"
    volumes:
      - ./owngit-data:/data
```

그 폴더는 `compose.yaml`이 있는 폴더에서 먼저 만듭니다.

```sh
sudo install -d -o 1000 -g 1000 -m 700 owngit-data
```

기본 `compose.yaml`의 이름 있는 볼륨은 `owngit` 계정이 소유하므로 다른 계정은 쓸 수 없습니다. 다른 계정이 바꿀 수 있는 `/data` 폴더는 OwnGit이 거부하며 로그에 그 문제를 고치는 `chmod` 명령을 알려 줍니다.

## Proxmox VE에서 실행하기

Proxmox VE 호스트에서는 명령 하나로 OwnGit용 권한 없는(unprivileged) Debian 13 컨테이너(LXC)를 만들고 그 안에 OwnGit을 서비스로 설치할 수 있습니다. 호스트의 셸(예: Proxmox 웹 화면에서 노드의 Shell)에서 root로 실행하세요.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh
```

스크립트는 다음 순서로 진행합니다.

1. 호스트에 `debian-13-standard` 템플릿이 없으면 `pveam`으로 최신 템플릿을 내려받습니다. `pveam`은 Proxmox의 서명된 템플릿 목록과 대조해 확인합니다.
2. 비어 있는 다음 ID로 `owngit`이라는 이름의 권한 없는 Debian 13 컨테이너를 만듭니다. 코어 2개, 메모리 1024MB, 스왑 512MB, `local-lvm`(없으면 `local-zfs`)에 8GB 디스크를 두고 브리지 `vmbr0`에 DHCP로 연결합니다. 컨테이너에는 `owngit` 태그가 붙고 `nesting` 기능이 켜지며, 호스트가 켜질 때 함께 시작합니다. 권한 없는 컨테이너에서 Debian 13의 systemd가 제대로 돌려면 이 기능이 필요합니다.
3. 컨테이너 안에 Git과 설치 스크립트에 필요한 도구를 설치하고 패키지를 업데이트한 뒤 릴리스의 `install.sh`를 실행합니다. [한 줄 설치](#한-줄-설치)에서 설명한 대로 설치 스크립트는 압축 파일을 `SHA256SUMS`와 대조하고 프로그램을 `/usr/local/bin/owngit`에 둔 뒤 root로 `owngit service install`을 실행합니다. 그래서 서비스는 `owngit` 계정으로 실행되고 상태는 `/var/lib/owngit/state`에 있습니다([root로 설치하기](#root로-설치하기) 참고).
4. `--repositories`를 주면 호스트의 폴더를 저장소 폴더로 OwnGit에 연결합니다(아래 참고).
5. OwnGit이 응답할 때까지 기다린 뒤 컨테이너 주소와 포트 7654를 보여 줍니다. 터미널에서 실행했다면 설정 링크도 보여 주고 아니면 링크를 보여 주는 명령을 알려 줍니다.

이 스크립트 말고는 OwnGit 릴리스에서 받은 것이 호스트에서 실행되지 않습니다. `install.sh`와 OwnGit은 권한 없는 컨테이너 안에서만 실행됩니다. OwnGit이 응답하기 전에 단계가 실패하면 스크립트는 자신이 만든 컨테이너를 지우고 저장소 폴더를 원래대로 돌려놓습니다. 스스로 만든 폴더는 지우고 원래 있던 폴더는 소유자와 모드를 되돌립니다. 내려받은 템플릿은 남습니다.

옵션은 `/bin/sh -s --` 뒤에 붙입니다.

```sh
/usr/bin/curl --proto '=https' --proto-redir '=https' -fsSL https://owngit.app/proxmox.sh | /bin/sh -s -- --repositories /tank/owngit
```

| 옵션 | 하는 일 |
| --- | --- |
| `--id N` | 비어 있는 다음 ID 대신 이 컨테이너 ID를 씁니다. |
| `--hostname NAME` | 컨테이너 이름을 `owngit` 대신 NAME으로 합니다. |
| `--storage NAME` | 컨테이너 디스크를 이 스토리지에 둡니다. |
| `--disk GB`, `--cores N`, `--memory MB` | 디스크 크기, CPU 코어 수, 메모리를 정합니다. |
| `--bridge NAME` | `vmbr0` 대신 이 브리지에 연결합니다. |
| `--ip ADDRESS/PREFIX`, `--gateway ADDRESS` | DHCP 대신 `192.168.1.50/24` 같은 고정 IPv4 주소와 게이트웨이를 줍니다. 둘 다 주어야 합니다. |
| `--repositories FOLDER` | 저장소를 호스트의 FOLDER에 둡니다. |
| `--template VOLUME` | 이미 있는 Debian 13 템플릿을 씁니다. 예: `local:vztmpl/debian-13-standard_13.6-1_amd64.tar.zst` |
| `--version X.Y.Z` | 최신 릴리스 대신 그 릴리스(1.1.3 이상)를 설치합니다. |

스크립트는 이미 있는 컨테이너를 바꾸지 않습니다. 클러스터에 같은 이름의 컨테이너가 있거나, 같은 ID의 컨테이너나 가상 머신이 있으면 멈춥니다. 그 컨테이너가 `owngit` 태그가 붙은 OwnGit 컨테이너라면 아무것도 바꾸지 않고 업데이트 방법을 알려 줍니다.

컨테이너 안의 OwnGit 명령은 `pct exec`에 스크립트가 알려 준 컨테이너 ID(아래 예에서는 105)를 붙여 실행합니다. `pct exec`는 `/usr/local/bin`에서 프로그램을 찾지 않으므로 전체 경로를 쓰세요.

```sh
pct exec 105 -- /usr/local/bin/owngit service status
```

### 저장소를 호스트 폴더에 두기

`--repositories /tank/owngit`을 주면 저장소는 호스트의 `/tank/owngit`(예: ZFS 데이터셋)에 남습니다. 컨테이너 안에서는 이 폴더가 설정에서 제안하는 폴더인 `/var/lib/owngit/OwnGit-Repositories`로 보이므로, 설정에서 제안된 폴더를 그대로 쓰세요.

- 폴더는 새 폴더이거나 비어 있어야 하고 바로 위 폴더는 있어야 합니다. 경로에 링크가 끼어 있으면 안 됩니다.
- 그 위의 모든 폴더와, 폴더가 이미 있다면 그 폴더도 root 소유여야 하고 root만 바꿀 수 있어야 합니다. 그렇지 않으면 호스트의 다른 계정이 폴더를 다른 곳으로 돌려놓거나, 컨테이너가 넘겨받기 전에 파일을 넣어 둘 수 있습니다. OwnGit용으로 만든 폴더라면 `chown root:root FOLDER && chmod go-w FOLDER`로 맞출 수 있습니다.
- 권한 없는 컨테이너에서는 컨테이너 안 계정이 호스트에서 다른 ID(보통 100000을 더한 값)로 보입니다. 스크립트는 컨테이너의 `owngit` 계정이 호스트에서 갖는 ID에게 폴더를 넘기고 모드를 0700으로 하며 호스트의 다른 것은 바꾸지 않습니다.
- 폴더를 붙이려고 스크립트는 OwnGit을 설치한 뒤 컨테이너를 한 번 멈췄다가 다시 시작합니다.
- Proxmox 백업(`vzdump`)에는 호스트 폴더가 들어가지 않습니다. 저장소는 [`owngit backup`](#백업)이나 호스트 자체 백업으로 백업하세요. 호스트 폴더가 붙은 컨테이너는 다른 노드로 옮길(migrate) 수 없습니다.
- 기존 저장소는 설정을 마친 뒤 가져오기(import)나 백업 복원으로 들여오세요. 스크립트는 내용이 이미 있는 폴더를 넘겨받지 않습니다.

### 업데이트와 컨테이너 삭제

`pct exec 105 -- /usr/local/bin/owngit update`는 OwnGit을 업데이트하는 명령을 보여 줍니다. 그 명령은 `pct enter 105`로 연 컨테이너 셸에서 실행하세요. 컨테이너가 멈춰 있으면 먼저 `pct start 105`로 시작합니다. [업데이트와 제거](#업데이트와-제거)에서 설명한 대로 새 릴리스의 설치 스크립트가 실행되고 서비스는 새 버전으로 다시 시작합니다.

`pct destroy 105`는 컨테이너를 상태와 컨테이너 안의 저장소까지 함께 지웁니다. `--repositories`로 준 호스트 폴더는 남습니다. 먼저 백업하세요.

## 설정 화면

설정 화면은 탭 다섯 개로 나뉩니다. 탭마다 주소가 따로 있어 JavaScript 없이도 일반 링크처럼 열립니다.

- **일반** (`/settings`): 이 브라우저의 화면 설정(언어, 화면 모드, 저장소 목록 정렬)은 고르는 즉시 적용되며 비밀번호를 묻지 않습니다. 서버 전체에 적용되는 새 릴리스 [업데이트 확인](#새-릴리스-알림)도 여기 있습니다.
- **접근 권한** (`/settings/access`): OwnGit에 접속할 수 있는 누구나 읽고 푸시할지, 공용 비밀번호를 아는 사람만 그렇게 할지 정합니다. [로그인이 얼마나 유지될지](#로그인-유지-시간), [다른 사이트의 링크](#다른-사이트의-링크)를 어떻게 다룰지, [로그인 시도 한도](#로그인-시도-한도), 관리자 비밀번호, [관리자 비밀번호를 얼마나 자주 물을지](#관리자-비밀번호-확인)도 여기서 정합니다.
- **네트워크** (`/settings/network`): 이 브라우저의 연결, [네트워크 설정](#네트워크-설정), [tailnet 공유](#tailnet에서-https로-공유하기)가 있습니다.
- **저장소** (`/settings/repositories`): [새 저장소의 첫 브랜치](#기본-브랜치-바꾸기), 덮어쓰거나 지운 기록을 [보관할지](#보관된-기록), [Git 전송 한도](#git-전송-제한), [보기 한도](#보기-한도), [체크 상한](#체크-상한), [저장소를 삭제할 때](#저장소-삭제하기) 이름을 물을지 정하고, 저장소마다 그 저장소의 설정으로 가는 링크가 있습니다.
- **보관과 복구** (`/settings/storage`): 저장소 폴더와 [백업](#대시보드에서-백업-관리하기)이 있으며(둘 다 관리자에게만 보입니다), [체크 원본 로그](#체크-원본-로그)를 얼마나 보관할지, [저장소 유지 관리](#저장소-유지-관리)와 [쓰지 않는 객체 정리](#쓰지-않는-객체-정리)를 어떻게 할지 정합니다.

탭 안의 항목마다 저장과 취소가 따로 있고 저장하면 그 항목만 보냅니다. 이 브라우저가 관리자로 확인되어 있지 않고 확인도 꺼 두지 않았다면 저장할 때 관리자 비밀번호를 묻습니다. 네트워크 설정은 다음에 시작할 때 적용되고 나머지는 저장하는 즉시 적용됩니다.

- OwnGit이 저장을 거부하면 그 항목에 이유가 나오고 비밀번호를 뺀 입력 내용은 그대로 남습니다. 다만 화면을 연 뒤에 네트워크 설정이 바뀌었다면 네트워크 항목은 지금 저장된 값을 보여 주므로, 값을 확인하고 바꿀 내용을 다시 입력하세요.
- 공용 비밀번호가 이미 켜져 있을 때 새 공용 비밀번호를 비워 두면 지금 비밀번호가 유지됩니다.
- 공용 비밀번호를 켜거나 바꾸면 공용 비밀번호로 로그인한 사람은 모두 로그아웃됩니다. 관리자로 확인되지 않은 브라우저는 새 비밀번호로 다시 로그인합니다.
- 관리자 비밀번호 변경은 언제나 현재 비밀번호를 묻는 별도 양식입니다.

### 저장하지 않은 변경이 있을 때

저장하지 않은 변경이 있는 채로 다른 페이지로 가거나 페이지를 다시 불러오는 다른 항목을 저장하면 설정 화면이 먼저 어떻게 할지 묻습니다. 비밀번호를 묻는 저장은 페이지를 다시 불러오고 비밀번호 없이 하는 저장은 페이지와 다른 항목의 입력 내용을 그대로 둡니다.

확인 창은 바뀐 내용을 항목마다 보여 주며 비밀번호는 ‘입력됨’으로만 표시합니다. 고를 수 있는 것은 저장하고 나가기, 저장하지 않고 나가기, 계속 편집입니다. Esc는 계속 편집과 같습니다. 화면 설정은 저장하지 않은 변경으로 치지 않습니다.

- 저장하고 나가기는 항목을 차례로 저장하고 모두 저장된 뒤에만 나갑니다. 저장이 거부된 항목이 있으면 이 페이지에 그대로 머뭅니다. 이미 저장한 항목은 저장된 값으로 보이고 나머지 항목에는 입력한 내용이 남습니다.
- 비밀번호 칸이 있는 항목은 나가면서 하나만 저장할 수 있고 링크나 뒤로 가기로 다른 OwnGit 페이지로 갈 때만 가능합니다. 그 항목에 관리자 비밀번호가 필요하면 확인 창에서 묻습니다. 확인 창에 ‘따로 저장’ 표시가 붙은 항목은 그 항목의 저장 버튼으로 저장하세요.
- 새로 고침, 탭 닫기, 뒤로 가기로 다른 사이트에 갈 때는 브라우저의 확인 창이 대신 뜹니다.

### 명령줄에서 설정 바꾸기

일반, 접근 권한, 저장소, 보관과 복구 탭에서 바꾸는 설정은 브라우저 화면 설정과 OwnGit 아이콘을 빼고 모두 `owngit settings`로도 바꿀 수 있습니다. 이 명령은 관리자 API를 쓰므로 명령마다 `--server`와 관리자 비밀번호가 든 `--password-file`이 필요하고 결과는 JSON으로 출력합니다.

- `owngit settings show`는 아래에 나열한 저장된 설정과 함께 접근 방식(`access_mode`, `open` 또는 `password`), 대시보드가 관리자 비밀번호를 묻는 주기(`admin_confirmation`), 새 릴리스 확인(`update_check`)을 출력합니다. 서버를 `--no-update-check`로 시작했다면 `update_check_forced_off`가 true입니다.
- `owngit settings set`은 `--session 7d`나 `--update-check off`처럼 옵션으로 지정한 설정만 바꿉니다.
- `owngit settings access --mode password`는 공용 비밀번호를 켜거나 바꿉니다. `--mode open`은 공용 비밀번호를 꺼서 OwnGit에 접속할 수 있는 누구나 읽고 푸시하게 합니다.
- `owngit settings admin-password`는 관리자 비밀번호를 바꿉니다.
- `owngit settings confirmation --choice CHOICE`는 대시보드가 관리자 비밀번호를 얼마나 자주 물을지 정합니다. `every`, `30m`, `1h`, `8h`, `1d`, `7d`, `30d`, `never`(묻지 않기) 중에서 고릅니다. `never`를 고를 때는 설정 화면과 같은 경고에 동의한다는 뜻으로 `--acknowledge-no-ask`도 붙여야 합니다.

```sh
owngit settings set --server http://127.0.0.1:7654 --accept-insecure-http \
  --password-file /path/to/admin-password --update-check off
```

`--accept-insecure-http`는 그 명령 한 번에 한해 암호화되지 않은 일반 HTTP를 받아들인다는 뜻입니다. 서버 주소가 `https://`로 시작하면 빼세요.

새 비밀번호는 명령줄 값으로 받지 않습니다. `settings access`는 `--access-password-file`로, `settings admin-password`는 `--new-password-file`로 넘긴 파일에서 읽으며 이 파일은 본인만 읽을 수 있어야 합니다([비밀번호 파일과 토큰 파일](#비밀번호-파일과-토큰-파일) 참고). 옵션을 빼면 입력한 내용이 화면에 보이지 않는 프롬프트에서 비밀번호를 두 번 묻습니다. 이렇게 묻는 것은 터미널에서 실행할 때뿐입니다. 너무 짧은 비밀번호나 다른 쪽 비밀번호와 같은 비밀번호처럼 설정 화면이 거부하는 비밀번호는 명령도 거부합니다. 공용 비밀번호를 바꾸면 이전 비밀번호로 로그인한 사람은 모두 로그아웃됩니다. 관리자 비밀번호를 바꾸면 모든 브라우저의 관리자 확인이 끝납니다. 이전 비밀번호를 파일에서 읽던 명령은 그 파일에 새 비밀번호를 넣을 때까지 실패합니다.

대시보드에서는 다음 설정의 저장된 값을 관리자로 확인한 사람에게만 보여 줍니다. [로그인 유지 시간](#로그인-유지-시간), [다른 사이트의 링크](#다른-사이트의-링크), [로그인 시도 한도](#로그인-시도-한도), [새 저장소의 첫 브랜치](#기본-브랜치-바꾸기), 서버 전체의 [보관된 기록](#보관된-기록) 선택, [Git 전송 한도](#git-전송-제한), [보기 한도](#보기-한도), [체크 상한](#체크-상한), [저장소를 삭제할 때](#저장소-삭제하기) 이름을 물을지, [체크 원본 로그](#체크-원본-로그) 보관 기간, [저장소 유지 관리](#저장소-유지-관리), [쓰지 않는 객체 정리](#쓰지-않는-객체-정리)입니다. 그 밖의 사람이 설정 화면을 열면 각 설정이 하는 일과 관리자 확인 버튼만 보입니다.

이 JSON과 관리자 API `/api/v1/settings`에서 Git 전송 한도는 `git_transfer` 묶음입니다. 보기 한도, 체크 상한, 저장소 유지 관리, 쓰지 않는 객체 정리는 각각 `browse_limits`, `check_ceilings`, `maintenance`, `unused_object_cleanup`입니다. 묶음의 일부 필드만 바꾸면 나머지 필드는 그대로 남습니다.

상태 데이터베이스를 직접 고치는 등의 이유로 저장된 설정을 읽을 수 없으면 `show`는 실패하면서 그 설정을 알려 줍니다. `set`은 지정한 설정을 그대로 저장하고 읽을 수 없는 설정은 `unreadable`에 적어 줍니다. 그 설정을 다시 정하면 바로잡힙니다. 그동안에는 그 설정에 기대는 기능만 멈추며 안내 메시지에 설정 이름이 나옵니다.

- 서버 전체의 보관된 기록 선택: 서버 설정을 따르는 모든 저장소로 오는 푸시와 가져오기를 거부합니다.
- Git 전송 한도: Git 요청을 거부합니다.
- 보기 한도: 파일, 차이(diff), 비교를 보여 주지 않습니다.
- 체크 상한: 체크 설정을 저장하지 않고 새 체크를 대기열에 넣지 않습니다.
- 저장소 유지 관리: 어떤 저장소도 유지 관리하지 않습니다.
- 쓰지 않는 객체 정리: 객체를 지우지 않고 유지 관리는 정리 없이 계속합니다.

`settings set`과 `settings confirmation`으로 바꾸는 설정은 이 설치 호스트에 속하며 백업에 들어가지 않으므로, 복원한 설치는 기본값으로 시작합니다. 접근 방식과 두 비밀번호는 백업에 들어갑니다.

네트워크 탭과 OwnGit 아이콘은 설치 호스트에서 실행하는 명령이 따로 있습니다. `owngit network`와 `owngit tailscale`([네트워크 설정](#네트워크-설정)), `owngit tray on`과 `owngit tray off`([OwnGit 아이콘](#owngit-아이콘))입니다.

### 대시보드나 명령줄 한쪽에만 있는 작업

설정 화면과 저장소 페이지에서 소유자가 하는 일은 모두 명령으로도 할 수 있습니다. 이 명령은 대부분 JSON을 출력하며 일부는 `--json`을 붙였을 때만 출력합니다. 텍스트로만 출력하는 명령은 아래에 나오는 설치 호스트 복구 명령, 프로세스를 실행하거나 제어하는 명령, `uninstall`입니다. `--json`을 붙인 명령은 상태를 가진 계정으로 실행을 시작하지 못할 때도 JSON 오류를 출력합니다. Windows의 관리자 터미널에서 관리자 권한 없는 복사본을 시작하지 못하면 `privilege_drop_failed`, Linux에서 서비스 계정으로 실행할 수 없으면 `state_unavailable`입니다.

다음 일만 일부러 한쪽에만 둡니다.

- 명령줄에만 있음: `owngit reset-admin`, `owngit setup-link`, `owngit approve-host`는 대시보드를 쓸 수 없을 때 설치 호스트에서 접근을 되찾는 명령입니다([설치 호스트에서 복구하기](#설치-호스트에서-복구하기), [호스트 이름](#호스트-이름) 참고). `owngit uninstall`은 대시보드를 제공하는 서비스 자체를 지웁니다([제거](#제거) 참고). `owngit restore`는 새 상태 디렉터리와 저장소 폴더를 만듭니다. 그래서 대시보드를 제공하는 설치를 바꾸려면 먼저 그 설치를 멈춰야 합니다. 대시보드는 복원을 실행하지 않고 [복원 순서](#대시보드의-복원-안내)를 보여 줍니다. `owngit backup --output`은 그 상태 디렉터리로 OwnGit이 실행 중이면 거부합니다. 실행 중인 OwnGit은 대시보드의 지금 백업으로 백업합니다.
- 대시보드에만 있음: 브라우저에 나오는 일반 HTTP 경고에 동의하는 일입니다. 그 브라우저 자신의 연결에 관한 경고라서 대시보드에만 둡니다. `settings`나 `repo` 같은 클라이언트 명령은 실행할 때마다 `--accept-insecure-http`로 그 명령이 쓰는 일반 HTTP에 따로 동의합니다. `owngit network set --accept-insecure-http`는 다른 컴퓨터가 접속하는 주소를 저장할 때 대시보드와 같은 동의를 한 번 기록합니다([네트워크 설정](#네트워크-설정) 참고).
- 실행되는 프로그램: `owngit serve`는 OwnGit을 실행하고, `owngit service`는 서비스로 설치하고 제어하며, `owngit runner`는 자동 체크를 실행하고, `owngit mcp`는 코딩 도구에 MCP 서버를 제공합니다. 프로세스를 시작하거나 제어하는 명령이라 대시보드 양식이 없습니다.
- 코딩 도구의 기록: 작업(task), 수정 라운드, 시도(`owngit check task new`, `check cycle reserve`, `check run`)는 코딩 도구가 남기는 증거입니다([코딩 도구](CODING_TOOLS.ko.md) 참고). 대시보드는 이 기록을 보여 주기만 하고 만들지는 않습니다. `owngit tasks`는 대시보드에 보이는 이 기록을 출력합니다.

### 로그인 유지 시간

접근 권한 탭의 "로그인 유지 시간"에서 공용 비밀번호로 로그인한 브라우저가 얼마나 로그인된 채로 있을지 정합니다. 1시간, 8시간, 12시간(기본값), 1일, 7일, 30일 중에서 고릅니다.

새 시간은 저장한 뒤 로그인할 때부터 쓰이고 이미 로그인한 브라우저는 원래 끝나는 시각을 그대로 씁니다. 모든 로그인을 지금 끝내려면 공용 비밀번호를 바꾸세요. 시간을 길게 할수록 로그인된 브라우저를 쓰는 사람이 비밀번호 없이 대시보드를 쓸 수 있는 시간도 길어집니다. 누구나 비밀번호 없이 접속하게 열어 두었다면 로그인할 일이 없으니 이 시간도 쓰이지 않습니다.

### 다른 사이트의 링크

채팅이나 웹메일, 다른 사이트에서 OwnGit 링크를 열 때 공용 비밀번호 로그인을 이어 쓸지는 접근 권한 탭의 "다른 사이트의 링크"에서 정합니다.

- **새로 열기 요구**(기본값): 링크를 열면 로그인 페이지나 로그인하지 않은 상태의 화면이 나옵니다. OwnGit 안에서 그 페이지를 다시 열면 로그인한 상태로 보입니다.
- **로그인 유지**: 링크를 열어도 이 브라우저의 공용 로그인이 그대로 쓰입니다. 채팅이나 메일의 링크를 바로 열고 싶을 때 고르세요.

어느 쪽을 골라도 다른 사이트의 링크로 열면 관리자 확인은 유지되지 않고 다른 사이트에서 보낸 양식은 OwnGit이 거부합니다. 저장하면 이 브라우저에는 바로 적용되고 다른 브라우저에는 다음 로그인부터 적용됩니다. 명령줄에서는 `owngit settings set --cross-site-links strict`나 `--cross-site-links lax`를 실행합니다. 누구나 비밀번호 없이 접속하게 열어 두었다면 로그인할 일이 없으니 이 선택도 쓰이지 않습니다.

저장된 값을 읽을 수 없으면 아무도 공용 비밀번호로 로그인할 수 없고 로그인 페이지에 어떤 설정이 문제인지 나옵니다. 설정의 접근 권한 탭이나 `owngit settings set --cross-site-links`로 다시 정하세요.

### 로그인 시도 한도

OwnGit은 비밀번호를 너무 많이 틀린 주소를 일정 시간 멈춥니다. 기본값으로는 한 주소에서 10분 안에 4번 틀리면 그 주소를 15분 동안 멈춥니다. 멈춘 동안에는 맞는 비밀번호로도 그 주소에서 로그인할 수 없습니다. 멈춤은 시간이 지나면 저절로 풀립니다. 맞는 비밀번호는 횟수에 넣지 않습니다.

이 한도는 대시보드, Git, API에 모두 적용됩니다. 공용 비밀번호와 관리자 비밀번호는 틀린 횟수를 따로 셉니다.

한도를 바꾸려면 접근 권한 탭의 로그인 시도에서 한도 바꾸기를 열거나 다음 명령을 실행하세요.

```sh
owngit settings set --login-attempts 4 --login-window 10m --login-pause 15m
```

- 틀린 비밀번호 횟수는 1부터 100까지, 세는 기간과 멈추는 시간은 각각 1분부터 24시간까지 정할 수 있습니다. 한도를 끌 수는 없습니다.
- 새 한도는 저장한 뒤 틀린 비밀번호부터 적용됩니다. 이미 멈춘 주소는 원래 끝나는 때까지 멈춰 있습니다.
- 횟수를 늘리거나 기간이나 멈추는 시간을 줄이면 OwnGit에 접속할 수 있는 사람이 비밀번호를 더 많이 시도할 수 있습니다. 설정 화면은 저장하기 전에 이를 알려 주고 `owngit settings set`은 `warnings`에 적어 줍니다.
- 프록시를 신뢰하도록 설정하기 전에는 같은 프록시를 거쳐 접속하는 사람이 OwnGit에 모두 프록시 주소로 보이므로 함께 멈춥니다. 한도를 느슨하게 하기 전에 먼저 네트워크 탭의 신뢰하는 프록시에 그 프록시를 추가하세요([리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기)). 그러면 사람마다 자기 주소로 셉니다.

멈춘 동안 Git과 API는 HTTP 429(Too Many Requests)를 받고 `Retry-After` 헤더에는 남은 시간이 초 단위로 들어 있습니다. Git은 비밀번호가 틀렸을 때의 인증 실패 대신 429를 받으므로 자격 증명 도우미에 저장해 둔 비밀번호가 지워지지 않습니다.

상태 데이터베이스를 직접 고치는 등의 이유로 저장된 한도를 읽을 수 없어도 맞는 비밀번호로는 로그인할 수 있습니다. 틀린 비밀번호는 횟수에 넣지 못한 채 거부되며 한도를 바로잡는 명령이 메시지에 나옵니다. 세 값은 설정의 접근 권한 탭이나 위 명령으로 한꺼번에 다시 정하세요. 저장된 한도를 읽을 수 없는 동안에는 하나나 두 개만 정하면 실패합니다.

### 관리자 비밀번호 확인

접근 권한 탭의 "관리자 비밀번호 묻기"에서 대시보드가 이 비밀번호를 언제 물을지 정합니다. 설정, 저장소 설정, 삭제, 가져오기, 체크, 체크 에이전트 토큰과 러너 토큰 관리에 모두 적용됩니다.

- **매번 묻기**: 변경할 때마다 묻습니다. 관리자로 로그인하면 관리자 페이지를 잠깐 열 수만 있습니다.
- **30분 뒤에 다시 묻기**(기본값), **1시간**, **8시간**, **하루**, **7일**, **30일**: 관리자 로그인 화면이나 양식에서 비밀번호를 입력하면 이 브라우저에서는 그동안 다시 묻지 않습니다. 시간은 입력한 때부터 재며 페이지를 옮겨 다녀도 늘어나지 않습니다. 다른 브라우저는 따로 입력해야 합니다. 사이드바에 언제까지 관리자로 확인되어 있는지 나오고 종료 버튼으로 바로 끝낼 수 있습니다. 로그아웃하거나 종료를 누르거나 관리자 비밀번호를 바꾸거나 재설정하면 확인이 끝납니다. 더 짧은 시간을 고르면 비밀번호를 입력한 때부터 새 시간만큼으로 줄어들므로, 바로 끝날 수도 있습니다.
- **묻지 않기**: 대시보드에 들어올 수 있는 사람이면 누구나 관리자 비밀번호 없이 설정을 바꾸고, 저장소를 삭제하고, 자격 증명을 발급하고, 자동 체크를 켤 수 있습니다. 누구나 접속할 수 있게 열어 두었다면 로그인도 필요 없습니다. 켤 때는 비밀번호를 마지막으로 한 번 묻고 경고를 확인했다는 체크도 받습니다. 켜 둔 동안에는 모든 페이지에 "관리자 비밀번호 확인 꺼짐"이 나오며 누르면 이 설정으로 돌아옵니다.

명령줄에서는 `owngit settings confirmation`으로 같은 선택을 합니다([명령줄에서 설정 바꾸기](#명령줄에서-설정-바꾸기) 참고).

더는 가지고 있지 않은 브라우저의 확인을 끝내려면 설정 화면이나 `owngit settings admin-password`로 관리자 비밀번호를 바꾸거나, 설치 호스트에서 `owngit reset-admin`을 실행하세요. 어느 쪽이든 모든 브라우저의 확인이 끝납니다.

명령줄과 관리자 API는 이 설정과 관계없이 조회와 변경 모두 언제나 관리자 비밀번호를 묻습니다. 대시보드에서 관리자로 확인된 브라우저라도 API에는 로그인되어 있지 않습니다.

이 설정은 이 설치 호스트에 속하며 백업에 들어가지 않으므로, 복원한 설치에서는 다시 30분 뒤에 묻습니다. 이전 릴리스로 되돌린 경우처럼 이 버전이 모르는 선택이 저장되어 있으면, 다시 고를 때까지 변경할 때마다 묻고 접근 권한 탭에 그 사실이 표시됩니다.

### OwnGit 아이콘

OwnGit 아이콘은 macOS에서는 메뉴 막대에, Windows에서는 알림 영역에, Linux에서는 데스크톱 패널에 나옵니다. [macOS의 아이콘](#macos의-아이콘), [Windows의 아이콘](#windows의-아이콘), [Linux의 아이콘](#linux의-아이콘)을 참고하세요. 세 운영체제 모두에서 아이콘은 푸시, 풀 리퀘스트, 실패한 체크, 끝나지 않은 가져오기와 백업, 새 버전을 [데스크톱 알림](#데스크톱-알림)으로도 알려 줍니다.

아이콘은 OwnGit이 실행되는 컴퓨터와 OwnGit을 실행하는 계정에 속합니다. 데스크톱이 있는 컴퓨터에서는 기본으로 보이고, SSH로 접속하는 서버처럼 데스크톱이 없는 컴퓨터에서는 보이지 않습니다. Linux에서 root가 설치하면 서비스가 전용 `owngit` 계정으로 실행되는데, 이 계정으로 데스크톱에 로그인하는 사람은 없으므로 이 설치 방식에는 아이콘이 없습니다. 설정 화면과 `owngit tray`가 그렇다고 알려 줍니다. `owngit tray off`로 이 컴퓨터에서 숨기면 다시 로그인하거나 재시작해도 `owngit tray on`으로 다시 켤 때까지 숨겨져 있습니다. `owngit tray`(또는 `owngit tray status`)는 아이콘이 보이는지 알려 주고, `--json`을 붙이면 `available`(아이콘이 없는 설치 방식이면 false), `shown`(숨겼을 때만 false), `desktop`(지금 이 컴퓨터에 데스크톱이 있는지), `state_dir`, `access_file`, `problem`(이 컴퓨터에서 아이콘이 나올 수 없는 이유, 없으면 빈 문자열)을 출력합니다. 설정의 일반 탭에 있는 "메뉴 막대, 알림 영역 또는 패널에 OwnGit 아이콘 표시" 스위치도 같은 일을 하며, 설정의 다른 부분처럼 관리자 비밀번호를 묻습니다. 이 스위치는 지금 브라우저가 있는 컴퓨터가 아니라 OwnGit이 실행되는 컴퓨터의 아이콘을 바꿉니다. 아이콘을 숨겨도 OwnGit은 멈추지 않으며 Git과 대시보드는 그대로 동작합니다. 이 선택은 상태 디렉터리의 `tray-hidden` 파일로 저장되므로 이 컴퓨터에 속하고 백업에는 들어가지 않습니다.

OwnGit은 시작할 때마다 상태 디렉터리에 `tray-access.json` 파일을 쓰며, 이 파일은 OwnGit을 실행하는 계정만 읽을 수 있습니다. 파일에는 이 컴퓨터에서 이 서버에 접속하는 주소(예: `http://127.0.0.1:7654`), 새 무작위 토큰, 새 무작위 증명 비밀값이 들어 있습니다. OwnGit이 멈춘 뒤에도 파일은 남지만, 이전 시작 때의 토큰은 더 이상 쓸 수 없습니다. `Authorization: Bearer <토큰>` 헤더와 새 무작위 32바이트를 패딩 없는 base64url로 적은 `X-OwnGit-Tray-Nonce` 헤더를 붙여 `GET /tray/status`를 요청하면 JSON으로 상태를 알려 줍니다. 상태(`running`, 또는 설정이 끝나지 않았거나 새 릴리스가 나왔거나 [점검](#점검)에서 문제가 발견되면 `attention`), 아이콘을 숨겼는지, 버전, 대시보드에 보이는 것과 같은 대시보드 주소와 클론 주소, 새 릴리스와 이 설치 방식의 업데이트 명령, 점검 결과, 최근 푸시 세 건(저장소, ref, 바뀐 ref 수, OwnGit이 푸시를 받은 시각, 어떤 권한으로 푸시했는지. Git 푸시는 언제나 일반 접근입니다)이 들어 있습니다. 푸시는 Git이 ref를 실제로 바꾼 경우에만 기록됩니다. 거부되거나 실패한 푸시, ref를 이미 가진 값으로 바꾸라고 한 푸시는 기록되지 않고, 커밋에 적힌 날짜를 푸시 시각으로 쓰지도 않습니다. 최근 푸시는 100건까지 보관하며 백업에는 들어가지 않습니다. 이 상태는 아이콘이 읽는 알림과 마찬가지로 세 조건을 모두 갖춘 요청에만 답합니다. `127.0.0.1` 같은 루프백 주소에서 접속하고, `X-Forwarded-For`나 `Forwarded` 같은 전달 헤더가 없고, 토큰이 붙어 있어야 합니다. [tailnet 공유](#tailnet에서-https로-공유하기)를 켰을 때처럼 `127.0.0.1`을 신뢰하는 프록시로 등록해도 아이콘은 그대로 동작합니다. 전달 헤더를 모두 지우는 프록시처럼 이 컴퓨터에서 헤더 없이 연결을 넘기는 프로그램은 직접 접속과 구별되지 않습니다. 그래서 그 뒤의 기기와 이 컴퓨터의 다른 계정으로부터 상태를 지키는 것은 토큰입니다. OwnGit을 실행하는 계정으로 도는 프로그램은 상태 디렉터리 자체를 읽을 수 있듯이 토큰도 읽을 수 있습니다. 상태 응답에는 `X-OwnGit-Tray-Proof` 헤더가 붙습니다. 이 값은 증명 비밀값을 키로 `owngit tray status`, 논스, 응답 본문 그대로를 각각 줄바꿈과 함께 이어 계산한 HMAC-SHA256을 패딩 없는 base64url로 적은 것입니다. 증명 비밀값은 전송되지 않으므로 OwnGit이 멈춘 동안 그 주소를 차지한 프로그램은 올바른 증명을 붙일 수 없고, 읽는 쪽은 증명이 없는 응답의 내용을 쓰지 않습니다. `/healthz`는 계속 빈 응답입니다.

#### macOS의 아이콘

macOS의 아이콘은 모든 릴리스 설치에 함께 들어 있는 OwnGit.app입니다. 릴리스 압축 파일과 한 줄 설치 명령은 `owngit` 프로그램 바로 옆(기본값 `~/.local/bin/OwnGit.app`)에, npm은 플랫폼 패키지의 실행 파일 옆에, Homebrew는 `$(brew --prefix)/opt/owngit/OwnGit.app`에 둡니다. 이 앱은 아이콘일 뿐이며 서버는 서비스가 실행합니다. 아이콘을 숨기거나 종료해도 Git과 대시보드는 멈추지 않습니다.

데스크톱이 있는 Mac에서 `owngit service install`은 데스크톱 로그인에서 아이콘을 열고, 앱은 그때 로그인할 때마다 열리도록 스스로 등록합니다. 헤드리스 서비스이거나, Mac 화면에 로그인하지 않은 채 SSH로 접속했거나, 아이콘을 숨긴 경우에는 열지 않습니다. 내 계정의 아이콘이 이미 실행 중이면 `owngit service install`과 `owngit service restart`는 아이콘을 종료했다가 다시 열어, 업데이트 뒤에 새 앱으로 실행되게 합니다. 숨긴 아이콘은 계속 숨겨져 있습니다. 시스템 설정은 이 항목을 로그인 항목 및 확장 프로그램의 로그인 시 열기에 OwnGit으로 표시합니다. Homebrew 설치는 대신 LaunchAgent `~/Library/LaunchAgents/app.owngit.icon.plist`를 쓰며, 이 에이전트는 Homebrew가 업그레이드 뒤에도 유지하는 `$(brew --prefix)/opt/owngit` 폴더에서 앱을 엽니다. 그래서 아이콘이 꺼져 있는 동안 업그레이드해도 다음 로그인 때 아이콘이 열립니다.

아이콘을 클릭하면 패널이 열립니다. 패널은 OwnGit이 실행 중인지, 확인이 필요한지(마칠 설정이 있거나, 새 버전이 나왔거나, 점검에서 문제가 발견된 경우이며, 이 설치를 업데이트하는 명령은 복사 버튼과 함께, 고치는 명령은 선택해서 복사할 수 있는 글자로 보여 줍니다), 실행되지 않는지(시작 버튼이 있습니다), 지금 상태를 알 수 없는지를 보여 줍니다. OwnGit이 실행 중이면 복사 버튼이 있는 클론 주소, 최근 푸시 세 건, 대시보드 열기도 보여 줍니다. 설정을 마치기 전에는 대시보드 열기 대신 설정 마치기가 나옵니다. Tab으로 패널 안을 이동하고, Space로 버튼을 누르고, Esc로 닫습니다. 패널은 macOS의 화면 모드(라이트 또는 다크)와 언어(macOS가 한국어를 우선하면 한국어, 아니면 영어)를 따릅니다.

톱니바퀴 버튼에는 이 Mac의 아이콘 설정이 있습니다. "로그인할 때 열기"는 로그인 항목을 켜거나 끕니다. "메뉴 막대에서 숨기기"는 `owngit tray off`와 같으며, OwnGit.app을 열거나 설정의 스위치를 켜거나 `owngit tray on`을 실행하면 다시 보입니다. "아이콘 종료"는 OwnGit.app을 다시 열거나 다시 로그인할 때까지 아이콘을 닫습니다. `owngit service uninstall`은 내 계정의 아이콘을 종료하고 로그인 항목을 끄며, 아이콘이 사라졌는지와 macOS가 항목을 껐다고 알려 주는지 확인한 뒤에만 그렇다고 알립니다.

앱이나 앱이 사용하는 `owngit` 프로그램이 이 Mac의 다른 계정이 바꿀 수 있는 폴더에 있으면 앱은 실행되지 않습니다. OwnGit이 어디에 있는지와 나만 바꿀 수 있는 폴더로 옮기라고 알려 주고, 아무것도 등록하거나 실행하지 않습니다. Windows의 아이콘처럼 서버의 증명이 붙은 상태 응답만 사용하고, 대시보드나 설정을 열기 전에 다시 확인하며, `tray-access.json`에 적힌 주소만 엽니다.

#### Windows의 아이콘

데스크톱이 있는 컴퓨터에서 `owngit service install`은 로그인할 때 아이콘을 시작하는 작업 스케줄러 작업 `OwnGit icon`도 하나 더 등록합니다. 서비스를 관리자 승인으로 설치했더라도 아이콘은 내 계정으로, 관리자 권한 없이 실행되며, 서비스와 같은 `owngit.exe`를 실행합니다. 화면이 없는 설치(`--headless`, SSH에서 설치할 때의 기본값)에는 아이콘이 없고 이 작업도 등록하지 않으며, `owngit service uninstall`은 이 작업을 지웁니다. 서비스 없이 쓸 때는 `owngit tray icon`을 실행하면 종료하거나 터미널을 닫을 때까지 아이콘이 보입니다.

아이콘을 클릭하면 대시보드가 열리고, 오른쪽 클릭하면 패널이 열립니다. 패널은 OwnGit이 실행 중인지, 확인이 필요한지(업데이트하거나 고치는 명령 하나와 복사 버튼), 실행되지 않는지(시작하는 명령), 지금 상태를 알 수 없는지를 보여 줍니다. OwnGit이 실행 중이면 복사 버튼이 있는 클론 주소와 최근 푸시 세 건도 보여 줍니다. Tab으로 패널 안을 이동하고 Enter나 Space로 버튼을 누르며 Esc로 닫습니다. 패널은 Windows의 색 모드(라이트 모드, 다크 모드)와 표시 언어(Windows가 한국어로 표시되면 한국어, 아니면 영어)를 따릅니다.

패널의 "알림 영역에서 숨기기"는 `owngit tray off`와 같습니다. 설정에서 다시 켜거나 `owngit tray on`을 실행할 때까지 다시 로그인한 뒤에도 숨겨져 있고, 다시 켜면 몇 초 안에 나타납니다. "아이콘 종료"는 다시 로그인할 때까지 아이콘만 닫습니다. 어느 쪽이든 OwnGit은 계속 실행됩니다. 로그인할 때 시작하는 방식으로 설치했다면 `owngit service stop`은 아이콘도 닫습니다. 아이콘이 업데이트 때 바뀌는 `owngit.exe`를 함께 실행하기 때문입니다. `owngit service start`나 `owngit service restart`는 아이콘을 다시 엽니다.

아이콘은 대시보드를 열기 전에 OwnGit이 그 주소에서 답하는지 다시 확인합니다. 그래서 OwnGit이 멈춘 뒤 그 주소를 차지한 다른 프로그램으로 브라우저를 보내지 않습니다. 이 확인에 실패하면 패널에 상태를 알 수 없다고 표시합니다.

Windows 11은 새 아이콘을 숨겨진 아이콘(^ 단추) 안에 둘 수 있습니다. 작업 표시줄에 늘 보이게 하려면 설정, 개인 설정, 작업 표시줄, 기타 시스템 트레이 아이콘에서 켜거나 아이콘을 작업 표시줄로 끌어 놓으세요.

#### Linux의 아이콘

Linux 데스크톱에서는 그 데스크톱의 터미널에서 `owngit service install`을 실행하세요. 데스크톱 패널에 아이콘이 바로 뜨고 `~/.config/autostart/owngit-icon.desktop` 파일이 만들어져서 로그인할 때마다 아이콘이 다시 시작됩니다. 아이콘은 `sudo` 없이 내 계정으로 실행됩니다. Homebrew로 설치했다면 이 파일이 Homebrew의 `opt/owngit` 링크를 가리키므로 `brew upgrade` 뒤에도 그대로 동작합니다. 이 파일이 이미 있는데 OwnGit이 쓴 파일이 아니거나 OwnGit이 읽을 수 없으면 설치는 파일을 그대로 두고 아이콘도 시작하지 않으며 그렇다고 알려 줍니다. `owngit service uninstall`은 OwnGit이 쓴 로그인 항목만 지웁니다. 다른 프로그램의 파일이나 읽을 수 없는 파일은 그대로 둡니다. 서비스 없이 쓸 때는 `owngit tray icon`을 실행하면 종료할 때까지 아이콘이 보입니다.

아이콘이 없는 설치도 있습니다. 화면이 없는 설치(`--headless`, SSH에서 설치할 때의 기본값)와, 서비스가 `owngit` 계정으로 실행되는 root 설치입니다.

데스크톱이 StatusNotifierItem 아이콘을 보여 줄 수 있어야 합니다. StatusNotifierItem은 프로그램이 패널에 아이콘을 두는 표준 방식입니다. 다음 두 데스크톱에서 확인했습니다.

- Omarchy: 바의 화살표 뒤 서랍에 아이콘이 들어갑니다.
- AppIndicator 확장(`gnome-shell-extension-appindicator` 패키지)을 켠 GNOME 48: GNOME은 이 확장이 있어야 이런 아이콘을 보여 줍니다.

KDE Plasma처럼 StatusNotifierItem 아이콘을 보여 주는 다른 데스크톱에서도 나올 것으로 보지만 확인하지는 않았습니다.

아이콘과 패널은 데스크톱의 `gjs`와 GTK 4로 그립니다. Omarchy에는 둘 다 들어 있습니다. 다른 데스크톱에서 `owngit tray status`가 없다고 알려 주면 배포판의 `gjs` 패키지를 설치하세요. OwnGit은 내 계정과 root 말고 다른 계정이 `gjs`를 직접 바꾸거나 경로 중간의 폴더나 링크로 바꿔치기할 수 없을 때만 `gjs`를 실행합니다. 바꿀 수 있으면 아이콘이 시작되지 않고 `owngit tray status`가 그 경로를 알려 줍니다. 아이콘이 없어도 OwnGit은 계속 실행됩니다.

Omarchy에서는 아이콘을 클릭하면 패널이, 오른쪽 클릭하면 메뉴가 열립니다. GNOME에서는 한 번 클릭하면 메뉴가, 두 번 클릭하면 패널이 열립니다. 패널에는 OwnGit이 실행 중인지, 확인이 필요한지(업데이트하거나 고치는 명령과 복사 버튼), 실행되지 않는지(시작하는 명령), 지금 상태를 알 수 없는지가 보입니다. 복사 버튼이 있는 클론 주소와 최근 푸시 세 건도 함께 보입니다.

"대시보드 열기"를 누르면 아이콘은 OwnGit이 제 주소에서 응답하는지, 그 응답에 증명이 붙어 있는지 다시 확인한 뒤에야 브라우저로 대시보드를 엽니다. 확인에 실패하면 패널에 "상태를 알 수 없음"이 나오고, 브라우저를 시작하지 못하면 그 이유가 나옵니다. Tab으로 패널 안을 이동하고 Enter나 Space로 버튼을 누르며 Esc로 닫습니다. 패널은 데스크톱의 라이트 모드나 다크 모드와 언어(세션 언어가 한국어면 한국어)를 따릅니다. Wayland에서는 프로그램이 창을 아이콘 바로 아래에 둘 수 없어서 패널은 데스크톱이 새 창을 두는 자리에 열립니다. GNOME에서는 화면 가운데입니다.

패널이나 메뉴의 "아이콘 숨기기"는 `owngit tray off`와 같습니다. "아이콘 종료"는 다시 로그인할 때까지 아이콘만 닫습니다. 어느 쪽이든 OwnGit은 계속 실행됩니다.

다른 바나 스크립트는 명령 두 개로 같은 상태를 보여 줄 수 있습니다. 두 명령이 접근 파일을 읽고 응답마다 증명을 직접 확인합니다.

- `owngit tray read --json`은 `{"shown": ..., "panel": {...}}`를 출력합니다. `shown`은 아이콘을 숨겼으면 false입니다. `panel`에는 `condition`(`running`, `attention`, `stopped`, `unavailable`), `state`, `tooltip`, `subtitle`, `notice`(문장 목록), `command_intro`와 `command`(복사할 명령, 없으면 빈 문자열), `clone_address`, `pushes`(최대 세 건, 각각 `repository`, `branch`, 말로 적은 시각 `when`), `no_pushes`, `can_open`, `labels`(고른 언어로 된 패널의 문구)가 들어 있습니다. 토큰이나 열 주소는 들어 있지 않습니다. `--lang en`이나 `--lang ko`로 언어를 고르며, 기본값은 `LC_ALL`, `LC_MESSAGES`, `LANG`을 따릅니다.
- `owngit tray open --json`은 새로 증명을 받은 뒤 대시보드를 열고 `{"ok": true, "url": ...}`를 출력합니다.

두 명령 모두 `--state-dir`을 받습니다. OwnGit이 응답하지 않거나 응답을 증명하지 못해도 `tray read`는 성공으로 끝나고 그 상태를 `panel`에 담습니다. 이때 `condition`은 `unavailable`이고, [점검](#점검)에서 OwnGit이 멈춘 것으로 나오면 `stopped`입니다. 실패한 명령은 `{"ok": false, "error": {"code": ..., "message": ...}}`를 출력하고 오류로 끝납니다. 아이콘이 없는 설치 방식이면 두 명령 모두 `tray_unavailable`을 냅니다. `status_unavailable`(OwnGit이 응답을 증명하지 못함)과 `open_failed`(브라우저를 시작하지 못했거나 여는 프로그램이 실패로 끝남)는 `tray open`만 내며, 두 경우 모두 대시보드를 열지 않았습니다. [Omarchy 바 위젯](../integrations/omarchy/owngit.status/README.md)이 이 두 명령으로 만들어져 있습니다.

#### 데스크톱 알림

OwnGit에서 일이 생기면 OwnGit 아이콘이 이 컴퓨터의 데스크톱 알림으로 알려 줍니다. 모든 종류가 기본으로 켜져 있습니다.

- 푸시. 브랜치와 마지막 커밋 메시지가 함께 나옵니다. 첫 푸시부터 1분 안에 들어온 푸시는 저장소가 달라도 알림 하나로 묶입니다. 제목은 "푸시 3번"이고 본문에는 "notes 2번, household 1번" 뒤에 마지막 푸시가 붙습니다.
- 새로 열린 풀 리퀘스트.
- 실패한 자동 체크.
- 끝나지 않은 가져오기.
- 끝나지 않은 백업.
- 새 OwnGit 버전. 이 설치에 업데이트 명령이 있으면 알림에 함께 나옵니다. 없으면 업데이트 방법이 대시보드에 있다고 알려 줍니다. [업데이트 확인](#새-릴리스-알림)이 켜져 있을 때만 알립니다.

일어난 일에 대한 알림은 1분에서 1분 30초쯤 뒤에 나옵니다. OwnGit은 조금 늦게 기록된 일도 놓치지 않도록 1분을 기다립니다. 아이콘은 그다음 상태를 읽을 때 알림을 가져옵니다. 새 버전 알림은 이 1분을 기다리지 않습니다. OwnGit이 새 릴리스를 찾으면 아이콘이 다음에 상태를 읽을 때 바로 나옵니다. 푸시가 아닌 한 종류가 한꺼번에 세 건보다 많이 들어오면 "끝나지 않은 가져오기 4건"처럼 건수를 센 알림 하나로 나옵니다.

알림을 클릭하면 대시보드에서 해당 페이지가 열립니다. 푸시는 그 브랜치의 커밋(브랜치나 태그를 삭제한 푸시는 저장소 페이지), 여러 저장소에 들어온 푸시와 건수를 센 알림은 전체 활동, 풀 리퀘스트는 그 풀 리퀘스트, 실패한 체크는 저장소의 체크 탭(풀 리퀘스트에서 실행된 체크는 그 풀 리퀘스트), 가져오기는 가져오기 탭, 백업은 설정, 새 버전은 홈 화면입니다. 아이콘은 대시보드 열기와 마찬가지로 페이지를 열기 전에 OwnGit이 제 주소에서 응답하는지 다시 확인합니다.

알림 설정은 이 컴퓨터에 속하고 다시 로그인하거나 재시작해도 유지됩니다. "알림 보기"는 알림 전체를 켜고 끕니다. 종류마다 스위치가 따로 있고 전체를 꺼 둔 동안에도 각 스위치는 제 값을 기억합니다. 설정은 아이콘의 패널에 있습니다. macOS에서는 톱니바퀴 버튼 안, Windows에서는 이 컴퓨터 부분의 알림 버튼 아래, Linux에서는 알림 부분입니다. 명령줄에서는 `owngit tray notifications`가 설정을 출력하고(`--json`이면 JSON), 설정 이름 뒤에 `on`이나 `off`를 붙이면 그 설정을 바꿉니다. 설정 이름은 `all`, `only_others`, `push`, `pull_request`, `check_failed`, `import_failed`, `backup_failed`, `update`입니다.

```sh
owngit tray notifications push off
owngit tray notifications only_others on
```

"내가 하지 않은 일만 알림"(`only_others`)은 기본으로 꺼져 있습니다. 켜면 이 컴퓨터에서 한 푸시, 풀 리퀘스트, 가져오기는 알리지 않습니다. 여기서 이 컴퓨터는 OwnGit이 실행되는 컴퓨터입니다. 그 컴퓨터의 Git으로 한 푸시나 브라우저에서 연 풀 리퀘스트가 그 예입니다. 리버스 프록시를 거쳐 들어온 요청은 다른 컴퓨터에서 온 것으로 봅니다. 이 컴퓨터에서 직접 한 푸시는 `127.0.0.1`이 신뢰하는 프록시여도 이 컴퓨터에서 한 것으로 보며, 이 컴퓨터에서 전달 헤더를 모두 지우고 넘기는 프록시의 요청도 마찬가지입니다. 다른 기기에서 온 푸시와 풀 리퀘스트에는 "다른 컴퓨터에서 푸시"나 "다른 컴퓨터에서 열림"이 붙습니다. 실패한 체크, 백업, 새 버전 알림은 이 설정과 상관없이 나옵니다. OwnGit은 어디서 온 일인지를 실행 중에만 기억합니다. 그래서 OwnGit이 재시작되기 직전에 이 컴퓨터에서 한 일은 한 번 알림으로 나올 수 있습니다.

아이콘을 숨긴 동안과 "알림 보기"를 끈 동안에는 알림이 나오지 않습니다. 꺼 둔 종류도 나오지 않습니다. 그사이에 생긴 일은 나중에도 알려 주지 않습니다. 아이콘을 처음 시작하거나 다시 보이게 하면 그때부터 알립니다. 아이콘을 종료했다면 다릅니다. 다시 시작할 때 꺼져 있던 동안 생긴 일을 알려 줍니다. 푸시는 OwnGit이 보관하는 최근 100건까지입니다.

방해 금지나 OwnGit 알림 끄기처럼 운영체제의 알림 설정도 함께 적용됩니다.

- **macOS**: 알림은 알림 센터에 나옵니다. 처음 알릴 일이 생길 때 macOS가 알림 허용 여부를 한 번 묻습니다. 시스템 설정의 알림에서 OwnGit 알림을 끄면 패널에 그렇게 표시되고 그 설정 화면을 여는 버튼이 나옵니다.
- **Windows**: 알림은 알림 영역의 풍선 알림이고 Windows 11에서는 "owngit.exe"가 보낸 알림으로 보입니다. 방해 금지를 켜 두면 Windows는 알림을 보여 주지도, 나중을 위해 남겨 두지도 않습니다. 나타난 알림도 알림 센터에 남지 않습니다. 이는 모든 풍선 알림에 똑같이 적용되는 Windows의 동작이며 OwnGit의 설정이 아닙니다. 또한 Windows는 어느 알림을 클릭했는지 아이콘에 알려 주지 않습니다. 그래서 클릭하면 지금까지 보인 알림 모두에 맞는 페이지가 열립니다. 모두 같은 페이지면 그 페이지, 모두 푸시면 전체 활동, 그 밖에는 대시보드 홈 화면입니다.
- **Linux**: 데스크톱에 알림 서비스가 있어야 합니다. GNOME과 Omarchy에는 기본으로 들어 있습니다.

## 다른 기기에서 서버에 접속하기

OwnGit은 일반 HTTP로 동작하며 TLS를 내장하지 않습니다. 다른 기기에서 접속하는 방법은 둘 중 하나입니다.

- **암호화된 주소**: Tailscale이 tailnet에 공유하게 하거나([tailnet에서 HTTPS로 공유하기](#tailnet에서-https로-공유하기)) OwnGit 앞에 리버스 프록시를 두세요([리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기)).
- **일반 HTTP**: Tailscale, 직접 운영하는 VPN([다른 비공개 네트워크](#다른-비공개-네트워크)), LAN에서는 OwnGit이 네트워크 주소에서 연결을 받게 하면 됩니다([네트워크 설정](#네트워크-설정)). 일반 HTTP는 처음 설정할 때나 설정 화면의 네트워크 탭에서 다른 기기의 접속을 허용할 때 한 번 받아들이면 됩니다. 연결이 암호화되었는지는 페이지 위쪽에 늘 표시됩니다.

OwnGit을 공개 인터넷에 노출하지 마세요. 공유 링크만 따로 공개 주소를 가질 수 있습니다([공유 링크용 공개 주소](#공유-링크용-공개-주소)).

tailnet의 기기가 `http://100.64.0.7:7654/`처럼 이 컴퓨터의 Tailscale 주소로 OwnGit을 열면 "Tailscale이 암호화함"이라고 표시되고 일반 HTTP를 받아들일지 묻지 않습니다. OwnGit은 요청이 Tailscale 주소에서 왔고, 이 컴퓨터의 Tailscale이 자기 주소라고 보고하는 주소로 들어왔는지 확인합니다. 막 시작한 직후나 Tailscale이 응답하지 않는 동안에는 이 표시가 빠질 수 있습니다. 사용자 공간 네트워킹 모드의 Tailscale은 `127.0.0.1`에서 접속하므로 표시가 붙지 않습니다.

### 이 컴퓨터의 방화벽

집 네트워크의 다른 기기는 OwnGit이 네트워크 주소에서 연결을 받고 이 컴퓨터의 방화벽이 그 연결을 들여보낼 때만 OwnGit에 접속할 수 있습니다. 이 컴퓨터가 어느 경우인지는 `owngit doctor`가 찾은 네트워크와 명령을 함께 알려 줍니다([점검](#점검)).

- **Windows**: 규칙으로 허용하지 않은 다른 기기의 연결은 Windows 방화벽이 막습니다. 관리자 계정에서 `owngit service install`을 실행하면 개인 네트워크용 규칙 `OwnGit`을 추가하므로 연결 주소를 바꾸거나 OwnGit을 업데이트해도 규칙은 그대로 있습니다. Windows가 공용으로 보는 네트워크는 계속 막혀 있으니 집 네트워크는 Windows 설정의 네트워크 및 인터넷에서 개인 네트워크로 지정하세요. 데스크톱에서 `owngit serve`를 직접 시작하면 Windows가 대신 물어볼 수 있는데 이때 개인 네트워크를 허용하면 됩니다. 표준 계정의 서비스에는 규칙이 생기지 않으며 관리자가 추가할 수 있습니다. Windows 보안에서 들어오는 연결을 모두 차단하면 어떤 기기도 들어오지 못합니다.
- **macOS**: 애플리케이션 방화벽은 직접 켜지 않았다면 꺼져 있습니다. 켜져 있으면 macOS가 OwnGit이 들어오는 연결을 받아도 되는지 물을 수 있으니 허용하세요. "들어오는 연결 모두 차단"을 켜 두면 어떤 기기도 들어오지 못합니다.
- **Linux**: ufw나 firewalld가 켜져 있으면(Omarchy는 ufw를 켜 둡니다) OwnGit의 포트를 사설 네트워크에만 허용하세요. 예를 들면 `sudo ufw allow from 192.168.1.0/24 to any port 7654 proto tcp`입니다. 포트만 지정한 규칙은 이 컴퓨터가 연결되는 다른 네트워크에도 포트를 엽니다.

### 호스트 이름

서버는 Host가 승인된 이름인 요청과, 이 컴퓨터에서 보낸 Host가 `localhost`, `127.0.0.1`, `::1`인 요청만 받습니다. 그 밖의 Host는 "unrecognized host"로 거부됩니다. 이번 실행에서만 LAN 이름을 쓰려면 다음과 같이 실행합니다.

```sh
owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

`--allowed-host`는 여러 번 지정할 수 있습니다. 이름을 영구히 승인하려면 설치 호스트에서 다음 명령을 실행하고 다시 시작하세요.

```sh
owngit approve-host gitbox.internal
```

### 네트워크 설정

다시 시작해도 주소를 유지하려면 저장하세요. 서비스처럼 옵션 없이 시작한 서버는 시작할 때마다 저장된 값을 씁니다. 다음 명령은 설치 호스트에서 실행합니다. 서버가 실행 중이든 아니든 동작하며 바꾼 값은 다음 시작부터 적용됩니다.

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal --accept-insecure-http
owngit network show
```

- `--listen`은 `host:port` 형식입니다. host를 비우거나 `0.0.0.0`, `::`로 쓰면 모든 인터페이스에서 연결을 받습니다.
- `0.0.0.0:7654`처럼 다른 컴퓨터가 접속하는 주소에서는 암호화되지 않은 일반 HTTP로 응답합니다. 이 설치에서 일반 HTTP에 아직 동의하지 않았다면 `set`은 그런 주소를 거부하고 아무것도 저장하지 않습니다. `--accept-insecure-http`를 한 번 붙이면 동의한 것으로 기록되며 설정 과정이나 네트워크 탭에서 동의했을 때와 같이 다시 묻지 않습니다. OwnGit 자체는 여전히 일반 HTTP로 응답하므로 기본 URL이 `https`여도 마찬가지입니다. `127.0.0.1:7654` 같은 루프백 주소에는 필요 없습니다.
- `--base-url`은 다른 기기가 쓰는 경로 없는 `http` 또는 `https` origin입니다. OwnGit은 그 호스트 이름을 받아들이고 clone 주소에 표시합니다. 없으면 clone 주소는 브라우저가 접속한 주소를 따릅니다.
- `--allowed-host`와 `--remove-allowed-host`는 `owngit approve-host`도 추가하는 목록을 바꿉니다.
- `--trusted-proxy`와 `--remove-trusted-proxy`는 OwnGit이 전달 헤더를 믿는 리버스 프록시 목록을 바꿉니다. 각각 IP 주소나 CIDR 범위를 씁니다([리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기) 참고).
- 모든 옵션은 여러 번 지정할 수 있습니다. `--base-url=`처럼 빈 값을 주면 저장된 그 값을 지웁니다.

`owngit serve`는 옵션이 있으면 옵션을, 없으면 저장된 값을, 둘 다 없으면 기본값(`127.0.0.1:7654`)을 씁니다. 옵션은 그 실행에만 적용됩니다. `set`은 연결 주소가 이 컴퓨터 밖에서 접속을 받게 되거나(다른 기기가 일반 HTTP로 접속합니다) `https` 기본 URL에 신뢰하는 프록시가 없으면 안내 한 줄을 출력합니다.

`network show`는 저장된 값, 실행 중인 서버가 실제로 쓰는 값, 다시 시작해야 하는지를 보여 주고 `--json`을 붙이면 같은 내용을 JSON으로 출력합니다. 설정 화면의 네트워크 탭에도 같은 내용이 있어 다음 시작 때 쓸 저장된 값과 실행 중인 서버가 쓰는 값을 나란히 보여 줍니다. 관리자 비밀번호로 바꿀 수 있으며 화면을 연 뒤에 설정이 바뀌었다면 저장을 거부합니다.

서비스 정의(LaunchAgent의 `ProgramArguments`, 유닛의 `ExecStart`)에서 `--listen`, `--base-url`, `--allowed-host`, `--trusted-proxy`를 넘기면 시작할 때마다 저장된 값보다 옵션이 우선하므로 빼 두세요. `owngit service install`이 쓰는 유닛은 이 옵션을 넘기지 않습니다. Homebrew 서비스는 이 옵션 없이 `owngit serve --no-open`만 실행하므로 저장된 값을 씁니다. 다른 기기에서 접속하려면 다음과 같이 합니다.

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --accept-insecure-http
brew services restart owngit
```

저장한 값 때문에 접속할 수 없게 되었다면(예를 들어 이 컴퓨터에 더 이상 없는 주소를 연결 주소로 저장했다면) 설치 호스트에서 되돌리고 다시 시작하세요.

```sh
owngit network reset
```

`reset`은 연결 주소, 기본 URL, 공유 링크용 공개 주소를 지웁니다. `--clear-allowed-hosts`나 `--clear-trusted-proxies`를 붙이지 않으면 허용한 Host와 신뢰하는 프록시는 그대로 둡니다. 웹 화면에서는 할 수 없습니다.

`network set`과 `network reset`도 `--json`을 받습니다. 그러면 JSON 객체 하나를 출력합니다. 필드는 `ok`, `network show --json`과 같은 보고 내용, `applies_at_next_start`(바꾼 값은 다음 시작부터 적용되므로 true), `plain_http_accepted`(이 설치에서 일반 HTTP에 동의했는지), `warnings`(텍스트 출력에 나오는 안내)입니다. 거부되면 `invalid_arguments`, `acknowledgement_required`(다른 컴퓨터가 접속하는 주소에 `--accept-insecure-http`를 붙이지 않았고 아무것도 저장하지 않음), `state_unavailable` 중 하나의 코드로 JSON 오류를 출력합니다.

네트워크 설정은 이 설치 호스트에 속합니다. 오프라인 백업에 포함되지 않으며 복원한 서버는 기본값으로 시작합니다. OwnGit이 이번 실행에서만 받아들이는 이름으로 다른 기기에서 웹 설정을 마치면, 설정 화면이 그 이름을 허용한 Host로 저장할지 묻습니다.

### tailnet에서 HTTPS로 공유하기

OwnGit을 실행하는 컴퓨터에서 Tailscale이 실행 중이면, OwnGit은 이 컴퓨터의 Tailscale 이름으로 들어오는 HTTPS 요청을 Tailscale이 받아 OwnGit에 넘기도록 설정할 수 있습니다. 그러면 tailnet의 기기에서 `https://NAME.TAILNET.ts.net/`을 열고 `https://NAME.TAILNET.ts.net/git/project.git`에서 clone할 수 있으며 페이지 위쪽에 "이 컴퓨터의 Tailscale이 암호화함"이라고 표시됩니다. tailnet 밖의 기기는 이 주소에 접속할 수 없습니다.

필요한 것은 다음과 같습니다.

- 이 컴퓨터에 설치되어 로그인된 Tailscale 1.50 이상
- Tailscale 관리 콘솔의 DNS 페이지에서 켠 MagicDNS와 HTTPS Certificates
- Linux에서는 사용자가 Tailscale 설정을 바꿀 수 있는 권한. `sudo tailscale set --operator=$USER`로 한 번 허용해 두세요. OwnGit이 `sudo`를 직접 실행하지는 않습니다.

그다음 설정 화면의 네트워크 탭에 있는 "tailnet에서 HTTPS로 공유"에서 "내 tailnet에 OwnGit 공유"를 켜고 관리자 비밀번호로 저장합니다. 설치 호스트에서는 명령줄로도 할 수 있습니다.

```sh
owngit tailscale on
owngit tailscale status
owngit tailscale off
```

설정 화면에서 바꾸면 다시 시작하지 않아도 바로 적용됩니다. `owngit tailscale on`과 `off`는 같은 내용을 저장하지만 실행 중인 서버에는 전달할 수 없으므로 다시 시작하라고 안내합니다. `owngit tailscale status`는 "on and ready"를, 설정 화면은 "켜져 있습니다. 이 컴퓨터의 Tailscale이 암호화합니다."를 표시하는데, 실행 중인 서버가 이름을 받아들이고 `127.0.0.1`을 신뢰하며 Tailscale에 주소가 남아 있을 때만입니다. 그렇지 않으면 무엇이 빠졌는지 알려 줍니다.

공유를 켠 뒤나 컴퓨터 이름을 바꾼 뒤 첫 HTTPS 연결은 1분 가까이 걸릴 수 있습니다. Tailscale이 주소를 처음 열 때 인증서를 받기 때문입니다. `owngit` 명령, MCP 서버, 러너는 이 연결을 75초까지 기다립니다.

Tailscale이 인증서를 발급할 때 `gitbox.tail0000.ts.net`처럼 이 컴퓨터와 tailnet의 이름이 공개 인증서 투명성(Certificate Transparency) 로그에 기록됩니다. 주소만 기록될 뿐 내용은 기록되지 않습니다. 설정 화면은 이 안내를 스위치 옆에 보여 주고 `owngit tailscale on`은 출력합니다(`--json`에서는 `certificate_log`).

#### 켜기가 하는 일

1. HTTPS 포트를 고릅니다. 443이 비어 있으면 443, 아니면 8443, 그것도 쓰이면 10000이며, 직접 고를 수도 있습니다([HTTPS 포트 바꾸기](#https-포트-바꾸기)). 다른 포트에 있는 것은 그대로 둡니다. 시도할 포트가 모두 쓰이고 있거나, OwnGit이 만든 기록 없이 이미 OwnGit을 가리키는 주소가 있으면 아무것도 바꾸지 않습니다. 이때 각 포트에 무엇이 있는지와 지우는 명령을 보여 주며 더는 필요 없는 설정이면 [그 자리에 OwnGit을 둘 수도](#포트의-기존-설정-바꾸기) 있습니다. `owngit tailscale status`는 이 내용을 항상 보여 주고 설정 화면은 관리자에게만 보여 줍니다. tailnet의 접근 제어가 포트를 제한한다면 OwnGit이 쓰는 포트를 허용하세요.
2. `tailscale serve --bg --https=HTTPS_PORT http://127.0.0.1:PORT`를 실행할 때처럼 주소를 Tailscale의 Serve 설정에 추가하고 그 주소가 OwnGit을 가리키는지 확인합니다. Tailscale은 Serve 설정이 OwnGit이 읽었을 때와 같을 때만 이 변경을 적용합니다. 그사이 다른 프로그램이나 사람이 설정을 바꿨다면 OwnGit은 아무것도 바꾸지 않고 다시 시도하라고 알려 줍니다.
3. HTTPS 주소를 기본 URL로, Tailscale 이름을 허용한 Host로, `127.0.0.1`을 신뢰하는 프록시로 저장합니다. 각각 아직 저장되지 않은 경우에만 저장합니다. `127.0.0.1`을 믿으면 이 컴퓨터에서 요청을 넘겨주는 다른 프로그램도 믿게 됩니다. [리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기)를 참고하세요.
4. 연결 주소를 정합니다. Tailscale은 `127.0.0.1`로 접속하므로 기본값 `127.0.0.1:7654`나 `0.0.0.0:7654`처럼 이를 받는 연결 주소는 그대로 둡니다. OwnGit이 Tailscale 주소에서만 연결을 받고 있었다면 `127.0.0.1:PORT`를 대신 저장하므로, 다음 시작부터 다른 기기는 HTTPS 주소로만 접속합니다. 새 연결 주소는 다음 시작부터 적용됩니다.

홈 네트워크 접속은 OwnGit이 스스로 열지 않습니다. "홈 네트워크에서도 허용 (암호화되지 않음)" 체크박스나 `owngit tailscale on --home-network`만 `0.0.0.0:PORT`를 저장합니다. 이는 일반 HTTP를 받아들인다는 확인으로도 기록됩니다. 실행 중인 OwnGit이 `--listen` 옵션으로 시작되었다면 그 옵션이 연결 주소를 정하며 화면도 그렇게 알려 줍니다.

#### 페이지가 HTTPS 주소로 이동

공유가 켜져 있고 준비된 동안에는 브라우저가 `http://NAME.TAILNET.ts.net:7654/settings`처럼 Tailscale 이름과 OwnGit 자체 포트로 대시보드 페이지를 열면 HTTPS 주소의 같은 페이지로 이동합니다. 페이지만 이동합니다. Git, API, `/healthz`, 처음 설정, 파일 원본과 압축 파일 내려받기, 양식, 비밀번호가 담긴 요청에는 요청받은 주소에서 그대로 응답합니다. IP 주소나 `localhost`, 다른 이름으로 연 페이지는 일반 HTTP로 남습니다. 그 브라우저가 Tailscale 이름으로는 접속하지 못할 수도 있기 때문입니다. 브라우저는 이 이동을 기억하지 않으므로 공유를 끄거나 준비되지 않은 상태가 되면 몇 초 안에 이동도 멈춥니다.

#### 끄기

끄기는 OwnGit이 만든 Tailscale 주소가 만들 때 모습 그대로일 때만 그 주소를 지우며, 이때도 Tailscale의 Serve 설정이 OwnGit이 읽었을 때와 같아야 합니다. 그렇지 않으면 아무것도 바꾸지 않고, 설정 화면과 `owngit tailscale status`가 포트를 되돌리거나 비우는 `tailscale serve` 절차를 보여 줍니다. 그다음 그 전에 저장되어 있던 기본 URL을 되돌리고, 자신이 추가한 허용 Host와 신뢰하는 프록시를 지우며, 연결 주소는 그대로 둡니다.

끄기에는 Tailscale의 응답이 필요하므로 Tailscale이 꺼져 있거나 로그아웃되어 있거나 1.50보다 오래된 버전이면 끄기를 제공하지 않습니다. HTTPS 주소로 연 페이지에서 끄면 이 컴퓨터에서 쓰는 OwnGit 주소를 알려 주는 짧은 페이지로 끝납니다.

#### HTTPS 포트 바꾸기

네트워크 탭의 "tailnet에서 HTTPS로 공유"에 있는 HTTPS 포트에서 고릅니다.

- **자동**(기본값): 443을 쓰고 443에 다른 것이 있으면 8443이나 10000을 씁니다. 공유가 켜져 있으면 지금 쓰는 포트를 그대로 씁니다.
- **직접 지정**: 입력한 포트를 씁니다. 1부터 65535까지 정할 수 있습니다.

명령줄에서는 `owngit tailscale on --https-port 8443`처럼 실행합니다. `--https-port`를 빼면 지금 공유가 쓰는 포트를 그대로 쓰거나 자동과 같은 방식으로 고릅니다.

공유가 켜진 채로 다른 포트를 저장하면 공유가 그 포트로 옮겨집니다. OwnGit은 새 포트가 비어 있는지 먼저 확인한 다음 예전 포트에서 공유를 끈 뒤 새 포트에서 켭니다. 예전 주소는 더는 쓸 수 없으므로 그 주소를 쓰는 클론마다 새 주소로 바꿔야 합니다.

```sh
git remote set-url origin https://NAME.TAILNET.ts.net:8443/git/project.git
```

새 포트가 쓰이고 있으면 아무것도 바꾸지 않습니다. 예전 포트에서 공유를 끈 뒤 Tailscale이 새 포트에서 켜기를 거부하면 공유는 꺼진 채로 남고 메시지가 그 이유를 알려 줍니다. 포트를 다시 저장하면 다시 켜집니다. 이 단계에서 설정을 저장하지 못했거나 Tailscale의 응답을 받지 못하는 등 다른 이유로 실패하면 Tailscale이 이미 새 포트에서 OwnGit을 제공하고 있을 수 있습니다. 이때 마무리하거나 되돌리는 방법은 [포트의 기존 설정 바꾸기](#포트의-기존-설정-바꾸기)의 마지막 경우에 있습니다. tailnet의 접근 제어가 포트를 제한한다면 새 포트를 허용하세요.

#### 포트의 기존 설정 바꾸기

비어 있는 포트를 고르면 Tailscale의 다른 설정을 모두 그대로 둘 수 있으니 그 방법부터 쓰세요. 어떤 포트에서 Tailscale이 제공하는 것이 더는 필요 없다면 그 자리에 OwnGit을 둘 수 있습니다.

1. 포트에 무엇이 있는지 확인합니다. 자동으로 쓸 수 있는 포트가 모두 쓰이고 있으면 네트워크 탭의 포트의 기존 설정 바꾸기에 사용 중인 포트와 그 포트에서 Tailscale이 제공하는 것이 나옵니다. 이 목록은 관리자로 확인한 브라우저에만 보입니다. `owngit tailscale status`도 같은 내용을 포트마다 바꾸는 명령과 함께 보여 줍니다. 443은 쓰이지만 8443이 비어 있는 경우처럼 자동으로 쓸 포트가 남아 있으면 이 목록이 나오지 않습니다. 이때는 설정에서 그 포트를 직접 지정으로 고르거나 `owngit tailscale on --https-port PORT`를 실행하세요. OwnGit이 켜기를 거부하면서 그 포트에 무엇이 있는지와 바꾸는 방법을 보여 줍니다.
2. 이 설정 바꾸기를 누르고 관리자 비밀번호를 입력하거나, 화면에 나온 `owngit tailscale on --https-port PORT --replace-endpoint DIGEST` 명령을 실행합니다. `DIGEST`는 확인한 내용을 가리키는 값입니다.

OwnGit은 확인한 내용만 바꿉니다. 확인한 뒤에 Tailscale이 제공하는 설정이 그 포트든 다른 포트든 조금이라도 바뀌었다면 그 포트를 바꾸지 않습니다. OwnGit이 처음 설정을 쓰기 전에 바뀐 것을 알아채면 아무것도 쓰지 않습니다. 공유는 켜져 있었으면 켜진 채로, 꺼져 있었으면 꺼진 채로 남고 그 포트는 새로 확인할 수 있게 다시 보여 줍니다. 바뀌는 것은 그 포트뿐이고 다른 포트와 이름, Funnel은 그대로입니다. Funnel로 공개된 포트는 바꾸면 OwnGit이 인터넷에 공개되므로 바꾸지 않습니다. 터미널에서 실행 중인 `tailscale serve`가 쓰는 포트도 바꾸지 않습니다.

공유가 다른 포트에서 켜져 있다면 공유도 이 포트로 옮겨집니다. 이때 OwnGit은 다른 이동과 마찬가지로 예전 포트를 먼저 비웁니다. 그다음 단계가 실패했을 때의 결과는 원인에 따라 다릅니다.

- 예전 포트를 비운 뒤에 Tailscale 설정이 바뀌었거나 Tailscale이 고른 포트에 쓰기를 거부했다면 그 포트는 바뀌지 않고 공유는 꺼진 채로 남습니다. 메시지도 그렇게 알려 줍니다([HTTPS 포트 바꾸기](#https-포트-바꾸기)).
- 그 밖의 이유로 실패했다면 Tailscale이 이미 고른 포트에서 OwnGit을 제공하고 있을 수 있습니다. OwnGit이 설정을 저장하지 못했거나 Tailscale의 응답을 받지 못한 경우가 그렇습니다. 이때 OwnGit은 아직 그 결과를 확인하지 못했습니다. 오류 메시지는 Tailscale에 이미 반영됐을 수 있다고 알려 주며 `owngit tailscale status`는 공유를 켜는 작업이 끝나지 않았다고 보여 줍니다. `owngit tailscale on`으로 마무리하거나 `owngit tailscale off`로 되돌리세요.

#### 자세한 내용과 문제 해결

- OwnGit이 `--base-url` 옵션으로 시작되었다면 clone 주소는 여전히 그 옵션이 정하므로 옵션을 빼고 다시 시작하세요.
- 켜기가 중간에 끊겼다면 스위치가 켜진 채로 남으며 저장하면 공유를 다시 켭니다.
- 이름을 바꾼 뒤에는(관리 콘솔이나 `tailscale set --hostname NAME`) 새 이름으로 공유를 다시 켜세요. 예전 이름은 인증서 로그에 남습니다. Tailscale에도 아무 데도 연결되지 않는 예전 이름의 주소가 남는데, 설정 화면과 `owngit tailscale status`가 그 주소와 지우는 `tailscale serve` 절차를 보여 줍니다.
- macOS용 Tailscale 앱(Homebrew의 `tailscaled`와 다릅니다)은 누군가 로그인해 있을 때만 실행되므로, 다시 시작한 뒤에는 누군가 로그인해야 HTTPS가 동작합니다. 자동 로그인을 켜거나, 로그인 없이 실행되는 Homebrew의 `tailscaled`를 쓰세요. 설정 화면은 이 앱을 감지하면 그렇게 알려 줍니다.
- OwnGit이 `tailscale` 명령을 스스로 찾지 못하면 `owngit serve --tailscale PATH`와 `owngit tailscale --tailscale PATH`로 지정하세요. OwnGit은 이 명령을 실행하지 않습니다. 이 명령이 옵션 없이 쓰는 경로로 Tailscale에 연결해 상태와 Serve 설정을 읽으므로, `--socket`을 따로 지정해 시작한 `tailscaled`는 지원하지 않습니다.
- OwnGit은 소켓에서 응답하는 프로그램이 root 계정으로 실행 중이라고 시스템이 알려 줄 때만 Unix 소켓으로 Tailscale에 연결합니다. Synology DSM 7에서는 Tailscale 패키지의 `tailscale` 계정도 인정합니다. 이 정보는 Linux, macOS, FreeBSD가 알려 줍니다. 그 밖의 시스템이거나 다른 계정이 응답하는 소켓이면 `untrusted_socket`을 보고합니다.
- `--json`을 주면 결과와 코드가 붙은 실패를 JSON으로 출력합니다.
- OwnGit은 Tailscale Serve를 초기화하거나 Funnel을 켜지 않으며 다른 Serve 설정은 그대로 둡니다. `Tailscale-Funnel-Request` 헤더가 붙은 요청을 모두 거부하므로 이 주소가 Funnel을 통해 인터넷에 열리지 않습니다. 공유 링크용 공개 주소는 사용자가 직접 Funnel에 연결하는 별도의 수신 주소입니다([공유 링크용 공개 주소](#공유-링크용-공개-주소)). `Tailscale-User-*` 헤더는 무시하며 누가 읽고 쓰고 관리할 수 있는지는 여전히 비밀번호로 정합니다.
- 공유 기록은 네트워크 설정처럼 이 설치 호스트에 속하며 오프라인 백업에 포함되지 않습니다.

### 다른 비공개 네트워크

NetBird, Tailscale 클라이언트와 함께 쓰는 Headscale, 일반 WireGuard에서는 그 네트워크에서 이 컴퓨터가 쓰는 주소로 연결을 받게 하고 다른 기기가 쓰는 이름을 기본 URL로 저장한 뒤 다시 시작하세요.

```sh
owngit network set --listen 100.64.0.7:7654 --base-url http://gitbox.netbird.selfhosted:7654 --accept-insecure-http
```

OwnGit은 기본 URL의 이름과 연결 주소를 Host로 받아들이며 다른 이름은 `--allowed-host 이름`으로 추가합니다. NetBird는 각 기기에 `gitbox.netbird.selfhosted` 같은 이름을, Headscale은 MagicDNS 설정의 `base_domain` 아래 이름을 붙입니다. 일반 WireGuard에는 이름이 없으니 주소나 직접 운영하는 DNS를 쓰세요.

이런 네트워크가 트래픽을 암호화하지만 OwnGit은 그 사실을 알 수 없습니다. 그래서 설정 과정에서 여전히 일반 HTTP를 받아들일지 묻고 페이지 위쪽에는 "OwnGit이 암호화하지 않음"이라고 표시합니다. "Tailscale이 암호화함" 표시에 필요한 정보는 Tailscale 클라이언트만 주고 tailnet 공유 스위치도 Tailscale에서만 동작합니다. Headscale에서는 스위치가 켜지지 않고 제어 서버가 HTTPS 인증서를 제공하지 않는다고 알립니다.

HTTPS 주소를 쓰려면 이 컴퓨터에서 네트워크 주소로 연결을 받는 리버스 프록시를 실행하고 OwnGit은 `127.0.0.1`에 두세요. Caddy를 쓰면 다음과 같습니다.

```caddyfile
gitbox.netbird.selfhosted {
	bind 100.64.0.7
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

```sh
owngit network set --listen 127.0.0.1:7654 --base-url https://gitbox.netbird.selfhosted --trusted-proxy 127.0.0.1
```

저장한 뒤 OwnGit을 다시 시작하세요. `bind`는 Caddy가 LAN 주소로는 응답하지 않게 합니다. `tls internal`은 Caddy 자체 인증 기관으로 인증서에 서명하므로 각 기기가 그 인증 기관을 신뢰해야 합니다([Caddy](#caddy) 참고). 명령 하나에만 쓰려면 `curl --cacert root.crt`나 `git -c http.sslCAInfo=root.crt clone`처럼 인증서를 직접 지정해도 됩니다. Linux에서 NetBird 0.79, Headscale 0.29, WireGuard로 시험했습니다.

### 리버스 프록시 뒤에서 운영하기

Caddy, nginx, Traefik, Nginx Proxy Manager 같은 리버스 프록시를 쓰면 OwnGit에 `https://git.example.internal`처럼 호스트 이름 하나의 루트에 있는 HTTPS 주소를 붙일 수 있습니다. 다른 사이트 아래 경로에 두는 방식은 지원하지 않습니다. 프록시 주소와 HTTPS 주소를 저장한 뒤 다시 시작하세요.

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
owngit network show
```

프록시를 거쳐 열면 페이지 위쪽에 "OwnGit 앞의 프록시가 암호화함"이라고 표시됩니다.

프록시를 신뢰한다고 알려 주기 전까지 OwnGit은 모든 클라이언트를 프록시로 봅니다. 그래서 기기 하나에서 비밀번호를 잘못 입력하면 모든 기기가 함께 멈추고([로그인 시도 한도](#로그인-시도-한도)), 쿠키에 `Secure`가 붙지 않으며, HTTPS로 보낸 양식은 Origin 확인에서 거부됩니다.

`--trusted-proxy`에는 프록시가 접속해 오는 주소를 씁니다.

- 같은 컴퓨터에서 돌면 `127.0.0.1`
- 이 컴퓨터의 Docker에서 돌면 `172.18.0.0/16` 같은 그 컨테이너의 Docker 네트워크 범위
- 다른 컴퓨터에서 돌면 그 컴퓨터의 주소

OwnGit은 기본값으로 어떤 프록시도 믿지 않습니다. IPv4는 `/8`, IPv6는 `/32`보다 넓은 범위와, 지정되지 않은 주소 `0.0.0.0`, `::`는 거부합니다. 범위를 쓰면 그 안의 모든 컴퓨터를 믿게 되므로 가능한 한 좁게 잡으세요. `127.0.0.1`을 믿으면 `ssh -L` 터널처럼 이 컴퓨터에서 요청을 넘겨주는 다른 프로그램도 믿게 됩니다. `owngit serve --trusted-proxy 주소`는 그 실행에서만 저장된 목록 대신 쓰이고 `--trusted-proxy ""`를 주면 아무 프록시도 믿지 않습니다.

프록시가 같은 컴퓨터에서 돌면 OwnGit은 `127.0.0.1:7654`에서 연결을 받게 두어 다른 기기가 프록시를 거쳐야만 닿게 하세요. 컨테이너 안의 프록시는 호스트 네트워크를 쓰지 않는 한 호스트의 `127.0.0.1`에 닿지 못합니다. 이때는 프록시가 닿을 수 있는 주소에서 OwnGit이 연결을 받게 하고 그 주소를 신뢰하세요. OwnGit이 네트워크 주소에서 연결을 받으면 다른 기기가 프록시 없이 일반 HTTP로 직접 접속할 수도 있습니다. 막으려면 방화벽 규칙 등으로 프록시만 OwnGit의 포트에 닿게 하세요.

Git 요청 하나는 기본적으로 최대 4 GB를 주고받고 30분까지 걸릴 수 있으므로([Git 전송 제한](#git-전송-제한)) 프록시의 한도도 OwnGit의 한도 이상이어야 합니다. 아래 설정으로 네 프록시를 거쳐 푸시와 clone을 시험했습니다.

#### 신뢰하는 프록시에서 읽는 헤더

OwnGit은 신뢰하는 프록시에서 온 요청에서만 다음 세 헤더를 읽습니다. `X-Forwarded-Proto`와 `X-Forwarded-Host`는 한 번만, 값 하나로 와야 합니다. 여러 번 오거나, 쉼표로 이은 목록이 오거나, 아래에 적지 않은 값이 오면 무시합니다. `X-Forwarded-For`는 여러 줄로 오면 순서대로 이어 한 목록으로 읽습니다. `Forwarded` 헤더는 무시합니다.

- `X-Forwarded-Proto`: 값이 정확히 `https`나 `http`일 때 씁니다. `https`이면 쿠키에 `Secure`를 붙이고, 양식을 `https` 주소와 비교하고, 연결을 암호화된 것으로 표시하고, 일반 HTTP 확인을 묻지 않으며, Git에도 HTTPS로 온 요청이라고 알립니다. 요청이 프록시 두 개를 거친다면 OwnGit에 가까운 쪽 프록시는 자기 연결의 `http`가 아니라 받은 `X-Forwarded-Proto`를 그대로 넘겨야 합니다. nginx에서는 `$http_x_forwarded_proto`로 넘깁니다.
- `X-Forwarded-For`: 마지막 항목부터 거꾸로 읽습니다. 신뢰하는 프록시인 항목은 건너뛰고, 처음 만나는 신뢰하지 않는 주소를 클라이언트 주소로 씁니다. 그러니 요청이 프록시 여러 개를 거친다면 거치는 프록시를 모두 신뢰하는 프록시로 등록하세요. OwnGit은 이 주소로 비밀번호 잠금과 설정 승인 경고를 처리하므로 프록시 뒤의 기기는 따로 잠깁니다. 각 프록시는 요청을 보낸 쪽 주소를 직접 덧붙여야 합니다. 클라이언트가 보낸 헤더를 그대로 넘기는 프록시는 클라이언트가 잠금에 쓰일 주소를 고를 수 있게 합니다. 헤더가 없거나, 클라이언트 주소에 닿기 전에 IP 주소가 아닌 항목이 나오거나, 모든 항목이 신뢰하는 프록시라면 원래 주소를 알 수 없습니다. 이때 잠금은 가장 가까운 프록시 주소를 기준으로 세고, 설정 화면에는 "프록시를 거쳐 온 접속, 원래 주소 확인 불가"라고 나옵니다. 클라이언트 주소보다 왼쪽에 있는 항목은 읽지 않습니다.
- `X-Forwarded-Host`: OwnGit이 그 Host와 요청 자체의 Host를 모두 받아들일 때 씁니다. 아래 예시는 원래 Host를 그대로 넘깁니다.

#### 페이지가 기본 URL로 이동

프록시가 기본 URL로 온 HTTPS 요청을 OwnGit에 한 번 넘기고 나면, 브라우저가 `http://git.example.internal:7654/`처럼 기본 URL의 호스트와 OwnGit 포트로 대시보드 페이지를 직접 열 때 기본 URL의 같은 페이지로 이동합니다. [tailnet 공유](#tailnet에서-https로-공유하기)와 마찬가지로 페이지만 이동합니다.

- 기본 URL 자체가 `https://192.168.1.5`처럼 IP 주소나 `localhost`라면 그 주소와 OwnGit 포트로 연 페이지도 이동합니다. 다른 이름이나 주소로 연 페이지는 일반 HTTP로 남습니다.
- OwnGit은 시작할 때마다, 그리고 tailnet 공유를 켜거나 끌 때마다 이런 요청을 다시 기다립니다.
- 프록시가 멈추면 이렇게 연 페이지는 OwnGit을 다시 시작하거나 기본 URL이 바뀔 때까지 계속 기본 URL로 이동합니다. 그동안에는 다른 이름이나 주소로 OwnGit을 여세요.
- 프록시가 클라이언트 주소(`X-Forwarded-For`나 `Forwarded`)를 붙여 넘긴 요청은 이동하지 않습니다. 스킴은 프록시가 정합니다. 이 컴퓨터의 브라우저가 그런 페이지를 직접 열면, 이 컴퓨터의 주소가 신뢰하는 프록시여도 이동합니다.

#### Caddy

```caddyfile
git.example.internal {
	tls internal
	reverse_proxy 127.0.0.1:7654
}
```

`reverse_proxy`는 원래 Host를 넘기고, `X-Forwarded-Proto`를 설정하며, `X-Forwarded-For`를 클라이언트 주소로 설정합니다. 긴 푸시를 끊는 크기 한도나 시간 제한도 없습니다. `tls internal`은 Caddy 자체 인증 기관으로 인증서에 서명하게 하므로 각 기기가 그 인증 기관을 신뢰해야 합니다. 이 줄은 빼지 마세요. Debian 13의 2.6.2 같은 예전 Caddy는 이 줄이 없으면 `git.example.internal` 같은 이름에 로컬 인증서를 발급하지 않습니다. Caddy의 Debian 패키지에서는 그 인증 기관의 루트 인증서가 `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`에 있으며 root와 `caddy` 사용자만 읽을 수 있습니다.

#### nginx

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

- 크기와 시간 제한은 OwnGit의 한도에 맞춘 값입니다.
- 버퍼링을 끄는 두 줄은 푸시, clone, 압축 파일을 디스크에 먼저 저장하지 않고 받는 대로 넘깁니다.
- `$proxy_add_x_forwarded_for`는 클라이언트 주소를 덧붙입니다.
- 빈 `X-Forwarded-Host`는 클라이언트가 이 헤더를 직접 보내지 못하게 합니다.

클라이언트가 443이 아닌 포트로 접속한다면 `proxy_set_header Host $http_host;`로 바꿔 OwnGit이 보는 Host가 브라우저의 주소와 같게 하세요.

#### Traefik

Traefik은 원래 Host를 넘기고 전달 헤더를 직접 설정합니다. 하지만 엔트리포인트가 기본값으로 요청을 60초까지만 읽으므로 긴 푸시가 HTTP 504로 끊깁니다. 정적 설정에서 `readTimeout`을 늘리고 라우터, 서비스, 인증서는 동적 설정 파일에 넣으세요.

```yaml
# /etc/traefik/traefik.yml (static configuration)
entryPoints:
  websecure:
    address: ":443"
    transport:
      respondingTimeouts:
        readTimeout: 30m
providers:
  file:
    filename: /etc/traefik/dynamic.yml
```

```yaml
# /etc/traefik/dynamic.yml
http:
  routers:
    owngit:
      rule: Host(`git.example.internal`)
      entryPoints: [websecure]
      service: owngit
      tls: {}
  services:
    owngit:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:7654
tls:
  certificates:
    - certFile: /etc/ssl/git.example.internal.crt
      keyFile: /etc/ssl/git.example.internal.key
```

Traefik은 `traefik --configFile=/etc/traefik/traefik.yml`로 시작합니다. 릴리스 바이너리로 설치한 Traefik 3.7에서 시험했습니다. Docker에서 돌면 Traefik이 접속해 오는 주소를 신뢰하세요.

#### Nginx Proxy Manager

프록시 호스트를 만들고 scheme은 `http`, OwnGit의 주소와 포트를 넣고, SSL 인증서를 붙이고 Force SSL을 켜세요. "Trust Upstream Forwarded Proto Headers"는 끈 채로 둡니다. 그다음 Advanced 탭의 Custom Nginx Configuration에 다음 줄을 넣으세요.

```nginx
client_max_body_size 4g;
proxy_request_buffering off;
proxy_read_timeout 30m;
proxy_send_timeout 30m;
set_real_ip_from 127.0.0.1;
```

Nginx Proxy Manager는 기본값으로 요청 본문을 2000 MB로 제한하고, OwnGit을 90초만 기다리며 큰 응답을 임시 파일에 저장합니다. 사설 주소에서 온 `X-Real-IP` 헤더도 클라이언트 주소로 받아들이므로, 내 네트워크의 기기가 OwnGit이 잠금에 쓰는 주소를 정할 수 있게 됩니다. `set_real_ip_from 127.0.0.1;`은 각 기기가 접속해 온 주소를 그대로 알려 주게 합니다.

이 줄들은 Websockets Support를 켜든 끄든 그대로 쓸 수 있습니다. `proxy_buffering off;`는 더해도 되지만 `proxy_http_version`은 넣지 마세요. Nginx Proxy Manager가 이미 설정하므로 Websockets Support를 켠 채 두 번 들어가면 호스트가 동작하지 않습니다. 접속해 오는 주소는 위에서 설명한 대로 신뢰하세요. Linux의 Docker Engine에서 돌린 Nginx Proxy Manager 2.16.0과 IPv4 클라이언트로 시험했습니다. Docker Desktop, rootless Docker, IPv6 클라이언트는 시험하지 않았습니다.

## 새 릴리스 알림

설정을 마친 뒤 OwnGit은 하루에 한 번 GitHub에 새 릴리스가 있는지 묻습니다. 새 버전이 있으면 대시보드에 릴리스 노트와 [설치](../README.ko.md#설치)로 가는 링크가 있는 알림이 나타납니다. OwnGit은 아무것도 직접 내려받거나 설치하지 않습니다.

- 관리자로 확인한 사람에게는 이 설치에 맞는 업데이트 명령([업데이트와 제거](#업데이트와-제거) 참고)과 복사 버튼도 보입니다. 명령에는 이 컴퓨터의 경로가 들어 있으므로 다른 사람에게는 이 컴퓨터에서 `owngit update`를 실행하거나 관리자로 확인하라고 안내합니다.
- 알림 닫기는 지금 쓰는 브라우저에서 그 버전의 알림을 숨깁니다.
- 확인에 실패하면 아무것도 표시하지 않고 로그를 최대 한 줄만 남깁니다.

확인은 OwnGit과 그 버전을 밝힌 User-Agent를 담아 `https://api.github.com/repos/juliankang4/owngit/releases/latest`에 보내는 HTTPS 요청 한 번입니다. 서버가 시작되고 약 30초 뒤나 설정을 마친 직후에 보냅니다. 저장소 데이터는 보내지 않으며 GitHub는 서버의 주소를 볼 수 있습니다. 초안과 사전 릴리스는 무시합니다. [가져오기](#다른-git-호스트에서-가져오기)와 직접 실행한 `owngit update`를 빼면 OwnGit이 다른 호스트에 여는 연결은 이것뿐입니다.

확인을 끄려면 설정 화면 일반 탭의 업데이트 확인에서 끄고 관리자 비밀번호로 저장하거나 `owngit settings set --update-check off`를 실행하세요. 이 설정은 이 설치 호스트에 속하며 백업에 들어가지 않습니다. 절대 확인하면 안 되는 환경이라면 서버를 `--no-update-check`로 시작하세요. 이 옵션이 저장된 설정보다 우선합니다.

```sh
owngit serve --no-update-check
```

## 설치 호스트에서 복구하기

두 절차 모두 설치 호스트에 접근할 수 있어야 합니다. OwnGit에는 이메일 복구나 계정 복구 기능이 없습니다. 대시보드를 쓸 수 없을 때 하는 절차이므로 명령으로만 할 수 있습니다.

설정을 마치기 전이라면 설치 호스트의 터미널에서 새 설정 링크를 발급합니다. `--base-url http://127.0.0.1:7654`를 붙이면 서버가 연결을 받는 주소 대신 그 주소로 만듭니다.

```sh
owngit setup-link --no-open
```

잊어버린 관리자 비밀번호를 재설정하려면 새 비밀번호를 소유자만 읽을 수 있는 파일에 넣고([비밀번호 파일과 토큰 파일](#비밀번호-파일과-토큰-파일) 참고) 다음 명령을 실행하세요.

```sh
owngit reset-admin --password-file /path/to/owner-only-password-file
```

재설정하면 모든 브라우저의 관리자 확인이 끝나고 저장소는 그대로 남습니다. [관리자 비밀번호 확인](#관리자-비밀번호-확인) 선택은 묻지 않기까지 포함해 그대로 유지됩니다.

### 비밀번호 파일과 토큰 파일

`reset-admin`, `settings`, `import`, `pr`, `repo`를 포함해 직접 만든 비밀번호 파일이나 토큰 파일을 읽는 명령은 모두 본인 계정만 읽을 수 있는 일반 파일을 요구합니다. OwnGit은 비밀번호를 명령줄 값으로 받지 않습니다. 새 비밀번호를 정하는 명령은 이런 파일에서 읽거나 화면에 보이지 않는 프롬프트에서 묻습니다. 파일을 거부할 때는 어떤 계정이 파일을 더 읽을 수 있는지와 이를 고치는 명령을 알려 줍니다.

비밀번호 파일에는 비밀번호를 한 줄로 적습니다. 끝에 줄바꿈이 하나 있어도 됩니다. 줄이 더 있거나 비밀번호가 너무 짧으면 그 이유를 알려 주며 거부합니다.

macOS와 Linux에서는 `umask 077`을 적용한 상태에서 파일을 만들거나 `chmod 600 FILE`로 고치세요. Windows에서 메모장이나 `echo`로 만든 파일은 폴더의 접근 항목을 상속합니다. 그래서 PowerShell에서 파일을 만들고 본인 계정으로 접근을 제한한 다음에 비밀번호를 적으세요.

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

이 명령은 Windows PowerShell 5.1과 PowerShell 7에서, 일반 창과 관리자 권한으로 실행한 창 모두 동작합니다. 관리자 창에서는 Administrators 그룹이 소유자가 되는데 본인 계정만 접근할 수 있으면 OwnGit은 이 소유자를 받아들입니다. `Read-Host -AsSecureString`은 비밀번호를 화면과 기록에 남기지 않습니다.

## 저장소

### 저장소 파일 되돌리기

이전 커밋의 파일을 되살리려면 다음 중 한 곳에서 되돌리기를 시작합니다.

- 저장소 개요(파일 되돌리기 칸의 되돌리기 시작)
- 브랜치, 태그, 보관된 기록 줄(여기서 파일 되돌리기)
- 파일 페이지(이 파일 되돌리기)

원본 커밋과 대상 브랜치를 고르고 추가, 변경, 삭제될 파일의 전체 목록을 미리 본 뒤 적용합니다. OwnGit은 대상 브랜치의 끝 커밋이 미리 볼 때와 그대로일 때만 새 커밋을 추가하거나, 삭제된 브랜치를 선택한 커밋에서 다시 만듭니다.

되돌리기는 OwnGit 안의 Git 내용만 바꾸며 다른 컴퓨터의 워킹 트리는 건드리지 않습니다. 기록을 다시 쓰지 않으므로 [보호된 기본 브랜치](#기본-브랜치-바꾸기)에서도 동작하고 보관된 기록에 새로 남는 것도 없습니다.

일부 파일만 되돌릴 때는 선택하지 않은 파일, 파일 모드, 바이너리 파일, 심볼릭 링크를 그대로 두고, 호스트에서 링크를 따라가지 않습니다. 서브모듈이나, 바꾸면 그 아래의 선택하지 않은 파일이 지워지는 경로는 거부합니다.

`main` 옆의 `Main`처럼 일부 파일 시스템이 다른 브랜치와 같은 이름으로 보는 대상 브랜치도 거부합니다. 둘 중 하나를 먼저 지우거나 이름을 바꾸세요. 명령줄과 MCP에서는 거부(`invalid_restore`) 메시지에 겹치는 브랜치 이름이 나옵니다.

#### 명령줄에서 되돌리기

명령줄과 [MCP 서버](CODING_TOOLS.ko.md#mcp-서버)에서도 같은 되돌리기를 할 수 있으며, 되돌리기 화면과 같은 일반 접근을 씁니다. 먼저 미리 본 다음 그 내용을 적용합니다.

```sh
owngit repo kept-history --repository NAME
owngit repo restore preview --repository NAME --source OID --target main
owngit repo restore apply --repository NAME --source OID --target main --expected-head OID
```

- `repo kept-history`는 보관된 기록을 JSON으로 보여 줍니다. 항목마다 어느 브랜치나 태그에서 보관됐는지, 그 커밋, 그리고 대시보드가 되돌릴 곳으로 제안하는 새 브랜치(`restore_target`)가 들어 있습니다.
- `repo restore preview`는 원본 커밋(`--source`, 전체 커밋 ID)의 트리 전체를 대상 브랜치에 되돌리면 무엇이 바뀌는지 보여 줍니다. `--target`에는 `refs/`로 시작하는 전체 ref 이름이 아니라 `main` 같은 브랜치 이름을 씁니다. 일부 파일만 되돌리려면 파일마다 `--path FILE`을 붙입니다. 미리 보기는 아무것도 바꾸지 않습니다. 되돌리기로 추가, 변경, 삭제될 모든 경로를 전후의 Git 파일 모드(`120000`은 심볼릭 링크)와 함께 보여 주고 미리 볼 때의 브랜치 끝 커밋을 `expected_head`로 알려 줍니다.
- `repo restore apply`는 같은 옵션에 `--expected-head`로 그 값을 넣어 실행합니다. 미리 본 뒤 브랜치가 움직였으면 적용은 `stale_revision`으로 거부되고 아무것도 바뀌지 않으니 다시 미리 보세요.

### 보관된 기록

강제 푸시나 가져오기, 삭제로 브랜치나 태그의 커밋이 바뀌면 OwnGit은 이전 커밋을 보관된 기록으로 남겨 둡니다. 이 기록은 둘러보거나 [되돌릴](#저장소-파일-되돌리기) 수 있습니다. 기본값은 보관이며 관리자가 끌 수 있습니다.

- 서버 설정을 따르는 모든 저장소: 설정 화면 저장소 탭의 보관된 기록 항목에서 덮어쓰거나 지운 기록을 보관하지 않음으로 바꾸거나 `owngit settings set --kept-history off`를 실행합니다.
- 저장소 하나: 저장소 설정 탭의 보관된 기록에서 서버 설정 따르기, 보관, 보관하지 않음 중 하나를 고르거나 `owngit repo settings set --repository NAME --kept-history off`를 실행합니다(`default`는 서버 설정을 따릅니다). 저장소에서 따로 고른 값은 서버 설정과 관계없이 적용됩니다.

바꾼 값은 저장한 뒤 시작하는 푸시와 가져오기부터 적용되며 이미 진행 중인 실행은 시작할 때의 선택을 따릅니다. 보관하지 않음을 고르면 그 뒤로 바뀐 커밋은 보관되지 않아 보관된 기록에 나오지 않고 그 기록에서 되돌릴 수도 없습니다. 이런 커밋도 전체 커밋 ID로는 열릴 수 있습니다. 이미 보관된 기록은 그대로 남아 계속 되돌릴 수 있으며 지워지는 것은 없습니다. fast-forward 푸시, 병합, 되돌리기, 백업, 저장소 삭제는 어느 쪽이든 똑같이 동작합니다.

`owngit repo settings show --repository NAME`은 저장소의 선택을 JSON으로 출력하며 지금 실제로 적용되는 값은 `kept_history_now`에 나옵니다. 두 `repo settings` 명령 모두 관리자 비밀번호가 든 `--password-file`이 필요합니다. 그 저장소의 clone 안에서는 `--server`와 `--repository`를 `origin` 원격에서 가져오며 이때 비밀번호 파일의 첫 줄에 그 서버가 적혀 있어야 합니다([자격 증명 파일과 서버 줄](CODING_TOOLS.ko.md#자격-증명-파일과-서버-줄) 참고). 저장소에 저장된 선택을 읽을 수 없으면 다시 저장할 때까지 그 저장소로 오는 푸시와 가져오기를 거부합니다. 명령줄에서 다시 저장할 때는 `--kept-history`와 `--protect-default-branch`를 함께 주세요. 서버 전체의 선택을 읽을 수 없는 동안에는 저장소가 계속 서버 설정을 따르게 되는 변경은 보호를 켜거나 끄는 것까지 모두 거부하며 아무것도 저장하지 않습니다. 같은 변경에서 저장소의 보관이나 보관하지 않음을 함께 고르거나 서버 설정을 다시 정하세요.

### 기본 브랜치 바꾸기

기본 브랜치는 OwnGit과 `git clone`이 처음 여는 브랜치(저장소의 `HEAD`)입니다. 관리자는 저장소의 설정 탭에서 기존 브랜치 중 하나를 고르거나, 관리자 비밀번호가 든 `--password-file`을 붙여 `owngit repo default-branch --repository NAME --branch BRANCH`를 실행합니다. 그 저장소의 clone 안에서는 `--server`와 `--repository`를 `origin` 원격에서 가져옵니다. 브랜치가 `master` 하나뿐인 가져온 저장소는 직접 고르기 전까지 기본 브랜치가 없다고 표시됩니다. 기본 브랜치를 바꿔도 브랜치가 새로 생기지 않으며 모든 ref와 보관된 기록은 그대로입니다.

기본 브랜치를 다시 쓰거나 지우지 못하게 하려면 저장소 설정 탭에서 기본 브랜치 보호를 켜거나 `owngit repo settings set --repository NAME --protect-default-branch on`을 실행하세요. 기본값은 꺼짐입니다. 켜 두면 기본 브랜치에 대한 fast-forward가 아닌 푸시와 기본 브랜치를 지우는 푸시를 거부하고 Git에는 `remote: OwnGit protects the default branch main and refused ...`와 `! [remote rejected]`가 나옵니다. 커밋을 더하는 푸시, 다른 브랜치와 태그, 풀 리퀘스트 병합, 파일 되돌리기, 저장소 삭제는 전과 같이 동작합니다. 가져오기는 기본 브랜치의 fast-forward는 그대로 따라갑니다. 원본이 기본 브랜치를 다시 썼거나 [원본과 달라진 브랜치 덮어쓰기](#원본과-달라진-브랜치-덮어쓰기)가 여기서 바뀐 기본 브랜치를 덮어쓰게 되면 새로고침은 `protected_default_branch`로 실패하고 아무것도 바꾸지 않습니다. 원본을 따르려면 보호를 끄고 다시 새로고침하세요. 보호 대상은 HEAD가 가리키는 브랜치입니다. HEAD가 다른 심볼릭 ref를 거쳐 가리켜도 마찬가지입니다. 기본 브랜치를 바꾸면 보호도 새 기본 브랜치로 옮겨 갑니다.

새 저장소는 `main` 브랜치로 시작합니다. `trunk`처럼 다른 브랜치로 시작하게 하려면 설정 화면 저장소 탭의 새 저장소에서 첫 브랜치를 바꾸거나 `owngit settings set --initial-branch trunk`를 실행하세요. 이름은 Git이 받아들이는 이름이어야 하며 영문자, 숫자, `-`, `_`, `.`, `/`로 100자까지 씁니다. 바꾼 뒤 대시보드, 명령줄, API로 만드는 저장소에 적용되고 이미 있는 저장소의 브랜치는 그대로입니다. 가져온 저장소는 원본의 기본 브랜치를 씁니다. 빈 저장소 페이지에는 그 저장소의 브랜치로 푸시하는 `git push` 명령이 나옵니다.

### 다른 ref 이름공간

푸시로 바꿀 수 있는 것은 브랜치(`refs/heads/`)와 태그(`refs/tags/`)입니다. `refs/notes/` 아래의 Git 노트처럼 다른 ref도 받게 하려면, 관리자가 저장소 설정 탭의 다른 ref 이름공간에 이름공간을 한 줄에 하나씩 적거나 다음 명령을 실행합니다. 이 설정은 저장소마다 따로 정합니다.

```sh
owngit repo settings set --repository NAME --extra-ref-prefixes refs/notes/,refs/meta/
```

기본값은 비어 있으며 빈 값을 주면 목록이 모두 지워집니다. 바꾼 목록은 저장한 뒤 시작하는 푸시부터 적용됩니다.

- 이름공간은 `refs/notes/`처럼 `refs/`로 시작하고 `/`로 끝납니다. ASCII 영문자, 숫자, `-`, `_`, `.`, `/`만 쓰며 100자를 넘을 수 없습니다. 저장소 하나에 32개까지 적을 수 있습니다.
- `refs/heads/`, `refs/tags/`, OwnGit이 쓰는 `refs/owngit/`, 목록의 다른 이름공간과 겹치는 이름공간은 대소문자와 관계없이 받지 않습니다. 겹친다는 것은 한쪽이 다른 쪽 안에 들어간다는 뜻입니다. `refs/notes/a/`와 `refs/Notes/b/`처럼 공유하는 폴더가 대소문자만 다른 두 이름공간도 받지 않습니다.
- 이 이름공간의 ref는 보관된 기록도 보호도 없습니다. 푸시로 덮어쓰거나 지울 수 있고 이전 값은 남지 않습니다. 이미 있는 ref와 일부 파일 시스템에서 같은 이름으로 취급되는 ref는 여기서도 거부합니다.
- 그 밖의 ref로 오는 푸시는 `OwnGit accepts branches, tags and the ref namespaces listed in the repository's settings.`와 함께 거부합니다.
- `HEAD`가 아닌 심볼릭 ref(symbolic ref)를 바꾸는 푸시는 어느 이름공간에서든 거부하며, 그 ref가 가리키는 ref로 직접 푸시하라고 안내합니다.
- 저장된 목록을 읽을 수 없으면 목록을 다시 저장할 때까지 그 저장소로 오는 푸시를 거부합니다.
- 백업에는 이 목록과 이름공간 안의 ref가 들어가고 복원하면 둘 다 돌아옵니다. 이 목록은 가져오기에 영향을 주지 않습니다. 가져오기는 따로 [추가 ref 네임스페이스](#추가-ref-네임스페이스)를 씁니다.

`owngit repo settings show`는 이 목록을 `extra_ref_prefixes`로 보여 줍니다. 저장소 설정 API도 같은 필드를 받으며 목록 전체를 바꿉니다. 푸시가 어디로 갈 수 있는지는 일반 접근 권한만 있어도 볼 수 있습니다. `owngit repo show`와 MCP 도구 `repository_show`는 푸시할 수 있는 이름공간을 `push_ref_namespaces`에 보여 줍니다. 목록을 읽을 수 없으면 그 이유를 `push_ref_namespaces_error`에 알려 줍니다.

### 저장소 이름 바꾸기

관리자는 저장소 이름을 바꿀 수 있습니다. 이름을 바꾸면 저장소 화면 주소와 클론 주소가 새 이름으로 옮겨 가고 저장소 안의 내용은 모두 그대로 남습니다. 예전 주소는 90일 동안 새 주소로 안내합니다. 그동안 기존 클론은 계속 동작하니 그 사이에 클론의 주소를 바꾸면 됩니다.

이름은 다음 중 한 곳에서 바꿉니다.

- 대시보드: 저장소의 설정 탭을 열고 이름과 주소 항목에 새 이름을 입력한 뒤 이름 바꾸기를 누릅니다. [설정에 따라](#관리자-비밀번호-확인) 관리자 비밀번호도 묻습니다.
- 명령줄: `owngit repo rename NAME NEW-NAME --server URL --password-file ADMIN-PASSWORD-FILE`을 실행합니다. 파일에는 관리자 비밀번호를 넣으며 명령은 이름을 바꾼 저장소를 JSON으로 출력합니다.
- API: `POST /api/v1/repositories/NAME/rename`에 본문 `{"name":"NEW-NAME"}`을 보냅니다. 사용자 이름 `admin`과 관리자 비밀번호로 Basic 인증을 합니다.

그다음 모든 클론이 새 클론 주소를 쓰도록 바꿉니다. 새 주소는 저장소 화면에 나옵니다.

```sh
git remote set-url origin http://HOST:7654/git/NEW-NAME.git
```

저장소를 이름으로 가리키는 [러너와 체크 에이전트](#이름을-바꾼-뒤의-러너와-체크-에이전트)도 같이 바꾸세요.

#### 이름을 바꾸면 달라지는 것

저장소 화면 주소와 클론 주소는 새 이름을 소문자로 바꾼 것을 씁니다. 대시보드에 보이는 이름은 입력한 대소문자를 그대로 쓰므로, `Tools-2`로 바꾸면 `Tools-2`로 보이고 주소는 `tools-2`가 됩니다. 대소문자만 바꾸면 보이는 이름만 바뀌고 주소는 그대로입니다.

파일, 기록, 풀 리퀘스트, 체크, 가져오기, 보관된 기록, 활동 내역은 그대로입니다. 저장 폴더와 저장소 ID도 바뀌지 않습니다. 저장소 ID는 저장소를 만들 때 쓴 이름의 소문자입니다.

#### 예전 주소

이름을 바꾼 뒤 90일 동안은 예전 주소가 새 주소로 안내합니다.

- 화면과 API는 새 주소의 같은 화면이나 같은 요청으로 리디렉션합니다.
- Git은 리디렉션을 따라가므로 기존 클론에서 계속 가져오고 푸시할 수 있습니다. 그때마다 Git이 `warning: redirecting to`와 새 주소를 출력합니다.
- 일반 접근이나 관리자 비밀번호를 쓰는 `owngit` 명령과 MCP 도구는 리디렉션을 따라가지 않습니다. `origin`이나 `--repository`를 바꾸기 전까지는 아무것도 바꾸지 않고 `repository_moved`로 멈추며 `details.address`에 새 이름이 나옵니다. 러너와 체크 에이전트는 [아래](#이름을-바꾼-뒤의-러너와-체크-에이전트)처럼 다르게 동작합니다.

설정 탭에는 아직 이 저장소로 안내하는 예전 주소와 각 주소의 안내가 끝나는 시각이 나옵니다. 그 시각이 지나면 예전 주소는 없는 저장소처럼 404로 답합니다.

안내가 끝난 예전 이름은 다시 비어서 새 저장소를 만들거나 다른 저장소의 이름을 바꿀 때 쓸 수 있습니다. 그러면 아직 예전 주소를 쓰는 클론은 그 이름을 가져간 저장소에 연결됩니다. 가져오면 그 저장소의 내용을 받고 푸시하면 그 저장소에 씁니다. 이런 일을 막으려면 90일 안에 `git remote set-url`로 원격 주소를 바꾸세요. 처음 이름만은 예외입니다. 처음 이름은 저장소 ID로 남기 때문에 이 저장소가 있는 동안 다른 저장소가 가져갈 수 없습니다. 다만 90일이 지나면 그 주소도 404로 답합니다.

#### 이름을 바꾼 뒤의 러너와 체크 에이전트

[러너](AUTOMATIC_CHECKS.ko.md) 토큰과 [체크 에이전트](CODING_TOOLS.ko.md) 토큰은 이름이 아니라 저장소에 묶여 있으므로 이름을 바꾼 뒤에도 그대로 쓸 수 있습니다. `--repository`나 클론의 `origin`으로 예전 주소를 쓰도록 설정한 러너와 체크 에이전트는 90일 동안 그 주소에서 계속 동작합니다. 90일이 지나면 예전 주소에서는 멈추므로 그 전에 새 이름으로 바꾸세요. 예를 들면 `owngit runner --repository NEW-NAME`입니다. 90일이 지나면 예전 주소는 이 토큰을 다른 저장소의 토큰으로 보고 거절합니다. 없는 이름에서 받는 답과 같으므로, 토큰으로는 어떤 이름이 있는지 알 수 없습니다.

나중에 다른 저장소가 예전 이름을 가져가면, 이름을 바꾼 저장소의 토큰은 그 주소에서 거부됩니다. 새 토큰을 발급하면 출력의 `repository_address`에 저장소의 지금 주소가 나옵니다.

#### 이름 바꾸기가 거부될 때

다른 저장소가 대소문자와 관계없이 이미 쓰는 이름은 거부합니다. 그 저장소의 이름이나 ID이거나, 아직 그 저장소로 안내하는 예전 이름인 경우입니다. 예약된 이름 `new`, `new-import`와 [이름 규칙](#저장소-폴더)에 맞지 않는 이름도 거부합니다.

저장소가 사용 중일 때도 거부합니다. 가져오기나 체크가 실행 중일 때, 푸시나 클론 같은 Git 작업이 저장소를 쓰고 있을 때, 유지 관리가 실행 중일 때, 백업이 저장소를 읽고 있을 때입니다. 끝난 뒤 다시 시도하세요. 거부된 경우에는 아무것도 바뀌지 않습니다.

#### 이름 바꾸기 더 알아보기

- 처음 이름으로는 언제든 되돌릴 수 있습니다. 되돌리면 그 이름이 다시 저장소 주소가 됩니다.
- 공용 비밀번호로 보호할 때 비밀번호가 없는 사람은 이름이 바뀌었다는 사실을 알 수 없습니다. 예전 화면 주소로 들어오면 로그인 화면으로 보내고 API와 Git은 리디렉션하기 전에 비밀번호부터 묻습니다.
- `http.followRedirects=false`로 설정한 Git 클라이언트는 예전 주소에서 리디렉션을 따라가지 않고 `The requested URL returned error: 307`로 실패합니다. 이 클론의 원격 주소를 바꾸세요.
- 백업에는 저장소마다 이름과 예전 주소가 들어 있으며 복원하면 안내가 끝나는 시각까지 그대로 돌아옵니다.
- 설정 탭에서는 OwnGit이 저장소의 Git 데이터를 읽을 수 있을 때만 이름을 바꿀 수 있습니다. 읽을 수 없으면 `owngit repo rename`이나 API를 쓰세요.

### 공유 링크

공유 링크가 있으면 채용 담당자, 외주 작업자, 검토자처럼 계정이 없는 사람도 저장소 하나를 읽을 수 있습니다. 관리자가 링크를 만들기 전에는 아무것도 공유되지 않습니다.

관리자는 저장소 설정 탭의 공유 링크에서 링크를 만들고 폐기합니다. 링크마다 다음을 정합니다.

- **이름**: 누구에게 주는 링크인지 적습니다. 관리자만 볼 수 있습니다(한 줄, 100바이트 이하).
- **권한**: *파일과 기록 보기* 또는 *보기와 Git 복제*.
- **만료**: 1일, 7일, 30일(기본값), 90일 뒤, 또는 *폐기할 때까지*. 명령줄과 API에서는 1일부터 3650일까지 원하는 일수를 정할 수도 있습니다.
- **추가 비밀번호**(선택, 8자 이상 1024자 이하): 방문자가 내용을 보기 전에 입력해야 합니다.

링크를 만들면 OwnGit은 `https://HOST/share/SECRET` 같은 주소를 한 번만 보여 줍니다. OwnGit은 비밀값의 SHA-256 지문만 보관하므로 주소를 다시 보여 줄 수 없습니다. 주소를 잃어버렸다면 새 링크를 만들고 이전 링크는 폐기하세요. 목록에는 링크마다 이름, 권한, 만료, 마지막 사용, 상태(사용 중, 만료됨, 폐기됨)와 방문자 페이지 주소의 앞부분(`/share/ID`)이 나옵니다. 폐기하면 링크가 바로 작동을 멈춥니다. 폐기되거나 만료된 링크도 기록으로 목록에 남습니다.

만료가 없는 링크, 복제를 허용하는 링크(링크를 폐기해도 이미 가져간 복사본은 되돌릴 수 없습니다), 추가 비밀번호가 없는 링크를 만들 때 OwnGit은 거부하지 않고 경고만 보여 줍니다.

**방문자가 보는 것.** 주소를 열면 브라우저에 그 링크의 쿠키가 저장되고 `/share/ID`로 이동하므로 비밀값이 주소 표시줄에서 사라집니다. 방문자는 저장소 개요, 파일, README, 브랜치와 태그의 커밋, 원본 파일을 탐색 한도 안에서 볼 수 있습니다. 다른 저장소, 대시보드, 활동, 풀 리퀘스트, 체크와 그 로그, 보관된 기록, 가져오기 정보, 설정, 무언가를 바꾸는 기능은 보이지 않습니다. 보관된 기록이나 브랜치와 태그가 아닌 ref에만 있는 커밋은 찾을 수 없다고 나옵니다. 알 수 없거나 만료되었거나 폐기된 링크는 *찾을 수 없음*으로 답합니다. 추가 비밀번호가 틀리면 틀렸다고 알려 주며 틀린 비밀번호는 [로그인 시도 한도](#로그인-시도-한도)에 따라 주소별로 로그인과 따로 셉니다. 그래서 방문자가 비밀번호를 틀려도 OwnGit 로그인은 멈추지 않습니다.

**복제.** 복제 링크의 페이지에는 Git 주소 `https://HOST/share/ID.git`이 나옵니다. Git이 사용자 이름과 비밀번호를 물으면 다음처럼 입력합니다.

- 추가 비밀번호가 없으면 비밀번호에 공유 링크의 `/share/` 뒤 부분을 넣고, 사용자 이름은 아무거나 씁니다.
- 추가 비밀번호가 있으면 사용자 이름에 `/share/` 뒤 부분을, 비밀번호에 추가 비밀번호를 넣습니다.

복제와 fetch로는 브랜치와 태그만 받을 수 있습니다. 저장소의 Git 설정과 상관없이 [다른 ref 이름공간](#다른-ref-이름공간)의 ref와 보관된 기록은 받을 수 없습니다. 푸시는 거부됩니다. 인증 정보가 틀리면 다른 Git 로그인 실패와 같은 응답을 받습니다.

**이름 변경, 삭제, 백업.** 링크는 저장소 주소가 아니라 저장소 자체를 가리키므로 이름을 바꿔도 계속 작동합니다. 저장소를 삭제하면 링크도 함께 삭제됩니다. 공유 링크는 백업에 들어가지 않으므로 복원한 뒤에는 링크가 없습니다. 새로 만드세요.

**비밀값이 드러날 수 있는 곳.** 비밀값은 첫 요청인 `/share/SECRET`의 경로에만 들어 있습니다. OwnGit은 이를 로그에 남기지 않고 `/share/ID`로 이동시킵니다. 이후 페이지, 링크, `Referer` 헤더에는 비밀값이 들어가지 않습니다(`/share/` 아래의 응답은 거부와 오류를 포함해 `Referrer-Policy: no-referrer`를 보냅니다. 주소에 비밀값이 없는 추가 비밀번호 입력 페이지만 `same-origin`을 보냅니다). 다만 OwnGit 앞의 리버스 프록시는 그 첫 경로를 로그에 남길 수 있으니 프록시를 거쳐 링크를 보내기 전에 접근 로그 설정을 확인하세요. Git에서는 비밀값이 로그인 정보의 일부입니다(추가 비밀번호가 없으면 비밀번호, 있으면 사용자 이름). 그래서 Git 자격 증명 도우미가 클론의 다른 로그인 정보와 함께 저장할 수 있습니다.

명령줄에서는 `--password-file`에 관리자 비밀번호를 넣고 실행합니다.

```sh
owngit repo share list   --repository NAME
owngit repo share create --repository NAME --label "Reviewer" [--scope browse|clone] [--days 30 | --until-revoked] [--link-password-file PATH]
owngit repo share revoke --repository NAME --id ID
```

저장소를 복제한 폴더 안에서는 `--server`와 `--repository`를 `origin` 원격에서 가져옵니다. `create`는 비밀값이 든 링크를 `url`에 한 번만 출력하고, 복제 링크라면 `clone_url`과 `clone_sign_in`도 출력합니다. `list`는 비밀값을 출력하지 않습니다. 추가 비밀번호는 소유자만 읽을 수 있는 파일에서 읽습니다([비밀번호 파일과 토큰 파일](#비밀번호-파일과-토큰-파일)). 소유자 API는 관리자 비밀번호로 `GET`, `POST /api/v1/repositories/NAME/share-links`(`label`, `scope`, `expires_in_days` 또는 `until_revoked`, `password`)와 `POST /api/v1/repositories/NAME/share-links/ID/revoke`를 씁니다. MCP 서버는 공유 링크를 관리하지 않습니다.

#### 공유 링크용 공개 주소

OwnGit 자체는 비공개로 두고, 공유 링크에만 두 번째 공개 주소를 줄 수 있습니다. 예를 들어 Tailscale Funnel이나 인터넷에 열린 리버스 프록시를 씁니다. 이 주소에서는 공유 링크 페이지, 공유 링크 복제, 그 페이지가 쓰는 몇 개의 파일(스타일시트와 글꼴, 스크립트, 로고)만 답합니다. 대시보드, 로그인, 설정 과정, 설정 화면, API, `/git/`, 링크가 없는 저장소 등 그 밖의 경로는 모두 똑같이 `404 page not found`로 답합니다. Funnel을 거친 요청도 이 주소에서는 받으며, OwnGit 자체 주소는 계속 거부합니다.

공개 주소는 기본으로 꺼져 있습니다. 켜려면 이 컴퓨터의 빈 포트와 방문자가 쓸 주소를 정해 둘 다 저장합니다.

- 설정 화면의 네트워크 탭에서 *공유 링크용 공개 주소*를 열고 *수신 주소*(예: `127.0.0.1:7655`)와 *공개 URL*(예: `https://box.tail1234.ts.net:8443`)을 적은 뒤 저장합니다.
- 또는 `owngit network set --public-share-listen 127.0.0.1:7655 --public-share-url https://box.tail1234.ts.net:8443`을 실행합니다.

두 값이 모두 필요하며 수신 주소는 OwnGit 자체 주소와 다른 포트를 써야 합니다. 다른 네트워크 설정처럼 OwnGit을 다음에 시작할 때 적용됩니다. 다시 시작해야 하는지는 설정 화면과 `owngit network show`가 알려 주고, `network set --json`은 `applies_at_next_start`와 `restart_needed`로 답합니다. 시작할 때 그 포트를 이미 다른 프로그램이 쓰고 있으면 OwnGit은 자체 주소로만 시작하고 이유를 로그에 남기며, 네트워크 탭과 `owngit network show`에도 이유를 보여 줍니다. 끄려면 두 칸을 모두 비우거나 `owngit network set --public-share-off`를 실행합니다. `owngit network reset`도 공개 주소를 끕니다.

켤 때 OwnGit은 경고를 보여 줍니다. 인터넷의 누구나 그 주소에 접속할 수 있고, 링크를 가진 사람은 링크가 만료되거나 폐기될 때까지 그 주소에서 링크를 쓸 수 있다는 내용입니다. `http:`로 시작하는 공개 URL은 다른 기기가 접속하는 수신 주소와 같은 일반 HTTP 확인이 필요하며(설정 화면의 확인란, 명령줄의 `--accept-insecure-http`), 링크와 추가 비밀번호, 저장소 내용이 암호화되지 않은 채 인터넷을 지난다고 경고합니다. 수신 주소가 이 컴퓨터 밖으로 열려 있으면 그 네트워크의 누구나 암호화되지 않은 HTTP로 바로 접속할 수 있다고 경고하고, 신뢰하는 리버스 프록시가 없을 때도 경고합니다. 터널이나 프록시가 접속해 오는 주소(이 컴퓨터의 Tailscale Funnel이면 127.0.0.1)를 신뢰하는 프록시로 추가하세요. 그러지 않으면 틀린 추가 비밀번호 횟수에서 모든 방문자가 그 주소 하나로 계산되고, 공유 쿠키가 HTTPS 전용으로 제한되지 않습니다.

공개 주소에서 추가 비밀번호 같은 양식은 공개 URL이나 수신 주소 자체에서 보낸 것만 받으며, 프록시가 방문자의 호스트 이름을 그대로 넘기든 바꾸든 같습니다. 링크는 두 주소에서 같습니다. 새 링크를 만들면 대시보드와 API, `owngit repo share create`의 `public_url`, `public_clone_url` 필드에 공개 주소와 (복제 링크라면) 공개 Git 주소가 기존 주소와 함께 나옵니다. 링크를 폐기하거나 만료되면 다음 요청부터 두 주소 모두에서 페이지와 Git이 멈춥니다.

OwnGit은 이를 위해 Tailscale을 바꾸지 않습니다. Tailscale Funnel을 쓰려면 Tailscale Serve가 OwnGit에 이미 쓰지 않는 포트(Funnel은 443, 8443, 10000을 제공합니다)에서 수신 주소를 직접 연결하세요.

```sh
tailscale funnel --bg --https=8443 http://127.0.0.1:7655
```

처음에는 Tailscale이 tailnet에서 Funnel을 허용할지 물을 수 있고, 주소를 처음 쓸 때 HTTPS 인증서를 받습니다. 다시 없애려면 `tailscale funnel --https=8443 off`를 실행합니다. 다른 리버스 프록시와 마찬가지로 터널이나 프록시는 비밀값이 든 링크의 첫 요청 `/share/SECRET`을 로그에 남길 수 있습니다.

### 저장소 삭제하기

관리자는 저장소 탭 맨 끝의 저장소 삭제에서 저장소를 삭제합니다. 이 페이지는 저장소 이름을 입력하게 하고(아래처럼 끌 수 있습니다) 설정에 따라 관리자 비밀번호도 묻습니다. 파일을 어떻게 할지는 직접 고릅니다.

- **OwnGit에서만 제거하고 파일은 남기기**를 고르면 bare 저장소를 그대로 저장소 폴더 안의 `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git`로 옮깁니다(`ID`는 저장소를 만들 때 쓴 이름의 소문자인 저장소 ID, 시각은 UTC). 브랜치, 태그, 보관된 기록은 직접 지우기 전까지 그 폴더에 남습니다. `.owngit-removed` 아래의 폴더는 저장소 목록에 나오지 않으며 백업에도 들어가지 않습니다.
- **파일까지 영구 삭제**를 고르면 보관된 기록을 포함해 bare 저장소를 지웁니다. 이전 백업에는 그대로 들어 있고 그 기록이 쓰던 데이터베이스 공간은 비워지지만 안전하게 지워지지는 않습니다.

어느 쪽이든 풀 리퀘스트, 리뷰, 작업(task), 체크 설정, 체크 작업(job)과 결과, 체크 에이전트 토큰과 러너 토큰, 공유 링크, 가져오기 설정과 인증 정보는 사라집니다. 대기 중인 체크 작업은 버려지며 그 이름을 다시 쓸 수 있습니다.

이름 입력은 기본으로 켜져 있습니다. 이름 없이 삭제하려면 설정 화면 저장소 탭의 저장소 삭제에서 묻지 않음을 고르거나 `owngit settings set --delete-requires-name off`를 실행하세요. 그러면 삭제 페이지는 저장소 이름을 보여 주며 맞는지 확인하라고 안내합니다. 이때는 실수로 삭제할 때도 이름을 입력하지 않습니다. 다시 이름을 물으려면 `--delete-requires-name on`을 실행합니다. 저장된 값을 읽을 수 없으면 OwnGit은 아무것도 삭제하지 않고 삭제 페이지에 다시 정하는 방법이 나옵니다.

남긴 저장소를 되살리려면 대시보드에서 같은 이름으로 빈 저장소를 만드세요. 그다음 대시보드가 남긴 폴더에 대해 보여 주는 푸시 명령을 OwnGit이 실행되는 컴퓨터에서 실행합니다. 다른 이름으로 푸시하려면 그 저장소의 URL을 씁니다.

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

이 명령으로는 브랜치와 태그만 돌아오고 보관된 기록, 풀 리퀘스트, 체크는 돌아오지 않습니다. `refs/notes/` 같은 ref도 되살리려면 먼저 새 저장소의 [다른 ref 이름공간](#다른-ref-이름공간)에 그 이름공간을 적고, `'refs/notes/*:refs/notes/*'`처럼 맞는 refspec을 명령에 더하세요. 남긴 저장소의 주 브랜치가 `main`이 아니면 푸시한 뒤 기본 브랜치를 바꾸세요.

#### 명령줄에서 삭제하기

`owngit repo delete`는 삭제 페이지와 같은 방식으로 저장소를 삭제하며 관리자 비밀번호가 필요합니다.

```sh
owngit repo delete --server http://HOST:7654 --accept-insecure-http --password-file /path/to/admin-password \
  --repository NAME --files keep --confirm-name NAME
```

- `--repository`는 언제나 적어야 합니다. `repo settings`나 `repo default-branch`와 달리 clone의 `origin` 원격에서 가져오지 않으므로, 엉뚱한 폴더에서 실행해도 그 폴더의 저장소가 지워지지 않습니다.
- `--files keep`은 위와 같이 파일을 `.owngit-removed`로 옮기고 옮긴 폴더를 결과의 `kept_path`로 알려 줍니다. `--files delete`는 파일까지 지웁니다.
- `--confirm-name`은 설정에서 이름을 묻는 동안 저장소 이름을 한 번 더 적는 옵션입니다. 이 옵션이 없으면 `name_mismatch`로 거부되고 아무것도 지우지 않습니다.
- 결과 JSON에는 `repository`와 `mode`가 나옵니다. 저장소는 OwnGit에서 사라졌지만 파일을 옮기거나 지우는 일은 다음 시작 때 마무리된다면 `incomplete`가 true입니다([삭제 도중 OwnGit이 멈추면](#삭제-도중-owngit이-멈추면) 참고).
- 거부되는 이유는 삭제 페이지와 같습니다. 예를 들어 저장소를 쓰는 중이면 `repository_busy`입니다([삭제가 거부될 때](#삭제가-거부될-때) 참고). 이름을 물을지 정한 설정을 읽을 수 없으면 `setting_unreadable`입니다.

#### 삭제가 거부될 때

이유에 따라 다음 할 일이 정해집니다.

- 가져오기가 실행 중이거나, 체크 작업이 가져감 상태이거나 실행 중이거나, 다른 Git 작업(푸시, clone, 되돌리기, 병합)이 저장소를 쓰고 있는 경우: 끝난 뒤 다시 시도하세요.
- 체크 컨테이너가 아직 OwnGit의 제거 확인을 기다리는 경우: 실패한 정리는 OwnGit이 시작할 때 다시 시도하므로, Docker를 다시 쓸 수 있게 된 뒤 OwnGit을 다시 시작하세요.
- 저장소의 자동 체크 화면에 남은 체크 컨테이너가 나오는 경우(예를 들어 Docker를 초기화하거나 다시 설치한 뒤): 아래 순서대로 기록을 지우세요.

남은 체크 컨테이너 목록에는 끝난 작업의 컨테이너 가운데 OwnGit이 지우지 못한 것이 나옵니다. 다른 Docker 데몬에서 만들어졌거나 Docker를 쓸 수 없었던 경우입니다. 항목마다 작업, 컨테이너 이름과 ID, Docker 데몬, `com.owngit.check-job=JOB` 레이블이 나옵니다. 기록은 이렇게 지웁니다.

1. 그 작업을 실행한 Docker 데몬에서 컨테이너를 지우거나, 그 데몬이 더 이상 없는지 확인합니다.
2. "이 컨테이너를 지웠거나 그 Docker 데몬이 더 이상 없습니다"를 선택하고 컨테이너 기록 지우기를 누릅니다.
3. OwnGit을 다시 시작해 그 작업의 체크 작업 공간을 정리합니다.

OwnGit은 컨테이너를 지우지 않고 기록만 지웁니다. OwnGit이 지금 쓰는 Docker 데몬의 기록은 지우지 않습니다. 그 컨테이너는 OwnGit이 다음에 시작할 때 직접 지웁니다. 끝나지 않은 작업의 기록도 지우지 않습니다.

대시보드를 열 수 없거나 OwnGit이 멈춰 있다면 OwnGit 컴퓨터에서 대신 기록을 지웁니다.

```sh
owngit forget-check-container --job JOB --confirm-container-removed
```

`JOB`은 목록이나 서버 로그에 나온 작업 식별자입니다. 기본 위치가 아닌 상태 디렉터리를 쓴다면 `--state-dir`을 붙입니다. `--json`을 붙이면 지운 기록을 JSON으로 출력합니다. 필드는 `job`, `repository`, `container_name`, Docker가 ID를 정했을 때의 `container_id`, `daemon_id`이며 Docker를 확인하지 못했다면 `docker_unavailable`도 나옵니다. 이 명령은 화면과 같은 기록을 거부하고 기록이 없는 작업도 거부합니다.

#### 삭제 도중 OwnGit이 멈추면

삭제는 그대로 마무리됩니다. OwnGit은 파일을 옮기거나 지우기 전에 삭제를 기록하므로, 저장소는 이미 대시보드와 Git URL에서 사라졌고 이름은 다음 시작 때 삭제를 마무리할 때까지 사용 중으로 남습니다. 마무리하지 못하면 이유가 서버 로그에 남습니다.

삭제가 끝나지 않은 동안 저장소 폴더의 `.owngit-deletion-ID` 파일은 올바른 저장 공간이 마운트되었음을 OwnGit에 알려 줍니다. 이 파일을 지우지 마세요. 지웠다면 서버 로그의 `token ...` 줄로 다시 만든 뒤 다시 시작하세요.

### 기존 저장소를 OwnGit으로 옮기기

대시보드에서 빈 저장소를 만든 뒤 기존 저장소의 clone에서 브랜치와 태그를 푸시합니다.

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

옮기기가 끝났다고 보기 전에 양쪽을 비교하세요.

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

브랜치나 태그를 만들거나 바꾸는 푸시는 그 이름이나 이름 속 어느 폴더가 다른 ref와 대소문자만 다르거나, 악센트 글자나 한글 음절을 적는 방식(한 글자로 적는지 여러 부분으로 나눠 적는지)만 다르거나, `ß`와 `ss`, `ı`와 `i`처럼 일부 파일 시스템이 같다고 보는 글자로만 다르면 거부합니다. 그런 파일 시스템은 이런 이름이나 폴더를 같은 자리에 저장해서, 한 ref가 다른 ref를 덮어쓰거나 가릴 수 있기 때문입니다. 예를 들어 `main` 옆의 `Main`, `release/main` 옆의 `Release/x`, 음절로 적은 `기본` 옆에 자모로 적은 `기본`이 그렇습니다. 글자 자체가 다른 이름은 다른 이름이므로 `cafe`와 `café`는 함께 있을 수 있습니다. Git에는 `OwnGit refused changing refs/heads/NAME because another branch or tag, or one of its folders, has a name that some file systems treat as the same, ...`가 나오니 확실히 다른 이름을 쓰세요.

이런 두 이름을 이미 가진 저장소도 있을 수 있습니다. 두 이름을 구별하는 시스템에서 복사해 온 경우가 그 예입니다. 이때는 둘 중 하나를 `git push origin --delete NAME`으로 지울 때까지 두 이름 모두 만들거나 바꾸는 푸시를 거부합니다. 이 삭제는 지정한 ref만 바꾸며 기록 보관이 켜져 있으면 그 ref의 마지막 커밋이 보관된 기록에 남습니다. 기본 브랜치는 어떤 철자로도 이렇게 지울 수 없으니 다른 쪽 이름을 지우거나 먼저 다른 기본 브랜치를 고르세요.

푸시로 바꿀 수 있는 것은 브랜치(`refs/heads/*`)와 태그(`refs/tags/*`), 그리고 관리자가 저장소마다 [다른 ref 이름공간](#다른-ref-이름공간)에 적은 `refs/notes/` 같은 이름공간입니다. 그 밖의 ref와 `refs/owngit/`는 언제나 거부합니다. 그래서 다른 호스트의 미러 clone에서 `git push --mirror`를 하면 `refs/pull/*` 같은 ref 때문에 실패합니다. 두 OwnGit 서버 사이에서 푸시하면 보관된 기록과 저장소 기록은 옮겨지지 않으니, 그것까지 옮기려면 [백업](#백업)을 쓰세요. 계속 쓰는 호스트에서 변경 사항을 받아 오려면 [다른 Git 호스트에서 가져오기](#다른-git-호스트에서-가져오기)를 보세요.

### 다른 호스트에 사본 두기

OwnGit이 직접 다른 호스트로 푸시하지는 않습니다. 모든 브랜치와 태그를 다른 곳에 복사하려면 미러 clone에서 작업합니다.

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

사본을 갱신할 때는 같은 디렉터리에서 `git fetch --prune`과 `git push --mirror`를 다시 실행합니다. `--mirror`는 삭제까지 포함해 다른 호스트를 사본과 정확히 같게 만들며 보관된 기록은 OwnGit에만 남습니다.

작업용 clone에서 푸시할 때마다 두 호스트를 함께 갱신하려면 원격에 푸시 URL을 두 개 지정하세요.

```sh
git remote set-url --add --push origin http://HOST:7654/git/PROJECT.git
git remote set-url --add --push origin https://git.example.test/team/project.git
```

그러면 Git은 푸시 URL로만 푸시하므로 OwnGit도 함께 적어야 합니다. fetch는 여전히 원래 URL을 쓰고 한 호스트가 거부해도 다른 호스트에 이미 한 푸시는 취소되지 않습니다.

### 압축 파일 내려받기

코드 탭에서 선택한 브랜치나 태그를 ZIP 또는 tar.gz 파일로, 커밋 페이지에서는 그 커밋을 내려받을 수 있습니다. 내려받으려면 코드 탭과 같은 접근 권한이 필요합니다.

압축 파일에는 Git 기록 없이 그 리비전의 파일만 `PROJECT-REF` 폴더(예: `project-main`) 하나에 담깁니다. `git archive`처럼 그 리비전의 `export-ignore`와 `export-subst` 속성을 따릅니다. 문자, 숫자, `.`, `-`, `_`가 아닌 글자는 `-`로 바뀌므로 `feature/login`은 `project-feature-login.zip`이 됩니다.

브라우저 없이 받으려면 API 경로를 씁니다.

- `ref`는 브랜치, 태그, 전체 커밋 ID입니다. 생략하면 기본 브랜치를 씁니다.
- `format`은 `zip` 또는 `tar.gz`이고 없는 값에는 404로 답합니다.
- 공용 비밀번호로 보호할 때는 `--user owngit`을 넣으면 curl이 비밀번호를 묻습니다.

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

`--remote-header-name`을 쓰면 curl은 전체 이름을 읽지 못하는 프로그램을 위해 OwnGit이 함께 보내는 ASCII 이름을 씁니다. 이름에 한글처럼 다른 글자가 있으면 이 ASCII 이름은 저장소 이름과 커밋 ID 앞 12글자입니다(예: `project-1a2b3c4d5e6f.zip`). 전체 이름으로 저장하려면 `--output`으로 이름을 넘기고 `--data-urlencode`로 ref를 인코딩하세요.

```sh
curl --fail --get --user owngit \
  --data-urlencode 'ref=기능/로그인' --data format=zip \
  --output 'project-기능-로그인.zip' \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive'
```

압축 파일 내려받기도 Git 전송이므로 [아래 제한](#git-전송-제한)을 똑같이 받습니다. Git이 실패하거나, 제한에 닿거나, 끝나기 전에 OwnGit이 멈추면 응답을 마무리하지 않은 채 연결을 닫습니다. 그러면 내려받기는 실패하고(curl은 `(18) transfer closed with outstanding read data remaining`을 표시합니다) 받은 부분도 올바른 압축 파일이 아닙니다.

### 전체 활동

사이드바의 전체 활동은 한 해 동안 모든 저장소에 날마다 커밋이 몇 개 있었는지 보여 주고, 그해의 커밋을 나열합니다. 그래프에서 날짜를 고르면 그 날짜의 커밋만 나열합니다. 목록에는 최신 커밋을 1,000개까지만 보여 주고, 더 있으면 목록 위에 그렇게 알립니다. 다른 커밋은 날짜를 골라서 보세요. 그래프는 읽은 커밋을 모두 셉니다. 커밋의 작성자 이름과 날짜는 커밋에 적힌 값이며 확인된 신원이 아닙니다. 아직 준비 중이거나 Git 데이터를 읽지 못한 저장소는 집계에서 빠지고, 읽지 못한 저장소는 이름이 표시됩니다.

`owngit activity`는 같은 내용을 JSON으로 출력합니다. `owngit repo list`처럼 일반 접근을 씁니다.

```sh
owngit activity --server https://owngit.example.test
owngit activity --server https://owngit.example.test --year 2025
owngit activity --server https://owngit.example.test --date 2026-09-29
```

결과에는 `year`, `date`(지정했을 때), `repository_count`, `total`(그해의 커밋 수), `complete`, `complete`가 false일 때의 `incomplete_reason`(`counting`, `preparing`, `unreadable`, `preparing_or_unreadable`, `limit` 중 하나), `unreadable`(저장소 이름), `days`(커밋이 있는 날과 그날의 `count`), `entries`(최신순이며 `repository`(저장소 ID), `repository_name`, `repository_address`(저장소의 지금 주소), `ref`, `ref_retained`, `oid`, `subject`, `author_name`, `author_date`가 들어 있음), `truncated`가 들어 있습니다. `truncated`는 해당하는 커밋이 1,000개보다 많을 때 true입니다. 1970년부터 9999년 밖의 연도나 지정한 해의 날짜가 아닌 값은 `invalid_request`로 실패합니다. 읽을 수 있는 저장소가 하나도 없으면 커밋 0개로 답하지 않고 `activity_unavailable`로 실패합니다. API 경로는 `GET /api/v1/activity`이고 선택 쿼리 매개변수는 `year`와 `date`입니다.

### 준비 중인 저장소

`owngit serve`는 시작할 때 각 저장소를 준비합니다. 안전 설정, 기록 보관 훅, 끝내지 못한 풀 리퀘스트 작업을 처리하는 일입니다. 한 번에 8개까지 처리하며 제공을 시작하기 전에 최대 10초 기다립니다. 그때까지 준비되지 않은 저장소는 준비가 끝나는 대로 제공합니다. 나중에 폴더를 읽지 못하게 된 저장소(예를 들어 공유가 마운트되지 않은 경우)는 잠그고 같은 방식으로 다시 준비합니다.

저장소가 잠겨 있는 동안에는 다음과 같습니다.

- Git은 HTTP 503과 `repository is being prepared; try again later`를 받습니다.
- 페이지와 API(`repository_preparing`)도 그렇게 알리고 대시보드는 준비 중 라벨을 붙입니다.
- 그 저장소의 예약 가져오기와 체크는 기다립니다.
- 관리자는 여전히 삭제할 수 있습니다.

OwnGit은 실패한 시도 30초 뒤에 다시 시도하고 그다음부터는 직전 대기 시간의 두 배를 최대 10분까지 기다립니다. 폴더를 읽지 못한 경우에는 5초마다 확인합니다. 서버 로그에 원인이 남습니다. 원인(마운트되지 않은 디스크, 권한, 로그에 인용된 충돌하는 Git 설정)을 고친 뒤 다음 재시도를 기다리거나 OwnGit을 다시 시작하세요.

폴더는 읽을 수 있지만 Git 데이터를 읽지 못하는 저장소는 대신 읽지 못함 라벨로 표시되고 Git이 오류를 직접 알립니다. OwnGit이 상태 데이터베이스에서 저장소 목록을 읽지 못하면 시작을 거부합니다.

## 다른 Git 호스트에서 가져오기

가져오기는 다른 Git 호스트의 저장소를 HTTPS로(그 원본에 허용했다면 암호화되지 않은 HTTP로) 새 OwnGit 저장소에 복사하며 나중에 필요할 때나 예약한 시각에 새로고침할 수 있습니다. 가져오기는 받아 오기만 합니다. OwnGit은 원본에 아무것도 쓰지 않으며 Git LFS 객체는 가져오지도 호스팅하지도 않습니다.

브라우저에서는 관리자가 대시보드의 저장소 가져오기로 가져오기를 시작합니다. 그 뒤 저장소의 가져오기 탭에서 원본, 인증 정보, [연결 설정과 한도](#연결-설정과-한도), [ref와 새로고침 설정](#ref와-새로고침-설정) 변경, 새로고침, 취소, 예약 설정을 합니다.

- 저장소를 볼 수 있는 사람은 누구나 탭의 상태, 실행 기록, ref 상태를 볼 수 있습니다. 원본 주소, 인증 정보 상태, 실행 메시지, 새로고침 설정은 관리자에게만 보입니다.
- 변경할 때 관리자 비밀번호를 묻는지는 [관리자 비밀번호 확인](#관리자-비밀번호-확인) 설정을 따릅니다.
- 인증 정보 양식은 입력한 것만 바꿉니다. 새 토큰이나 Basic 인증 정보를 저장해도 저장된 CA는 남고, **새 로그인 정보 없음 (CA만)**은 인증 정보를 남깁니다. 지우는 방법은 인증 정보 지우기뿐입니다.
- 양식은 1 MiB로 제한되므로 그 크기에 가까운 CA 번들은 명령줄로 저장하세요.

가져오기마다 모드를 기록합니다. **독립**은 사본이 주 사본이라는 뜻이고 **공존**은 다른 호스트를 계속 기준으로 둔다는 뜻입니다. 모드는 표시일 뿐이며 둘 다 같은 [새로고침 규칙](#가져오기가-게시하는-내용)을 따릅니다.

### 명령줄에서 가져오기

명령줄에서도 기존 가져오기의 원본 URL, 모드, 동의를 바꾸는 일을 빼면 같은 작업을 할 수 있습니다. 관리자 비밀번호는 `reset-admin`과 같은 검사를 거치는 파일에서 읽습니다. 원본 토큰이나 Basic 인증 정보는 비공개 파일이나 대화형 입력에서 읽으며 인수나 환경 변수로는 받지 않습니다.

```sh
owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

모든 가져오기 명령은 같은 `--server`, `--accept-insecure-http`, `--password-file` 플래그를 받습니다. 아래에서는 생략했습니다. `--accept-insecure-http`는 그 명령에 한해 암호화되지 않은 HTTP로 OwnGit에 접속하는 데 동의한다는 뜻입니다. 원본 연결에는 적용되지 않습니다. 원본은 따로 `--allow-plain-http` 설정을 씁니다.

```sh
owngit import refresh PROJECT
owngit import status PROJECT
owngit import history PROJECT --limit 20
owngit import cancel PROJECT
owngit import schedule PROJECT --enable --interval 6h
owngit import schedule PROJECT --disable
owngit import credentials PROJECT --token-file /path/to/owner-only-token
owngit import credentials PROJECT --ca-file /path/to/source-ca.pem
owngit import credentials PROJECT --clear
owngit import configure PROJECT --redirects same_origin
owngit import configure PROJECT --limit run_seconds=2h --limit pack_bytes=32GiB
owngit import configure PROJECT --extra-ref-prefixes refs/notes/
owngit import configure PROJECT --overwrite-diverged --follow-upstream-deletions
owngit import resolve PROJECT
```

- Basic 인증 정보에는 `--token-file` 대신 `--basic-file`을 씁니다(사용자 이름과 비밀번호를 서로 다른 줄에). `--ca-file`은 원본의 인증 기관을 최대 1 MiB까지 저장합니다. `import credentials`는 넘긴 것만 바꾸고 `--clear`는 인증 정보와 CA를 지웁니다. 출력에는 인증 정보의 종류와 저장 여부만 나오며 비밀값은 나오지 않습니다.
- `--allow-private-network`는 사설 LAN, CGNAT, tailnet, 루프백 주소에 있는 원본을 허용합니다. `--git-only-consent`는 Git LFS 포인터가 있는 저장소를 받아들입니다([Git LFS](#git-lfs)).
- `import add`와 `import configure`는 원본의 [연결 설정과 한도](#연결-설정과-한도)와 [ref와 새로고침 설정](#ref와-새로고침-설정)을 받습니다. `import configure`는 넘긴 옵션만 바꾸고 주소, 모드, 동의는 그대로 둡니다.
- `import add`는 저장소를 만들고 이미 있는 이름은 `repository_taken`으로 거부합니다. `import refresh`는 저장된 원본에서 갱신합니다. 둘 다 실행 전체가 끝날 때까지 기다립니다. 서버는 원본의 전체 실행 시간(기본값 1시간)이 지나면 실행을 멈추고 그 뒤 1분쯤 안에 응답합니다. 명령 자체의 요청 제한은 원본 설정과 관계없이 24시간 2분으로 고정되어 있습니다. 허용되는 가장 긴 실행 시간에 여유를 더한 값입니다. 성공하면 0으로 끝납니다. 원본과 다른 OwnGit의 ref를 그대로 두었다면 그 ref를 나열하고 3으로, 취소되었다면 130으로, 그 밖의 실패는 1로 끝납니다.
- `import status`는 마지막 실행과 진행 중인 실행, 원본의 연결 설정, 바꾼 한도, 새로고침 설정, 원본과 일치하지 않는 가져온 ref를 보여 줍니다. 새로고침 설정이 [지금 바꿀 ref](#바뀔-ref-미리-보기)도 함께 나옵니다. `--json`을 붙이면 적용 중인 모든 한도를 포함해 JSON으로 출력합니다.
- `import cancel`은 결과가 게시되기 전까지만 실행을 멈출 수 있습니다. 첫 가져오기라면 저장소가 나타나는 순간이 그 경계입니다. 첫 가져오기가 실패하거나 취소되면 OwnGit은 그 이름으로 저장했던 원본과 인증 정보를 지우고(멈췄다면 다음 시작 때), 다시 시도할 때는 새로 넘긴 것만 씁니다.
- 예약 간격은 60초에서 7일 사이이며(그 밖은 `invalid_schedule`), 예약 새로고침은 `owngit serve`가 실행 중일 때만 동작합니다.
- 모든 가져오기 명령은 `--json`을 받습니다. 붙이면 텍스트 대신 서버의 응답을 JSON으로 출력하고, 종료 코드는 같습니다. `import status --json`은 상태 자체를 출력하고, 나머지 명령은 응답 전체를 출력합니다. 예를 들어 `import add`와 `import refresh`는 `run`과 `status`를, `import schedule`은 `enabled`와 `interval_seconds`를 담습니다. 변경은 저장했지만 그 뒤 상태를 읽지 못했다면 텍스트의 경고 대신 응답에 `status_error`가 들어갑니다. `--json`을 붙이면 오류도 `"ok": false`와 `code`가 있는 JSON으로 출력합니다.

### 원본 연결

원본 URL은 TLS 1.2 이상의 HTTPS와 ASCII 호스트 이름을 써야 하며 사용자 이름, 비밀번호, 쿼리, 프래그먼트를 담을 수 없습니다. `http://` URL은 그 원본에 [암호화되지 않은 HTTP를 허용한](#암호화되지-않은-http) 경우에만 쓸 수 있습니다. IPv6 zone 식별자는 지원하지 않습니다. OwnGit은 기본적으로 리디렉션을 따라가지 않습니다. 프록시 환경 변수, 쿠키, Git credential helper는 항상 무시합니다.

OwnGit은 호스트 이름을 한 번 조회하고 돌아온 주소를 모두 검사합니다. 허용되지 않는 주소가 하나라도 있으면 실행을 멈춥니다.

- 공인 주소는 허용합니다.
- 사설 LAN, CGNAT, tailnet, 루프백 주소는 사설망 동의가 있어야 합니다(`--allow-private-network`, 브라우저에서는 사설망 원본 허용).
- 문서용 범위나 벤치마크용 범위 같은 그 밖의 특수 용도 주소는 원본의 [예외 대상 주소](#예외-대상-주소) 설정이 있어야 합니다.
- 링크 로컬 주소(클라우드 메타데이터 주소 169.254.169.254 포함), 멀티캐스트 주소, 지정되지 않은 주소, 로컬용 NAT64 범위 64:ff9b:1::/48, 그리고 몇몇 예약 범위에는 설정과 관계없이 절대 연결하지 않습니다.
- 잘 알려진 NAT64 범위 64:ff9b::/96의 주소는 예외 대상 주소 설정이 있어야 하고 그 안에 담긴 IPv4 주소도 허용되는 주소여야 합니다.

리디렉션이 향하는 호스트도 같은 방식으로 검사합니다. 직접 지정한 CA는 시스템 루트 인증서에 더해질 뿐 인증서나 호스트 이름 검사를 끄지 않습니다. 인증서 때문에 실패한 실행은 그렇게 알립니다.

### 연결 설정과 한도

가져오기 원본마다 연결 설정과 한도를 따로 둡니다. 대부분의 원본은 기본값으로 충분합니다. 보호를 느슨하게 하는 설정은 그 원본에서 직접 켜기 전까지 모두 꺼져 있습니다. 새 가져오기 양식이나 가져오기 탭의 접힌 **연결과 한도** 묶음, `owngit import configure`, API 중 편한 곳에서 바꿉니다. 바꾼 값은 다음 실행부터 적용됩니다. 다만 연결 설정을 바꾸면(한도는 제외) 아직 게시하지 않은 진행 중 실행도 취소되고 그 실행은 `superseded`로 끝납니다.

| 브라우저의 설정 | 기본값 | 명령줄 | API 필드 |
|---|---|---|---|
| 이 원본에 암호화되지 않은 HTTP 허용 | 꺼짐 | `--allow-plain-http` | `allow_plain_http` |
| 리디렉션 | 리디렉션 거부 | `--redirects`에 `refuse`, `same_origin`, `approved` 중 하나 | `redirects` |
| 승인한 출처 | 없음 | `--approved-origin` | `approved_redirect_origin` |
| 예외 대상 주소 허용 | 꺼짐 | `--allow-exceptional-destination` | `allow_reserved_addresses` |

API에서는 `PUT /api/v1/repositories/{id}/import`와 새 가져오기를 시작하는 요청(`POST /api/v1/repositories/{id}/import/run`)에 이 필드와 `limits` 객체를 넣습니다. `PATCH /api/v1/repositories/{id}/import`는 요청에 적은 필드만 바꿉니다. 원본 JSON의 `options` 객체에는 설정, 적용 중인 모든 한도(`limits`), 직접 바꾼 한도의 이름(`changed_limits`)이 들어 있습니다. 저장된 설정을 쓸 수 없으면 `problem`도 함께 옵니다.

#### 암호화되지 않은 HTTP

`http://` 원본 주소는 그 원본에 암호화되지 않은 HTTP를 허용하기 전까지 거부됩니다. 거부 메시지에는 켜야 할 설정이 나옵니다. 이 설정을 켜면 원본의 코드와 인증 정보가 암호화되지 않은 채 오가므로 도중에 노출되거나 바뀔 수 있습니다. 믿을 수 있는 네트워크에서만 쓰세요. 같은 설정을 켜면 HTTPS에서 암호화되지 않은 HTTP로 가는 리디렉션도 따르고 승인한 출처에 `http://`도 쓸 수 있습니다.

#### 리디렉션

기본값인 리디렉션 거부에서는 리디렉션을 만나면 실행을 멈추고 원본이 가리킨 출처를 알려 줍니다. 같은 출처 안의 리디렉션만 따르기를 고르면 원본과 스킴, 호스트, 포트가 같은 곳으로 가는 리디렉션을 따릅니다. 승인한 출처 한 곳으로의 리디렉션도 따르기를 고르면 여기에 더해 승인한 출처 칸에 입력한 출처 한 곳으로 가는 리디렉션도 따릅니다. 출처는 `https://mirror.example`처럼 경로 없이 스킴과 호스트만 적습니다. OwnGit은 호스트를 소문자로 바꾸고 기본 포트를 뺀 형태로 저장합니다. 형식이 잘못된 출처는 지금 고른 리디렉션 방식이 그 출처를 쓰지 않더라도 거부합니다.

리디렉션은 원본의 ref 목록을 받는 첫 요청에서만 따르며 연달아 5번까지 허용합니다. 옮겨 간 주소도 `/info/refs?service=git-upload-pack`으로 끝나야 합니다. 그 실행의 이후 요청은 모두 그 주소로 갑니다. 리디렉션이 순환하거나 6번째 리디렉션이 오거나 첫 요청이 아닌 요청에서 리디렉션이 오면 실행을 멈춥니다.

원본의 인증 정보와 직접 지정한 CA는 원본 자신의 출처로만 보냅니다. 다른 출처에는 로그인 정보를 보내지 않으며 인증서도 시스템 인증 기관으로만 확인합니다. 그 출처에 로그인이 필요하다면 원본 주소를 그 출처로 바꾸세요.

#### 예외 대상 주소

예외 대상 주소 허용을 켜면 평소 차단하는 특수 용도 주소에 이 원본이 연결할 수 있습니다. 문서용 범위(192.0.2.0/24, 2001:db8::/32)나 벤치마크용 범위 198.18.0.0/15의 주소가 그 예입니다. 사설 주소는 이 설정으로 열리지 않으며 여전히 사설망 동의가 필요합니다. [원본 연결](#원본-연결)에서 절대 연결하지 않는다고 적은 주소도 이 설정으로 열리지 않습니다.

#### 한도

한도는 한 번의 실행이 내려받을 수 있는 양과 단계별로 걸릴 수 있는 시간을 정합니다. 가져오기가 어떤 한도에 걸려 실패할 때 그 한도를 높이세요. 한도를 높이면 가져오기가 디스크를 더 많이 쓰고 서버가 더 오래 바쁠 수 있습니다. 브라우저에서는 칸마다 기본값과 허용 범위가 옆에 나옵니다. 칸을 비워 두면 기본값을 씁니다. OwnGit은 바꾼 한도만 저장합니다. 한도를 기본값과 같은 값으로 정하면 기본값으로 돌아갑니다.

| 한도 | 브라우저 표시 | 기본값 | 허용 범위 |
|---|---|---|---|
| `pack_bytes` | 최대 팩 크기 | 16 GiB | 1 MiB ~ 1 TiB |
| `run_seconds` | 전체 실행 시간 | 1시간 | 1분 ~ 24시간 |
| `fetch_seconds` | 내려받기 시간(색인 포함) | 30분 | 1분 ~ 24시간 |
| `index_seconds` | 색인 시간 | 20분 | 1분 ~ 24시간 |
| `verify_seconds` | 검증 시간 | 10분 | 1분 ~ 24시간 |
| `refs` | 목록에 올 수 있는 ref 수 | 50,000 | 1 ~ 200,000 |
| `advertisement_bytes` | ref 목록 최대 크기 | 16 MiB | 64 KiB ~ 64 MiB |
| `tls_handshake_seconds` | TLS 핸드셰이크 시간 | 15초 | 1초 ~ 10분 |
| `response_header_seconds` | 응답 헤더 대기 시간 | 30초 | 1초 ~ 1시간 |
| `lfs_objects` | Git LFS 확인 대상 객체 수 | 200,000 | 1 ~ 1,000,000 |

마지막 네 가지는 안쪽의 **전송과 검사 한도** 묶음에 있습니다. 단계별 시간은 서로 맞아야 합니다. 내려받기 시간과 검증 시간은 전체 실행 시간보다 길 수 없으며 색인 시간은 색인을 포함하는 내려받기 시간보다 길 수 없습니다. 이 관계가 깨지거나 허용 범위를 벗어난 값은 거부되고 메시지에 해당 한도가 나옵니다. 브라우저에서는 그 칸에 메시지가 표시됩니다.

API는 크기를 바이트로, 시간을 초로, 개수를 숫자로 받습니다. `owngit import configure --limit NAME=VALUE`도 같은 숫자를 받습니다. `32GiB`처럼 단위가 붙은 크기(KiB, MiB, GiB, TiB)와 `90m`, `2h` 같은 시간도 쓸 수 있습니다. 한도를 여러 개 바꾸려면 `--limit`을 반복합니다.

#### 원본 주소를 바꿀 때

연결 설정은 원본 주소에 묶여 있습니다. 원본 주소가 바뀌면 OwnGit은 암호화되지 않은 HTTP, 예외 대상 주소, [원본과 달라진 브랜치 덮어쓰기](#원본과-달라진-브랜치-덮어쓰기), [원본의 삭제 따르기](#원본의-삭제-따르기)를 끄고 승인한 출처를 지우며 리디렉션은 리디렉션 거부로 되돌립니다. 한도, 사설망 동의, 추가 ref 네임스페이스는 그대로 남습니다.

가져오기 탭에서는 주소를 고치는 즉시 화면에서 이 설정을 비우고 그 사실을 알려 줍니다. 새 주소를 먼저 입력한 뒤 그 주소에 필요한 설정을 다시 고르세요.

JavaScript를 쓰지 않는 브라우저에서는 화면이 설정을 비울 수 없으므로 OwnGit이 저장할 때 정리합니다. 새 주소와 같은 저장에서 바꾼 설정은 그 주소에 대한 동의로 봅니다. 예전 주소의 값을 그대로 둔 설정은 버립니다. 저장이 거부되면(예를 들어 새 `http://` 주소에 암호화되지 않은 HTTP 허용이 필요한 경우) 양식이 새 주소 기준으로 다시 나오며 예전 주소에서 넘어온 설정은 꺼진 상태로 표시됩니다. 필요한 설정을 다시 켜고 저장하세요.

API에서는 같은 요청에 이 설정을 함께 넣지 않으면 새 URL과 함께 초기화됩니다.

#### 설정 때문에 실행이 멈출 때

원본 설정 때문에 실행이 멈추면 가져오기 탭의 마지막 실행 메시지가 설정에 따라 일부러 연결하지 않았다고 밝히고 켜야 할 설정을 알려 줍니다. 관리자는 기술 세부 정보에서 거부된 주소와 그 범위, 또는 리디렉션이 향한 출처도 볼 수 있습니다. 다른 사용자에게는 설명만 보입니다. API와 명령줄에서는 이런 거부에 따로 분류가 붙습니다.

- `address_needs_private_network`: 사설망 동의를 켜세요.
- `address_needs_exceptional_destination`: 예외 대상 주소 허용을 켜세요.
- `address_refused`: 링크 로컬이나 멀티캐스트 주소처럼 어떤 설정으로도 허용되지 않는 주소입니다. 다른 원본 주소를 쓰세요.
- `redirect_not_allowed`: 리디렉션 방식을 고르세요. 원본의 출처를 벗어나는 리디렉션이라면 그 출처도 승인해야 합니다.
- `redirect_needs_plain_http`: HTTPS에서 암호화되지 않은 HTTP로 가는 리디렉션이므로, 이 원본에 암호화되지 않은 HTTP를 허용해야 합니다.

리디렉션이 순환하거나 너무 많거나 Git 저장소 주소가 아닌 곳을 가리키면 `protocol`로 보고합니다.

저장된 설정이나 한도를 더는 쓸 수 없게 되면(예를 들어 승인한 출처에 경로가 붙어 있거나 전체 실행 시간이 내려받기 시간보다 짧으면) 가져오기 탭과 `import status`가 그 설정을 알려 줍니다. 그 원본의 실행은 연결하기 전에 멈춥니다. 저장된 [추가 ref 네임스페이스](#추가-ref-네임스페이스) 목록도 마찬가지입니다. 가져오기 탭이나 `owngit import configure --redirects`, `--limit`, `--extra-ref-prefixes`로 그 설정을 다시 저장하면 실행이 재개됩니다. 네임스페이스는 빈 목록으로 저장해도 됩니다.

#### 연결 설정과 한도는 이 컴퓨터에만 남습니다

백업에는 원본의 연결 설정과 한도가 들어가지 않습니다. [복원](#백업-복원하기)한 뒤에는 모든 원본이 기본값에서 다시 시작하므로, `http://` 원본의 암호화되지 않은 HTTP 허용처럼 원본에 필요한 설정을 다시 켜세요. [ref와 새로고침 설정](#ref와-새로고침-설정)은 백업에 들어가므로 복원하면 그대로 돌아옵니다.

### 가져오기가 게시하는 내용

실행할 때마다 전체 사본을 비공개 준비 영역으로 받아 옵니다. 저장소에 반영하기 전에 원본이 알린 ref 가운데 가져오는 ref와 HEAD가 모두 완전한 객체 그래프와 함께 있는지 검사합니다.

- 브랜치와 태그를 게시하고 `refs/notes/`처럼 목록에 넣은 [추가 ref 네임스페이스](#추가-ref-네임스페이스)의 ref도 게시합니다. 목록에 없는 네임스페이스의 ref는 건너뜁니다. 예를 들어 풀 리퀘스트 ref는 `refs/pull/`을 목록에 넣지 않는 한 건너뜁니다. `refs/heads/` 밖을 가리키는 HEAD는 거부합니다.
- 훅과 설정은 복사하지 않습니다.
- 객체 형식(SHA-1 또는 SHA-256)이 다른 원본은 실패하며 417바이트보다 긴 ref 이름은 `unsupported_refs`로 실패합니다.
- OwnGit은 원본에 Git 프로토콜 v2로 HEAD, 브랜치, 태그, 추가 네임스페이스만 요청하므로 풀 리퀘스트 ref 같은 다른 네임스페이스의 ref는 받지 않습니다. 원본이 알린 ref가 원본의 ref 한도(기본값 50,000개)를 넘으면 `too_many_refs`로 실패합니다. 이 개수에는 OwnGit이 건너뛰는 ref도 원본이 알린 것이면 모두 들어갑니다. 프로토콜 v2를 지원하지 않는 원본은 모든 ref를 알리며 프로토콜 v2 원본도 요청하지 않은 ref를 보낼 수 있습니다. 이런 원본은 [ref 한도](#한도)를 높이거나, clone한 뒤 브랜치와 태그를 푸시해 [직접 옮기세요](#기존-저장소를-owngit으로-옮기기).

기본적으로 새로고침은 OwnGit에서 한 작업을 덮어쓰지 않습니다. 두 가지 [새로고침 설정](#ref와-새로고침-설정)으로 이 가운데 일부 규칙을 바꿀 수 있습니다. 어느 규칙인지는 아래 항목에 적었습니다.

- 없는 ref는 만들고 같은 ref는 그대로 둡니다.
- 브랜치는 OwnGit이 이 원본 URL에서 마지막으로 본 값을 아직 가지고 있거나, 그 값에서 앞으로만 나아갔고 새 원본 값이 그것을 포함할 때만 원본을 따라갑니다.
- 태그와 추가 네임스페이스의 ref는 마지막으로 본 값과 정확히 같을 때만 바뀝니다.
- 그 밖의 경우는 **원본과 다름**으로 보고 그대로 두며, 실행 결과에 보고합니다. [원본과 달라진 브랜치 덮어쓰기](#원본과-달라진-브랜치-덮어쓰기)를 켜면 원본의 값으로 바꿉니다.
- 원본에서 지운 ref는 OwnGit에 남습니다(가져오기 탭과 `import status`에 **원본에서 삭제됨**). [원본의 삭제 따르기](#원본의-삭제-따르기)를 켜면 여기서도 지웁니다.
- 이름이나 이름 속 어느 폴더가 [기존 저장소를 OwnGit으로 옮기기](#기존-저장소를-owngit으로-옮기기)에서 설명한 방식으로 로컬 ref와 같다고 보이는 원본 ref는 만들지 않고 원본과 다름으로 보고하며, HEAD도 그런 이름으로 원본을 따라가지 않습니다. `cafe`와 `café`는 다른 이름입니다.
- 교체된 브랜치나 태그의 값은 새로고침을 시작할 때 그 저장소의 기록 보관이 켜져 있었으면 보관된 기록에 남고, 보관하지 않음이면 남지 않습니다. 추가 네임스페이스에서 교체된 값은 보관하지 않습니다.
- HEAD는 같은 원본의 이전 가져오기에서 OwnGit이 정했고 그 뒤로 아무것도 바꾸지 않았을 때만 원본을 따라갑니다.

원본 URL을 바꾼 뒤에는 OwnGit이 새 원본의 ref를 아직 보지 못했으므로, 값이 다른 ref는 바꾸지 않고 원본과 다름으로 보고하며 어떤 ref도 지우지 않습니다.

### ref와 새로고침 설정

원본마다 새로고침이 무엇을 가져오고 원본을 얼마나 그대로 따를지 정하는 설정이 세 가지 있습니다. 기본적으로 새로고침은 브랜치와 태그를 가져오고 OwnGit에서 바꾼 것은 그대로 둡니다. 원본에서 삭제된 ref는 보고만 합니다. 이 설정은 새 가져오기 양식과 가져오기 탭의 접힌 **연결과 한도** 묶음 안의 **ref와 새로고침** 부분에 있습니다. 다음 새로고침부터 적용되며 관리자에게만 보입니다.

| 브라우저의 설정 | 기본값 | 명령줄 | API 필드 |
|---|---|---|---|
| [추가 ref 네임스페이스](#추가-ref-네임스페이스) | 없음 | `--extra-ref-prefixes refs/notes/,refs/changes/` | `extra_ref_prefixes` |
| [원본과 달라진 브랜치 덮어쓰기](#원본과-달라진-브랜치-덮어쓰기) | 꺼짐 | `--overwrite-diverged` | `overwrite_diverged` |
| [원본의 삭제 따르기](#원본의-삭제-따르기) | 꺼짐 | `--follow-upstream-deletions` | `follow_upstream_deletions` |

명령줄에서 `--extra-ref-prefixes=`는 목록을 비웁니다. `--overwrite-diverged=false`나 `--follow-upstream-deletions=false`는 그 설정을 끕니다. API는 [연결 설정](#연결-설정과-한도)과 같은 요청에서 이 필드를 받습니다. `extra_ref_prefixes`는 배열이고 `[]`를 보내면 목록이 빕니다. 요청에 넣지 않은 필드는 저장된 값을 유지합니다. 원본의 `options` 객체에는 세 설정이 모두 나옵니다. 가져오기 탭은 기본값에서 바꾼 설정을 새로고침 항목에 보여 줍니다.

뒤의 두 설정은 OwnGit에서 한 작업을 바꾸거나 지울 수 있습니다. 그래서 양식에는 "여기서 한 작업이 바뀔 수 있고, 원본에서 삭제된 ref는 여기서도 삭제됩니다."라는 경고가 나옵니다. `owngit import configure`도 바꾼 뒤 둘 중 하나가 켜져 있으면 텍스트 출력에 같은 경고를 붙입니다. 켜기 전에 [무엇이 바뀌는지 확인하세요](#바뀔-ref-미리-보기).

세 설정 가운데 하나라도 바꾸면 아직 게시하지 않은 진행 중 새로고침이 멈추고 `superseded`로 끝납니다. 원본 주소가 바뀌면 원본과 달라진 브랜치 덮어쓰기와 원본의 삭제 따르기는 꺼지고 네임스페이스는 남습니다([원본 주소를 바꿀 때](#원본-주소를-바꿀-때)).

#### 추가 ref 네임스페이스

추가 ref 네임스페이스를 지정하면 `refs/notes/` 아래의 Git notes처럼 브랜치와 태그가 아닌 ref도 가져옵니다. 브라우저에서는 한 줄에 하나씩, 명령줄에서는 쉼표로 구분해 적습니다.

- 네임스페이스는 `refs/`로 시작하고 그 뒤에 이름이 오며 슬래시로 끝납니다. `refs/notes/`, `refs/changes/`가 그 예입니다.
- 원본마다 32개까지, 중복 없이 적습니다.
- `refs/heads/`, `refs/tags/`, `refs/owngit/` 안에 있거나 이를 포함하는 네임스페이스는 거부합니다.

거부된 목록은 저장하지 않습니다. 양식은 그 칸에 오류를 표시하고 입력한 내용을 그대로 둡니다.

이 네임스페이스의 ref도 브랜치처럼 받아 오고 게시하며 백업에서 복원합니다. 한도와 이름 규칙도 같습니다. 새로고침은 이런 ref를 태그처럼 마지막으로 본 값에서 바뀌지 않았을 때만 바꿉니다. 아래 두 설정을 켜면 그 범위가 넓어집니다. 보관된 기록은 이 네임스페이스를 다루지 않으므로 여기서 덮어쓰거나 지운 ref의 이전 값은 남지 않습니다.

목록에서 네임스페이스를 빼도 그 네임스페이스로 가져온 ref는 그대로 남습니다. 이후 새로고침은 그 ref를 바꾸거나 지우지 않고 원본에서 삭제된 것으로 세지도 않습니다. 다만 원본이 여전히 그 ref를 알리면 [ref 한도](#한도)에는 들어갑니다. 가져오기 탭과 `import status`에는 **가져오지 않는 네임스페이스**(JSON에서는 `not_imported`)로 나옵니다.

이 목록은 가져오기에만 적용됩니다. 푸시로 바꿀 수 있는 ref는 [다른 ref 이름공간](#다른-ref-이름공간)에서 따로 정합니다.

#### 원본과 달라진 브랜치 덮어쓰기

이 설정을 켜면 새로고침은 원본을 마지막으로 확인한 뒤 OwnGit에서 바뀐 브랜치, 태그, 추가 ref를 원본과 다름으로 남겨 두지 않고 원본의 값으로 바꿉니다. 이 원본 주소에서 본 적이 있는 ref에만 적용되므로 OwnGit에서만 만든 ref는 건드리지 않습니다. 기록 보관이 켜져 있으면 바뀌기 전의 브랜치나 태그 커밋은 보관된 기록에 남습니다.

이 설정을 켜도 다음은 그대로 둡니다.

- 보호된 기본 브랜치. 덮어쓰면 기본 브랜치를 다시 쓰게 되는 경우 새로고침은 `protected_default_branch`로 멈추고 아무것도 바꾸지 않습니다([기본 브랜치 바꾸기](#기본-브랜치-바꾸기)).
- 심볼릭 ref, 그리고 [기존 저장소를 OwnGit으로 옮기기](#기존-저장소를-owngit으로-옮기기)에서 설명한 방식으로 다른 ref와 이름이 겹치는 ref.
- OwnGit에서 바꾼 HEAD.

#### 원본의 삭제 따르기

이 설정을 켜면 원본에서 삭제된 ref를 새로고침이 여기서도 지웁니다. 다만 다음 조건을 모두 만족할 때만 지웁니다.

- 이 원본 주소에서 그 ref를 본 적이 있고 지금의 로그인 정보로 한 새로고침에서도 본 적이 있습니다([로그인 정보를 바꾸거나 복원한 뒤](#로그인-정보를-바꾸거나-복원한-뒤)).
- 그 ref가 마지막으로 본 값에서 바뀌지 않았습니다. 원본과 달라진 브랜치 덮어쓰기도 켜져 있으면 이 조건은 따지지 않습니다.
- 그 ref가 가져오기가 아직 가져오는 네임스페이스에 있습니다.

기록 보관이 켜져 있으면 지운 브랜치나 태그의 커밋은 보관된 기록에 남습니다. 추가 네임스페이스에서 지운 ref는 남지 않습니다. 지운 ref의 이름은 다시 비므로 나중에 그 이름으로 만든 ref는 OwnGit에서 한 작업으로 봅니다.

새로고침은 다음을 지우지 않습니다.

- 심볼릭 ref
- HEAD가 직접 또는 다른 심볼릭 ref를 거쳐 가리키는 브랜치, 그리고 그 새로고침에서 원본의 HEAD가 가리키는 브랜치
- `refs/notes/`를 가져올 때의 `refs/Notes/commits`처럼 대소문자나 표기만 다를 뿐 다른 ref와 같은 이름으로 보이는 ref

원본이 가져오기 대상 ref를 하나도 알리지 않으면 아무것도 지우지 않습니다. 원본 목록이 비었다고 해서 저장소를 비우지는 않습니다.

#### 로그인 정보를 바꾸거나 복원한 뒤

새 로그인 정보로는 예전보다 적은 ref만 보일 수 있습니다. 그래서 목록에 없는 ref가 정말 삭제된 것인지 보이지 않을 뿐인지 알 수 없습니다. 로그인 정보가 바뀐 뒤에는 새 로그인 정보로 한 새로고침이 그 ref를 본 다음에야 지웁니다.

- 다른 토큰이나 Basic 인증 정보를 저장하거나 저장된 인증 정보를 지우면 로그인 정보가 바뀐 것으로 봅니다. CA만 바꾸거나 같은 인증 정보를 다시 저장한 것은 해당하지 않습니다.
- [복원](#백업-복원하기)도 모든 원본에서 로그인 정보가 바뀐 것으로 봅니다.

예전 로그인 정보로 한 마지막 새로고침과 새 로그인 정보로 한 첫 새로고침 사이에 원본에서 지운 ref는 자동으로 지워지지 않습니다. 직접 지울 때까지 원본에서 삭제됨으로 남습니다. 나머지는 전과 같습니다. 브랜치는 계속 원본을 따라가고 원본과 달라진 브랜치 덮어쓰기도 그대로 적용됩니다.

#### 바뀔 ref 미리 보기

가져오기 탭의 **지금 이 설정이 바꿀 ref**에는 원본과 달라진 브랜치 덮어쓰기나 원본의 삭제 따르기가 바꿀 로컬 ref가 모두 나옵니다. 설정을 아직 켜지 않았어도 나옵니다. `owngit import status`도 같은 목록을 출력합니다. ref마다 다음을 알려 줍니다.

- 원본의 값으로 바뀌는지, 삭제되는지
- 어느 설정이 필요한지(원본에서 삭제됐고 OwnGit에서도 바뀐 ref는 두 설정이 모두 필요합니다)
- 지금 커밋이 보관된 기록에 남는지

기본 브랜치 보호 때문에 덮어쓰기가 멈추는 경우에는 그 브랜치가 **새로고침 멈춤**으로 나옵니다. 원본과 달라진 브랜치 덮어쓰기를 켜면 보호를 끌 때까지 새로고침이 그 브랜치에서 멈추고 아무것도 바꾸지 않습니다. 이때 나머지 항목은 삭제 따르기만 켰을 때 지워질 ref입니다.

이 목록은 마지막으로 완료된 새로고침이 본 원본에 새로고침 규칙을 적용해 만듭니다. 목록을 만드는 일 자체는 아무것도 바꾸지 않습니다. 다음 새로고침은 그때의 원본을 기준으로 판단하므로 결과가 다를 수 있습니다. 완료된 새로고침이 아직 없으면 목록은 비어 있습니다.

목록을 만들 수 없으면 빈 목록 대신 알아내지 못했다고 표시합니다. 저장소에 쓰는 중이거나, 저장 공간을 읽을 수 없거나, 저장된 설정을 쓸 수 없을 때 그렇습니다. 잠시 뒤 페이지를 다시 불러오거나 명령을 다시 실행하세요.

JSON(`import status --json`과 API)에서는 이 목록이 `refresh_effects`입니다. 항목마다 `name`, `effect`(`replace`, `delete`, `refused`), `local_oid`, `local_changed`, `history`(`kept` 또는 `not_kept`)가 있습니다. 목록을 만들지 못했으면 `refresh_effects_unknown`이 `true`입니다.

### 두 번에 나눠 쓰는 새로고침

새로고침은 보통 모든 ref 변경을 Git 트랜잭션 한 번에 씁니다. 그래서 다른 클라이언트에는 변경이 전부 보이거나 하나도 보이지 않습니다. 다만 ref를 지우거나 기본 브랜치 보호가 켜진 상태에서 브랜치를 fast-forward가 아닌 방식으로 다시 쓰는 새로고침이 HEAD가 가리키는 브랜치까지 갱신하면 두 번에 나눠 씁니다. 먼저 나머지 변경을 모두 쓰고 그다음에 그 브랜치를 씁니다.

OwnGit을 거치는 clone과 fetch는 새로고침이 저장소에 쓰는 동안 기다리므로 두 단계 사이의 상태를 보지 않습니다. 저장소 폴더를 직접 읽는 프로그램은 이 상태를 볼 수 있습니다. 다른 변경은 이미 쓰였는데 HEAD의 브랜치는 아직 예전 값일 수 있습니다. 두 번째 단계가 실패하면 이 상태가 그대로 남아 clone과 fetch에도 보입니다. 이때 실행은 [미해결](#미해결-게시)로 끝나며 완료로 기록되지 않습니다.

새로고침이 ref를 지우거나 기본 브랜치 보호가 켜진 상태에서 브랜치를 다시 쓰는 동안 OwnGit은 HEAD와 HEAD가 거쳐 가는 심볼릭 ref를 모두 잠급니다. 그동안 HEAD를 바꾸려는 다른 Git 명령은 `cannot lock ref 'HEAD'`로 실패하니 새로고침이 끝난 뒤 다시 실행하세요. 잠그기 직전에 HEAD가 새로고침이 지우거나 다시 쓸 ref로 옮겨졌다면 새로고침은 아무것도 쓰기 전에 멈추며 실행은 미해결로 보고됩니다.

### Git LFS

OwnGit은 받아 온 객체에서 Git LFS 포인터 파일을 찾습니다(원본의 LFS 확인 대상 객체 수 한도까지, 기본값으로 객체 200,000개, 후보 파일 100,000개, 후보 내용 32 MiB까지). 포인터를 찾거나 이 한도 안에서 검사를 마치지 못하면 `git_lfs_required`로 실행을 멈춥니다. Git 내용만 받기에 동의하면 가져오기를 계속하고, 포인터 파일은 그대로 두며, 내용이 불완전하다고 표시합니다. OwnGit은 `.gitattributes`를 읽지 않으므로, 검사에서 아무것도 나오지 않았다고 해서 저장소가 LFS를 쓰지 않는다는 증거는 아닙니다.

### 실패와 취소

저장소마다 한 번에 하나의 실행만 진행되며(그 밖에는 `busy`), 실행은 원본의 전체 실행 시간(기본값 60분)을 넘을 수 없습니다(`limit`). 그 밖의 결과는 `cancelled`, `protected_default_branch`([기본 브랜치 바꾸기](#기본-브랜치-바꾸기) 참고), `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved`, `nothing_to_resolve`입니다. OwnGit이 분류하지 못한 실패는 `unclassified`입니다. `unsupported`는 원본이나 대상이 가져오기에서 지원하지 않는 기능을 쓴다는 뜻입니다.

`owngit serve`가 멈추면 실행 중인 가져오기를 취소하고 각각이 결과를 기록할 때까지 최대 45초 기다립니다. 다음 시작 때는 중단된 실행을 표시하고, 진행 중이던 게시를 확인합니다. 이때 쓰기를 반복하거나 되돌리지 않습니다. 가져오기 서비스를 시작할 수 없으면 가져오기 탭과 `import status`에 그렇게 표시되고 Git은 계속 동작합니다.

### 미해결 게시

OwnGit이 게시가 어떻게 끝났는지 증명할 수 없으면(예를 들어 ref는 썼지만 HEAD는 쓰지 못했거나 [두 번에 나눠 쓰는 새로고침](#두-번에-나눠-쓰는-새로고침)의 두 번째 단계가 실패한 경우) 그 게시는 미해결 상태가 됩니다. 저장소를 현재 상태 그대로 인정하기 전까지 새로고침은 거부됩니다.

1. 브랜치, 태그, HEAD와 마지막 실행에 적힌 이유를 확인합니다.
2. 남기고 싶지 않은 것은 일반 Git 작업으로 고칩니다.
3. 가져오기가 실행 중이 아닌지 확인합니다.
4. `owngit import resolve PROJECT`를 실행하거나 가져오기 탭의 버튼을 누릅니다.

OwnGit은 Git에 아무것도 쓰지 않고 현재 ref와 HEAD를 인정한 상태로 기록합니다. 다음 새로고침은 마지막으로 확인한 값을 아직 가진 ref는 원본을 따라가고 나머지는 원본과 다름으로 둡니다.

처음 가져오기가 미해결인데 그 저장소가 아직 없으면 OwnGit을 다시 시작하세요. 문제가 계속되면 그 가져오기의 `.owngit-create-*` 디렉터리를 저장소 폴더 밖으로 옮기고 다시 시작하세요.

## 명령줄 풀 리퀘스트

푸시로는 풀 리퀘스트가 생기지 않습니다. 서로 다른 원본 브랜치와 대상 브랜치를 푸시한 뒤 풀 리퀘스트를 만드세요(`--body-file`과 `--review`는 선택 사항입니다).

```sh
owngit pr create \
  --server http://HOST:7654 \
  --accept-insecure-http \
  --repository PROJECT \
  --source feature-branch \
  --target main \
  --title "Describe the change" \
  --body-file description.md \
  --review request \
  --password-file /path/to/owner-only-shared-password-file
```

- `--body-file`은 마크다운 설명을 파일에서 읽고 `-`이면 표준 입력에서 읽습니다.
- `--review skip`은 리뷰를 일부러 생략했다고 기록하며 승인이 아닙니다. `--review`를 빼도 나중에 `pr review request`를 실행할 수 있습니다.
- `--password-file`에는 관리자 비밀번호가 아니라 일반 접근용 공용 비밀번호를 넣으며 `reset-admin`과 같은 소유자 전용 검사를 거칩니다. 접근이 열려 있으면 뺍니다.
- `--accept-insecure-http`는 그 명령에 한해 일반 HTTP에 동의한다고 기록합니다.
- CLI는 URL에 넣은 인증 정보를 거부하며 리디렉션을 따라가지 않습니다.
- OwnGit 저장소의 clone 안에서는 `--server`와 `--repository`를 clone의 `origin` 원격에서 가져옵니다. 이때 비밀번호 파일은 첫 줄에 그 서버가 적혀 있을 때만 보냅니다([클론 안에서 실행하기](CODING_TOOLS.ko.md#클론-안에서-실행하기), [자격 증명 파일과 서버 줄](CODING_TOOLS.ko.md#자격-증명-파일과-서버-줄) 참고).

다른 명령도 같은 `--server`, `--accept-insecure-http`, `--repository`, `--password-file` 플래그를 받습니다. `pr show`는 현재 원본과 대상의 객체 ID를 알려 주며 모든 리뷰 결정과 병합에는 두 값을 모두 넘겨야 합니다.

```sh
owngit pr list
owngit pr show --number 1
owngit pr edit --number 1 --edit-revision 0 --title "New title" --body-file description.md
owngit pr diff --number 1
owngit pr mergeability --number 1
owngit pr review request --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr review submit --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID \
  --decision approved --reviewer "existing-tool: reviewer label" --note-file note.md
owngit pr review skip --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr merge --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
owngit pr close --number 1
owngit pr reopen --number 1
```

### 풀 리퀘스트 규칙

- 원본 브랜치와 대상 브랜치 한 쌍에는 풀 리퀘스트를 하나만 열어 둘 수 있습니다. 두 번째는 `pull_request_exists`로 거부되고 `error.details.number`가 열려 있는 번호를 알려 줍니다.
- 닫기(`pr close` 또는 페이지의 풀 리퀘스트 닫기)는 브랜치를 바꾸지 않고 기록을 남기며 그 쌍을 비웁니다. `pr reopen`은 같은 쌍으로 열린 다른 풀 리퀘스트가 없으면 다시 엽니다. 병합된 풀 리퀘스트는 닫거나 다시 열 수 없습니다(`pull_request_merged`). 닫기와 다시 열기에는 병합과 같은 접근 권한이 필요합니다.
- 설명과 리뷰 메모는 최대 64KiB의 마크다운이며 페이지에서는 HTML과 이미지 없이 보여 줍니다. `pr show`에는 설명(`body`)과 최근 리뷰 메모 다섯 개가 들어 있고 `pr list`에는 둘 다 없습니다.
- `pr edit`에는 `pr show`가 출력한 수정 번호(`edit_revision`)를 넘기고 바꿀 것만 지정합니다. `--title`, `--body-file`, 또는 둘 다입니다. 그사이 다른 사람이 풀 리퀘스트를 수정했다면 아무것도 바뀌지 않고 `stale_edit`으로 실패하며, 지금 수정 번호는 `error.details.current_edit_revision`에 있습니다. 다시 확인한 뒤 바꿀 내용을 다시 적용하세요. 페이지의 제목과 설명 수정도 같은 방식으로 동작하며 입력한 내용은 양식에 그대로 남습니다. 병합한 뒤를 포함해 어느 상태에서나 수정할 수 있고, 브랜치, 리뷰, 체크, 병합 기록은 바뀌지 않습니다.
- 리뷰는 `approved` 또는 `changes_requested`입니다. 리뷰어 라벨은 누가 제출했는지 기록할 뿐 독립적인 리뷰였다는 뜻은 아닙니다. 대기 중이거나 변경을 요청한 리뷰가 병합을 막지는 않습니다. 브랜치가 움직이면 이전 결정은 더 이상 적용되지 않으므로 다시 살펴보고 새 객체 ID로 결정하세요. 리뷰에는 메모를 남길 수 있고(`--note-file` 또는 페이지의 리뷰 결과 기록) 메모는 리뷰한 두 커밋에 묶입니다. 어느 브랜치든 움직인 뒤에도 메모는 이전 커밋 기준이라는 표시와 함께 계속 보입니다.
- 결과의 `created_by`, `edited_by`, `merged_by`, `review.actor`(지금 리뷰, 리뷰 요청 또는 생략)와 각 메모의 `actor`는 그 변경을 한 접근 방식을 알려 줍니다. OwnGit은 요청을 허가한 접근 방식을 기록하며(지금은 일반 접근) Git 커밋 작성자에서 가져오지 않습니다. OwnGit 1.1.3 전에 한 변경과, 중단된 병합을 OwnGit이 스스로 마무리한 경우에는 기록된 사람이 없어 이 필드가 빠집니다.
- `pr diff`는 풀 리퀘스트가 바꾸는 내용을 비교한 객체 ID와 함께 출력합니다(`--stat`은 패치 없이, `--patch`는 패치만). [풀 리퀘스트 변경 내용](CODING_TOOLS.ko.md#풀-리퀘스트-변경-내용)을 보세요. 변경 내용은 원본이 대상에서 갈라진 뒤 바꾼 내용, 곧 병합 기준(merge base)에서 원본까지의 차이입니다. 두 브랜치에 공통 커밋이 없거나 병합 기준이 여러 개이면 페이지가 그 이유를 알리고 변경 목록을 표시하지 않습니다.
- `pr mergeability`나 풀 리퀘스트 페이지의 병합 가능 여부 확인은 현재 원본과 대상 커밋으로 지금 병합할 수 있는지 알려 줍니다. 이 확인은 아무것도 바꾸지 않습니다. OwnGit은 요청할 때만 확인하므로 풀 리퀘스트를 열거나 목록을 볼 때는 병합을 계산하지 않습니다. `status`는 다음 가운데 하나입니다.
  - `clean`: 병합하면 쓰일 방식 `method`(`fast_forward`, `merge_commit`, `up_to_date`)가 함께 나옵니다.
  - `conflict`: 충돌한 경로가 `conflict_paths`에 최대 100개 나오고 더 있으면 `conflict_paths_truncated`가 붙습니다. 두 브랜치에 공통 기록이 없으면 `reason`이 `no_merge_base`입니다.
  - `unavailable`: OwnGit이 알아낼 수 없었을 때이며 `unsupported_git`이나 `source_branch_missing` 같은 `reason`이 함께 나옵니다.
  - `stale`: 넘긴 커밋이 더 이상 현재 커밋이 아닐 때이며 `source`와 `target`에 현재 커밋이 나옵니다.

  답은 어디에도 저장되지 않고 어느 브랜치든 움직이면 더 이상 맞지 않습니다. 이전 답이 아직 맞는지 확인하려면 그 답의 객체 ID를 `--source-oid`와 `--target-oid`로 넘기세요. 병합할 때는 OwnGit이 스스로 다시 확인합니다. 페이지에서 충돌이라는 답을 받으면 페이지를 다시 열 때까지 병합 버튼이 꺼집니다.
- 모든 명령은 JSON 결과를 출력합니다. 실패하면 바뀌지 않는 `error.code`와 0이 아닌 종료 코드를 내고, `connection_failed`에는 원인이 함께 나옵니다. 풀 리퀘스트 결과의 `checks`는 현재 원본 리비전에 기록된 체크 결과를 알려 주며, 기록이 없으면 `absent`입니다. 그 리비전의 `.owngit/checks.json`과 다른 체크를 실행했다면 `stale`입니다. 체크는 참고용이며 병합을 막지 않습니다.
- 병합은 fast-forward를 하거나, 이전 대상을 첫째 부모로 하는 병합 커밋을 만듭니다. 작성자는 `OwnGit <owngit@localhost>`이고 메시지에는 번호와 제목이 들어갑니다. squash, rebase, 강제 갱신, 원본 브랜치 삭제는 하지 않으며, 병합을 다시 시도하거나 중간에 끊겨도 커밋이 두 번 만들어지지 않습니다. 충돌 등으로 거부되었거나 도중에 멈춘 병합이 있어도 이후의 병합과 백업은 막히지 않습니다. 대상에 원본이 이미 들어 있으면 새 커밋 없이 `merge.mode`가 `up_to_date`인 병합으로 기록됩니다. 병합하려면 OwnGit 호스트에 Git 2.38 이상이 필요합니다(그 밖에는 `unsupported_git`).

## 프로젝트 체크

OwnGit은 체크 에이전트(helper)로 직접 실행한 체크를 기록하고 소유자가 켠 자동 체크를 실행합니다.

- 직접 실행하는 체크 에이전트는 사용자의 환경과 권한을 물려받으며 샌드박스가 아닙니다. OwnGit은 시도할 때마다 워킹 트리 상태를 기록하며, 변경이 있거나 상태를 알 수 없는 워킹 트리를 테스트한 커밋으로 보고하지 않습니다.
- 자동 체크는 OwnGit 계정으로, 제한된 로컬 Docker 컨테이너에서, 또는 별도로 연결한 러너에서 실행됩니다. 커밋된 `.owngit/checks.json`, 소유자 정책, 유효한 동의가 모두 있어야 합니다([자동 체크](AUTOMATIC_CHECKS.ko.md) 참고).

관리자 비밀번호로 저장소 범위의 체크 에이전트 토큰을 만듭니다. 토큰은 소유자만 읽을 수 있는 `--output` 파일에만 쓰이고 서버에는 해시로만 저장됩니다.

```sh
owngit helper-credential create \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --label laptop \
  --password-file /path/to/admin-password-file \
  --output ~/.owngit-helper-token
```

- `--output` 위치에 파일이나 링크가 이미 있으면 교체하지 않고 알려 줍니다. 만들기가 실패해 남은 파일은 직접 확인할 수 있게 그대로 둡니다.
- 응답을 받지 못하면 명령이 새 토큰을 취소하거나, 직접 취소할 수 있도록 생성 식별 정보를 출력합니다(토큰 자체는 출력하지 않습니다).
- `helper-credential list`와 `helper-credential revoke --id ID`로 토큰을 관리합니다. 저장소 체크 탭의 체크 에이전트 토큰 링크에서도 브라우저로 같은 일을 할 수 있습니다. 취소한 토큰은 바로 쓸 수 없게 됩니다.
- 명령은 언제나 관리자 비밀번호를 묻습니다. 브라우저에서 발급하고 취소할 때는 [관리자 비밀번호 확인](#관리자-비밀번호-확인) 설정을 따릅니다.

그다음 고정된 작업을 만들고 체크를 실행합니다.

```sh
owngit check task new \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --title "Fix the failing build"

owngit check run \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --task TASK_ID --check "unit=go test ./..." --check "lint=go vet ./..."
```

[코딩 도구](CODING_TOOLS.ko.md)에서 이 명령들과 수정 라운드, 결과 필드, 종료 코드를 설명합니다. Windows의 `cmd.exe`는 알 수 없는 명령에 종료 코드 1을 돌려주므로 OwnGit은 그 결과를 `failed`로 기록합니다.

### 체크 원본 로그

체크의 원본 로그는 `owngit.sqlite`에 로그마다 256 KiB까지 저장되고 체크가 시작된 때부터 세어 기본값으로 30일 동안 보관됩니다. 보관 기간은 설정의 보관과 복구 탭에 있는 체크 원본 로그 항목이나 `owngit settings set --check-logs 90d`로 바꾸며, 7일, 30일, 90일, 1년, 계속 보관 중 하나를 고릅니다.

- 새로 고른 기간은 지금 보관 중인 원본 로그 전체에 바로 적용되며, 저장해도 보관 중인 로그를 다시 쓰지는 않습니다. 기간이 지난 로그는 더는 열 수 없고, OwnGit이 시작하거나 체크 결과가 들어올 때 하는 다음 정리에서 지워집니다.
- 기간을 줄이면 오래된 로그를 볼 수 없게 되고 늘리면 상태 데이터베이스가 공간을 더 씁니다.
- 계속 보관하는 동안에는 로그에 삭제 예정일이 없으므로, API는 `log_expires_at`을, `owngit check log`는 `expires_at`을 넣지 않습니다.
- 저장된 보관 기간을 읽을 수 없을 때도 API는 이 날짜를 넣지 않습니다. `check log`는 다시 정해야 할 설정을 알려 주며, 대시보드는 로그를 읽을 수 없다고 표시합니다.
- 로그가 만료된 뒤에도 작업과 시도 기록, 결과, 발췌는 남습니다(`log_expired`). 그 전에 사라진 로그는 `log_missing`을 돌려줍니다.
- 결과를 저장하는 중에 데이터베이스가 가득 차거나 저장된 원본 로그 보관 기간을 읽을 수 없으면, OwnGit은 로그 없이 결과만 저장하고 그 이유를 로그 오류로 시도에 기록합니다.

## Git 전송 제한

- clone, fetch, 푸시, 압축 파일 같은 Git 요청 하나는 최대 4 GB까지 받고 따로 최대 4 GB까지 보낼 수 있으며, 30분 안에 끝나야 합니다. 이 한도는 설정 화면 저장소 탭의 Git 전송에서 최대 전송 크기와 최대 전송 시간으로 바꾸거나 `owngit settings set --transfer-size 8GB --transfer-time 2h`로 바꿉니다. 크기는 1 MB부터 64 GB까지(1 GB는 1024 MB), 시간은 1분부터 24시간까지 정할 수 있습니다. 바꾼 한도는 그 뒤에 시작하는 전송부터 적용됩니다. 한도를 높이면 크거나 느린 전송이 서버를 더 오래 붙잡고 디스크도 더 많이 씁니다.
- 크기 제한을 넘는 푸시는 HTTP 413으로 거부되는데, Git은 `fatal: the remote end hung up unexpectedly`만 표시할 수도 있습니다. 제한을 넘는 clone이나 fetch는 중간에 끊깁니다. 클라이언트가 무응답 한도(기본 1분) 동안 데이터를 주고받지 않는 전송도 끊깁니다. OwnGit은 Git LFS를 제공하지 않으므로 전체 기록이 크기 한도보다 큰 저장소는 OwnGit으로 clone할 수 없습니다. 큰 바이너리 파일은 Git 기록에 넣지 마세요.
- 한 저장소는 Git 요청을 동시에 4개까지 실행합니다. 서버는 여기에 추가 자리 1개를 두는데, 이 자리는 진행 중인 전송이 없는 저장소만 쓸 수 있습니다. 그래서 한 저장소가 바빠도 다른 저장소는 기다리지 않습니다. 전체로는 5개까지 실행됩니다. 빈자리가 없는 요청은 최대 90초 기다린 뒤 HTTP 503과 `Git service is busy with other transfers; try again shortly`를 받습니다. 명령을 다시 실행하세요.
- 저장소당 동시 전송, 추가 자리, 무응답 한도, 자리 대기 시간은 설정 화면 저장소 탭의 Git 전송에 있는 동시 전송과 대기에서 바꿉니다. 명령줄에서는 `owngit settings set`에 `--transfer-per-repository`(1부터 32까지), `--transfer-extra-slots`(0부터 32까지), `--transfer-idle`(10초부터 1시간까지, 예: `1m`), `--transfer-queue`(5초부터 10분까지, 예: `90s`)를 줍니다. 설정 API에서는 `git_transfer`의 `per_repository`, `extra_slots`, `idle_seconds`, `queue_seconds`입니다. 바꾼 값은 저장한 뒤 자리를 요청하는 전송부터 적용됩니다. 수를 낮춰도 이미 진행 중인 전송은 멈추지 않습니다. 새 전송은 진행 중인 전송이 충분히 끝날 때까지 기다립니다. 어느 값이든 기본값보다 크게 하면 전송이 자리를 더 오래 차지해 다른 클라이언트가 기다릴 수 있다는 경고가 나옵니다.
- 비밀번호를 너무 많이 틀리면 그 주소가 일정 시간 멈추고 그동안 Git은 `Retry-After`와 함께 HTTP 429(Too Many Requests)를 받습니다. [로그인 시도 한도](#로그인-시도-한도)를 참고하세요.
- OwnGit을 멈추면 실행 중인 요청을 최대 10초 기다린 뒤 나머지를 끊고 몇 개를 끊었는지 로그에 남깁니다.

## 보기 한도

보기 한도는 한 페이지가 파일, 차이(diff), 풀 리퀘스트 비교를 보여 주려고 읽는 양을 제한합니다. 그래서 아주 큰 파일이나 변경이 서버를 오래 붙잡지 않습니다. 설정 화면 저장소 탭의 보기 한도에서 바꾸거나 `owngit settings set`에 아래 옵션을 줍니다. 크기는 `4MB`, `256KB`처럼 씁니다(1 MB는 1024 KB).

| 한도 | 기본값 | 범위 | 옵션 |
|---|---|---|---|
| 원본 파일 내려받기 | 10 MB | 1 MB부터 256 MB까지 | `--browse-raw` |
| 파일 보기 | 2 MB | 64 KB부터 64 MB까지 | `--browse-file` |
| 커밋 차이(커밋 페이지의 모든 파일) | 2 MB | 64 KB부터 64 MB까지 | `--browse-commit-diff` |
| 한 파일만의 차이 | 8 MB | 64 KB부터 64 MB까지 | `--browse-file-diff` |
| 커밋 페이지 안의 파일 하나 | 256 KB | 16 KB부터 16 MB까지 | `--browse-commit-file` |
| 풀 리퀘스트 비교 | 8 MB | 64 KB부터 64 MB까지 | `--browse-compare` |
| 비교 시간 | 20초 | 5초부터 1분까지 | `--browse-compare-time` |

- 한도를 넘는 원본 파일은 브라우저에서 내려받을 수 없으니 저장소를 클론해서 받으세요. 파일 화면은 한도까지만 보여 주고 나머지는 내려받기로 제공합니다. 커밋 페이지에서 차이가 한도보다 큰 파일은 목록에만 나오고, 그 파일만의 차이로 가는 링크가 붙습니다.
- 바꾼 한도는 저장한 뒤 여는 페이지부터 적용됩니다. 페이지에는 여전히 자체 시간 한도가 있습니다. 그래서 비교 시간을 그보다 길게 잡으면 불완전한 비교 대신 페이지 오류가 납니다.
- API의 풀 리퀘스트 차이, `owngit pr diff`, MCP 도구 `pull_request_diff`는 기본값과 같은 고정 한도를 따로 씁니다.
- 어느 한도든 기본값보다 크게 하면 메모리를 더 쓰고 다른 페이지가 늦어질 수 있다는 경고가 나옵니다.
- 설정 API의 `browse_limits` 묶음에는 `raw_bytes`, `file_bytes`, `commit_patch_bytes`, `file_patch_bytes`, `commit_file_bytes`, `compare_bytes`, `compare_seconds`가 있습니다.

## 체크 상한

체크 상한은 저장소의 [체크 설정](AUTOMATIC_CHECKS.ko.md)이 이 컴퓨터에서 고를 수 있는 최댓값입니다. 설정 화면 저장소 탭의 체크 상한에서 바꾸거나 `owngit settings set`에 아래 옵션을 줍니다. 기본값은 이전 릴리스가 적용하던 한도와 같습니다.

| 상한 | 기본값 | 범위 | 옵션 |
|---|---|---|---|
| 체크 하나의 시간 | 24시간 | 1초부터 168시간(7일)까지 | `--check-time` |
| 체크 하나의 출력 | 64 MB | 1 KB부터 1024 MB까지 | `--check-output` |
| 저장소당 대기 체크 수 | 1000 | 1부터 10000까지 | `--check-queue` |
| 저장소당 동시 실행 체크 수 | 100 | 1부터 1000까지 | `--check-active` |
| 컨테이너 CPU | 64 | 0.1부터 1024까지 | `--check-cpus` |
| 컨테이너 메모리 | 64 GB | 64 MB부터 1024 GB까지 | `--check-memory` |
| 컨테이너 프로세스 수 | 4096 | 16부터 65536까지 | `--check-processes` |
| 컨테이너 임시 공간 | 16 GB | 1 MB부터 1024 GB까지 | `--check-scratch` |
| 체크용으로 복사하는 소스 | 4 GB | 1바이트부터 1024 GB까지 | `--check-source` |

- 상한은 체크 설정을 저장할 때와 체크를 대기열에 넣을 때 확인합니다. 자동 체크 화면은 필드마다 상한 안의 범위를 보여 주고, 상한보다 큰 값은 관리자가 상한을 올릴 수 있다는 안내와 함께 거부합니다.
- 상한을 바꿔도 저장된 설정이나 그 동의는 바뀌지 않습니다. 상한을 낮춰 설정이 그보다 커지면 상한을 올리거나 설정을 줄일 때까지 새 체크를 대기열에 넣지 않습니다. 체크 상한에 그런 저장소 목록이 나오고, 그 저장소의 자동 체크 화면에도 안내가 나옵니다. 이미 대기열에 들어간 체크는 들어갈 때의 한도로 실행됩니다.
- 출력 상한은 체크 하나가 출력할 수 있는 양입니다. OwnGit은 이보다 많이 출력한 체크를 멈추며 그 체크는 완료되지 않음(`incomplete`)으로 끝납니다. OwnGit은 출력을 모두 세지만 체크마다 원본 로그에 저장할 수 있는 최대치인 처음 256 KiB만 보관하므로 출력 상한을 키워도 체크가 메모리를 더 쓰지 않습니다.
- 상한을 기본값보다 올리면 체크를 켠 저장소가 자원을 더 많이 쓰도록 요청할 수 있다고 경고합니다.
- 설정 API의 `check_ceilings` 묶음에는 `timeout_seconds`, `output_bytes`, `queue_limit`, `active_jobs`, `container_cpu_millis`, `container_memory_bytes`, `container_pids`, `container_scratch_bytes`, `source_total_bytes`가 있습니다. 저장된 설정보다 상한을 낮추는 `PATCH`는 해당 저장소를 밝힌 경고와 함께 답합니다.
- 상한은 이 컴퓨터의 설정이라 백업에 들어가지 않습니다. 더 높은 상한에서 저장한 설정은 백업 형식 11로 백업되어 어디서든 복원되며, 상한이 더 낮은 컴퓨터에서는 그곳의 상한을 올릴 때까지 체크를 대기열에 넣지 않습니다.

## 저장 공간

OwnGit은 자기 기록을 상태 디렉터리에, Git 데이터를 저장소 폴더에 둡니다. 프로그램을 지워도 둘 다 남으니 더 필요 없을 때 직접 지우세요.

### 상태 디렉터리

상태 디렉터리는 플랫폼의 설정 디렉터리 아래 `owngit`이며 설정 디렉터리가 없으면 `~/.owngit`입니다. 여기에는 다음이 있습니다.

- `owngit.sqlite`(쓰는 동안에는 `-wal`과 `-shm` 파일도)
- 가져오기 인증 정보 `import-credentials/NAME.json`. 암호화하지 않은 채 OwnGit 계정만 읽을 수 있게 저장되므로 상태 디렉터리를 인증 정보처럼 보호하세요.

상태 디렉터리는 로컬 디스크에 두고 다른 컴퓨터와 함께 쓰는 공유에는 두지 마세요. 공유 파일 시스템을 제공하는 쪽은 그 안에 든 계정과 인증 정보를 읽고 바꾸거나 다른 상태로 바꿔치기할 수 있습니다. 그래서 OwnGit은 상태 디렉터리가 다음에 있거나, 그런 폴더를 거쳐 가면 거부합니다.

- 네트워크 공유. Windows UNC 경로도 여기에 해당합니다.
- FUSE나 9P 파일 시스템
- 가상 머신의 공유 폴더. WSL에서 보이는 `/mnt/c` 같은 Windows 드라이브(대신 WSL의 Linux 파일 시스템에 있는 폴더를 쓰면 됩니다)와 Docker Desktop의 바인드 마운트(대신 이름 있는 볼륨(named volume)을 쓰면 됩니다)도 여기에 해당합니다.

상태 디렉터리로 가는 길에는 다음 규칙도 적용됩니다.

- Unix에서는 소유자나 상위 폴더 때문에 다른 계정이 바꿔치기할 수 있는 상태 디렉터리를 거부합니다. 표시된 `chmod` 명령을 실행하거나 다른 위치를 고르세요.
- 상태 디렉터리나 `--log-file` 폴더로 가는 길에서 이런 파일 시스템에 있는 링크는 따라가지 않습니다. 그래서 네트워크 홈 폴더 안의 링크를 거쳐 갈 수 없습니다. 링크 대신 실제 경로를 쓰세요.
- root로, 또는 Windows에서 관리자 권한으로 실행하면 로그를 포함해 이런 파일 시스템의 폴더를 전혀 쓰지 않습니다. 다른 계정은 로그를 공유에 둘 수 있습니다.
- Linux에서는 알려진 파일 시스템 종류 목록으로 이를 판단하며 목록에 없는 종류는 로컬로 봅니다.
- Windows에서는 가는 길에 링크나 정션, 볼륨을 마운트한 폴더가 있어도 거부합니다. 마운트한 디스크는 드라이브 문자로 쓰세요.
- macOS에서는 가는 길의 모든 폴더를 OwnGit을 실행하는 계정이 읽을 수 있어야 합니다.

상태 디렉터리 자체를 거부하면 OwnGit은 그 이유를 그곳에 기록할 수 없습니다. 이때 `owngit service install`은 OwnGit이 응답하지 않았다고만 알리고 로그를 가리킵니다. 이유는 로그에 있으며 터미널에서 `owngit serve`를 실행해도 볼 수 있습니다.

### 저장소 폴더

설정할 때 고른 저장소 폴더는 다른 디스크나 마운트한 SMB, NFS 공유에 두어도 되지만 한 번에 OwnGit 하나만 쓰게 하세요. OwnGit은 폴더에 이미 있는 파일은 건드리지 않으며 `.git`으로 끝나는 bare 저장소를 만듭니다. 실행되는 동안에는 그 폴더의 `.owngit-serve.lock` 파일을 잠가 둡니다. 그래서 상태 디렉터리 사본으로 시작한 서버처럼 같은 폴더를 쓰는 두 번째 서버는 시작하지 않습니다. 네트워크 공유에서는 이 감지가 공유의 파일 잠금 지원에 따라 다릅니다.

- 저장소 이름은 `.git`으로 끝날 수 없고, 어느 플랫폼에서든 확장자가 있든 없든 `CON`, `AUX`, `NUL`, `COM1`, `LPT1` 같은 Windows 장치 이름을 쓸 수 없습니다. `new`와 `new-import`는 예약된 이름입니다.
- 새 저장소는 임시 이름 `.owngit-create-*`로 쓴 뒤 제자리로 이름을 바꿉니다. Windows에서는 백신이나 검색 색인이 새 디렉터리를 잠시 잠글 수 있어 OwnGit은 약 2초 동안 다시 시도합니다.
- 삭제한 저장소는 `.owngit-delete-*`, `.owngit-removed`, `.owngit-deletion-*` 이름을 씁니다([저장소 삭제하기](#저장소-삭제하기) 참고).

### 사용 중인 저장소와 캐시

어떤 Git 작업이 저장소를 붙잡고 있으면 대시보드는 최대 1초만 기다린 뒤 마지막으로 읽은 브랜치 목록을 보여 주거나 그 저장소를 사용 중으로 표시합니다. 그 저장소의 페이지는 요청 기한 직전까지 기다린 뒤 HTTP 503과 `Retry-After`로 답합니다.

OwnGit은 쓰기 사이사이에 저장소마다 브랜치와 태그를 기억해 둡니다. 최근에 읽은 폴더 목록, 4 MiB 이하의 파일, diff, 풀 리퀘스트 비교를 합계 64 MiB까지 메모리에 두므로, 다시 연 페이지는 Git 프로세스를 시작하지 않습니다. 저장소 폴더에서 직접 바꾼 ref는 OwnGit이 그 저장소의 ref를 다음으로 바꾸거나 다시 시작한 뒤에 반영됩니다. 활동 집계는 시작할 때와 브랜치가 바뀐 뒤 백그라운드에서 계산하며 느린 공유에서는 일부 저장소를 아직 집계하는 중이라고 대시보드가 알려 줍니다.

### 저장소 유지 관리

OwnGit은 아무도 쓰지 않는 저장소를 유지 관리해서 Git이 빠르게 동작하고 공간을 덜 쓰게 합니다. 저장소가 바뀐 뒤 5분 동안 푸시나 요청이 없으면 흩어진 ref와 객체를 묶고 commit-graph를 갱신합니다. 매일 정한 시간대(현지 시각 03:00부터 05:00까지)에는 pack이 20개보다 많은 저장소의 pack을 하나로 합칩니다. 유지 관리는 ref나 보관된 기록을 지우지 않고, [쓰지 않는 객체 정리](#쓰지-않는-객체-정리)를 켜지 않았다면 객체도 지우지 않습니다. 한 단계가 도는 동안에만 저장소를 붙잡으므로 그때 들어온 푸시나 페이지는 그 단계를 기다리며 실행할 때마다 로그 한 줄을 남깁니다. 시작할 때는 중단된 Git 명령이 남긴 임시 pack 파일과 잠금 파일을 지우고 그 이름을 로그에 남깁니다.

유지 관리 설정은 설정 화면 보관과 복구 탭의 저장소 유지 관리에서 바꾸거나 `owngit settings set`에 다음 옵션을 줍니다.

- `--maintenance on` 또는 `--maintenance off`: 유지 관리를 할지 정합니다. 기본값은 켬입니다. 끄면 어떤 저장소도 유지 관리하지 않고 정리도 하지 않습니다. 꺼져 있다는 사실은 로그에 한 번 남깁니다.
- `--maintenance-window 3-5`: 매일 시간대로, 호스트의 현지 시각을 한 시간 단위로 적습니다. 시작과 끝은 달라야 합니다. `22-6`처럼 자정을 넘겨도 됩니다. 이런 시간대는 시작한 날짜에 속합니다.
- `--maintenance-idle 5m`: 저장소가 이만큼 쓰이지 않아야 시작합니다. 1분부터 24시간까지 정합니다.
- `--maintenance-step-time 30m`: 보통 유지 관리의 각 단계에 쓸 수 있는 시간입니다. 1분부터 24시간까지 정합니다.
- `--maintenance-consolidation-time 2h`: 저장소의 모든 pack을 하나로 합치는 데 쓸 수 있는 시간입니다. 1분부터 24시간까지 정합니다.
- `--maintenance-packs 20`: pack이 이보다 많은 저장소를 시간대에 합칩니다. 2부터 1000까지 정합니다.

예로 든 값이 기본값입니다. 설정 API의 `maintenance` 묶음에는 `enabled`, `window_start_hour`, `window_end_hour`, `idle_seconds`, `command_seconds`, `full_repack_seconds`, `pack_threshold`가 있습니다. 바꾼 설정은 다음 유지 관리부터 적용됩니다. 이미 진행 중인 작업은 시작할 때의 설정으로 끝까지 돕니다. 유지 관리를 끄거나 시간대를 2시간보다 길게 잡으면 경고가 나옵니다. 끄면 Git이 느려지고 공간을 더 쓸 수 있으며 시간대를 넓히면 작업하는 동안 컴퓨터가 바쁠 수 있다는 내용입니다. 저장된 설정을 읽을 수 없으면 다시 정할 때까지 어떤 저장소도 유지 관리하지 않습니다. 로그에는 그 설정 이름이 나옵니다.

### 쓰지 않는 객체 정리

쓰지 않는 객체 정리는 어떤 ref로도 닿지 않는 오래된 Git 객체를 지웁니다. 기록을 보관하지 않은 강제 푸시가 남긴 객체가 여기에 해당합니다. 기본값은 꺼짐이며 꺼 두면 아무것도 지우지 않습니다. 켜려면 설정 화면 보관과 복구 탭의 쓰지 않는 객체 정리에서 켜거나 `owngit settings set --unused-object-cleanup on`을 실행하세요.

- 어떤 ref로도 닿지 않고 유예 기간보다 오래된 객체만 지웁니다. 유예 기간은 기본 14일이고 2일부터 365일까지 정합니다(`--cleanup-grace-days`). 최소 2일이면 전송, 가져오기, 체크가 아직 쓰고 있을지 모르는 새 객체는 지워지지 않습니다.
- ref가 닿는 것은 무엇도 지우지 않습니다. 브랜치, 태그, [다른 ref 이름공간](#다른-ref-이름공간), 보관된 기록, 풀 리퀘스트 ref가 모두 여기에 듭니다. `.keep` 파일이 붙은 pack은 그대로 두며 백업이 저장소를 잡고 있는 동안에는 정리하지 않습니다.
- 유지 관리가 켜져 있을 때만 매일 밤 유지 관리 시간대에 저장소마다 한 번 돕니다. 그날 밤의 pack 합치기 대신 저장소의 모든 pack을 다시 쓰므로 큰 저장소는 시간이 더 걸립니다. 누가 저장소를 쓰면 지금 단계만 마치고 멈춘 뒤 나중에 이어서 합니다.
- 끝나지 않은 체크(대기, 배정, 실행 중)나 진행 중인 가져오기가 있는 저장소는 다음 유지 관리 시간대까지 정리를 미룹니다. 정리가 이미 체크 작업의 커밋을 지웠다면 그 작업을 다시 실행하려 해도 `check_source_missing`으로 거부합니다.
- 호스트에 Git 2.37 이상이 필요합니다. 더 오래된 Git이면 정리가 실패하고 로그에 이유가 남으며 아무것도 지워지지 않습니다.
- 켜면 유예 기간보다 오래된 닿지 않는 객체가 영구히 지워질 수 있다는 경고가 나옵니다.

정리는 기록이나 이미 푸시한 비밀값을 없애는 수단이 아닙니다. 보관된 기록이나 다른 ref가 아직 닿는 것은 그대로 남기 때문입니다. 비밀값을 없애는 방법은 [백업](#백업)을 보세요. 설정 API의 `unused_object_cleanup` 묶음에는 `enabled`와 `grace_days`가 있습니다. 저장된 설정을 읽을 수 없으면 객체를 지우지 않고 유지 관리만 정리 없이 계속합니다. 로그에는 그 설정 이름이 나옵니다.

### 상태 데이터베이스 업그레이드

시작할 때 OwnGit은 이전 릴리스의 데이터베이스를 [먼저 백업한 뒤](#업그레이드-전-백업) 한 트랜잭션 안에서 그 자리에서 업그레이드합니다. 그리고 `state database upgraded from schema 15 to 16` 같은 한 줄을 남깁니다(`backup` 같은 오프라인 명령이면 표준 오류에).

- 더 새로운 버전의 데이터베이스는 거부합니다. 테이블, 인덱스, 트리거, 뷰가 기록된 스키마 버전에서 OwnGit이 만드는 것과 다른 데이터베이스도 거부합니다. 어느 경우든 파일은 그대로 둡니다.
- 이전 빌드는 새 빌드가 업그레이드한 데이터베이스를 거부합니다. 되돌리려면 업그레이드 전에 만든 백업을 이전 버전으로 복원하세요.
- OwnGit 밖에서 데이터베이스를 바꿨다면 그 변경을 되돌리세요. 아니면 백업을 만든 OwnGit 버전으로 그 백업을 복원하세요.
- 만료된 로그와 삭제된 기록이 차지하던 공간은 데이터베이스 안에서 재사용되지만, 파일 크기는 줄지 않고 옛 바이트가 안전하게 지워지지도 않습니다. 전체 크기 제한은 없고 OwnGit은 `VACUUM`을 실행하지 않습니다.
- 시작할 때 `-wal`이나 `-shm` 파일이 있으면 검사하려고 데이터베이스를 비공개 임시 디렉터리로 복사하므로, 임시 볼륨에 그만큼 여유 공간이 필요합니다.

## 백업

OwnGit은 실행 중에 직접 백업을 만들 수 있습니다. 정해 둔 예약에 따라 만들거나, 요청하면 바로 만듭니다. 관리자는 [대시보드](#대시보드에서-백업-관리하기)나 `owngit backup` 명령으로 백업을 관리합니다. 멈춰 있는 OwnGit은 [`owngit backup --output`](#멈춘-owngit-백업하기)으로 백업합니다. 어느 쪽이든 백업은 폴더 하나이며 [`owngit restore`](#백업-복원하기)로 복원하고 [`owngit backup verify`](#백업-검사하기)로 검사합니다. 백업 하나를 `.tar` 파일 하나로 [내려받을](#백업-내려받기) 수도 있습니다. [업그레이드 전 백업](#업그레이드-전-백업)은 이와 별개이고 따로 켜고 끕니다.

보관된 기록 덕분에 강제 푸시나 삭제를 해도 작업이 사라지지 않지만 보관된 기록은 백업이 아닙니다.

한 번이라도 푸시한 비밀값은 강제 푸시나 브랜치 삭제 뒤에도, 보관된 기록을 꺼 두었어도 저장소의 Git 데이터에 남습니다. 저장소를 읽을 수 있고 그 커밋 ID를 아는 사람은 브라우저에서 계속 열어 볼 수 있습니다. 기록을 보관하는 저장소라면 보관된 기록과 이후의 모든 백업에도 남습니다. 확실히 없애는 방법은 [저장소를 파일까지 삭제](#저장소-삭제하기)하는 것뿐이고 그 전에 만든 백업에는 여전히 남습니다. [쓰지 않는 객체 정리](#쓰지-않는-객체-정리)는 보관된 기록이나 다른 ref가 아직 닿는 것을 남기므로 여기에 기대지 마세요. 실수로 푸시한 비밀값은 새것으로 교체하세요.

### 대시보드에서 백업 관리하기

관리자는 설정 화면의 보관과 복구 탭(`/settings/storage`)에서 백업을 관리합니다. 백업 항목은 관리자로 확인한 브라우저에만 보입니다. 그 밖의 사람에게는 백업이 하는 일과 관리자 확인 버튼만 보입니다.

- **백업**: 예약을 정하는 항목이며 저장 버튼이 따로 있습니다. 백업 폴더, 예약 백업 켜기와 끄기, 백업 간격(12시간, 하루, 7일), 보관할 백업 수(1부터 1000까지), 새 백업마다 검사 켜기와 끄기를 정합니다. 뜻은 [`owngit backup schedule set`](#예약-백업)의 옵션과 같습니다. 예약 백업이나 검사를 끄면 무엇을 잃는지 알려 주는 경고가 바로 나오고 저장한 뒤에도 다시 나옵니다. 아직 폴더를 저장하지 않았다면 OwnGit이 백업을 만들지 않는다고 알려 줍니다.
- **현재 상태**: 예약 백업이 켜짐, 꺼짐, 설정 안 됨 중 어느 상태인지, 지금 진행 중인 백업, 마지막 백업의 결과와 시각, 마지막으로 검사를 통과한 백업, 다음 예약 백업을 보여 줍니다. 마지막으로 [요청한 검사](#남아-있는-백업-다시-검사하기)의 결과도 나옵니다. 백업 폴더가 있는 디스크로는 복원할 수 없으면 경고가 나옵니다([백업을 둘 수 있는 곳](#백업을-둘-수-있는-곳) 참고). 지금 백업 버튼도 여기 있으며 [`owngit backup now`](#지금-백업하기)와 같게 동작합니다.
- **백업 기록**: 기록된 백업을 새것부터 모두 보여 줍니다. 백업마다 결과, 시작 시각, 종류(예약 또는 지금 백업), 검사(통과, 실패, 검사 안 함), Git 쓰기 최대 대기, 메시지, 폴더가 나옵니다. OwnGit이 아직 남겨 둔 백업에는 다시 검사와 내려받기 버튼, 이 백업으로 복원 안내가 있습니다. 백업이 남아 있지 않은 기록에는 그렇다고 표시합니다.
- **백업 파일로 복원**: OwnGit에서 내려받은 백업 파일을 다시 받습니다. [백업 파일로 복원하기](#백업-파일로-복원하기)를 참고하세요.

버튼마다 설정 화면과 같은 조건으로 관리자 비밀번호를 묻습니다. [관리자 비밀번호 확인](#관리자-비밀번호-확인)을 참고하세요. 지금 백업, 다시 검사, 올리기는 끝나기를 기다리지 않고 바로 돌아옵니다. 끝났는지는 페이지를 새로 고쳐 확인하세요.

### 예약 백업

백업을 둘 폴더를 정하고 예약 백업을 켜세요. 대시보드의 백업 항목에서 해도 되고 다음 명령으로 해도 됩니다.

```sh
owngit backup schedule set \
  --destination /absolute/path/to/backups \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

예약을 저장하면 첫 백업이 바로 시작됩니다. `owngit backup schedule`, `now`, `status`, `runs`, `check`, `download`, `upload`는 실행 중인 서버에 요청하는 명령입니다. [가져오기 명령](#명령줄에서-가져오기)과 같은 `--server`, `--accept-insecure-http`, `--password-file` 플래그로 관리자 비밀번호를 받고 결과를 JSON으로 출력합니다. 아래에서는 이 플래그를 생략했습니다. [`owngit backup --output`](#멈춘-owngit-백업하기)과 [`owngit backup verify`](#백업-검사하기)는 서버나 그 비밀번호 없이 이 컴퓨터에서 바로 동작합니다.

- `--destination`은 OwnGit을 실행하는 컴퓨터의 폴더이며 절대 경로로 씁니다. OwnGit의 상태 디렉터리나 저장소 폴더 안이면 안 되고 [백업을 둘 수 있는 곳](#백업을-둘-수-있는-곳)의 규칙을 따릅니다. 폴더가 없으면 OwnGit을 실행하는 계정만 쓸 수 있게 만듭니다.
- `--interval`은 `12h`, `1d`(기본값), `7d` 중 하나입니다.
- `--keep`은 OwnGit이 그 폴더에 남겨 둘 자기 백업의 개수로, 1부터 1000까지입니다(기본값 7).
- `--verify on`(기본값)이면 새 백업마다 [새 백업 검사](#새-백업-검사)에서 설명하는 검사를 합니다. `--verify off`면 검사하지 않습니다.

`set`은 예약 백업을 켜고 넘긴 옵션만 바꿉니다. 처음 `set`할 때는 `--destination`이 필요합니다. `owngit backup schedule show`는 예약을 보여 줍니다. `owngit backup schedule off`는 예약 백업을 멈춥니다. 이미 만든 백업은 그대로 남고 `owngit backup now`도 계속 쓸 수 있습니다. 예약 백업이나 검사를 끄면 무엇을 잃는지 알려 주는 경고가 응답에 함께 옵니다.

다음 예약 백업은 마지막 예약 백업을 시작한 때로부터 한 주기 뒤에 합니다. 예약 백업은 OwnGit이 실행 중일 때만 만들어집니다.

- 백업할 때가 됐는데 OwnGit이 멈춰 있었다면, 다시 시작할 때 백업을 하나만 만듭니다. 놓친 주기마다 하나씩 만들지는 않습니다.
- 실패했거나 중단된 예약 백업은 다음 주기가 오기 전에는 다시 하지 않습니다. 바로 다시 하려면 `owngit backup now`를 실행하세요.

### 지금 백업하기

대시보드에서 지금 백업을 누르거나 다음 명령을 실행하세요.

```sh
owngit backup now
```

이 명령은 예약에 정한 폴더로 백업을 바로 시작합니다. 백업이 끝나기를 기다리지 않고 곧바로 돌아옵니다. 예약 백업을 꺼 두었어도 됩니다. 진행 상황은 `owngit backup status`로 보세요. 검사와 이전 백업 정리는 예약 백업과 똑같이 합니다.

- 폴더를 정하기 전에는 `Choose a backup folder first.`라고 답합니다.
- 백업, 검사, 올리기는 한 번에 하나만 실행됩니다. 백업이 진행 중일 때 백업이나 검사, 올리기를 시작하면 `A backup is already running. Wait for it to finish.`라고 답합니다. 검사나 올리기가 진행 중일 때 무엇이든 시작하면 `A backup, a verification or an upload is running. Wait for it to finish.`라고 답합니다. 검사나 올리기 도중에 예약 백업 시각이 되면 그 작업이 끝나는 대로 예약 백업을 시작합니다.

### 백업 상태 확인하기

`owngit backup status`는 대시보드의 현재 상태처럼 백업 상태를 보여 줍니다.

- `schedule`: `state`는 폴더를 정하기 전에는 `not_configured`, 그 뒤에는 `on`이나 `off`입니다. 폴더, 주기, 남길 개수, 검사 여부도 함께 나옵니다.
- `running`: 지금 실행 중인 백업. 없으면 null입니다.
- `last_run`: 마지막으로 끝난 백업
- `last_verified`: 검사를 통과했고 OwnGit이 아직 남겨 둔 가장 새 백업
- `next_run`: 다음 예약 백업 시각. 이미 지난 시각이면 실행 중인 백업이 끝나는 대로 시작한다는 뜻입니다.
- `check`: 마지막으로 [요청한 검사](#남아-있는-백업-다시-검사하기). 없으면 null입니다.
- `upload`: [올린 백업](#백업-파일로-복원하기). 없으면 null입니다. 검사를 통과하면 그 백업을 복원하는 명령이 `upload_restore_command`에 나옵니다.
- `restore_limit`: 백업 폴더가 있는 디스크로 복원할 수 없을 때만 나오며 폴더(`destination`)와 파일 시스템(`file_system`)을 알려 줍니다.

`owngit backup runs`는 기록된 백업을 새것부터 모두 보여 줍니다. 백업마다 다음이 나옵니다.

- `kind`: `scheduled`, 또는 지금 백업하기로 만든 `manual`
- `status`: `running`, `succeeded`, `failed`, `interrupted` 중 하나. `interrupted`는 백업이 끝나기 전에 OwnGit이 멈췄거나 프로세스가 강제로 종료됐다는 뜻이며 완성된 폴더가 남았더라도 이런 백업은 완성된 백업이 아닙니다. [백업이나 복원이 중단되면](#백업이나-복원이-중단되면)을 참고하세요.
- `verification`: `passed`, `failed`, `not_run` 중 하나. `passed`일 때만 검증된 백업입니다.
- `backup_name`, `path`: 백업 폴더. 백업을 만들지 못했거나, OwnGit이 백업을 지웠거나, 백업이 없어졌거나 다른 것으로 바뀐 것을 OwnGit이 알게 되면 둘 다 비어 있습니다.
- `started_at`, `finished_at`, 그리고 백업이 실패한 이유나 OwnGit이 그대로 둔 것을 알려 주는 `message`
- `longest_hold_ms`, `longest_hold_repository`: [OwnGit이 실행 중일 때의 백업](#owngit이-실행-중일-때의-백업) 참고

두 명령은 OwnGit의 기록에 있는 내용만 보여 주고 백업 폴더는 읽지 않습니다. 그래서 폴더가 느리거나 없어도 기다리지 않습니다. OwnGit은 다음 백업을 만들 때 폴더를 확인합니다. 직접 지운 백업도 그때까지는 목록에 나옵니다.

백업이 끝날 때마다 서버 로그에 폴더와 결과가 한 줄씩 남습니다. OwnGit은 최근 백업 100개의 기록, 아직 남겨 둔 백업의 기록, 마지막 예약 백업의 기록을 보관합니다. 마지막 예약 백업의 시작 시각으로 다음 예약 백업 시각을 정하기 때문입니다.

### 새 백업 검사

검사를 켜 두면 OwnGit은 새 백업마다 [`owngit backup verify`](#백업-검사하기)와 같은 검사를 합니다. 시스템 임시 폴더에서 복원을 연습하므로 그 디스크에 저장소를 담을 공간이 있어야 합니다. 2시간 안에 끝나지 않은 검사는 그 이유로 실패합니다. 검사를 통과하지 못한 백업은 `failed`로 기록되고 OwnGit은 그 뒤에 이전 백업을 지우지 않습니다.

### 남아 있는 백업 다시 검사하기

OwnGit이 남겨 둔 백업은 언제든 다시 검사할 수 있습니다. 검사를 꺼 둔 채 만든 백업이 그런 예입니다. 백업 기록에서 다시 검사를 누르거나 다음 명령을 실행하세요.

```sh
owngit backup check --run ID
```

`ID`는 `owngit backup runs`가 보여 주는 32자짜리 `id`입니다. 새 백업을 검사할 때와 같은 검사이고 2시간 제한도 같습니다. 검사는 뒤에서 진행되며 명령은 `status`가 `running`인 검사를 출력하고 바로 끝납니다. 그 뒤로는 대시보드의 현재 상태와 `owngit backup status`의 `check`에 `running`, `passed`, `failed` 중 하나로 나오고, 실패하면 메시지도 함께 나옵니다. 이 결과는 OwnGit이 멈출 때까지 보입니다.

검사 결과는 백업 기록에도 남으므로 `owngit backup runs`의 `verification`도 바뀝니다. 검사에 실패한 백업은 이제 검사를 통과한 백업으로 치지 않으며 OwnGit도 이 백업을 검사를 통과한 가장 새 백업으로 남겨 두지 않습니다.

백업이 이미 폴더에서 없어졌거나 폴더에 다른 것이 들어 있으면 검사를 시작하지 않고 백업이 없어졌다고 답합니다. 검사하는 도중에 백업 폴더를 옮기거나 지우거나 다른 폴더로 바꿔 넣으면, 또는 매니페스트가 바뀌거나 읽을 수 없게 되면 그렇다는 메시지와 함께 검사가 실패합니다. 이때는 아무것도 기록하지 않으므로 그 백업의 `verification`은 전과 같습니다. 폴더와 매니페스트는 그대로인데 번들이 손상된 경우는 평소처럼 검사하므로, 검사가 실패하고 그 백업은 실패로 기록됩니다.

### OwnGit이 남기는 백업

백업은 대상 폴더 안에 `owngit-backup-YYYYMMDD-HHMMSS-XXXXXXXX`라는 새 폴더로 만들어지며, 시각은 시작 시각(UTC)입니다. 백업이 성공하면(검사를 켜 두었다면 검사까지 통과하면) OwnGit은 그 폴더에서 자기가 만든 백업 가운데 `--keep`만큼의 최신 백업과, 검증된 가장 새 백업을 남깁니다. 최신은 나중에 시작한 백업을 말하며 같은 초 안에서도 시작 순서를 따릅니다. 그보다 오래된 자기 백업은 지웁니다.

OwnGit은 백업마다 매니페스트의 SHA-256 해시를 함께 기록합니다. 폴더에 정확히 그 매니페스트가 있을 때만 자기 백업으로 보며, 이름만으로는 판단하지 않습니다. 대상 폴더의 다른 파일이나 폴더는 건드리지 않습니다. 다음 경우에는 폴더를 그대로 두고 새 백업의 메시지에 알려 줍니다.

- 직접 넣은 파일처럼 매니페스트와 매니페스트에 적힌 번들 말고 다른 것이 들어 있는 백업 폴더
- 링크인 폴더, 또는 복사하거나 바꿔 넣은 백업처럼 OwnGit이 거기에 쓴 것과 다른 백업이 든 폴더. OwnGit은 이제 이런 폴더를 자기 백업으로 세지 않습니다.
- 매니페스트를 읽을 수 없는 백업

프로세스가 강제로 종료되는 등의 이유로 OwnGit이 끝을 기록하지 못한 백업은 세지도 지우지도 않습니다. 실패했거나 중단된 백업 뒤에는 아무것도 지우지 않습니다. 대상 폴더를 바꾸면 이전 폴더의 백업은 그대로 둡니다.

### 백업 폴더의 여유 공간

새 백업을 다 쓴 뒤에 이전 백업을 지우므로, 폴더에는 백업 하나가 더 들어갈 공간이 있어야 합니다. OwnGit은 백업을 시작하기 전에 폴더의 여유 공간을 자기가 그 폴더에 마지막으로 만든 백업의 크기와 비교합니다. 크기는 지금도 있는 저장소만 셉니다. 공간이 모자라면 백업은 `not enough free space in DIR: a new backup needs about N MiB, the size of the last backup here, and M MiB is free` 같은 메시지와 함께 바로 실패합니다. 마지막 백업을 읽을 수 없으면 백업은 그대로 진행하고 여유 공간을 확인하지 않았다는 사실을 메시지에 남깁니다.

폴더에 첫 백업을 만들 때는 비교할 백업이 없습니다. 백업 도중 디스크가 가득 차면 `not enough free space in DIR`로 끝나고 완성되지 않은 백업이 그 이름으로 생기지 않습니다.

### OwnGit이 실행 중일 때의 백업

백업은 한 시점의 상태를 담습니다. 백업을 시작하면 OwnGit은 이미 진행 중인 푸시와 다른 Git 쓰기가 끝나기를 기다립니다. 그다음 모든 저장소의 ref를 읽는 동안만 새 쓰기를 멈춰 두고 백업을 쓰는 동안에는 쓰기를 다시 받습니다. Linux에서는 이렇게 멈추는 시간이 보통 1초보다 훨씬 짧고 Windows에서는 저장소 1,000개일 때 약 8초였습니다. `owngit backup runs`의 `longest_hold_ms`는 한 저장소의 Git 쓰기가 가장 오래 기다린 시간이고 `longest_hold_repository`는 그 저장소의 이름입니다.

- 그 시점 뒤에 들어온 푸시, 병합, 가져오기는 백업에 들어가지 않습니다.
- 저장소가 백업에 기록되는 동안에는 그 저장소를 삭제할 수 없고 유지 관리는 그동안 기다립니다.

**백업에는 ref나 HEAD에서 닿는 커밋만 들어갑니다.** ref는 커밋을 가리키는 이름입니다. 브랜치와 태그, 그리고 풀 리퀘스트, 가져오기, 보관된 기록에 쓰는 OwnGit 자체 ref가 여기에 속합니다. 백업은 저장소마다 모든 ref와 HEAD를 그것들이 닿는 커밋과 함께 담고 어디에서도 닿지 않는 커밋은 담지 않습니다. 보관된 기록을 켜 두면 덮어쓴 브랜치 끝 커밋이 보관된 기록에서 닿으므로 백업에도 들어갑니다. 꺼 두면 덮어쓴 커밋은 다른 ref가 여전히 가리키지 않는 한 나중에 만든 백업에 없습니다. 체크, 풀 리퀘스트, 가져오기 기록은 가리키는 커밋 ID를 그대로 가지고 있습니다. 복원한 뒤 저장소에 그 커밋이 없으면 기록에는 내용 없이 ID만 보입니다.

다음 경우에는 백업을 거부하고 해당 저장소 이름을 알려 줍니다.

- 저장소의 HEAD를 읽을 수 없을 때
- 저장소가 다른 저장소의 객체를 빌려 쓰거나(`objects/info/alternates`) 부분 클론일 때
- 가져오기가 저장소의 ref를 쓰는 Git 프로세스를 멈추지 못했을 때. 먼저 OwnGit을 다시 시작하세요.

### 백업 내려받기

백업 기록에서 내려받기를 누르면 OwnGit이 남겨 둔 백업을 `.tar` 파일 하나로 받습니다. 파일 이름은 `owngit-backup-YYYYMMDD-HHMMSS-XXXXXXXX.tar`처럼 백업 이름을 따르며 페이지는 그대로 있습니다. 명령줄에서는 다음처럼 받습니다.

```sh
owngit backup download --run ID --output /path/to/new-file.tar
```

파일에는 백업 폴더가 들어 있습니다. 폴더 안에는 `manifest.json`과 매니페스트에 적힌 저장소 번들만 있습니다.

**이 파일에는 모든 저장소, OwnGit 기록, 관리자 비밀번호와 공용 비밀번호의 해시가 들어 있습니다.** 백업 폴더처럼 남이 볼 수 없게 보관하세요.

- 다른 컴퓨터에서 확인하려면 `tar -xf FILE`로 풀고 풀려 나온 폴더를 [`owngit backup verify`](#백업-검사하기)로 검사하세요. 복원하는 방법은 [백업 파일로 복원하기](#백업-파일로-복원하기)에 있습니다.
- 크기 제한은 없습니다. 받는 쪽이 1분 동안 아무것도 받지 않으면 내려받기를 멈춥니다.
- 내려받는 도중에 백업의 매니페스트가 바뀌면 내려받기는 실패합니다.
- 내려받기가 시작된 뒤에 실패하면 OwnGit이 연결을 끊습니다. 그래서 브라우저나 명령이 실패로 알리며 잘린 파일이 온전한 파일로 보이는 일이 없습니다.
- 명령은 이미 있는 파일에는 쓰지 않으며 실패하면 받다 만 파일을 지웁니다. 성공하면 파일의 `path`와 크기(`bytes`)를 JSON으로 출력합니다.
- 내려받는 중인 백업은 지우지 않습니다. 지울 차례가 되더라도 다음 백업 때까지 남겨 두고 새 백업의 메시지에 그렇다고 적습니다.

### 백업 상태를 볼 수 있는 사람

대시보드의 백업 항목은 관리자로 확인한 브라우저에만 보입니다. `owngit backup schedule`, `now`, `status`, `runs`, `check`, `download`, `upload`에는 관리자 비밀번호가 필요합니다. 응답에 폴더와 저장소 이름, 오류 메시지가 나오고 내려받은 백업에는 모든 저장소와 비밀번호 해시가 들어 있기 때문입니다. `owngit backup --output`과 `owngit backup verify`에는 서버 비밀번호가 필요 없습니다. 이 컴퓨터에서 실행되며 상태 디렉터리나 백업에 접근할 수만 있으면 됩니다. 코딩 도구는 그 대신 [`backup_status` MCP 도구](CODING_TOOLS.ko.md#도구)로 요약을 받습니다. 요약은 일반 접근으로 읽으므로 공용 비밀번호가 필요하고 접근이 열려 있으면 비밀번호가 없어도 됩니다. 요약에는 예약 백업이 `not_configured`, `off`, `on` 중 어느 상태인지, 마지막 백업이 어떻게 언제 끝났는지, 검증된 가장 새 백업이 언제 끝났는지, 다음 백업이 언제인지가 들어 있습니다. 폴더, 저장소, 오류는 나오지 않습니다.

백업 명령은 관리자 API를 씁니다. 관리자 API는 사용자 이름 `admin`과 관리자 비밀번호로 HTTP Basic 인증을 받고 공용 비밀번호와 체크 에이전트 토큰, 러너 토큰은 거부합니다.

- `GET /api/v1/backups`: `owngit backup status`가 출력하는 상태
- `GET /api/v1/backups/runs`: 기록된 백업 목록. `POST`는 지금 백업합니다.
- `GET /api/v1/backups/schedule`: 예약 보기. `PATCH`는 예약을 바꿉니다.
- `POST /api/v1/backups/runs/ID/verify`: 남아 있는 백업 다시 검사
- `GET /api/v1/backups/runs/ID/archive`: 백업을 `.tar` 파일로 응답
- `PUT /api/v1/backups/upload`: 본문으로 `.tar` 파일을 받습니다. `Content-Length` 헤더가 있어야 합니다.

거부할 때는 `backup_running`, `backup_busy`, `backup_not_found`, `backup_gone`, `invalid_backup_archive`, `length_required` 같은 코드를 JSON으로 답합니다.

### 멈춘 OwnGit 백업하기

`owngit backup --output`은 실행 중이 아닌 OwnGit을 백업합니다. 그 상태 디렉터리로 OwnGit이 실행 중이면 거부하니 그때는 `owngit backup now`를 쓰세요. 출력 디렉터리는 아직 없어야 합니다.

```sh
owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

`--json`을 붙이면 `ok`, 백업 폴더 `backup`, SHA-256 해시가 무엇을 알려 주는지 적은 `note`를 출력합니다. 거부하면 `code`가 있는 JSON 오류를 출력합니다. 예를 들어 그 상태 디렉터리로 OwnGit이 실행 중이면 `offline_required`, 그 밖에 `state_missing`, `setup_incomplete`, `backup_failed`가 있습니다.

### 백업에 들어가는 것

백업에는 매니페스트 하나와 비어 있지 않은 저장소마다 Git 번들 하나가 들어갑니다. 둘을 합쳐 다음을 담습니다.

- 보관된 기록을 포함한 모든 ref, 저장소마다의 HEAD와 메타데이터
- 저장소마다 따로 고른 보관된 기록 선택, 기본 브랜치 보호, [다른 ref 이름공간](#다른-ref-이름공간)
- 풀 리퀘스트, 리뷰, 병합 기록
- 작업(task), 체크 설정과 결과, 자동 체크 정책과 작업(job)
- 가져오기 원본과 기록(원본마다의 추가 ref 네임스페이스와 새로고침 설정 포함)
- 접근 모드와 비밀번호 해시. 비밀번호 해시는 민감한 정보이므로 백업을 비공개로 보관하세요.

체크의 원본 로그, 모든 종류의 인증 정보와 토큰, 가져오기 예약, 가져오기 원본별 [연결 설정과 한도](#연결-설정과-한도), 백업 예약과 이전 백업의 기록, 동의, [업데이트 확인](#새-릴리스-알림) 설정, 그 밖의 서버 전체 설정(로그인 유지 시간, 새 저장소의 첫 브랜치, 보관된 기록 선택, Git 전송 한도, 보기 한도, 체크 상한, 저장소 유지 관리, 쓰지 않는 객체 정리, 체크 원본 로그 보관 기간)은 들어가지 않습니다. 로그인 세션, 네트워크 설정, 공유 링크, 최근 푸시 목록, 업그레이드 전 백업을 끄는 설정도 들어가지 않습니다. 복원이 빼놓는 것 전체와 다시 설정하는 곳은 [복원한 뒤에 할 일](#복원한-뒤에-할-일)에 있습니다.

백업에는 저장소를 빼고 OwnGit 기록을 1 GiB까지 담을 수 있습니다. 크기는 기록이 메모리에서 차지하는 양으로 세며 기록이 이보다 많으면 백업을 거부합니다. 백업을 만들고 복원할 때는 기록을 메모리에 올리므로 기록이 많을수록 메모리가 더 필요합니다.

### 백업 버전

OwnGit은 백업 버전 1, 2, 9, 10, 11을 복원하고 나머지는 거부합니다. 이전 빌드는 더 새로운 백업을 기록을 버리지 않고 거부합니다. 버전 11에만 담기는 기록이 없고 매니페스트가 버전 10이 허용하는 64 MiB를 넘지 않으면 백업은 버전 10으로 만들어지며, 버전 10은 OwnGit 1.0.3부터 1.1.2까지가 복원합니다. 버전 11이 필요한 기록에는 저장소마다 따로 고른 보관된 기록 선택, 기본 브랜치 보호, 다른 ref 이름공간, [이름을 바꾼](#저장소-이름-바꾸기) 저장소의 이름 기록과 가져오기 원본의 추가 ref 네임스페이스, 새로고침 설정, 체크 정책이나 체크 작업의 [컨테이너 설정](AUTOMATIC_CHECKS.ko.md#컨테이너-설정)(이름을 지정한 Docker 네트워크 포함)이 있습니다. 가져오기 기록에 브랜치와 태그 밖의 ref나 원본을 따라 한 삭제가 남아 있으면 그 설정을 끈 뒤에도 버전 11로 만들어집니다. 그래서 [업그레이드 전 백업](#업그레이드-전-백업)을 비롯해 새 기능을 쓰지 않는 설치의 백업은 이전 버전으로도 복원할 수 있습니다. 다만 체크 기록에 이전 빌드가 거부하는 시각 순서가 담긴 백업은 OwnGit 1.1.4 이상에서만 복원됩니다. 체크 작업이 대기열에 들어가기도 전에 실행을 맡은 것처럼 기록된 경우가 그 예이며, 컴퓨터 시계를 뒤로 맞춘 뒤에 생길 수 있습니다.

### 백업 복원하기

아직 없는 새 경로에 복원하세요. 새 경로는 덮어쓰지 않고 이름을 바꿀 수 있는 파일 시스템에 있어야 합니다([백업을 둘 수 있는 곳](#백업을-둘-수-있는-곳) 참고). 복원은 이미 있는 상태 디렉터리나 저장소 폴더를 거부하므로 실행 중인 OwnGit의 폴더에는 쓰지 않습니다. OwnGit이 실행 중이어도 복원할 수 있습니다. 지금 쓰는 설치를 바꾸려면 [복원 순서](#대시보드의-복원-안내)처럼 먼저 OwnGit을 멈추고 폴더 이름을 바꾸세요.

```sh
owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

복원은 새 상태를 게시하기 전에 모든 번들, ref, 객체, 기록을 검사하고 저장소마다 `git fsck`를 실행하며 실패한 저장소는 모두 알려 줍니다. SHA-256 해시로 손상은 찾아내지만, 누군가 매니페스트와 함께 바꿔치기한 백업은 알아내지 못합니다.

- 복원은 번들을 검사하면서 새 저장소 폴더에 복사하고 그 복사본으로 복원합니다. 그래서 시작하기 전에 저장소 폴더가 있는 디스크에 모든 번들과, 가장 큰 번들을 한 번 더 담을 공간이 있어야 합니다. 공간이 모자라면 복원은 필요한 크기를 알려 주고 멈춥니다.
- Ctrl+C를 누르면 복원이 멈추고 만든 것을 지운 뒤, 아무것도 복원하지 않았다고 알리며 종료 상태 130으로 끝납니다. 두 폴더가 이미 제자리에 있으면 대신 복원을 끝냅니다.
- Git 파일 이름은 정확히 그대로 유지됩니다. 그래서 백슬래시가 들어간 이름처럼 Git은 받아들이지만 Windows는 받아들이지 않는 이름은 그곳에서 체크아웃되지 않을 수 있습니다.
- `--json`을 붙이면 복원을 마친 뒤 `ok`, `state_dir`, `repository_root`, `--verify`를 주었을 때의 검사 결과 `verification`, 그리고 `notes`를 출력합니다. `notes`에는 복원하지 않은 것마다 한 문장, 기본값으로 시작하는 서버 전체 설정에 한 문장이 들어 있고, 각 문장은 다시 설정하는 방법을 알려 줍니다. `--json` 없이 실행하면 결과 뒤에 같은 내용을 한 줄에 하나씩 출력합니다. 거부하면 `code`가 있는 JSON 오류를 출력하고 종료 상태는 같습니다. 코드는 `backup_not_verified`(검사 결과가 `details`에 들어감), `restore_failed`, Ctrl+C로 멈췄을 때의 `interrupted`(종료 상태 130)입니다.

### 대시보드의 복원 안내

브라우저에서는 복원하지 않습니다. 백업 기록에서 OwnGit이 남겨 둔 백업마다 "이 백업으로 복원"이 있고, 펼치면 이 설치의 폴더 경로를 채운 복원 순서가 나옵니다.

1. OwnGit을 멈춥니다. 서비스로 실행 중이면 `owngit service stop`을 쓰고 아니면 `owngit serve` 프로세스를 끝냅니다. 예를 들어 그 터미널에서 Ctrl+C를 누르면 됩니다.
2. 지금 쓰는 상태 폴더와 저장소 폴더의 이름 끝에 `.before-restore`를 붙입니다. 복원은 이미 있는 폴더에는 쓰지 않습니다.
3. 화면에 나온 명령을 실행합니다. 복사 버튼으로 복사할 수 있습니다. 명령은 `owngit restore --input BACKUP --state-dir STATE --repository-root REPOSITORIES --verify` 형태라서 먼저 백업을 검사하고 통과한 경우에만 복원합니다. OwnGit이 Linux 시스템 서비스로 실행 중이면 명령 앞에 `sudo -u owngit`이 붙습니다. Windows에서는 PowerShell용 명령이며 화면에도 PowerShell이라고 표시됩니다. 명령 프롬프트(`cmd.exe`)가 아니라 PowerShell에서 실행하세요.
4. OwnGit을 다시 시작합니다. `owngit service start`를 쓰거나 전에 시작하던 방법으로 시작합니다.

복원이 끝나면 백업에 들어 있지 않아 되살리지 못한 것을 알려 줍니다. 자세한 내용은 [복원한 뒤에 할 일](#복원한-뒤에-할-일)에 있습니다. 이름을 바꾼 폴더는 직접 지울 때까지 남습니다.

### 백업 파일로 복원하기

OwnGit에서 [내려받은](#백업-내려받기) `.tar` 파일로 복원하려면 보관과 복구 탭의 백업 파일로 복원에서 파일을 올리거나 다음 명령을 실행하세요.

```sh
owngit backup upload --input /path/to/backup.tar
```

OwnGit은 올린 백업을 새 백업과 같은 방법으로 검사합니다. 검사를 통과하면 페이지에 같은 [복원 순서](#대시보드의-복원-안내)가 나오고 `owngit backup status`의 `upload_restore_command`에도 복원 명령이 나옵니다. 이 명령의 `--input`은 2단계에서 상태 폴더 이름을 바꾼 뒤의 경로를 이미 가리킵니다. 명령을 실행하기 전에는 아무것도 바뀌지 않습니다.

올린 백업은 상태 폴더 안의 `backup-uploads` 폴더에 하나만 보관합니다.

- 새로 올린 파일은 OwnGit이 여유 공간을 확인한 뒤부터 이전 것을 대신합니다. 여유 공간이 모자라거나 다른 백업, 검사, 올리기가 진행 중이어서 그 전에 거부되면 이전에 올린 백업은 그대로 남습니다. 여유 공간 확인을 통과한 뒤에는 이전 것을 먼저 지우므로 그다음에 거부되거나 검사에 실패하면 올린 백업이 하나도 남지 않습니다.
- 올린 지 24시간이 지나면 지웁니다. OwnGit이 시작할 때마다 지우며 검사에 실패하면 바로 지웁니다. 그러니 OwnGit을 다시 시작하기 전에 복원하세요. 언제 지우는지는 페이지에 나옵니다.
- `owngit backup upload`는 `status`가 `verifying`인 올리기를 출력합니다. 그 뒤로는 `owngit backup status`의 `upload`에 `status`(`verifying`, `passed`, `failed`), `removes_at`, 실패했을 때의 `message`가 나옵니다.

다음 경우에는 올리기를 거부하고 아무것도 남기지 않습니다.

- 상태 폴더가 있는 디스크의 여유 공간이 파일 크기에 1 GiB를 더한 것보다 적을 때. 파일을 읽기 전에 확인합니다.
- 올리기가 크기를 알리지 않을 때. 브라우저와 `owngit backup upload`는 늘 크기를 알립니다.
- 1분 동안 데이터가 하나도 오지 않을 때
- 파일에 `manifest.json`과 `repositories/*.bundle`이 든 최상위 폴더 하나 말고 다른 것이 있을 때. 절대 경로, `..`, 링크, 다른 파일이 그런 예입니다.
- 최상위 폴더 이름에 ASCII 영문자, 숫자, `.`, `_`, `-` 말고 다른 문자가 있을 때. OwnGit에서 내려받은 파일은 언제나 이 규칙에 맞습니다.

브라우저가 파일을 다 보내기 전에 OwnGit이 거부하면(여유 공간이 모자랄 때처럼) 일부 브라우저는 거부 이유 대신 연결 오류를 보여 줍니다. `owngit backup upload`는 이유를 출력합니다.

### 복원한 뒤에 할 일

복원한 뒤에는 다른 방식으로 쓰기 전에 먼저 복원한 상태로 `owngit serve`를 시작하세요. 시작 과정이 중단된 기록을 정리합니다. 그다음 백업에 들어가지 않는 것을 다시 설정합니다. `owngit restore`가 끝날 때 아래 항목과 다시 설정하는 곳을 하나씩 알려 줍니다.

- 로그인 세션과 설정 링크: 다시 로그인하세요.
- 네트워크 설정(수신 주소, 기본 URL, 허용한 Host, 신뢰하는 프록시, 공유 링크용 공개 주소, Tailscale Serve, 일반 HTTP 사용 확인): 설정 화면이나 `owngit network set`, `owngit tailscale on`으로 다시 설정하세요.
- 체크 에이전트 토큰과 러너 토큰: 예전 토큰은 거부됩니다. 저장소마다 체크 에이전트 토큰 화면과 러너 토큰 화면에서, 또는 `owngit helper-credential create`와 `owngit runner-credential issue`로 새로 만드세요.
- 자동 체크 실행 동의: 저장소마다 자동 체크 탭이나 `owngit check-policy enable`로 체크를 다시 켜세요. 끝나지 않은 작업은 `interrupted`로 표시됩니다.
- 가져오기 인증 정보, 원본마다 정한 [연결 설정과 한도](#연결-설정과-한도), 가져오기 예약: 가져오기 탭에서, 또는 `owngit import credentials`, `owngit import configure`, `owngit import schedule`로 인증 정보를 다시 저장하고 원본에 필요한 설정을 다시 켜세요.
- 공유 링크: 저장소마다 설정 탭의 공유 링크에서, 또는 `owngit repo share create`로 새로 만드세요.
- 예약 백업과 백업 기록: 설정 화면이나 `owngit backup schedule set`으로 예약을 다시 설정하세요. 예전에 예약이나 지금 백업으로 만든 백업은 백업 기록에 더 이상 나오지 않지만, 그 폴더는 만든 곳에 그대로 있고 `owngit backup verify`와 `owngit restore`로 계속 쓸 수 있습니다.
- [업그레이드 전 백업](#업그레이드-전-백업)은 새로 설치했을 때처럼 켜져 있습니다. 꺼 두었다면 `owngit upgrade-backup off`를 다시 실행하세요.
- 체크 원본 로그와 최근 푸시 목록은 없고, 정리되지 않은 가져오기 게시는 적용하지 않고 닫습니다.
- 원본의 삭제를 따르는 가져오기는 새로고침이 ref를 다시 본 뒤에야 그 ref를 지웁니다([로그인 정보를 바꾸거나 복원한 뒤](#로그인-정보를-바꾸거나-복원한-뒤)).
- 서버 전체 설정은 백업한 설치에서 더 엄격하게 정했든 느슨하게 정했든 새로 설치했을 때처럼 기본값으로 시작합니다. 로그인은 12시간 유지되고 새 저장소는 `main`에서 시작합니다. 서버 설정을 따르는 저장소는 덮어쓰거나 지운 기록을 보관합니다. Git 전송 하나는 4 GB와 30분까지이고 체크 원본 로그는 30일 동안 보관합니다. 저장소를 삭제할 때는 이름을 묻습니다. 10분 안에 비밀번호를 4번 틀린 주소는 15분 동안 멈추고 다른 사이트의 링크는 공용 로그인 없이 열립니다. 전송 자리와 대기 시간, 보기 한도, 체크 상한, 저장소 유지 관리, 관리자 비밀번호 확인, 새 릴리스 확인도 기본값으로 돌아가고 쓰지 않는 객체 정리는 꺼집니다. 저장소 설정 탭에서 저장소마다 고른 설정은 저장소와 함께 돌아옵니다. 서버 전체 설정은 설정 화면이나 `owngit settings set`으로 다시 정하세요.

### 백업이나 복원이 중단되면

전원이 나가거나 프로세스가 강제로 종료되어 복원이 정리하지 못하고 멈췄다면 이렇게 하세요.

1. 두 대상 어디에서도 OwnGit을 시작하지 말고, `.owngit-restore-pending` 표시 파일도 지우지 마세요.
2. 두 대상과 `TARGET.owngit-restore-...` 형제 항목을 격리 위치로 옮기세요.
3. 새 경로에 다시 복원하세요.

끝나기 전에 멈춘 백업은 디스크에 무엇이 남았든 완성된 백업이 아닙니다. 실행 중인 백업 프로세스가 없을 때 출력 위치 옆을 확인하세요. `OUTPUT`은 출력 이름이고 예약 백업과 지금 백업하기에서는 대상 폴더 안의 백업 폴더 이름입니다.

- 숨은 형제 항목 `.OUTPUT.owngit-backup-...`에는 끝나지 않은 백업이 들어 있습니다. 보관하거나 격리하세요.
- [덮어쓰지 않고는 이름을 바꾸지 못하는 파일 시스템](#백업을-둘-수-있는-곳)에서는 매니페스트 없는 폴더가 최종 이름으로 남을 수 있습니다. 백업이 아니니 지우거나 격리하세요.
- 검사 도중처럼 폴더가 다 만들어진 뒤에 멈췄다면 완성된 폴더가 남습니다. 이 백업은 계속 `interrupted`로 기록되고 검증되지 않은 상태이므로, 믿고 쓰기 전에 `owngit backup verify`로 검사하세요.

예약 백업이나 지금 백업하기 도중에 OwnGit이 멈추면 그 백업은 `interrupted`로 기록됩니다. 프로세스가 강제로 종료됐다면 OwnGit이 다음에 시작할 때 `interrupted`로 기록하고 서버 로그에도 남깁니다.

### 백업을 둘 수 있는 곳

백업과 복원은 새 폴더를 최종 위치 옆에 임시 이름으로 만든 뒤 이름을 바꿉니다. 다만 덮어쓰지 않고는 이름을 바꾸지 못하는 파일 시스템(이 절 끝에서 설명합니다)에서는 백업을 새 폴더 안으로 옮깁니다. 그래서 새 폴더가 들어갈 폴더는 OwnGit이 넣은 것을 다른 계정이 이름을 바꾸거나 지울 수 없는 곳이어야 합니다.

- macOS와 Linux에서는 상태 디렉터리로 가는 길과 마찬가지로 그 폴더와 가는 길의 모든 폴더를 다른 계정이 바꿀 수 없어야 합니다. `/tmp`처럼 스티키 비트가 있는 폴더는 허용합니다. 그렇지 않으면 OwnGit은 아무것도 만들기 전에 멈추고 해당 폴더를 알려 주며, 고칠 수 있는 `chmod` 명령이 있으면 그 명령도 보여 줍니다. 이 계정만 바꿀 수 있는 폴더를 골라도 됩니다.
- 가는 길에 없는 폴더는 이 계정만 쓸 수 있게 새로 만듭니다. 다만 다른 계정이 이름을 만들 수 없는 폴더 안에서만 만들므로 `/tmp` 바로 아래에는 만들지 않습니다.
- Windows에서는 가는 길의 링크나 정션, 볼륨을 마운트한 폴더를 따라가지 않습니다. 작업하는 동안에는 가는 길의 폴더 이름을 바꿀 수 없게 막습니다.
- 백업과 복원한 저장소 폴더는 네트워크 공유에 두어도 됩니다. 다만 root로, 또는 Windows에서 관리자 권한으로 실행할 때는 공유를 쓸 수 없습니다. 복원하려면 파일 시스템이 아래에서 설명하는 조건을 갖춰야 합니다. 복원한 상태 디렉터리는 다른 상태 디렉터리처럼 로컬 디스크에 있어야 합니다.

다음 두 종류의 파일 시스템은 주의해야 합니다.

- macOS의 exFAT, Linux의 NFS와 일부 네트워크 공유처럼 새 이름에 있는 것을 덮어쓰지 않고는 폴더 이름을 바꾸지 못하는 파일 시스템
- macOS의 FAT16, FAT32 볼륨처럼 macOS가 파일의 속성을 `._`에 파일 이름을 붙인 별도 파일에 보관하는 파일 시스템

두 종류 모두 백업할 수 있습니다. 덮어쓰지 않고는 이름을 바꾸지 못하는 파일 시스템에서는 OwnGit이 백업 폴더를 먼저 만들고(그 이름에 무엇이든 있으면 실패합니다) 완성된 백업을 그 안으로 옮깁니다. 옮긴 것은 하나씩 디스크에 기록하고 매니페스트는 마지막에 옮깁니다. 도중에 멈춰 매니페스트 없이 남은 폴더는 백업이 아니며 OwnGit은 이를 세지도 지우지도 않습니다. 오래된 백업을 지울 때는 macOS가 백업 파일 옆에 둔 `._` 파일도 함께 지웁니다. 이름만 `._` 파일처럼 생긴 것을 포함해 백업 폴더에 다른 파일이 있으면 그 백업은 지우지 않고 둡니다. `owngit backup verify`는 시스템 임시 폴더에서 연습하므로 두 종류의 디스크에 둔 백업을 모두 검사할 수 있습니다.

저장소 복원은 두 종류 모두 할 수 없습니다. macOS와 Linux에서 `owngit restore`는 작업을 시작하기 전에 대상 폴더를 확인합니다. 저장소 폴더의 파일 시스템이 덮어쓰지 않고는 이름을 바꾸지 못하거나 macOS가 그곳에서 속성을 `._` 파일에 보관하면 아무것도 바꾸지 않고 멈추며 파일 시스템 이름(예: `exfat`, `msdos`)을 알려 줍니다. `._` 파일은 Git이 복원한 저장소의 일부로 읽기 때문입니다. 저장소는 다른 디스크의 폴더에 복원하세요.

백업 폴더가 이렇게 저장소 복원이 멈추는 디스크에 있으면 대시보드의 현재 상태가 파일 시스템 이름과 함께 알려 주며 `owngit backup status`에는 `restore_limit`으로 나옵니다.

복원한 상태 디렉터리에는 덮어쓰지 않고 이름을 바꾸는 기능만 있으면 됩니다. 그래서 저장소를 다른 디스크에 복원한다면, 상태 디렉터리는 macOS의 FAT16이나 FAT32 볼륨에 두어도 됩니다. exFAT에는 둘 수 없습니다.

macOS에서 디스크 이미지로 exFAT, FAT16, FAT32 볼륨에 백업하고 그 백업을 검사하고 지우는 것, 그리고 복원을 거부하는 것을 시험했습니다. NFS는 시험하지 않았습니다.

### 백업 검사하기

`owngit backup verify`는 백업이 제대로 복원되는지 확인합니다. 백업과 지금 쓰는 상태는 건드리지 않으므로 OwnGit을 멈추지 않아도 됩니다.

```sh
owngit backup verify /path/to/backup
```

모든 검사를 통과하고 연습 폴더까지 지웠을 때만 백업이 검증된(`verified`) 것으로 봅니다. 백업이 검사를 통과하지 못하면 종료 상태는 1이고 도중에 멈추면 130입니다. `owngit restore --verify`는 같은 연습을 먼저 하고 검사를 통과한 백업만 복원합니다. 그래서 손상된 백업은 대상 경로에 아무것도 만들기 전에 거부됩니다.

이 명령은 시스템 임시 폴더 안에 이 계정만 쓸 수 있는 새 폴더를 만들고, `owngit restore`가 하는 검사를 모두 거쳐 백업을 그곳에 복원한 뒤 폴더를 지웁니다. Ctrl+C로 멈춰도 폴더를 지웁니다.

- 저장소마다 번들이 SHA-256 해시와 맞는지, Git이 백업에 기록된 ref와 HEAD 그대로 복원하는지, 그 ref에서 닿는 객체를 `git fsck`가 모두 찾는지 확인합니다.
- 복원한 데이터베이스가 이 버전이 만드는 스키마인지, SQLite의 무결성 검사와 외래 키 검사를 통과하는지 확인합니다.
- 복원이 받아들이는 백업 버전은 모두 같은 검사를 받습니다.
- 이 명령은 백업만 읽으므로 OwnGit 상태에 접근할 필요가 없고 명령을 실행한 계정으로 동작합니다.

결과에는 저장소마다 통과, 실패와 그 이유, 검사가 그 전에 멈춰 검사하지 못함 중 하나가 나옵니다. 그다음에 데이터베이스 결과와 이 검사로 알 수 없는 것이 나옵니다. SHA-256 해시로 손상은 찾아내지만, 누군가 매니페스트와 함께 바꿔치기한 백업은 알아내지 못합니다.

복원 연습에는 복원할 때처럼 저장소를 담을 공간이 필요합니다. 임시 폴더가 있는 디스크의 공간이 모자라면 시작하기 전에 멈추고 필요한 크기를 알려 줍니다. 일부 Linux 시스템은 임시 폴더를 메모리에 둡니다. `--temp-dir DIR`을 주면 다른 폴더에서 연습합니다. 연습 도중 공간이 떨어지면 공간 부족이라고 알려 줄 뿐, 백업이 손상됐다고 하지 않습니다.

`--json`을 붙이면 결과를 JSON으로 출력합니다. 필드는 `backup`, `verified`, `error`, `version`, `created_at`, `repositories`(저장소마다 `id`, `passed`, `failed`, `not_run` 중 하나인 `status`, `refs`, `error`), `database`, `limits`, `cleanup_error`(연습 폴더를 지우지 못함), `leftovers`입니다.

연습 폴더는 전원이 나갔을 때처럼 OwnGit이 지우지 못했을 때만 남습니다. 다음 검사는 같은 임시 폴더에 남은 이 계정의 `owngit-verify-...` 폴더를 `leftovers`에 적어 줍니다. 실행 중인 검사가 없을 때 직접 지우세요.

### 업그레이드 전 백업

새 버전의 OwnGit이 자기가 쓰는 것보다 오래된 스키마의 상태로 시작하면, 먼저 지금 상태 그대로 오프라인 백업을 만듭니다. 상태는 그 백업이 끝난 뒤에만 업그레이드합니다. `owngit backup --output`도 자기 백업을 만들기 전에 똑같이 합니다. 실행 중인 서버 옆에서도 쓰는 다른 명령은 오래된 상태를 건드리지 않고, 새 버전의 OwnGit을 한 번 시작(또는 다시 시작)하거나 먼저 `owngit backup --output`을 실행하라고 알려 줍니다.

- 백업은 `pre-1.1.3-20260929T101500Z` 같은 새 폴더로 생깁니다. 상태 디렉터리 옆에 그 이름 뒤에 `-backups`를 붙인 폴더 안에 들어갑니다. 예를 들면 `~/.config/owngit` 옆의 `~/.config/owngit-backups`입니다.
- 모든 저장소가 들어가므로 그 디스크에 저장소만큼의 여유 공간이 있어야 합니다.
- 백업이 어디 있는지와 복원 명령은 서버 로그에(`owngit backup`이면 표준 오류에) 나오고, 백업 안의 `owngit-upgrade-backup.txt`에도 같은 내용이 있습니다.
- 새 상태와 설정을 마치지 않은 상태는 백업하지 않습니다.
- 이전 버전에서 거부되었거나 도중에 멈춘 병합이 남아 있는 상태도 다른 상태와 똑같이 백업합니다.
- 이 백업은 OwnGit 1.0.3 이상에서 복원할 수 있지만 예외가 하나 있습니다. 이전 버전에서 병합이 거부되었거나 도중에 멈춘 뒤 풀 리퀘스트가 바뀌었고, 바뀐 커밋으로 병합되었다면 백업에 그 병합과 맞지 않는 이전 시도의 기록이 남아 있을 수 있습니다. 이전 빌드는 이런 백업을 거부하고 애초에 이런 상태를 백업하지도 못했으므로, 이 백업은 OwnGit 1.1.4 이상에서만 복원됩니다. 바꾸지 않고 다시 시도해서 성공한 병합은 이런 기록을 남기지 않습니다.

OwnGit은 `-backups` 폴더를 이 계정만 쓸 수 있게 만듭니다. 폴더가 이미 있다면 이 계정의 것이어야 하고, 다른 계정이 그 안의 내용을 바꿀 수 없어야 합니다. 그렇지 않으면 OwnGit은 업그레이드를 거부하고 이유를 알려 줍니다.

이전 버전으로 돌아가려면 OwnGit을 멈추고 상태 디렉터리를 다른 곳으로 옮긴 뒤, 출력된 명령을 이전 버전으로 실행하세요. 이전 버전이 위의 이유로 백업을 거부하면 이 설치는 이전 버전으로 돌아갈 수 없습니다. 옮겨 둔 원래 상태 디렉터리를 제자리로 돌려놓고 1.1.4 이상을 계속 쓰세요. 예를 들면 다음과 같습니다.

```sh
owngit restore \
  --input ~/.config/owngit-backups/pre-1.1.3-20260929T101500Z \
  --state-dir ~/.config/owngit \
  --repository-root ~/.config/owngit-backups/pre-1.1.3-20260929T101500Z-repositories
```

그다음 이전 버전을 시작하세요. 복원된 저장소는 업그레이드할 때의 모습으로 백업 옆의 새 폴더에 있습니다. 이 계정이 만들 수 있는 곳이라면 다른 새 폴더를 지정해도 됩니다. 이전 버전이 그곳의 복원된 저장소를 쓰는 동안에는 `-backups` 폴더를 지우지 마세요.

이전 버전이 1.0.3이면 OwnGit 1.1.0 이상이 쓴 체크 에이전트 토큰 파일과 러너 토큰 파일을 고쳐야 합니다. 이 파일들은 `owngit-server:` 줄로 시작하는데, 1.0.3은 이 줄까지 토큰으로 읽어 연결에 실패합니다. 파일마다 첫 줄을 지우거나 1.0.3으로 파일을 다시 만드세요.

#### 업그레이드 전 백업이 실패하면

디스크가 꽉 찼거나, 폴더를 만들 수 없거나, 저장소 폴더를 쓸 수 없어서 백업을 만들지 못하면 OwnGit은 상태를 업그레이드하지 않고 이유를 알리며 멈춥니다. 이때 상태는 이전 버전에서 그대로 쓸 수 있습니다. 원인을 해결하고 OwnGit을 다시 시작하세요.

백업을 만드는 도중에 OwnGit이 멈추면 상태는 업그레이드되지 않습니다. 이때 남은 숨은 폴더 `.owngit-upgrade-copy-...`와 `.pre-...owngit-backup-...`는 실행 중인 OwnGit이 없을 때 지우세요.

#### 업그레이드 전 백업을 두는 곳

새 백업이 끝나면 OwnGit은 그 폴더에서 같은 상태 디렉터리를 업그레이드하기 전에 자기가 만든 이전 백업을 지웁니다. 어느 백업이 그런 것인지는 `owngit-upgrade-backup.txt`에 적혀 있으며 폴더의 다른 항목은 건드리지 않습니다.

이 백업을 다른 로컬 디스크에 두려면 macOS와 Linux에서는 `-backups` 폴더를 그 디스크에서 이 계정만 바꿀 수 있는 폴더를 가리키는 링크로 만드세요. Windows는 경로 중간의 링크를 따라가지 않으므로 상태 디렉터리를 그 디스크에 두세요.

#### 업그레이드 전 백업 끄기

다른 방법으로 백업하고 있어서 백업 없이 업그레이드하려면 이 백업을 끄세요.

```sh
owngit upgrade-backup off
```

그러면 OwnGit은 백업 없이 업그레이드하고 그럴 때마다 경고를 로그에 남깁니다. `owngit upgrade-backup on`은 다시 켜고 `owngit upgrade-backup`은 현재 설정을 보여 줍니다(`--json`이면 JSON). 이 명령은 새 버전이 상태를 업그레이드하기 전에도 쓸 수 있으므로, 백업에 실패했을 때 백업을 끄고 다시 시작할 수 있습니다. 이 설정은 이 컴퓨터의 상태 디렉터리에만 적용되고 백업에는 들어가지 않습니다. 백업을 꺼 두었다면 새 버전을 설치하기 전에 OwnGit을 멈추고 지금 버전의 `owngit backup`으로 백업하세요.

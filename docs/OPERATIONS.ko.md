# 운영 안내

<p align="center"><a href="OPERATIONS.md">English</a> | <b>한국어</b></p>

## 처음 설정하기

소스 체크아웃에서 실행합니다.

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve
```

기본 주소는 `http://127.0.0.1:7654`입니다. 설정 과정에서 저장소 저장 위치, 일반 접근을 공용 비밀번호로 보호할지 여부(선택 사항), 별도의 관리자 비밀번호를 정합니다. 이후 보안 설정을 바꿀 때마다 현재 관리자 비밀번호를 묻습니다. 설정을 마치면 빈 대시보드가 나옵니다. 대시보드의 **새 저장소**에서 저장소를 만들 수 있습니다. clone 주소는 `http://HOST:7654/git/PROJECT.git` 형식입니다.

### 터미널에서 설치하기

아직 설정되지 않은 설치를 `owngit serve`로 시작할 때 입력과 출력이 모두 터미널이고 그 터미널의 포그라운드에서 실행 중이면, 그 터미널에서 설치를 진행합니다. 먼저 언어(English 또는 한국어)를 묻습니다. Enter만 누르면 로캘의 언어를 쓰고, 나중에 L 키로 바꿀 수 있습니다. 이어서 "이 터미널에서 계속"과 "웹 대시보드 열기" 중 하나를 고릅니다. 질문에 답하는 동안 나온 서버 로그는 모아 두었다가 질문 사이에 보여 줍니다. 이 방식은 설정 파일을 만들지 않습니다.

터미널에서는 웹 설정 화면과 같은 내용을 같은 순서로 묻습니다. 저장소 폴더, 저장소를 읽고 쓸 수 있는 사람, 관리자 비밀번호, "다른 기기에서 접속"을 차례로 묻고, OwnGit이 네트워크 주소에서 연결을 받는다면 암호화되지 않는 연결로 계속할지도 묻습니다. 비밀번호는 두 번 입력하며 입력하는 동안 화면에 아무것도 나타나지 않습니다. 답에는 웹 페이지가 받는 문자가 모두 그대로 들어갑니다. 붙여 넣은 줄바꿈 없는 공백이나 이모지도 마찬가지이며, Enter, Backspace, Ctrl-U, Ctrl-C, Escape만 키로 동작합니다. 확인 카드에서 "설치 완료"를 고르기 전에는 아무것도 저장하지 않습니다. 질문에 답하는 동안 Ctrl-Z와 Ctrl-D는 키로 동작하지 않고, 다른 제어 문자처럼 답의 일부가 됩니다. 설치 중에 OwnGit이 멈췄다가 백그라운드에서 다시 실행되면(예: `kill -STOP` 뒤 `bg`) 서버는 계속 동작하지만 질문은 멈춥니다. `fg`를 실행하면 질문이 이어집니다. 설치를 마친 뒤에는 OwnGit이 터미널을 더 읽지 않으므로 Ctrl-Z와 `bg`를 써도 서버가 계속 실행됩니다. Ctrl-C를 누르면 아무것도 저장하지 않고 서버가 멈춥니다. 다시 하려면 `owngit serve`를 다시 실행하세요. OwnGit이 이 컴퓨터에서만 연결을 받으면 터미널에서는 일반 HTTP 확인을 묻지 않습니다. 나중에 다른 기기에서 OwnGit에 접속한다면 설정 화면에서 확인해 주세요. 확인하기 전까지는 다른 설치와 마찬가지로 설정 화면이 이 확인을 계속 묻습니다. 일반 HTTP 질문에 아니요라고 답하면 OwnGit을 이 컴퓨터에서만 쓰는 방법을 알려 줍니다. `--listen 127.0.0.1:PORT` 옵션으로 다시 시작하면 됩니다. 다만 네트워크 주소가 저장된 [네트워크 설정](#네트워크-설정)에서 왔다면, 저장된 주소는 매번 시작할 때 적용되므로 OwnGit을 멈추고 `owngit network set --listen 127.0.0.1:PORT --base-url ""`를 실행한 뒤 다시 시작해야 합니다.

"다른 기기에서 접속" 단계는 이 컴퓨터에서 Tailscale이 실행 중인지 확인합니다. `tailscale` 명령은 `owngit serve --tailscale PATH`를 포함해 [tailnet 공유](#tailnet에서-https로-공유하기)와 같은 방식으로 찾습니다. `tailscale status --json`만 실행하며 아무것도 바꾸지 않습니다. Tailscale이 실행 중이면 이 컴퓨터의 Tailscale 주소와 MagicDNS 이름을 보여 주고, 이 값을 [네트워크 설정](#네트워크-설정)으로 저장하는 명령을 한 줄에 따로 출력합니다. 예를 들면 `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654`입니다. 이 명령을 실행하고 설치를 마친 뒤 OwnGit을 다시 시작하세요. 저장된 설정은 백그라운드 서비스를 포함해 이후 모든 시작에 적용됩니다. 기본 위치가 아닌 상태 디렉터리를 쓴다면 명령에 `--state-dir`이 들어갑니다. 경로에 탭 같은 제어 문자나 방향 제어 문자가 있으면 ANSI-C 따옴표(`$'...'`)로 적습니다. zsh, bash, ksh는 이를 읽지만 `dash` 같은 순수 POSIX 셸은 읽지 못합니다. 설치 과정에서 이 명령을 실행하거나 저장하지는 않습니다. Tailscale이 기기 사이의 연결을 암호화하더라도 OwnGit은 그 보호를 확인할 수 없어 계속 일반 HTTP로 표시합니다. OwnGit이 암호화된 연결로 표시하는 HTTPS 주소를 쓰려면 대신 [tailnet에서 HTTPS로 공유](#tailnet에서-https로-공유하기)하세요. Enter를 누르면 다음으로 넘어갑니다. OwnGit을 서비스로 실행한다면 서비스 정의에서 `--listen`과 `--base-url`을 빼 두세요. [백그라운드 서비스의 옵션](#백그라운드-서비스의-옵션)을 보세요.

### 브라우저에서 설치하고 터미널에서 승인하기

"웹 대시보드 열기"를 고르면 브라우저에서 `http://127.0.0.1:7654/setup`을 엽니다. `--no-open`을 붙였다면 터미널에 주소만 출력합니다. 이 주소에는 비밀값이 없습니다. 브라우저에서 "터미널에 승인 요청"을 누르면 페이지에 짧은 코드가 나오고, 터미널에는 "브라우저가 OwnGit 설치를 요청했습니다"라는 카드와 함께 같은 코드와 요청을 보낸 주소가 나옵니다. 내 브라우저에 그 코드가 보일 때만 `y`로 답하세요. 승인은 그 브라우저 하나에만 적용되며, 그 브라우저가 웹 페이지에서 설치를 이어 갑니다.

- 다른 기기에서 온 요청은 터미널에 경고와 함께 표시합니다.
- 승인을 기다릴 수 있는 브라우저는 한 번에 하나입니다. 다른 브라우저에는 나중에 다시 시도하라고 안내합니다.
- 거절한 브라우저는 1분이 지나야 다시 요청할 수 있고, 한 주소에서는 10분에 다섯 번까지만 요청할 수 있습니다.
- 답하지 않은 요청과 브라우저가 쓰지 않은 승인은 10분 뒤에 만료됩니다.
- 기다리는 동안 T를 누르면 터미널에서 설치합니다. 이미 승인한 브라우저의 설치 세션은 이때 끝납니다.
- 브라우저를 승인한 뒤에도 새 요청은 터미널에 나타납니다. 예를 들어 다른 브라우저로 다시 요청할 수 있습니다. 새 요청을 승인하면 먼저 승인한 브라우저의 설치 세션은 끝나며, 요청 카드에도 이 점을 표시합니다.

브라우저에서 설치를 마치면 터미널에 저장된 답을 보여 주고 서버는 계속 실행됩니다.

### 설정 파일로 설치하기

`brew services`, LaunchAgent, systemd처럼 터미널 없이 시작하거나, 출력을 다른 곳으로 돌렸거나, 셸의 백그라운드 작업(`owngit serve &`)으로 시작했다면, OwnGit은 상태 디렉터리 안에 소유자만 읽을 수 있는 설정 파일을 만들고 설치한 소유자의 브라우저에서 엽니다. `--no-open`을 붙였거나 브라우저를 열 수 없으면 서버 로그에 파일 경로가 나옵니다. 설정용 비밀값은 화면에 출력되거나 브라우저 실행 인수로 전달되지 않습니다. `owngit setup-link`로 새 파일을 발급할 수 있으며, 터미널에서 설치를 기다리는 중에도 쓸 수 있습니다.

설치를 마치기 전에는 OwnGit을 시작할 때 알려 주지 않은 주소로도 다른 기기에서 설정 링크를 열 수 있습니다. 예를 들어 OwnGit이 모든 네트워크 인터페이스에서 연결을 받을 때 이 컴퓨터의 LAN 주소로 열 수 있습니다. `owngit setup-link --base-url http://192.168.1.20:7654`는 그 주소용 설정 파일을 만듭니다. 링크를 쓰기 전까지 그 주소는 링크를 쓰는 페이지만 보여 주고 다른 요청은 모두 거부합니다. 링크를 쓴 뒤에는 그 주소의 그 브라우저만 설치를 이어 갈 수 있습니다. 설치 화면은 이 주소를 계속 받아들일지 묻고, 선택하지 않으면 설치를 마친 뒤 그 주소를 거부합니다.

## 다른 기기에서 서버에 접속하기

OwnGit은 일반 HTTP로 동작하므로 연결이 암호화되지 않으며, TLS를 내장하지 않습니다. HTTPS로 접속하려면 이 컴퓨터의 Tailscale로 tailnet에 공유하거나([tailnet에서 HTTPS로 공유하기](#tailnet에서-https로-공유하기) 참고) OwnGit 앞에 리버스 프록시를 두세요([리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기) 참고). 비공개 네트워크 주소로 접속하려면 Tailscale이나 직접 운영하는 VPN을 쓰세요. 이름이 Tailscale과 관련되어 보인다는 것만으로 전체 경로가 보호된다고 볼 수는 없습니다. 일반 LAN에서 HTTP로 접속해도 됩니다. 이때 OwnGit은 비밀번호를 받기 전에 한 번 경고를 보여 주고, 화면에 연결 상태를 계속 표시합니다. OwnGit을 공개 인터넷에 노출하지 마세요.

LAN 이름을 쓰려면 다음과 같이 실행합니다.

```sh
./bin/owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

서버는 Host가 `localhost`, `127.0.0.1`, `::1`이거나 승인된 이름인 요청만 받습니다. `--allowed-host`는 여러 번 지정할 수 있습니다. 다른 이름을 영구히 승인하려면 설치 호스트에서 다음 명령을 실행하고 서버를 다시 시작하세요.

```sh
./bin/owngit approve-host gitbox.internal
```

### 네트워크 설정

OwnGit은 연결 주소, 기본 URL, 허용한 Host 이름, 신뢰하는 리버스 프록시를 저장할 수 있습니다. 백그라운드 서비스처럼 옵션 없이 시작한 서버도 시작할 때마다 저장된 값을 씁니다. 다음 명령은 설치 호스트에서 실행합니다. 서버가 실행 중이든 아니든 동작하며, 바꾼 값은 다음 시작부터 적용됩니다.

```sh
./bin/owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal
./bin/owngit network show
```

- `--listen`은 `host:port` 형식입니다. host를 비우거나 `0.0.0.0`, `::`로 쓰면 모든 네트워크 인터페이스에서 연결을 받습니다.
- `--base-url`은 다른 기기가 쓰는 주소로, 경로 없는 `http` 또는 `https` origin입니다. OwnGit은 이 주소의 호스트 이름을 받아들이고 클론 주소에도 이 주소를 표시합니다. 기본 URL이 없으면 화면의 클론 주소는 브라우저가 접속한 주소를 따릅니다.
- `--allowed-host`와 `--remove-allowed-host`는 `owngit approve-host`가 이름을 추가하는 저장 목록을 바꿉니다. 둘 다 여러 번 지정할 수 있습니다.
- `--trusted-proxy`와 `--remove-trusted-proxy`는 OwnGit이 전달 헤더를 믿는 리버스 프록시 목록을 바꿉니다. 각각 IP 주소나 CIDR 범위를 받으며 여러 번 지정할 수 있습니다. [리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기)를 보세요.
- `--base-url ""`처럼 빈 값을 주면 저장된 그 값을 지웁니다.

`--listen`이나 `--base-url` 옵션처럼 이번 실행에서만 받아들이는 이름으로 다른 기기에서 웹 설치를 마치면, 설치 화면에 "다시 시작한 뒤에도 이 주소 받아들이기"가 나타납니다. 선택하면 설치를 마칠 때 그 이름을 허용한 Host로 저장합니다. 선택하지 않으면 아무것도 저장하지 않습니다.

연결 주소가 이 컴퓨터 밖에서 접속을 받는 주소이면 `set`은 안내 한 줄을 출력합니다. 다른 기기는 암호화되지 않은 일반 HTTP로 접속하게 되기 때문입니다. HTTPS 리버스 프록시나 [Tailscale HTTPS](#tailnet에서-https로-공유하기)를 쓰면 이 연결을 암호화할 수 있습니다. 기본 URL이 `https`인데 신뢰하는 리버스 프록시가 없을 때도 안내 한 줄을 출력합니다.

`owngit serve`는 값마다 옵션이 있으면 옵션을, 없으면 저장된 값을, 둘 다 없으면 기본값을 씁니다. 기본값은 `127.0.0.1:7654`이고, 기본 URL은 연결 주소에서 정합니다. 옵션은 그 실행에만 적용되며 저장된 값을 바꾸지 않습니다. `localhost`, `127.0.0.1`, `::1`은 무엇을 저장했든 항상 받아들입니다.

`owngit network show`는 저장된 값을 보여 줍니다. 그 상태 디렉터리로 서버가 실행 중이면 서버가 실제로 쓰는 값과, 저장한 변경을 적용하려면 다시 시작해야 하는지도 함께 보여 줍니다. `--json`을 붙이면 같은 내용을 JSON으로 출력합니다.

같은 설정은 설정 화면의 "네트워크"에도 있습니다. 설정 화면을 열 수 있는 사람은 누구나 저장된 값, 실행 중인 서버가 쓰는 값, 다시 시작해야 하는지를 `network show`와 같은 기준으로 볼 수 있고, `network set`이 출력하는 안내도 함께 나옵니다. 화면에서 값을 바꾸려면 관리자 비밀번호가 필요하며, `network set`과 같은 검사를 거칩니다. 저장한 변경은 다음 시작부터 적용되고, 그때까지 실행 중인 서버는 지금 값을 그대로 씁니다. 새 연결 주소로 다른 기기가 접속할 수 있는데 아직 일반 HTTP 사용을 확인하지 않았다면, 설치 때처럼 화면에서 그 확인도 받습니다. 화면을 연 뒤에 `network set` 같은 방법으로 설정이 바뀌었다면 저장을 거부하고 현재 값을 보여 줍니다. 설정을 기본값으로 되돌리는 일은 화면에서 할 수 없으니 아래의 `owngit network reset`을 쓰세요.

저장한 값 때문에 접속할 수 없게 되었다면, 예를 들어 이 컴퓨터에 더 이상 없는 주소를 연결 주소로 저장했다면, 설치 호스트에서 설정을 되돌리고 서버를 다시 시작하세요.

```sh
./bin/owngit network reset
```

`reset`은 저장된 연결 주소와 기본 URL을 지웁니다. `--clear-allowed-hosts`를 붙이지 않으면 허용한 Host 이름은 그대로 두고, `--clear-trusted-proxies`를 붙이지 않으면 신뢰하는 프록시도 그대로 둡니다. 이 작업은 상태 디렉터리에 접근할 수 있어야 하며 웹 화면에서는 할 수 없습니다.

네트워크 설정은 이 설치 호스트에 속합니다. 오프라인 백업에 포함되지 않으며, 복원한 설치는 기본값으로 시작합니다.

### 백그라운드 서비스의 옵션

`--listen`, `--base-url`, `--allowed-host`, `--trusted-proxy`는 `owngit serve`의 옵션이므로 서버를 시작하는 명령에만 적용됩니다. 서비스 정의(LaunchAgent의 `ProgramArguments`, systemd 유닛의 `ExecStart` 줄)에서 이 옵션을 넘기면 시작할 때마다 저장된 값보다 옵션이 우선합니다. 저장된 설정을 쓰려면 서비스 정의에서 이 옵션을 빼세요.

Homebrew 서비스(`brew services start owngit`)는 다른 옵션 없이 `owngit serve --no-open`을 실행하므로 저장된 설정을 씁니다. 다른 기기에서 접속하려면 다음과 같이 합니다.

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654
brew services restart owngit
```

### tailnet에서 HTTPS로 공유하기

OwnGit을 실행하는 컴퓨터에서 Tailscale이 실행 중이면, OwnGit은 이 컴퓨터의 Tailscale 이름으로 들어오는 HTTPS 요청을 Tailscale이 받아 OwnGit에 넘기도록 설정할 수 있습니다. 그러면 tailnet에 로그인한 다른 기기에서 `https://NAME.TAILNET.ts.net/`을 열고 `https://NAME.TAILNET.ts.net/git/project.git` 같은 주소로 클론할 수 있습니다. 인증서는 이 컴퓨터의 Tailscale이 관리하고 연결도 Tailscale이 암호화합니다. 이 주소로 OwnGit을 열면 화면 상단에 "이 컴퓨터의 Tailscale이 암호화함"이라고 표시됩니다. tailnet 밖의 기기는 이 주소에 접속할 수 없습니다.

이 컴퓨터에 Tailscale이 설치되어 있고 로그인되어 있어야 합니다. tailnet에서는 MagicDNS와 HTTPS Certificates가 켜져 있어야 하며, 둘 다 Tailscale 관리 콘솔의 DNS 페이지에서 켭니다. Linux의 Tailscale은 root나 지정된 operator만 설정을 바꿀 수 있습니다. `sudo tailscale set --operator=$USER`로 사용자를 한 번 허용해 두세요. OwnGit이 `sudo`를 직접 실행하지는 않습니다.

공유는 설정 화면의 "tailnet에서 HTTPS로 공유"에서 관리자 비밀번호를 입력해 켜거나, 설치 호스트에서 다음 명령으로 켭니다.

```sh
./bin/owngit tailscale on
./bin/owngit tailscale status
./bin/owngit tailscale off
```

공유를 켜면 OwnGit은 다음 순서로 동작합니다.

1. 먼저 현재 Tailscale Serve 설정을 읽습니다. 이 컴퓨터의 HTTPS 443 포트가 이미 다른 용도로 쓰이고 있으면 아무것도 바꾸지 않고 그 포트에 무엇이 있는지 보여 줍니다. 설정 화면은 이 목록을 예전 이름의 주소, Tailscale이 출력한 내용과 마찬가지로 관리자 세션이거나 관리자 비밀번호를 방금 입력한 경우에만 보여 줍니다. 다른 사용자에게는 포트가 이미 쓰이고 있다는 사실만 보입니다. `owngit tailscale status`는 항상 목록을 보여 줍니다. Tailscale Funnel도 여기에 해당합니다.
2. `tailscale serve --bg --https=443 http://127.0.0.1:PORT`를 실행합니다. PORT는 OwnGit의 포트입니다. 그런 다음 설정을 다시 읽어 이 주소가 OwnGit을 가리키는지 확인합니다.
3. HTTPS 주소를 기본 URL로, Tailscale 이름을 허용한 Host 이름으로, `127.0.0.1`을 신뢰하는 프록시로 저장합니다. 이미 저장된 값은 다시 넣지 않습니다. 공유를 끌 때 정확히 그만큼만 되돌릴 수 있도록 OwnGit이 만든 내용도 기록합니다.

`127.0.0.1`을 믿으면 [리버스 프록시 뒤에서 운영하기](#리버스-프록시-뒤에서-운영하기)에서 설명한 것처럼 이 컴퓨터의 다른 프로그램도 모두 믿게 됩니다. 이 컴퓨터에서 돌아가는 리버스 프록시(NAS에 내장된 것 포함), `ssh -L` 터널, 다른 Tailscale Serve 전달처럼 다른 기기의 연결을 `127.0.0.1`에서 넘겨주는 프로그램도 여기에 포함됩니다. 공유가 켜져 있는 동안 이런 프로그램은 OwnGit에 전달 헤더를 보낼 수 있고, 클라이언트의 `X-Forwarded-For`를 그대로 넘기는 전달 프로그램이라면 그 클라이언트도 같은 일을 할 수 있습니다. 그래서 OwnGit이 비밀번호 잠금에 쓰는 클라이언트 주소를 정하거나, 자신의 요청을 HTTPS로 온 것처럼 보이게 할 수 있습니다. Tailscale Serve도 `127.0.0.1`에서 접속하므로 OwnGit은 이들을 구별할 수 없습니다. 이미 `127.0.0.1`을 믿고 있었다면 공유를 켜도 이 점은 달라지지 않습니다.

Tailscale은 `127.0.0.1`로 OwnGit에 접속합니다. 기본값 `127.0.0.1:7654`나 `0.0.0.0:7654`처럼 연결 주소가 이 접속을 받을 수 있으면 OwnGit은 연결 주소를 그대로 둡니다. OwnGit이 Tailscale 주소에서만 접속을 받는 경우처럼 그렇지 않으면 `127.0.0.1:PORT`를 저장합니다. OwnGit은 홈 네트워크 접속을 스스로 열지 않습니다. "홈 네트워크에서도 허용 (암호화되지 않음)" 체크박스를 켜거나 `owngit tailscale on --home-network`를 쓰면 `0.0.0.0:PORT`를 저장하므로 홈 네트워크의 기기도 일반 HTTP로 접속할 수 있습니다. 이 선택으로 홈 네트워크가 열리면 일반 HTTP를 허용한다는 확인으로도 기록됩니다. 실행 중인 OwnGit이 `--listen` 옵션으로 시작되었다면 이 옵션이 연결 주소를 정합니다. 공유를 켜도 그 주소를 그대로 쓰며, 설정 화면은 체크박스 대신 이 사실을 알려 줍니다. `--home-network=false`는 이 컴퓨터에서만 접속을 받게 합니다. 새 연결 주소는 다음 시작부터 적용됩니다.

설정 화면에서 켜거나 끄면 바로 적용됩니다. 실행 중인 서버가 다시 시작하지 않아도 이름을 받아들이고, 프록시를 신뢰하고, 클론 주소에 HTTPS 주소를 씁니다. `owngit tailscale on`과 `off`도 같은 내용을 저장하지만 실행 중인 서버에는 전달할 수 없으므로 OwnGit을 다시 시작하라고 안내합니다. 실행 중인 서버가 이름을 받아들이고 `127.0.0.1`을 신뢰하며 Tailscale에 주소가 남아 있을 때만 `owngit tailscale status`는 "on and ready"를, 설정 화면은 "켜져 있습니다. 이 컴퓨터의 Tailscale이 암호화합니다."를 표시합니다. 그렇지 않으면 다시 시작처럼 무엇이 남았는지 알려 줍니다. 변경은 설정 화면과 명령줄 사이에서도 한 번에 하나씩 실행되며, 변경을 요청한 브라우저 탭을 닫아도 끝까지 진행됩니다. OwnGit이 멈추는 등의 이유로 켜기가 중간에 끊겼다면 설정 화면이 이를 알리고 공유를 다시 켤 수 있게 합니다. OwnGit이 `tailscale` 명령을 찾지 못하면 `owngit serve --tailscale PATH`와 `owngit tailscale --tailscale PATH`로 경로를 지정합니다. `status --json`은 같은 내용을 JSON으로 출력합니다. 설정 화면은 여러 사람이 동시에 열어도 Tailscale에 상태를 3초에 한 번까지만 묻고, 공유를 켜거나 끈 직후에는 바로 다시 묻습니다. 그래서 다른 곳에서 바꾼 내용은 몇 초 늦게 보일 수 있습니다.

Tailscale이 인증서를 발급할 때 `gitbox.tail0000.ts.net`처럼 이 컴퓨터와 tailnet의 이름이 공개 인증서 투명성(Certificate Transparency) 로그에 기록됩니다. 주소를 열었다는 기록만 남을 뿐 코드, 저장소, 비밀번호 같은 내용은 기록되지 않습니다. 설정 화면은 이 안내를 켜기 버튼 옆에 보여 주고, `owngit tailscale on`은 Tailscale에 주소 제공을 요청하기 전에 출력합니다(`--json` 출력에서는 `certificate_log` 필드). 명령줄 출력은 터미널 설치 과정을 빼고 영어입니다. 이 컴퓨터의 이름은 Tailscale 관리 콘솔에서 바꿀 수 있습니다.

이름을 바꾼 뒤에는 새 이름을 쓰도록 공유를 다시 켜세요. OwnGit은 새 이름으로 주소를 만들고, 새 기본 URL과 허용한 Host 이름을 저장하며, 예전 이름을 자신이 추가했다면 지웁니다. 예전 주소는 예전 이름으로 Tailscale에 남습니다. Tailscale은 지금 이름으로만 응답하므로 이 주소는 아무 데도 연결되지 않지만, `tailscale serve`는 컴퓨터가 그 이름일 때만 이 주소를 지울 수 있습니다(Tailscale 이슈 16992). 지우려면 이름을 예전 이름으로 되돌리고 `tailscale serve --https=443 --set-path=/ off`를 실행한 뒤 다시 이름을 바꾸세요. 이 컴퓨터에서 Tailscale이 다른 것을 제공하지 않는다면 `tailscale serve reset`으로도 지울 수 있습니다. 설정 화면과 `owngit tailscale status`는 이런 주소를 이 방법과 함께 보여 줍니다. 이름을 바꾼 뒤 공유를 끄면 OwnGit은 자신의 설정을 되돌리고 예전 주소는 그대로 둡니다.

공유를 끄면 OwnGit은 자신이 만든 Tailscale 주소가 만들 때 모습 그대로 남아 있을 때만 그 주소를 지웁니다. 그 뒤에 누가 주소를 바꿨다면 아무것도 바꾸지 않고 이유를 알려 줍니다. 원래대로 되돌리거나 `tailscale serve`로 지운 뒤 다시 끄세요. OwnGit이 공유를 켜기 전부터 있던 주소는 그대로 두고, 이미 없어진 주소는 지울 것이 없습니다. 끌 때도 먼저 Tailscale에 확인하므로 Tailscale이 응답해야 합니다. 그다음 HTTPS 주소가 아직 기본 URL로 저장되어 있으면 그 전에 저장되어 있던 기본 URL로 되돌리고, OwnGit이 추가한 허용 Host 이름과 신뢰하는 프록시를 지웁니다. 연결 주소는 바꾸지 않습니다. OwnGit은 `tailscale serve reset`이나 `tailscale funnel`을 실행하지 않습니다.

OwnGit은 `Tailscale-Funnel-Request` 헤더가 붙은 요청을 모두 거부하므로 이 주소가 Funnel을 통해 인터넷에 열리지 않습니다. `Tailscale-User-Login` 같은 `Tailscale-User-*` 헤더는 무시합니다. 누가 읽고 쓰고 관리할 수 있는지는 여전히 비밀번호로 정합니다.

macOS용 Tailscale 앱(App Store 앱이나 독립 실행형 앱으로, Homebrew의 `tailscaled`와는 다릅니다)은 누군가 로그인해 있을 때만 실행됩니다. Mac을 다시 시작하면 누군가 로그인할 때까지 HTTPS가 동작하지 않습니다. 자동 로그인을 켜거나, 로그인 없이 실행되는 Homebrew의 `tailscaled`를 쓰세요. 설정 화면은 이 앱을 감지하면 같은 안내를 한 줄로 보여 줍니다.

공유 기록은 네트워크 설정처럼 이 설치 호스트에 속하며 오프라인 백업에 포함되지 않습니다.

### 리버스 프록시 뒤에서 운영하기

Caddy, nginx, Traefik, Nginx Proxy Manager 같은 리버스 프록시를 쓰면 OwnGit에 HTTPS 주소를 붙일 수 있습니다. OwnGit은 `https://git.example.internal`처럼 호스트 이름 하나의 루트에 있어야 합니다. `https://example.internal/git`처럼 다른 사이트 아래 경로에 두는 방식은 지원하지 않습니다.

프록시 뒤에서는 모든 요청이 프록시에서 일반 HTTP로 OwnGit에 도착합니다. 프록시를 신뢰한다고 알려 주기 전까지 OwnGit은 모든 클라이언트를 프록시로 봅니다. 그래서 기기 하나에서 비밀번호를 잘못 입력하면 모든 기기가 15분 동안 막히고, 쿠키에 `Secure`가 붙지 않으며, 브라우저가 HTTPS로 보낸 양식은 Origin 확인에서 거부됩니다. 프록시 주소와 HTTPS 주소를 저장한 뒤 OwnGit을 다시 시작하세요.

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
owngit network show
```

`network show`는 저장된 신뢰 프록시를 보여 주고, OwnGit이 실행 중이면 실제로 쓰는 목록도 보여 줍니다. 다시 시작한 뒤 프록시를 거쳐 OwnGit을 열면 화면 위쪽의 연결 상태에 "OwnGit 앞의 프록시가 암호화함"이라고 표시됩니다.

`--trusted-proxy`에는 프록시가 접속해 오는 주소를 씁니다. 프록시가 같은 컴퓨터에서 돌면 `127.0.0.1`이고, Docker 네트워크라면 `172.18.0.0/16` 같은 CIDR 범위를 쓸 수 있습니다. 여러 번 지정할 수 있습니다. OwnGit은 기본값으로 어떤 프록시도 믿지 않으며 `127.0.0.1`도 예외가 아닙니다. IPv4는 `/8`, IPv6는 `/32`보다 넓은 범위(`0.0.0.0/0`, `0.0.0.0/1`, `::/0` 등)와 지정되지 않은 주소 `0.0.0.0`, `::`는 거부합니다. 범위를 쓰면 그 안의 모든 컴퓨터를 믿게 되므로 가능한 한 좁게 잡으세요. `127.0.0.1`을 믿으면 이 컴퓨터의 모든 프로그램도 믿게 되며, 그 프로그램은 OwnGit이 보는 클라이언트 주소를 정할 수 있습니다. `ssh -L` 터널이나 다른 프록시처럼 이 컴퓨터에서 다른 기기의 연결을 넘겨주는 프로그램도 여기에 포함됩니다. `owngit serve --trusted-proxy 주소`는 그 실행에서만 저장된 목록 대신 쓰이고, `--trusted-proxy ""`를 주면 그 실행에서는 아무 프록시도 믿지 않습니다.

OwnGit은 신뢰하는 프록시에서 온 요청에서만 다음 세 헤더를 읽습니다.

- `X-Forwarded-Proto`: 한 번만 오고 값이 정확히 `https`나 `http`일 때 씁니다. `https`이면 OwnGit은 쿠키에 `Secure`를 붙이고, 브라우저 양식을 `https` 주소와 비교하고, 연결을 프록시가 암호화한 연결로 표시하고, 일반 HTTP 확인을 묻지 않으며, Git에도 HTTPS로 온 요청이라고 알립니다.
- `X-Forwarded-For`: 마지막 항목이 IP 주소일 때 씁니다. 마지막 항목은 프록시가 붙인 값입니다. OwnGit은 이 주소로 비밀번호 잠금과 설치 승인 경고를 처리하므로, 프록시 뒤의 두 기기는 따로 잠깁니다. 그 앞의 항목은 클라이언트가 보낸 값이라 무시합니다.
- `X-Forwarded-Host`: 한 번만 오고, OwnGit이 그 Host와 요청 자체의 Host를 모두 받아들일 때 씁니다. 이미 Host 확인을 통과하는 이름 가운데 하나를 고를 수만 있고 새 이름을 더할 수는 없습니다. 아래 예시는 원래 Host를 그대로 넘기며, nginx 예시는 클라이언트가 보낸 `X-Forwarded-Host`를 지웁니다.

헤더가 여러 번 오거나, 값 하나가 와야 할 자리에 목록이 오거나, 다른 값이 오면 OwnGit은 그 헤더를 무시하고 연결에서 보이는 값을 씁니다. `Forwarded` 헤더도 무시합니다. 다른 주소에서 온 요청은 전과 똑같이 처리하므로, OwnGit에 직접 접속한 기기는 이 헤더로 아무것도 바꿀 수 없습니다. 프록시는 클라이언트 주소를 `X-Forwarded-For`에 직접 덧붙여야 합니다. 클라이언트가 보낸 헤더를 그대로 넘기는 프록시를 쓰면 클라이언트가 잠금에 쓰일 주소를 고를 수 있습니다.

프록시가 같은 컴퓨터에서 돌면 OwnGit은 기본값인 `127.0.0.1:7654`에서 연결을 받게 두세요. 그러면 다른 기기는 프록시를 거쳐야만 OwnGit에 닿습니다. 컨테이너 안의 프록시는 호스트 네트워크를 쓰지 않는 한 호스트의 `127.0.0.1`에 닿지 못합니다. 이때는 프록시가 닿을 수 있는 주소에서 OwnGit이 연결을 받게 하고, 프록시가 접속해 오는 주소를 신뢰하세요. 같은 컴퓨터의 Docker 안에서 도는 프록시는 `172.18.0.0/16` 같은 그 컨테이너의 Docker 네트워크 범위에서 접속합니다. 다른 컴퓨터에서 도는 프록시는 그 컴퓨터에서 Docker로 돌더라도 그 컴퓨터의 주소에서 접속합니다. Docker가 컨테이너 주소를 그 컴퓨터의 주소로 바꾸기 때문입니다.

OwnGit이 네트워크 주소에서 연결을 받으면 다른 기기도 프록시를 거치지 않고 일반 HTTP로 OwnGit에 직접 접속할 수 있습니다. `network set`과 설정 화면도 이를 알려 줍니다. OwnGit은 그런 기기가 보낸 전달 헤더를 믿지 않지만, 그 연결은 암호화되지 않습니다. 모든 기기가 프록시를 거치게 하려면 방화벽 규칙 등으로 프록시만 OwnGit의 포트에 닿게 하세요.

Git 요청 하나는 최대 4 GiB를 주고받고 최대 30분까지 걸릴 수 있습니다([Git 전송 제한](#git-전송-제한) 참고). 프록시의 한도가 이보다 작으면 큰 푸시나 클론이 프록시에서 실패합니다.

#### Caddy

```caddyfile
git.example.internal {
	reverse_proxy 127.0.0.1:7654
}
```

`reverse_proxy`는 기본값으로 원래 Host를 넘기고, `X-Forwarded-Proto`를 설정하며, `X-Forwarded-For`를 클라이언트 주소로 설정합니다. 클라이언트가 보낸 값은 무시합니다. 요청 크기 한도가 없고, 긴 푸시를 끊는 시간 제한도 없습니다. 공개 이름이면 Caddy가 인증서를 자동으로 받습니다. `git.example.internal` 같은 이름에는 Caddy 자체 인증 기관을 쓰므로 각 기기가 그 인증 기관을 신뢰해야 합니다. Caddy의 Debian 패키지로 설치했다면 이 인증 기관의 인증서는 `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`에 있습니다. root와 `caddy` 사용자만 읽을 수 있으니 root 권한으로 복사한 뒤 각 기기의 신뢰하는 인증서에 추가하세요.

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

`client_max_body_size`와 두 시간 제한은 OwnGit의 한도에 맞춘 값입니다. `proxy_request_buffering off`와 `proxy_http_version 1.1`을 함께 쓰면 nginx가 푸시를 디스크에 모두 저장하지 않고 받는 대로 넘깁니다. `proxy_buffering off`는 반대 방향에서 같은 일을 합니다. 클론과 압축 파일을 임시 파일에 먼저 저장하지 않고 OwnGit이 보내는 대로 클라이언트에 넘깁니다. `$proxy_add_x_forwarded_for`는 클라이언트 주소를 헤더 끝에 덧붙입니다. nginx는 클라이언트가 보낸 다른 헤더를 그대로 넘기고 빈 값을 주면 그 헤더를 지우므로, `X-Forwarded-Host` 줄은 클라이언트가 이 헤더를 직접 보내지 못하게 합니다. `$host`에는 포트가 없으므로, 클라이언트가 443이 아닌 포트로 접속한다면 `proxy_set_header Host $http_host;`로 바꾸세요. 그래야 OwnGit이 보는 Host가 브라우저의 주소와 같아집니다.

#### Traefik

Traefik은 원래 Host를 넘기고, `X-Forwarded-Proto`를 설정하며, `X-Forwarded-For`에 클라이언트 주소를 덧붙입니다. `forwardedHeaders.trustedIPs`를 설정하지 않으면 클라이언트가 보낸 전달 헤더는 버립니다. 엔트리포인트는 기본값으로 요청을 60초까지만 읽으므로 긴 푸시가 HTTP 504로 끊깁니다. HTTPS 엔트리포인트의 `readTimeout`을 늘리세요. 이 값은 정적 설정에 넣고, 라우터와 서비스와 인증서는 동적 설정 파일에 넣습니다.

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

Traefik은 `traefik --configFile=/etc/traefik/traefik.yml`로 시작합니다. 이 예시는 릴리스 바이너리로 설치한 Traefik 3.7에서 시험했습니다. Traefik이 Docker에서 돌면 위에서 설명한 대로 Traefik이 접속해 오는 주소를 신뢰하세요.

#### Nginx Proxy Manager

기본 설정의 Nginx Proxy Manager에서는 내 네트워크의 기기가 OwnGit이 보는 주소를 직접 정할 수 있습니다. 이 프로그램의 `nginx.conf`는 `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`에 속한 주소가 보낸 `X-Real-IP` 헤더를 받아들여 그 값을 클라이언트 주소로 쓰고, 그 값을 `X-Forwarded-For`에 덧붙입니다. 이 설정 그대로 OwnGit에서 Nginx Proxy Manager를 신뢰하면 가정용 네트워크의 기기는 비밀번호를 추측할 때마다 주소를 바꿔 잠금을 피할 수 있고, 다른 기기의 주소를 보내 그 기기를 잠글 수 있으며, 설치 승인 요청이 이 컴퓨터에서 온 것처럼 보이게 해서 다른 기기가 요청했다는 경고가 나오지 않게 할 수 있습니다.

아래 Advanced 탭 설정에 있는 `set_real_ip_from 127.0.0.1;` 줄을 넣으면 OwnGit용 프록시 호스트에서는 이 동작이 꺼집니다. Nginx Proxy Manager는 자기 컨테이너에서 온 `X-Real-IP`만 받아들이고 각 기기가 접속해 온 주소를 그대로 알려 주므로, 기기마다 따로 잠깁니다. Linux의 Docker Engine에서 돌린 Nginx Proxy Manager 2.16.0과 IPv4 클라이언트로, Websockets Support를 켠 경우와 끈 경우를 모두 시험했습니다. Docker Desktop, rootless Docker, IPv6 클라이언트는 시험하지 않았습니다. 이 줄은 Nginx Proxy Manager가 `X-Real-IP`를 믿지 않게 할 뿐이므로, Docker가 이미 바꾼 클라이언트 주소를 되살리지는 못합니다.

이 줄을 넣지 않았다면 Nginx Proxy Manager에 접속할 수 있는 기기가 모두 내 기기일 때만 신뢰하세요. 내 네트워크에서 오는 추측은 잠금으로 늦출 수 없으므로 긴 비밀번호를 쓰고, 설치 요청을 승인할 때 보이는 주소를 믿지 마세요.

설정할 때는 프록시 호스트를 만들고 scheme은 `http`, Forward Hostname / IP와 Forward Port는 OwnGit의 주소와 포트로 정한 뒤 SSL 인증서를 붙이고 Force SSL을 켜세요. "Trust Upstream Forwarded Proto Headers"는 끈 채로 두세요. Nginx Proxy Manager 2.14.0부터는 클라이언트가 보낸 `X-Forwarded-Proto` 값을 그대로 넘깁니다. 이 옵션을 끈 채로 Force SSL을 켜면 일반 HTTP 요청은 모두 HTTPS로 넘겨지므로 OwnGit에는 HTTPS 요청만 도착합니다. HTTPS로 접속하면서 `X-Forwarded-Proto: http`를 보내는 클라이언트는 자기 요청만 일반 HTTP로 처리되게 할 뿐입니다. Nginx Proxy Manager는 `X-Forwarded-Host`를 설정하지 않으므로 클라이언트가 보낸 값이 OwnGit까지 오지만, OwnGit은 위에서 설명한 경우에만 그 값을 씁니다. 기본값은 요청 본문을 2000 MB로 제한하고, OwnGit이 데이터를 보내거나 받기를 최대 90초만 기다리며, 큰 응답을 임시 파일에 저장합니다. Advanced 탭의 Custom Nginx Configuration에 다음 줄을 넣으세요.

```nginx
client_max_body_size 4g;
proxy_request_buffering off;
proxy_read_timeout 30m;
proxy_send_timeout 30m;
set_real_ip_from 127.0.0.1;
```

Websockets Support를 켜든 끄든 그대로 쓸 수 있습니다. `proxy_buffering off;`를 더하면 클론과 압축 파일을 임시 파일에 먼저 쓰지 않고 OwnGit이 보내는 대로 넘깁니다. `proxy_http_version`은 넣지 마세요. Nginx Proxy Manager가 이미 설정하므로 Websockets Support를 켜면 값이 두 번 들어가 프록시 호스트가 동작하지 않습니다.

Nginx Proxy Manager가 접속해 오는 주소는 위에서 설명한 대로 신뢰하세요. OwnGit이 같은 컴퓨터에서 돌면 그 Docker 네트워크 범위이고, 다른 컴퓨터에서 돌면 Nginx Proxy Manager를 실행하는 컴퓨터의 주소입니다.

## 새 릴리스 알림

설정을 마친 뒤 OwnGit은 하루에 한 번 GitHub에 새 릴리스가 있는지 묻습니다. 첫 확인은 서버가 시작되고 약 30초 뒤에 하며, 새로 설치했다면 설정을 마친 직후에 합니다. OwnGit과 그 버전을 밝힌 User-Agent를 담아 `https://api.github.com/repos/juliankang4/owngit/releases/latest`에 HTTPS 요청을 한 번 보냅니다. 저장소 데이터는 보내지 않으며, GitHub는 서버의 네트워크 주소를 볼 수 있습니다. 초안(draft)과 사전 릴리스(prerelease)는 무시합니다. [가져오기](#다른-git-호스트에서-가져오기)는 직접 설정한 원본 호스트에만 접속하며 예약 새로고침도 마찬가지입니다. 이를 빼면 OwnGit이 다른 호스트에 여는 연결은 이 확인뿐입니다. **업데이트 확인** 설정과 `--no-update-check`는 가져오기에 영향을 주지 않습니다.

새 버전이 있으면 대시보드의 활동 그래프 위에 알림이 나타납니다. 알림에는 **릴리스 노트** 링크와, 업데이트 방법을 설명하는 [설치](../README.ko.md#설치) 절로 가는 링크가 있습니다. OwnGit은 아무것도 직접 내려받거나 설치하지 않습니다. **알림 닫기**를 누르면 지금 쓰는 브라우저에서 그 버전의 알림이 사라지고, 더 새 버전이 나오면 다시 나타납니다. 확인 결과는 메모리에만 두므로 다시 시작하면 새로 확인합니다. 확인에 실패하면(네트워크 없음, 요청 한도 초과, 예상하지 못한 응답) 아무것도 표시하지 않습니다. 다시 성공할 때까지 로그는 최대 한 줄만 남기며, Git이나 페이지에는 영향을 주지 않습니다.

확인을 끄려면 **설정**에서 **업데이트 확인**을 쓰세요. 관리자 비밀번호를 묻습니다. 끄면 이후의 릴리스 확인을 멈추고 알림도 바로 숨깁니다. 켜면 몇 초 안에 다시 확인합니다. 이 설정은 이 설치 호스트에 속합니다. 오프라인 백업에는 들어가지 않으며, 복원한 설치는 확인이 켜진 상태로 시작합니다.

새 릴리스를 절대 확인하면 안 되는 환경이라면 서버를 `--no-update-check`로 시작하세요. 그러면 저장된 설정과 관계없이 릴리스 확인 요청을 하지 않으며, **설정** 화면에는 시작 옵션 때문에 확인이 꺼졌다고 표시됩니다.

```sh
./bin/owngit serve --no-update-check
```

## 설치 호스트에서 복구하기

설정을 마치기 전이라면 다음 명령으로 새 설정 링크를 발급합니다.

```sh
./bin/owngit setup-link --base-url http://127.0.0.1:7654 --no-open
```

잊어버린 관리자 비밀번호를 재설정하려면 새 비밀번호를 소유자만 읽을 수 있는 파일에 넣으세요.

```sh
./bin/owngit reset-admin --password-file /path/to/owner-only-password-file
```

비밀번호 파일은 일반 파일이어야 합니다. Unix 계열 시스템에서는 그룹이나 다른 사용자가 읽을 수 없어야 합니다. Windows에서는 폴더의 접근 항목을 상속하지 않고 본인 계정에만 접근을 허용해야 합니다([비밀번호 파일과 토큰 파일](#비밀번호-파일과-토큰-파일) 참고). OwnGit은 비밀번호를 명령줄 값으로 받지 않습니다. 관리자 비밀번호를 재설정하면 관리자 세션이 로그아웃되고 저장소는 그대로 남습니다.

OwnGit에는 이메일 복구나 계정 복구 기능이 없습니다. 두 절차 모두 설치 호스트에 접근할 수 있어야 합니다.

### 비밀번호 파일과 토큰 파일

직접 만든 비밀번호 파일이나 토큰 파일을 읽는 명령은 모두 이 방식으로 검사합니다. `reset-admin`, `import`, `pr`, `repo`도 마찬가지입니다. 비공개가 아니라는 이유로 파일을 거부할 때는 어떤 계정이 파일을 더 읽을 수 있는지처럼 무엇이 문제인지와 이를 고치는 명령(Windows에서는 PowerShell 한 줄)을 함께 알려 줍니다.

macOS와 Linux에서는 파일이 그룹이나 다른 사용자에게 어떤 권한도 주지 않아야 합니다. `umask 077`을 적용한 상태에서 파일을 만들거나, 이미 있는 파일은 `chmod 600 FILE`로 고치세요.

Windows에서 메모장이나 `echo`로 만든 파일은 폴더의 접근 항목을 상속하고, 이 항목은 보통 다른 계정에도 읽기를 허용합니다. PowerShell에서 파일을 만들고 본인 계정으로 접근을 제한한 다음에 비밀번호를 적으세요.

```powershell
$file = "$HOME\owngit-password.txt"
New-Item -ItemType File -Path $file
$acl = Get-Acl -LiteralPath $file
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
Set-Acl -LiteralPath $file -AclObject $acl
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

`$acl`이 들어간 세 줄은 파일의 접근 목록을 `D:P(A;;FA;;;SID)`로 바꿉니다. 아무것도 상속하지 않고(`P`) 본인 계정에만 모든 권한(`FA`)을 허용하는(`A`) 목록입니다. 계정은 보안 식별자(SID)로 지정합니다. `Read-Host -AsSecureString`은 비밀번호를 화면과 PowerShell 기록에 남기지 않습니다. 이 명령은 일반 PowerShell 창과 관리자 권한으로 실행한 창에서 똑같이 동작합니다. 관리자 창에서는 Windows가 새 파일의 소유자를 Administrators 그룹으로 정합니다. 관리자는 어차피 어떤 파일이든 소유권을 가져올 수 있으므로, 접근 항목에 본인 계정만 있으면 OwnGit은 이 소유자를 받아들입니다.

## 저장소 파일 되돌리기

되돌리기는 다음 세 곳에서 시작합니다.

- 저장소 **개요**의 **파일 되돌리기** 칸에 있는 **되돌리기 시작**
- **개요**의 브랜치, 태그, 보관된 기록 줄과 커밋 페이지에 있는 **여기서 파일 되돌리기**
- 파일 페이지의 **이 파일 되돌리기**

원본 커밋과 대상 브랜치를 고른 뒤 추가, 변경, 삭제될 파일의 전체 목록을 미리 봅니다. OwnGit은 대상 브랜치의 끝 커밋이 미리 볼 때와 그대로일 때만 확인한 트리를 적용합니다.

기존 브랜치에는 이전 끝 커밋을 부모로 하는 새 커밋이 추가됩니다. 삭제된 브랜치는 선택한 커밋에서 다시 만들어집니다. 일부 파일만 되돌릴 때는 선택하지 않은 파일, 파일 모드, 바이너리 파일, 심볼릭 링크를 그대로 두며, 호스트에서 링크를 따라가지 않습니다. 선택한 서브모듈이나, 바꾸면 그 아래의 선택하지 않은 파일이 지워지는 경로는 되돌리지 않습니다.

되돌리기는 OwnGit 안에서 Git이 추적하는 내용만 바꿉니다. 다른 컴퓨터의 워킹 트리나 커밋하지 않은 파일은 건드리지 않습니다.

## 기본 브랜치 바꾸기

기본 브랜치는 OwnGit과 `git clone`이 처음 여는 브랜치(저장소의 `HEAD`)입니다. 브랜치가 `master` 하나뿐인 가져온 저장소는 직접 고르기 전까지 기본 브랜치가 없다고 표시됩니다. 관리자는 저장소의 **설정** 탭에서 기존 브랜치 중 하나를 고릅니다. 기본 브랜치를 바꿔도 브랜치가 새로 생기지 않으며, 모든 ref와 보관된 기록은 그대로입니다.

## 저장소 삭제하기

저장소를 삭제하면 OwnGit에서 저장소와 함께 풀 리퀘스트, 리뷰, 작업(task), 체크 설정, 체크 작업(job)과 결과, 러너 토큰과 체크 에이전트 토큰, 가져오기 설정, 실행 기록, 저장된 가져오기 인증 정보가 사라집니다. 대기 중인 체크 작업은 버려집니다. 관리자는 저장소 탭 맨 끝의 **저장소 삭제**에서 저장소 이름과 관리자 비밀번호를 입력해 삭제합니다. 삭제가 끝나면 그 이름으로 새 저장소를 만들 수 있습니다. 파일을 어떻게 할지는 직접 고릅니다.

- **OwnGit에서만 제거하고 파일은 남기기**를 고르면 bare 저장소를 그대로 저장소 폴더 안의 `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git`로 옮깁니다. `ID`는 Git URL에 쓰이는 소문자 저장소 이름이고 시각은 UTC입니다. 같은 이름이 이미 있으면 숫자를 덧붙입니다. 브랜치, 태그, 보관된 기록은 직접 지우기 전까지 그 폴더에 남습니다.
- **파일까지 영구 삭제**를 고르면 보관된 기록을 포함해 bare 저장소를 지웁니다.

다음 경우에는 삭제를 거부합니다. 가져오기가 실행 중일 때, 체크 작업이 가져감 상태이거나 실행 중일 때, 체크 컨테이너가 OwnGit의 제거 확인을 기다리고 있을 때, 다른 Git 작업(푸시, clone, 되돌리기, 병합)이 잠시 기다린 뒤에도 저장소를 쓰고 있을 때입니다. 끝난 뒤 다시 시도하세요. 실패한 컨테이너 정리는 OwnGit이 시작할 때 다시 시도하므로, Docker를 다시 쓸 수 있게 되면 OwnGit을 다시 시작하세요. 서버 로그에 작업이 다른 Docker 데몬에 속한다고 나오면(예를 들어 Docker를 초기화하거나 다시 설치한 뒤) OwnGit은 정리를 확인할 수 없으며, 시작할 때 오래된 체크 작업 공간도 지우지 않습니다. 그 작업을 실행한 데몬에서 `com.owngit.check-job=JOB` 라벨이 붙은 남은 컨테이너를 지우거나, 그 데몬이 더는 없는지 확인하세요. 그런 다음 OwnGit 컴퓨터에서 기록을 해제합니다.

```sh
./bin/owngit forget-check-container --job JOB --confirm-container-removed
```

`JOB`은 서버 로그에 나온 작업 식별자입니다. 기본 위치가 아닌 상태 디렉터리를 쓴다면 `--state-dir`을 붙이세요. 이 명령은 OwnGit이 실행 중일 때도 쓸 수 있고, 컨테이너를 지우지 않으며, 기록된 컨테이너 이름, ID, 데몬, 라벨을 출력합니다. 기록이 없는 작업과, 지금 실행 중인 Docker 데몬에 속한 기록은 거부합니다. 그런 컨테이너는 OwnGit이 다음에 시작할 때 직접 지우기 때문입니다. 끝나지 않은 작업(대기, 가져감, 시작됨)도 거부합니다. 실행 중인 OwnGit이 그 컨테이너를 아직 쓰거나 지우고 있을 수 있기 때문입니다. 작업이 끝나기를 기다리거나 작업을 취소하세요. 또는 OwnGit을 한 번 시작해 중단된 작업으로 표시하게 한 뒤 명령을 다시 실행하세요. 저장소는 바로 삭제할 수 있으며, 다음에 시작할 때 체크 작업 공간을 정리합니다.

OwnGit은 파일을 옮기거나 지우기 전에 삭제를 상태 데이터베이스에 기록합니다. OwnGit이 도중에 멈추거나 파일을 옮기거나 지우지 못하면 삭제 결과에 파일 처리가 끝나지 않았다고 나옵니다. 이때 저장소는 이미 대시보드와 Git URL에서 사라졌고, 같은 이름으로 저장소를 만들거나 가져오면 이름이 사용 중이라고 나옵니다. 파일은 `ID.git`, 파일을 남긴 폴더 경로, 또는 임시 이름 `.owngit-delete-*` 아래에 남습니다. OwnGit이 다음에 시작할 때 이동이나 삭제를 마무리하고 이름을 풀어 줍니다. 마무리하지 못하면 이유가 서버 로그에 남고 파일은 그 자리에 그대로 있습니다. OwnGit 1.0.0은 저장소를 삭제할 수 없고 삭제를 마무리하지도 않습니다. 삭제가 끝나지 않은 상태에서 1.0.0으로 돌아가면, 1.0.1 이상을 다시 시작할 때까지 삭제는 끝나지 않은 채로 남습니다.

삭제가 끝나지 않은 동안 저장소 폴더에는 그 삭제의 무작위 토큰이 담긴 작은 파일 `.owngit-deletion-ID`도 있습니다. 이 파일로 OwnGit은 그 폴더가 삭제를 시작한 저장 공간임을 확인합니다. 시작할 때 이 파일이 없거나 다른 토큰이 들어 있으면(예를 들어 저장 공간이 마운트되지 않았거나 예전 사본이 마운트된 경우) OwnGit은 삭제 기록을 유지하고, 저장 공간을 쓸 수 없는 것 같다고 로그에 남긴 뒤 다음 시작 때 다시 시도합니다. 삭제가 끝나지 않은 동안에는 이 파일을 지우지 마세요. 올바른 저장 공간이 마운트된 상태에서 직접 지웠다면, 서버 로그에 인용된 `token ...` 줄로 저장소 폴더에 파일을 다시 만든 뒤 OwnGit을 다시 시작하세요. 삭제가 끝난 뒤 남은 파일은 문제가 되지 않습니다. OwnGit은 삭제할 때 심볼릭 링크를 따라가지 않으며, 그 저장소 디렉터리 밖의 것은 지우지 않습니다. 삭제가 시작될 때 이미 기다리고 있던 Git 요청은 삭제 뒤에 실패합니다.

이전 백업에는 삭제한 저장소가 그대로 들어 있습니다. 저장소의 기록이 쓰던 데이터베이스 공간은 해제되지만 안전하게 덮어써 지워지지는 않습니다. `.owngit-removed` 아래의 폴더는 저장소 목록에 나오지 않으며 백업에도 들어가지 않습니다.

파일을 남기고 삭제하면 대시보드에 남긴 폴더가 한 번 표시되고, 그 폴더의 브랜치와 태그를 같은 이름의 새 저장소로 푸시하는 명령도 함께 나옵니다. 폴더가 OwnGit을 실행하는 컴퓨터에 있으므로 명령도 그 컴퓨터에서 실행하세요. 저장소를 되살리려면 대시보드에서 같은 이름으로 빈 저장소를 만든 뒤 그 명령을 실행합니다. 대시보드가 남긴 폴더를 확인하지 못하면 명령은 나오지 않으니, 그 폴더에서 직접 푸시하세요. 다른 이름의 저장소로 푸시하려면 그 저장소의 URL을 쓰면 됩니다.

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

보관된 기록은 옮겨지지 않습니다. 보관된 기록에만 있는 커밋은 남긴 폴더에 그대로 있고, 새 저장소에는 보관된 기록이 처음부터 새로 쌓입니다. 풀 리퀘스트, 체크 같은 다른 기록도 돌아오지 않습니다. 남긴 저장소의 주 브랜치가 `main`이 아니면 푸시한 뒤 기본 브랜치를 바꾸세요.

## 기존 저장소를 OwnGit으로 옮기기

대시보드에서 빈 저장소를 만듭니다. 기존 저장소의 clone에서 OwnGit을 원격으로 추가하고 브랜치와 태그를 푸시합니다.

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

OwnGit은 브랜치(`refs/heads/*`)와 태그(`refs/tags/*`)로 가는 푸시만 받습니다. 그래서 다른 호스트의 미러 clone에서 `git push --mirror`를 하면 `refs/pull/*` 같은 다른 ref 때문에 실패합니다.

옮기기가 끝났다고 보기 전에 브랜치와 태그 ref를 비교하세요.

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

두 OwnGit 설치 사이에서 푸시하면 보관된 기록과 저장소 기록은 옮겨지지 않습니다. 그것까지 옮겨야 한다면 오프라인 백업을 쓰세요. 계속 쓰는 호스트에서 변경 사항을 받아 오려면 [다른 Git 호스트에서 가져오기](#다른-git-호스트에서-가져오기)를 보세요.

## 다른 호스트에 사본 두기

OwnGit이 직접 다른 호스트로 푸시하지는 않지만, 일반 Git으로 다른 곳에 사본을 둘 수 있습니다.

OwnGit의 모든 브랜치와 태그를 다른 호스트로 복사하려면 미러 clone에서 작업하세요.

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

나중에 사본을 갱신하려면 같은 디렉터리에서 `git fetch --prune`과 `git push --mirror`를 다시 실행합니다. `--mirror`는 다른 호스트를 사본과 정확히 같게 만듭니다. 그곳의 ref를 덮어쓰고, 사본에 없는 ref는 지웁니다. OwnGit은 보관된 기록을 내보내지 않으므로 그 기록은 OwnGit에만 남습니다.

작업용 clone에서 푸시할 때마다 두 호스트를 함께 갱신하려면 원격에 푸시 URL을 두 개 지정하세요.

```sh
git remote set-url --add --push origin http://HOST:7654/git/PROJECT.git
git remote set-url --add --push origin https://git.example.test/team/project.git
```

원격에 푸시 URL이 지정되면 Git은 푸시 URL로만 푸시하므로 OwnGit도 함께 적어야 합니다. fetch는 여전히 원래 URL을 씁니다. Git은 URL마다 차례로 푸시하며, 한 호스트가 거부해도 다른 호스트에 이미 한 푸시는 취소되지 않습니다.

## 다른 Git 호스트에서 가져오기

가져오기는 다른 Git 호스트의 저장소를 HTTPS로 새 OwnGit 저장소에 복사하며, 나중에 새로고침할 수 있습니다. 가져오기는 받아 오기만 합니다. OwnGit은 원본에 아무것도 쓰지 않습니다. Git LFS 객체는 가져오지도, 호스팅하지도 않습니다.

가져오기마다 모드를 기록합니다. `standalone`(화면의 **독립**)은 OwnGit을 주 사본으로 쓴다는 뜻이고, `coexistence`(화면의 **공존**)는 다른 호스트를 계속 기준으로 두고 OwnGit에는 새로고침한 사본을 둔다는 뜻입니다. 모드는 사용 방식을 적어 두는 표시일 뿐이며, 가져오기와 새로고침의 동작을 바꾸지 않습니다. 두 모드 모두 같은 [새로고침 규칙](#가져오기가-게시하는-내용)을 따르고, 어느 모드에서도 OwnGit에서 한 작업을 덮어쓰지 않습니다.

브라우저에서는 관리자가 대시보드의 **저장소 가져오기**로 가져오기를 시작합니다. 원본 설정과 변경, 인증 정보 변경, 새로고침, 취소, 예약 설정은 저장소의 **가져오기** 탭에서 합니다. 저장소를 볼 수 있는 사람은 누구나 가져오기 탭에서 상태, 실행 기록, ref 상태를 볼 수 있으며, 원본 주소, 인증 정보 상태, 실행의 기술 메시지는 관리자에게만 보입니다. 변경할 때마다 현재 관리자 비밀번호를 묻습니다. 인증 정보 양식을 저장하면 입력한 것만 바뀝니다. 새 토큰이나 Basic 인증 정보를 저장해도 저장된 원본 CA는 남고, CA만 저장해도(인증 형식 **새 로그인 정보 없음 (CA만)**) 저장된 토큰이나 Basic 인증 정보는 남습니다. 이것들을 지우는 방법은 **인증 정보 지우기**뿐입니다. 브라우저 양식은 전체 1 MiB로 제한되므로, 그 크기에 가까운 CA 번들은 명령줄로 저장하세요.

이미 있는 가져오기의 원본 URL이나 옵션을 바꾸는 일은 **가져오기** 탭에서만 할 수 있고, 나머지 작업은 명령줄에서도 할 수 있습니다. 가져오기 명령은 관리자 비밀번호를 파일에서 읽으며, 그 파일은 `reset-admin`과 같은 검사를 거칩니다. 원본 토큰이나 Basic 인증 정보는 비공개 파일이나 대화형 입력에서 읽습니다. 비밀값을 인수나 환경 변수로는 받지 않습니다.

```sh
./bin/owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

모든 가져오기 명령은 같은 `--server`, `--accept-insecure-http`, `--password-file` 플래그를 받습니다. 아래에서는 생략했습니다.

```sh
./bin/owngit import refresh PROJECT
./bin/owngit import status PROJECT
./bin/owngit import history PROJECT --limit 20
./bin/owngit import cancel PROJECT
./bin/owngit import schedule PROJECT --enable --interval 6h
./bin/owngit import schedule PROJECT --disable
./bin/owngit import credentials PROJECT --token-file /path/to/owner-only-token
./bin/owngit import credentials PROJECT --ca-file /path/to/source-ca.pem
./bin/owngit import credentials PROJECT --clear
./bin/owngit import resolve PROJECT
```

- Basic 인증 정보를 쓰려면 `--token-file` 대신 `--basic-file`을 씁니다. 이 파일에는 사용자 이름과 비밀번호를 서로 다른 줄에 적습니다. `--ca-file`은 원본의 인증 기관(CA)을 최대 1 MiB까지 저장합니다. `import credentials`는 넘긴 것만 바꿉니다. `--ca-file`만 쓰면 저장된 토큰이나 Basic 인증 정보는 남고, `--token-file`이나 `--basic-file`만 쓰면 저장된 CA는 남습니다. `--clear`는 저장된 인증 정보와 CA를 지웁니다.
- `--allow-private-network`는 사설 LAN, CGNAT, Tailnet, 루프백 주소에 있는 원본을 허용합니다.
- `--git-only-consent`는 Git LFS 포인터가 있는 저장소를 받아들입니다. [Git LFS](#git-lfs)를 보세요.
- `--accept-insecure-http`는 그 명령에 한해 일반 HTTP로 OwnGit에 접속하는 데 동의한다는 뜻입니다. 가져올 원본 자체는 HTTPS를 써야 합니다.
- 출력에는 인증 정보의 종류와 저장 여부만 나오며 토큰, 비밀번호, CA는 나오지 않습니다.
- `import add`는 새 저장소를 만듭니다. 이미 있는 저장소에는 `repository_taken`으로 거부하고 아무것도 바꾸지 않습니다. 저장된 원본에서 갱신하려면 `import refresh`를 쓰세요.
- `import add`와 `import refresh`는 실행 전체가 끝날 때까지 기다립니다. 기본값으로 최대 약 62분입니다. 실행은 끝났지만 원본과 다른 OwnGit의 ref를 그대로 두었다면 그 ref를 나열하고 0 대신 종료 코드 3으로 끝납니다. `import cancel` 등으로 끝나기 전에 취소된 실행은 취소된 `check run`과 같이 종료 코드 130으로 끝납니다. 그 밖의 실패는 종료 코드 1입니다. `import status`는 마지막 실행과 진행 중인 실행, 그리고 원본과 일치하지 않는 브랜치와 태그를 상태와 함께 보여 줍니다.
- `import cancel NAME`은 아직 저장소 NAME을 만들고 있는 첫 가져오기도 멈춥니다. 취소는 결과가 게시되기 전까지만 실행을 멈출 수 있습니다. 첫 가져오기라면 저장소가 나타나는 순간이 그 경계입니다. 그 뒤에 도착한 취소는 너무 늦어서, 실행은 `complete`로 끝나고 `import add`나 `import refresh`는 취소가 실행을 멈추지 못했다고 알려 줍니다.
- 새 저장소의 첫 가져오기가 실패하거나 취소되면 OwnGit은 그 이름으로 저장했던 원본과 인증 정보를 지우고, 실패한 실행은 명령 결과에 그대로 나옵니다. 충돌 등으로 가져오는 도중에 OwnGit이 멈췄다면 다음에 시작할 때 지웁니다. 다시 시도할 때는 새로 넘긴 것만 씁니다. 같은 이름으로 저장소를 만들 때도 남아 있는 가져오기 설정을 지우며, 그 이름의 이전 가져오기가 아직 실행 중이거나 복구가 필요하면 만들기를 거부합니다. 저장소가 없는 이름이라도 `import credentials NAME --clear`로 지울 수 있습니다.
- 예약 간격은 60초에서 7일 사이입니다. 그 밖의 간격은 `invalid_schedule`로 거부합니다. 예약 새로고침은 `owngit serve`가 실행 중일 때만 동작합니다.

### 원본 연결

원본 URL은 TLS 1.2 이상의 HTTPS를 써야 하며, 사용자 이름, 비밀번호, 쿼리, 프래그먼트를 담을 수 없습니다. 호스트 이름은 ASCII여야 하며 IPv6 zone 식별자는 지원하지 않습니다. OwnGit은 리디렉션을 따라가지 않으며 프록시 환경 변수, 쿠키, Git credential helper를 무시합니다.

OwnGit은 호스트 이름을 한 번 조회하고, 돌아온 주소를 모두 검사한 뒤 연결합니다. 공인 주소는 허용합니다. 사설 LAN, CGNAT, Tailnet, 루프백 주소는 `--allow-private-network`가 있어야 하며, DNS 응답에 공인 주소와 사설 주소가 섞여 있을 때도 마찬가지입니다. 그 밖의 특수 용도 주소는 항상 거부합니다. 직접 지정한 CA는 시스템 루트 인증서에 더해질 뿐이며, 인증서나 호스트 이름 검사를 끄지 않습니다. 원본 인증서를 신뢰할 수 없거나, 호스트 이름과 맞지 않거나, TLS 핸드셰이크가 실패해서 실행이 실패하면 오류 메시지에 그 이유가 나옵니다.

### 가져오기가 게시하는 내용

실행할 때마다 전체 사본을 비공개 준비 영역으로 받아 온 뒤, 저장소에 반영하기 전에 검사합니다. 원본이 알린 브랜치, 태그, HEAD가 모두 알린 객체 그대로 있어야 하고 객체 그래프가 완전해야 합니다. 새 저장소는 완전할 때만 나타납니다.

OwnGit은 브랜치와 태그만 게시합니다. notes, replace ref, 풀 리퀘스트 ref 같은 다른 ref는 건너뜁니다. HEAD가 `refs/heads/` 밖을 가리키는 원본은 거부합니다.

새로고침은 OwnGit 저장소에서 한 작업을 덮어쓰지 않습니다. ref마다 다음과 같이 처리합니다.

- 없는 ref는 만들고, 같은 ref는 그대로 둡니다.
- 브랜치는 OwnGit이 이 원본 URL에서 마지막으로 본 값을 아직 가지고 있을 때, 또는 그 값에서 앞으로만 나아갔고 새 원본 값이 지금 OwnGit에 있는 값을 포함할 때만 원본을 따라갑니다.
- 태그는 OwnGit이 마지막으로 본 태그와 정확히 같을 때만 바뀝니다.
- 그 밖의 경우는 **원본과 다름**(divergent)으로 봅니다. OwnGit의 ref를 그대로 두고 실행 결과에 보고합니다.

OwnGit에 이미 있는 ref와 대소문자만 다른 이름의 원본 브랜치나 태그는 만들지 않고, 실행 결과에 원본과 다른 ref로 집계합니다. 원본 ref를 가져오려면 둘 중 하나의 이름을 바꾸거나 지우세요.

원본에서 지운 브랜치나 태그를 OwnGit에서 지우지는 않으며, **가져오기** 탭과 `import status`에 **원본에서 삭제됨**으로 표시합니다. 교체된 이전 값은 모두 보관된 기록에 남습니다. 원본 URL을 바꾼 뒤에는 OwnGit이 새 원본의 ref를 아직 본 적이 없으므로, 값이 다른 ref는 교체하지 않고 **원본과 다름**으로 보고합니다.

새로고침은 같은 원본의 이전 가져오기에서 OwnGit이 저장소의 HEAD를 정했고 그 뒤로 아무것도 HEAD를 바꾸지 않았을 때만 HEAD를 바꿉니다. 그렇지 않으면 HEAD를 그대로 두고, 실행 결과에 원본과 다른 ref로 집계합니다.

저장소 훅과 설정은 복사하지 않습니다. 저장소와 객체 형식(SHA-1 또는 SHA-256)이 다른 원본은 실패하고, 대소문자만 다른 ref 이름은 거부합니다. 브랜치, 태그, HEAD 대상의 이름이 417바이트보다 긴 원본은 아무것도 게시하기 전에 `unsupported_refs`로 실패합니다. 가져오려면 원본에서 그 이름을 줄이세요.

### Git LFS

OwnGit은 받아 온 객체에서 Git LFS 포인터 파일을 찾습니다. 검사 한도는 객체 200,000개, 후보 파일 100,000개, 후보 내용 32 MiB입니다. 포인터를 찾거나 이 한도 안에서 검사를 마치지 못하면 `git_lfs_required`로 실행을 멈춥니다. **Git 내용만 받기**에 동의하면 가져오기를 계속하고, 포인터 파일은 그대로 두며, 상태에 내용이 불완전하다고 표시합니다. LFS 객체 자체는 절대 내려받지 않습니다. OwnGit은 `.gitattributes`를 읽지 않으므로, 검사에서 아무것도 나오지 않았다고 해서 저장소가 LFS를 쓰지 않는다는 증거는 아닙니다.

### 실패와 취소

저장소마다 한 번에 하나의 실행만 진행됩니다. 그동안 들어온 다른 요청은 `busy`를 돌려받습니다. 실행 시간은 기본값으로 60분까지입니다. 취소한 실행은 `cancelled`로, 시간 한도에 걸린 실행은 `limit`로 기록합니다. 그 밖의 충돌은 `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved`, `nothing_to_resolve`를 돌려줍니다.

`owngit serve`가 멈추면 실행 중인 가져오기를 취소하고, 각각이 결과를 기록할 때까지 최대 45초 기다립니다. 다음에 시작할 때 OwnGit은 중단된 실행을 표시하고, 진행 중이던 게시를 저장소와 대조해 확인한 결과를 기록합니다. 쓰기를 반복하거나 되돌리지는 않습니다. 가져오기 서비스를 시작할 수 없으면 가져오기 페이지와 `import status`에 그렇게 표시되고, 일반 Git 서비스는 계속 동작합니다.

### 미해결 게시

OwnGit이 게시가 어떻게 끝났는지 증명할 수 없으면 그 게시는 미해결 상태가 됩니다. 예를 들어 ref는 썼지만 HEAD는 쓰지 못한 경우입니다. 소유자가 저장소를 현재 상태 그대로 인정하기 전까지 새로고침은 거부됩니다.

1. 저장소의 브랜치, 태그, HEAD와 마지막 실행에 적힌 이유를 확인합니다. 남기고 싶지 않은 것은 일반 Git 작업으로 고칩니다.
2. 가져오기가 실행 중이 아닌지 확인합니다. 실행이나 다른 Git 작업이 저장소를 쓰고 있으면 `busy`로, 미해결 게시가 없으면 `nothing_to_resolve`로 거부합니다.
3. `owngit import resolve PROJECT` 또는 **가져오기** 탭의 **현재 저장소 상태 인정** 버튼으로 해결합니다. OwnGit은 현재 ref와 HEAD를 인정한 상태로 기록합니다. Git에는 아무것도 쓰지 않으며 이전 실행 기록도 바꾸지 않습니다.
4. 새로고침합니다. 원본과 같은 ref는 그대로 두고, 마지막으로 확인한 원본 값을 아직 가진 ref는 원본을 따라가며, 나머지는 **원본과 다름** 상태로 남습니다.

처음 가져오기가 미해결인데 그 저장소가 아직 없으면 `import resolve`는 거부합니다. OwnGit을 다시 시작하세요. 문제가 계속되면 그 가져오기의 `.owngit-create-*` 디렉터리를 저장소 폴더 밖으로 옮기고 다시 시작하세요.

## 명령줄 풀 리퀘스트

일반 푸시로는 풀 리퀘스트가 생기지 않습니다. 서로 다른 원본 브랜치와 대상 브랜치를 푸시한 뒤 풀 리퀘스트를 만드세요. `--review`는 선택 사항입니다.

```sh
./bin/owngit pr create \
  --server http://HOST:7654 \
  --accept-insecure-http \
  --repository PROJECT \
  --source feature-branch \
  --target main \
  --title "Describe the change" \
  --review request \
  --password-file /path/to/owner-only-shared-password-file
```

리뷰를 일부러 생략할 때는 `--review skip`을 씁니다. 생략은 승인이 아니라 생략으로 기록됩니다. `--review`를 빼면 리뷰를 요청하지 않으며, 나중에 `pr review request`를 실행할 수 있습니다. 일반 접근이 열려 있으면 `--password-file`을 빼세요. 이 파일에는 관리자 비밀번호가 아니라 일반 접근용 공용 비밀번호를 넣으며, `reset-admin`과 같은 소유자 전용 검사를 거칩니다.

원본 브랜치와 대상 브랜치 한 쌍에는 풀 리퀘스트를 하나만 열어 둘 수 있습니다. 두 번째 풀 리퀘스트를 만들면 `pull_request_exists`로 거부되고, `error.details.number`가 열려 있는 풀 리퀘스트 번호를 알려 줍니다. 웹 화면에서는 그 풀 리퀘스트로 가는 링크를 보여 줍니다. 이 규칙이 생기기 전에 만든 풀 리퀘스트는 그대로 열려 있고 이전처럼 동작합니다.

더 필요 없는 풀 리퀘스트는 페이지의 풀 리퀘스트 닫기나 `pr close`로 병합하지 않고 닫을 수 있습니다. 닫아도 브랜치는 바뀌지 않습니다. 닫힌 풀 리퀘스트는 목록에 닫힘으로 남고 기록도 유지되며, 리뷰나 병합을 할 수 없고 그 브랜치 쌍도 더 이상 차지하지 않으므로 새 풀 리퀘스트를 열 수 있습니다. 풀 리퀘스트 다시 열기나 `pr reopen`으로 다시 열 수 있지만, 같은 쌍으로 열려 있는 다른 풀 리퀘스트가 있으면 `pull_request_exists`로 거부됩니다. 병합된 풀 리퀘스트는 닫거나 다시 열 수 없습니다(`pull_request_merged`). 닫기와 다시 열기에는 병합과 같은 접근 권한이 필요합니다.

일반 HTTP에서는 비밀번호와 풀 리퀘스트 내용이 네트워크에 노출됩니다. `--accept-insecure-http`는 그 명령에 한해 동의를 기록합니다. HTTPS라면 빼세요. CLI는 URL에 넣은 인증 정보를 거부하며 리디렉션을 따라가지 않습니다.

다른 `pr` 명령도 같은 `--server`, `--accept-insecure-http`, `--repository`, `--password-file` 플래그를 받습니다. 아래에서는 생략했습니다. OwnGit 저장소의 클론 안에서는 `--server`와 `--repository`를 클론의 `origin` 원격에서 가져오므로 생략할 수 있으며, 이때 비밀번호 파일은 첫 줄에 그 서버가 적혀 있을 때만 보냅니다([클론 안에서 실행하기](CODING_TOOLS.ko.md#클론-안에서-실행하기), [자격 증명 파일과 서버 줄](CODING_TOOLS.ko.md#자격-증명-파일과-서버-줄) 참고). `pr show`는 현재 원본과 대상의 객체 ID를 알려 주며, 모든 리뷰 결정과 병합에는 두 값을 모두 넘겨야 합니다.

```sh
./bin/owngit pr list
./bin/owngit pr show --number 1
./bin/owngit pr diff --number 1
./bin/owngit pr review request --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
./bin/owngit pr review submit --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID \
  --decision approved --reviewer "existing-tool: reviewer label"
./bin/owngit pr review skip --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
./bin/owngit pr merge --number 1 --source-oid SOURCE_OID --target-oid TARGET_OID
./bin/owngit pr close --number 1
./bin/owngit pr reopen --number 1
```

제출하는 리뷰는 `approved` 또는 `changes_requested`입니다. 리뷰어 라벨은 누가 리뷰를 제출했는지 기록할 뿐, 독립적인 리뷰였다거나 체크를 실행했다는 뜻은 아닙니다. 대기 중이거나 변경을 요청한 리뷰가 병합을 막지는 않습니다. 원본이나 대상이 움직이면 이전 리뷰와 생략 결정은 더 이상 적용되지 않으므로, 풀 리퀘스트를 다시 살펴보고 새 객체 ID로 결정하세요.

`pr diff`는 풀 리퀘스트가 바꾸는 내용을 비교한 정확한 객체 ID와 함께 출력합니다. `--stat`은 패치를 빼고, `--patch`는 패치만 출력합니다. 커밋 쌍 고정, 제한, 결과 형식은 [풀 리퀘스트 변경 내용](CODING_TOOLS.ko.md#풀-리퀘스트-변경-내용)에 있습니다.

모든 명령은 JSON 결과를 출력합니다. 실패하면 바뀌지 않는 `error.code`와 0이 아닌 종료 코드를 냅니다. 서버에 연결하지 못하면 `connection_failed`에 연결 거부나 TLS 오류 같은 원인이 함께 나옵니다. 풀 리퀘스트 결과의 `checks`는 현재 원본 리비전에 기록된 체크 결과를 알려 주며, 기록이 없으면 `absent`입니다. 그 결과가 원본 리비전에 커밋된 `.owngit/checks.json`과 다른 체크를 실행했다면 `stale`입니다. 다른 브랜치에서 기록된 설정은 영향을 주지 않습니다. 실패했거나, 오래되었거나, 워킹 트리에 변경이 있었거나, 불완전한 체크는 참고용이며 병합을 막지 않습니다.

풀 리퀘스트 페이지와 풀 리퀘스트를 만드는 페이지에 보이는 변경 내용은 원본 브랜치가 대상 브랜치에서 갈라진 뒤에 바꾼 내용입니다. 즉 GitHub처럼 두 브랜치의 병합 기준(merge base)에서 원본까지의 차이이며, 그사이 대상 브랜치에 새로 들어온 변경은 보이지 않습니다. 두 브랜치에 공통 커밋이 없거나 병합 기준이 여러 개이면(예: 두 브랜치를 서로에게 병합한 뒤) 브랜치 끝끼리 비교하거나 기준 하나를 임의로 고르지 않고, 그 이유를 알리며 변경 목록을 표시하지 않습니다. 너무 커서 끝까지 읽지 못한 비교는 일부만 보여 준다고 표시합니다. 리뷰와 병합은 지금처럼 정확한 원본 커밋과 대상 커밋에 적용됩니다.

병합은 fast-forward를 하거나, 이전 대상을 첫째 부모로, 원본을 둘째 부모로 하는 새 병합 커밋을 만듭니다. 작성자는 `OwnGit <owngit@localhost>`이고 메시지에는 풀 리퀘스트 번호와 제목이 들어갑니다. squash, rebase, 강제 갱신을 하지 않으며, 원본 브랜치를 지우거나 누구의 워킹 트리도 바꾸지 않습니다. 병합하려면 OwnGit 호스트에 Git 2.38 이상이 필요합니다. 더 오래된 Git에서는 `unsupported_git`을 돌려주고, 다른 Git 기능은 계속 동작합니다. 병합을 다시 시도하거나 병합이 중간에 끊겨도 병합 커밋이 두 번 만들어지지 않습니다. 다른 방법으로 이미 병합해 대상 브랜치에 원본이 이미 들어 있으면, Git의 "Already up to date"처럼 커밋을 만들지 않고 대상 브랜치를 그대로 둡니다. 이때 풀 리퀘스트는 병합됨으로 기록되며, `merge.mode`는 `up_to_date`, `merge.oid`는 바뀌지 않은 대상 커밋입니다.

## 프로젝트 체크

OwnGit은 체크 에이전트(helper)로 직접 실행한 체크를 기록하고, 소유자가 켠 자동 체크를 실행합니다.

체크 에이전트는 사용자의 작업 환경에서 실행되며, 리비전에 묶인 결과를 올립니다. 사용자의 환경과 권한을 그대로 물려받으므로 샌드박스가 아닙니다. 체크는 사용자 계정이 접근할 수 있는 파일과 인증 정보를 읽을 수 있습니다. OwnGit은 시도할 때마다 워킹 트리 상태를 기록하며, 변경이 있거나 상태를 알 수 없는 워킹 트리를 테스트한 커밋으로 보고하지 않습니다.

자동 체크는 OwnGit 계정으로, 제한된 로컬 Docker 컨테이너에서, 또는 별도로 연결한 러너에서 실행됩니다. 커밋된 `.owngit/checks.json`, 소유자 정책, 유효한 동의가 모두 있어야 합니다. [자동 체크](AUTOMATIC_CHECKS.ko.md)를 보세요.

관리자 비밀번호로 저장소 범위의 체크 에이전트 토큰을 만듭니다. 토큰은 소유자만 읽을 수 있는 `--output` 파일에만 쓰이고, 서버에는 해시로만 저장됩니다.

```sh
./bin/owngit helper-credential create \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --label laptop \
  --password-file /path/to/admin-password-file \
  --output ~/.owngit-helper-token
```

`--output` 위치에 파일이나 심볼릭 링크가 이미 있으면 교체하지 않고 알려 줍니다. 만들기나 전달이 실패하면 OwnGit은 다른 사람의 파일을 지울 위험을 피하려고 출력 파일을 그대로 둡니다. 다시 시도하기 전에 그 파일을 확인하고 지우세요. 응답을 받지 못하면 명령이 새 토큰을 취소합니다. 취소를 확인하지 못하면 직접 취소할 수 있도록 생성 식별 정보를 출력합니다. 토큰 자체는 출력하지 않습니다. `helper-credential list`와 `helper-credential revoke --id ID`로 토큰을 관리하며, 취소한 토큰은 바로 쓸 수 없게 됩니다. 관리자는 저장소 **체크** 탭의 **체크 에이전트 토큰** 링크에서도 토큰을 발급하고 취소할 수 있습니다. 브라우저에 로그인해 있어도 발급과 취소에는 항상 관리자 비밀번호를 묻습니다.

고정된 작업을 만든 뒤 체크를 실행합니다.

```sh
./bin/owngit check task new \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --title "Fix the failing build"

./bin/owngit check run \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --credential-file ~/.owngit-helper-token \
  --task TASK_ID --check "unit=go test ./..." --check "lint=go vet ./..."
```

[코딩 도구](CODING_TOOLS.ko.md)에서 이 명령들과 수정 라운드, 결과 필드, 종료 코드를 설명합니다. Windows의 `cmd.exe`는 알 수 없는 명령에 종료 코드 1을 돌려주므로 OwnGit은 그 결과를 `failed`로 기록합니다.

체크 명령의 출력을 그대로 담은 원본 로그는 `owngit.sqlite`에 저장됩니다. 로그마다 256 KiB까지이고 기본값으로 30일 동안 보관합니다. 로그가 만료된 뒤에도 작업과 시도 기록은 남습니다. 만료된 로그를 읽으면 `log_expired`를, 그 전에 사라진 로그는 `log_missing`을 돌려줍니다. 무결성 검사에 실패한 로그는 거부하고, 잘린 로그는 잘렸다고 표시합니다. 결과를 저장하는 중에 데이터베이스가 가득 찼거나 I/O 오류가 나면, OwnGit은 원본 로그 없이 결과만 저장하고 그 시도에 로그 오류를 기록합니다.

## 준비 중인 저장소

`owngit serve`는 시작할 때 각 저장소를 준비한 뒤 제공합니다. 저장소의 안전 설정과 기록 보관 훅을 확인하고, 이전 실행이 끝내지 못한 풀 리퀘스트 작업을 마무리합니다. 한 번에 저장소 8개까지 준비합니다. 시작 과정은 이 준비를 최대 10초 기다립니다. 그때까지 준비되지 않은 저장소는 준비가 끝나는 대로 제공합니다.

OwnGit이 실행되는 동안 대시보드가 목록을 만들 때 어떤 저장소의 폴더를 읽지 못하면(예를 들어 폴더가 옮겨졌거나, 권한이 바뀌었거나, 공유가 마운트되지 않은 경우), 그 저장소를 잠그고 같은 방식으로 다시 준비합니다. 폴더는 읽을 수 있지만 Git 데이터를 읽지 못하는 저장소는 잠그지 않습니다. 대시보드는 그 저장소에 **읽지 못함** 라벨을 붙이고 활동 집계에서 빼며, 서버 로그에는 원인이 한 번 남고, Git은 오류를 직접 알립니다. 어느 경우든 대시보드는 다른 저장소를 계속 보여 줍니다.

준비에 실패했거나 준비가 끝나지 않은 저장소는 잠긴 상태로 남고, 다른 저장소는 평소대로 제공됩니다. 잠겨 있는 동안에는 다음과 같습니다.

- Git clone, fetch, 푸시는 HTTP 503과 `repository is being prepared; try again later` 메시지를 받습니다.
- 저장소 페이지에는 준비 중이라고 표시되고, API는 오류 코드 `repository_preparing`으로 응답합니다. 대시보드는 **준비 중** 라벨을 붙여 목록에 보여 주고, 활동 집계에서 뺍니다.
- 이 저장소의 예약 가져오기와 프로젝트 체크는 기다립니다. 실패로 기록되지 않으며, 준비가 끝나면 실행됩니다.
- 관리자는 여전히 저장소를 삭제할 수 있고(준비 시도가 진행 중일 때는 제외), 체크 에이전트 토큰과 러너 토큰을 발급하고 취소할 수 있습니다.

OwnGit은 실패한 준비를 스스로 다시 시도합니다. 시도가 끝나고 30초 뒤에 다시 시도하고, 그다음부터는 직전 대기 시간의 두 배를 기다리며 최대 10분까지 늘립니다. 폴더를 읽지 못해서 실패했다면 5초마다 폴더를 확인해, 다시 읽을 수 있게 되는 즉시 다시 시도합니다. 끝나지 않은 시도가 있으면 다른 시도를 겹쳐 실행하지 않습니다. 서버 로그에는 실패할 때마다 저장소 이름과 원인이 남고, 저장소를 다시 제공하기 시작할 때도 기록이 남습니다. 페이지에는 원인이 나오지 않습니다. 원인(예를 들어 마운트되지 않은 디스크, 파일 권한, 로그에 인용된 충돌하는 Git 설정)을 고친 뒤 다음 재시도를 기다리거나, OwnGit을 다시 시작해 바로 다시 시도하세요.

다만 OwnGit이 상태 데이터베이스에서 저장소 목록을 읽지 못하면 시작을 거부합니다.

## 압축 파일 내려받기

**코드** 탭의 최상위 폴더에서 선택한 브랜치나 태그를 ZIP 또는 tar.gz 파일로 내려받을 수 있고, 커밋 페이지에서는 그 커밋을 내려받을 수 있습니다. 압축 파일에는 Git 기록 없이 그 리비전의 파일만 `PROJECT-REF` 폴더(예: `project-main`) 하나에 담깁니다. `git archive`처럼 그 리비전에 커밋된 `export-ignore`와 `export-subst` 속성을 따르므로, **코드** 탭에 보이는 파일과 다를 수 있습니다. 파일 이름도 같습니다. 문자, 숫자, `.`, `-`, `_`가 아닌 글자는 `-`로 바뀌므로 `feature/login`은 `project-feature-login.zip`이 됩니다. 내려받으려면 **코드** 탭을 읽을 때와 같은 접근 권한이 필요합니다.

브라우저 없이 받으려면 API 경로를 씁니다. `ref`는 짧은 이름이나 `refs/heads/main` 같은 전체 이름으로 쓴 브랜치나 태그, 또는 전체 커밋 ID이며, 생략하면 기본 브랜치입니다. `format`은 `zip` 또는 `tar.gz`입니다. 없는 ref나 형식에는 404로 답합니다. 공유 비밀번호로 보호할 때는 `--user owngit`을 넣으면 curl이 비밀번호를 묻습니다. 접근이 열려 있으면 빼세요.

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

`--remote-header-name`을 쓰면 curl은 전체 이름을 읽지 못하는 프로그램을 위해 OwnGit이 함께 보내는 ASCII 파일 이름만 씁니다. 이름에 한글처럼 ASCII가 아닌 글자가 있으면 이 ASCII 이름은 저장소 이름과 커밋 ID 앞 12글자로 이루어지며(예: `project-1a2b3c4d5e6f.zip`), 압축 파일 안의 폴더는 전체 이름을 그대로 씁니다. 브라우저는 전체 이름을 씁니다. curl로 전체 이름을 지정해 저장하려면 `--output`으로 이름을 넘기세요. `--data-urlencode`는 ref를 인코딩합니다.

```sh
curl --fail --get --user owngit \
  --data-urlencode 'ref=기능/로그인' --data format=zip \
  --output 'project-기능-로그인.zip' \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive'
```

압축 파일 내려받기도 Git 전송이므로 아래 제한을 똑같이 받습니다. 최대 4 GiB, 30분이며, 이 시간에는 자리나 같은 저장소의 푸시를 기다리는 시간도 들어갑니다. 실행 중인 Git 요청의 자리 하나를 씁니다. 시간 안에 내려받기를 시작하지 못하면 `Retry-After`와 함께 HTTP 503으로 답합니다. OwnGit은 Git이 압축 파일을 만드는 동안 바로 보냅니다. Git이 실패하거나, 제한에 닿거나, 끝나기 전에 OwnGit이 멈추면 응답을 마무리하지 않은 채 연결을 닫습니다. 그래서 내려받기는 완료되지 않고 실패합니다. curl은 `(18) transfer closed with outstanding read data remaining` 같은 오류로 끝나고, 브라우저는 내려받기가 실패했다고 표시합니다. 받은 부분도 올바른 압축 파일이 아닙니다. OwnGit은 ZIP 파일의 끝부분과 tar.gz 파일의 gzip 트레일러를 Git이 성공적으로 끝난 뒤에만 보내기 때문입니다. Git이 아무것도 쓰기 전에 실패하면 대신 HTTP 오류로 답합니다.

## Git 전송 제한

- clone, fetch, 푸시 같은 Git 요청 하나는 최대 4 GiB까지 보내거나 받을 수 있고, 30분 안에 끝나야 합니다. 크기 제한을 넘는 푸시는 HTTP 413으로 거부됩니다. 어느 한쪽 제한을 넘는 clone이나 fetch는 중간에 끊기고, Git은 전송이 완료되지 않았다고 알립니다. 서버 로그에도 실패가 남습니다. 일시 중지된 clone이나 도중에 멈춘 업로드처럼 클라이언트가 60초 동안 데이터를 주고받지 않는 전송도 끊깁니다. Git이 스스로 작업하는 시간은 여기에 들어가지 않지만, 클라이언트는 데이터를 계속 주고받아야 하므로 초당 몇 KB 수준의 아주 느린 연결은 끊길 수 있습니다. 이 제한은 이 버전에서 고정되어 있으며 바꾸는 옵션은 없습니다. OwnGit은 Git LFS를 제공하지 않으므로, 전체 기록이 4 GiB보다 큰 저장소는 OwnGit으로 clone할 수 없습니다. 큰 바이너리 파일은 Git 기록에 넣지 마세요.
- Git 요청은 한 번에 5개까지 실행됩니다. 한 저장소는 그중 4개까지 쓸 수 있고, 다섯 번째 자리는 실행 중인 요청이 없는 저장소만 씁니다. 그래서 한 저장소의 느린 전송이 다른 저장소를 막지 않습니다. 빈자리가 없으면 요청은 최대 90초 기다린 뒤 HTTP 503과 `Git service is busy with other transfers; try again shortly` 메시지를 받고, 서버 로그에도 기록됩니다. Git 명령을 다시 실행하세요.
- 공유 비밀번호로 보호할 때, 한 주소에서 10분 안에 비밀번호를 4번 틀리면 그 주소는 15분 동안 막힙니다. 올바른 비밀번호는 세지 않으므로, 올바른 비밀번호로 Git 명령 여러 개를 동시에 실행해도 됩니다. 관리자 비밀번호에도 같은 제한이 따로 적용됩니다.
- OwnGit을 멈추면 실행 중인 요청을 최대 10초 기다린 뒤, 느린 clone처럼 그때까지 끝나지 않은 요청을 끝내고 끝낸 Git 전송 수를 로그에 남깁니다. 이는 정상적인 종료입니다.

## 저장 공간

- 상태 디렉터리는 플랫폼의 설정 디렉터리 아래 `owngit`이며, 설정 디렉터리를 쓸 수 없으면 `~/.owngit`입니다. 여기에 `owngit.sqlite`가 있고, 데이터베이스를 쓰는 동안에는 `-wal`과 `-shm` 파일도 생깁니다. 상태 디렉터리는 로컬 저장 장치에 두고, 다른 컴퓨터와 함께 쓰는 네트워크 공유에는 두지 마세요. Windows 네트워크(UNC) 경로는 거부합니다.
- 가져오기 원본의 인증 정보(토큰, Basic 비밀번호, 원본 CA)는 상태 디렉터리 안의 `import-credentials/NAME.json`에 저장소마다 파일 하나씩, 암호화하지 않은 JSON으로 저장됩니다. OwnGit은 이 폴더와 파일을 OwnGit을 실행하는 계정만 읽을 수 있게 제한하며, 백업에는 절대 넣지 않습니다. 그 계정으로 상태 디렉터리를 읽을 수 있으면 누구나 이 비밀값을 읽을 수 있으므로, 상태 디렉터리를 비밀값 자체처럼 보호하세요.
- 저장소 폴더는 설정할 때 고릅니다. 별도 디스크나 마운트한 SMB, NFS 공유에 두어도 되지만, 한 번에 OwnGit 하나만 쓰게 하세요. OwnGit은 폴더에 이미 있는 파일은 건드리지 않으며, 저장소를 `.git`으로 끝나는 bare 저장소로 만듭니다.
- OwnGit은 실행되는 동안 저장소 폴더의 `.owngit-serve.lock` 파일을 잠가 둡니다. 같은 폴더를 쓰는 두 번째 서버(예: 상태 디렉터리를 복사해서 시작한 서버)는 실행 중인 서버의 저장소 hook을 다시 쓰게 되므로, 폴더 이름을 알려 주고 시작하지 않습니다. 멈춘 상태 디렉터리를 옮기는 것은 전과 같이 됩니다. 공유가 아직 마운트되지 않은 경우처럼 시작할 때 폴더가 비어 있거나 없으면, 그 폴더에 처음 쓰기 전에 잠급니다. 시작할 때 다른 이유로 잠그지 못하면, 확인하지 못했다는 내용을 서버 로그에 남기고 그대로 시작합니다. 네트워크 공유에서 다른 컴퓨터의 서버까지 알아차리는지는 공유의 파일 잠금 지원에 달려 있습니다.
- 긴 클론 뒤에서 기다리는 푸시처럼 어떤 Git 작업이 저장소를 붙잡고 있으면, 대시보드는 그 저장소를 최대 1초만 기다립니다. 그 뒤에는 마지막으로 읽은 브랜치 목록을 보여 주거나, 그 저장소를 **사용 중**으로 표시합니다. 지난번에 Git 데이터를 읽지 못한 저장소는 계속 **읽지 못함**으로 표시하고, 그사이 삭제된 저장소는 목록에서 뺍니다. 그 저장소의 페이지는 요청 기한 직전까지 기다린 뒤, 다른 Git 작업이 저장소를 쓰고 있다고 답합니다(HTTP 503과 `Retry-After`).
- 활동 그래프와 최근 활동은 서버가 시작할 때 백그라운드에서 집계하고, 브랜치가 바뀐 뒤 페이지를 열면 다시 집계합니다. 브랜치가 바뀔 때까지는 집계 결과를 재사용합니다. 느린 공유에서는 집계가 끝나기 전에 대시보드가 나타날 수 있습니다. 이때는 일부 저장소를 아직 집계하는 중이라고 표시하며, 새로 고치면 전체 집계가 보입니다.
- OwnGit은 쓰기 사이사이에 저장소마다 브랜치와 태그를 기억해 두므로, 대시보드를 열 때마다 공유에 있는 모든 저장소를 다시 읽지 않습니다. OwnGit을 통한 푸시, 병합, 가져오기, 되돌리기, 삭제, 기본 브랜치 변경은 다음 페이지에 바로 반영됩니다. OwnGit을 거치지 않고 저장소 폴더에서 ref를 직접 바꾸면, OwnGit이 그 저장소의 ref를 다음으로 바꾸거나 다시 시작한 뒤에 페이지에 반영됩니다. ref를 하나도 바꾸지 않은 푸시나 예약 가져오기는 여기에 해당하지 않습니다.
- OwnGit은 최근에 읽은 폴더 목록, 4 MiB 이하의 파일, 커밋 diff, 풀 리퀘스트 비교도 합계 64 MiB까지 메모리에 둡니다. 이 내용은 절대 바뀌지 않는 커밋과 파일을 기준으로 저장하므로, 다시 연 페이지(예: 다시 돌아온 파일)는 Git 프로세스를 시작하지 않습니다. OwnGit이 마지막으로 읽은 뒤 저장소의 브랜치와 태그가 바뀌지 않았다면, 저장소를 붙잡은 푸시도 기다리지 않습니다. 저장소를 삭제하면 그 저장소의 내용을 버리고, 다시 시작하면 모두 버립니다.
- OwnGit은 아무도 쓰지 않는 저장소를 정리해서, 푸시가 쌓여도 읽기가 느려지지 않게 합니다. 저장소가 바뀐 뒤 5분 동안 푸시나 요청이 없으면 흩어진 ref와 객체를 묶고 commit-graph를 갱신합니다. 시작한 뒤에는 모든 저장소에 이 정리를 한 번씩 합니다. 또 OwnGit 컴퓨터의 현지 시각으로 03:00부터 05:00 사이에는 pack이 20개보다 많은 저장소의 pack을 하나로 합칩니다. 이때도 그 시간 안에 저장소에 5분 동안 푸시나 요청이 없어야 합니다. 이 시간에 컴퓨터가 잠자기 상태이거나 OwnGit이 실행 중이 아니면 다음 날 밤으로 미룹니다. 정리는 객체나 보관된 기록을 절대 지우지 않습니다. 정리는 세 단계로 이루어지며, 저장소는 한 단계가 도는 동안에만 붙잡습니다. 단계가 도는 동안 그 저장소에 들어온 푸시, 페이지, 다른 Git 작업은 그 단계가 끝날 때까지 기다립니다. 네트워크 공유에 있는 큰 저장소에서는 수십 초가 걸릴 수 있고, 밤에 pack을 합칠 때는 더 걸립니다. 그러면 정리는 멈추고 그 작업을 먼저 보내며, 남은 단계는 저장소에 다시 5분 동안 쓰임이 없을 때까지 미룹니다. 다른 저장소의 페이지는 기다리지 않고, 대시보드는 위에서 설명한 대로 최대 1초만 기다립니다. 저장소를 삭제하면 그 저장소의 정리는 멈춥니다. 서버 로그에는 정리할 때마다 한 줄이 남습니다. OwnGit은 시작할 때, 시작 전에 중단된 Git 명령이 저장소에 남긴 임시 pack 파일과 정리용 잠금 파일(예: 정리 중에 컴퓨터가 다시 시작된 경우)을 지우고 그 이름을 서버 로그에 남깁니다.
- 새 저장소는 임시 이름 `.owngit-create-*`로 쓴 뒤 제자리로 이름을 바꿉니다. Windows에서는 백신이나 검색 색인이 새 디렉터리를 잠시 잠글 수 있습니다. OwnGit은 약 2초 동안 다시 시도하며, 그래도 오류가 계속되면 다시 시도하세요.
- 파일까지 지우는 저장소 삭제는 먼저 임시 이름 `.owngit-delete-*`로 바꾼 뒤 지웁니다. 파일을 남긴 저장소는 `.owngit-removed` 폴더로 갑니다. `.owngit-deletion-*` 파일은 아직 끝나지 않은 삭제를 표시합니다. [저장소 삭제하기](#저장소-삭제하기)를 보세요.
- 저장소 이름은 `.git`으로 끝날 수 없고, 확장자가 있든 없든 `CON`, `AUX`, `NUL`, `COM1`, `LPT1` 같은 Windows 장치 이름을 쓸 수 없습니다. `new`와 `new-import`는 예약된 이름입니다. 이 규칙은 모든 플랫폼에 적용됩니다.
- 만료된 로그가 차지하던 공간은 데이터베이스 안에서 재사용되지만, 파일 크기는 줄지 않고 이전 데이터는 안전하게 지워지지 않으며 전체 크기 제한도 없습니다. OwnGit은 `VACUUM`을 실행하지 않습니다.
- 시작할 때 `-wal`이나 `-shm` 파일이 있으면 OwnGit은 검사를 위해 데이터베이스와 WAL을 비공개 임시 디렉터리로 복사합니다. 임시 볼륨에 그만큼 여유 공간이 필요합니다.
- 시작할 때 OwnGit은 이전에 커밋된 버전이나 이전 OwnGit 릴리스의 데이터베이스를 그 자리에서 업그레이드합니다. 그 뒤의 스키마 변경을 한 트랜잭션 안에서 차례로 모두 적용합니다. 업그레이드하면 서버 로그에, `backup` 같은 오프라인 명령이면 표준 오류에 `state database upgraded from schema 14 to 15`처럼 한 줄을 남기므로, 이전 빌드가 언제부터 이 데이터베이스를 거부하는지 알 수 있습니다. 더 새롭거나 알 수 없는 버전, 또는 출시되지 않은 개발 빌드의 데이터베이스는 거부하고 파일을 그대로 둡니다. 새 빌드가 업그레이드한 데이터베이스는 이전 빌드가 거부하므로, 실행 파일을 바꾸기 전에 지금 쓰는 실행 파일로 백업하세요. 새 빌드의 명령은 `backup`을 포함해 상태를 여는 모든 명령이 먼저 데이터베이스를 업그레이드합니다. 업그레이드한 뒤 OwnGit 1.0으로 되돌리려면 OwnGit 1.0이 만든 백업을 복원하세요. OwnGit 1.0은 업그레이드된 데이터베이스도, 이 버전이 만든 백업도 거부합니다.
- OwnGit은 이전 버전이 남긴 `logs/` 디렉터리를 읽거나 지우지 않습니다. 그 디렉터리를 쓰는 이전 OwnGit 프로세스가 없으면 직접 지우세요.
- `owngit` 실행 파일을 지워도 상태 디렉터리와 저장소는 그대로 남습니다. 더 필요 없을 때만 직접 지우세요.
- 내장 AI 리뷰가 있던 출시되지 않은 개발 빌드의 데이터베이스에는 리뷰 기록과 제공자 토큰이 남아 있을 수 있습니다. OwnGit은 이를 쓰지도 지우지도 않습니다. 백업은 토큰을 복사하지 않으며, 리뷰 기록이 남아 있는지 확인해 하나라도 있으면 실행을 거부합니다.

## 오프라인 백업

보관된 기록은 강제 푸시와 삭제로부터 작업을 지켜 주지만 백업은 아닙니다. 한 번이라도 푸시한 비밀값은 강제 푸시나 브랜치 삭제 뒤에도 브라우저에 계속 보이고, 이후의 모든 백업에 들어갑니다. 그 기록을 없애는 방법은 [저장소를 파일까지 삭제](#저장소-삭제하기)하는 것뿐이며, 그 전에 만든 백업에는 여전히 남아 있습니다. 실수로 푸시한 비밀값은 새것으로 교체하세요. OwnGit은 백업을 예약해 주지 않습니다. 백업을 만들기 전에 OwnGit을 멈추세요. 출력 디렉터리는 아직 없어야 합니다.

```sh
./bin/owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

백업에는 매니페스트 하나와, 비어 있지 않은 저장소마다 Git 번들 하나가 들어갑니다. 포함되는 내용은 다음과 같습니다.

- OwnGit의 보관된 기록을 포함한 모든 ref, 저장소마다의 HEAD와 메타데이터
- 풀 리퀘스트, 리뷰, 병합 기록, 작업(task), 체크 설정, 체크 결과, 자동 체크 정책과 작업(job)
- 가져오기 원본, 실행 기록, 게시 기록
- 접근 모드와 비밀번호 해시

체크의 원본 로그, 모든 종류의 인증 정보와 토큰, 가져오기 예약, 동의, [업데이트 확인](#새-릴리스-알림) 설정은 들어가지 않습니다. 비밀번호 해시는 민감한 정보이므로 백업을 비공개로 보관하세요.

아직 없는 저장소에 대한 가져오기 게시가 정리되지 않았으면 백업을 거부합니다. OwnGit을 한 번 시작했다가 멈춰 기록을 정리하게 한 뒤 다시 백업하세요. 오류가 계속되면 그 가져오기의 `.owngit-create-*` 디렉터리를 저장소 폴더 밖으로 옮기고, OwnGit을 다시 시작했다가 멈춘 뒤 백업하세요.

매니페스트는 64 MiB로 제한됩니다. 이를 넘는 백업은 출력을 쓰지 않고 실패하며, 한도에 맞추려고 기록을 빼지 않습니다. 백업을 만드는 동안 내보낼 내용 전체를 메모리에 올립니다.

OwnGit은 백업 버전 1, 2, 9, 10을 복원하고 나머지는 거부합니다. 출시되지 않은 개발 빌드만 만들었던 버전 3부터 8까지도 거부합니다. 이전 빌드는 모르는 기록을 버리지 않고 더 새로운 백업을 거부합니다. 아직 없는 새 경로에 복원하세요.

```sh
./bin/owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

복원은 새 상태를 게시하기 전에 모든 번들, ref, 객체, 기록을 검사합니다. SHA-256 해시로 손상은 찾아내지만, 누군가 매니페스트와 함께 바꿔치기한 백업은 알아내지 못합니다.

복원한 뒤에는 다음과 같습니다.

- 로그인 세션, 설정 링크, 승인된 Host, 저장된 네트워크 설정, 인증 정보, 예약, 모든 동의가 사라집니다.
- 체크 에이전트 토큰과 러너 토큰을 새로 만드세요. 인증 정보가 필요한 가져오기는 새로고침하기 전에 가져오기 인증 정보를 다시 저장하세요.
- 자동 체크는 소유자가 다시 켤 때까지 꺼져 있고, 끝나지 않은 체크 작업은 다시 실행하지 않고 `interrupted`로 표시합니다.
- 정리되지 않은 가져오기 게시는 적용하지 않고 닫습니다.
- 체크의 원본 로그가 없으므로, 로그는 만료 전까지는 없음으로, 그 뒤에는 만료됨으로 읽힙니다.
- 다른 방식으로 쓰기 전에 먼저 복원한 상태로 `owngit serve`를 시작해, 시작 과정이 중단된 기록을 정리하게 하세요.

백업과 복원은 Git 파일 이름을 정확히 그대로 유지합니다. 백슬래시가 들어간 이름처럼 Git은 받아들이지만 Windows에서는 체크아웃되지 않을 수 있는 이름이 있습니다. 호환되는 워킹 트리에서 이름을 바꾸거나, bare clone으로 저장소를 살펴보세요.

복원이 중단되면 두 대상 어디에서도 OwnGit을 시작하지 말고, `.owngit-restore-pending` 표시 파일도 지우지 마세요. 두 대상과 `TARGET.owngit-restore-...` 형제 항목을 무엇도 합치거나 덮어쓰지 말고 별도의 격리 위치로 옮긴 뒤, 새 경로에 다시 복원하세요. 백업이 끝나기 전에 멈추면 출력 디렉터리는 생기지 않습니다. 실행 중인 백업 프로세스가 없을 때, 숨은 형제 항목 `.OUTPUT.owngit-backup-...`을 보관하거나 격리하세요.

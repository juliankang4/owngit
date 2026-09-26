# Operations

<p align="center"><b>English</b> | <a href="OPERATIONS.ko.md">한국어</a></p>

## First-time setup

From the source checkout:

```sh
go build -o bin/owngit ./cmd/owngit
./bin/owngit serve
```

The default address is `http://127.0.0.1:7654`. Setup configures repository storage, optional shared-password protection for general access, and a separate administrator password. Every later security-setting change asks for the current administrator password. Setup finishes at an empty dashboard, where New repository creates a repository. Its clone address has the form `http://HOST:7654/git/PROJECT.git`.

### Setup in the terminal

When `owngit serve` starts an installation that is not set up yet, both its input and its output are a terminal, and it runs in the foreground of that terminal, setup runs in that terminal. It asks for the language first (English or 한국어; Enter keeps the language of your locale, and L switches it later), then offers "Continue in this terminal" and "Open the web dashboard". Server log lines written while a question is open are held and shown between the questions. This path writes no setup file.

In the terminal, OwnGit asks what the web setup page asks, in the same order: the repository folder, who can read and write repositories, the administrator password, "Other devices", and, when OwnGit listens on a network address, whether to continue with a connection it does not encrypt. Each password is typed twice and nothing appears while you type. An answer keeps every character the web page keeps, including a pasted no-break space or emoji; only Enter, Backspace, Ctrl-U, Ctrl-C and Escape act as keys instead. Nothing is saved until you choose "Finish setup" on the review card. While a question is open, Ctrl-Z and Ctrl-D are not keys: like any other control character, they become part of the answer. If OwnGit is stopped during setup and continued in the background (for example `kill -STOP`, then `bg`), it keeps serving but stops asking; run `fg` to continue the questions. After setup, OwnGit no longer reads the terminal, so Ctrl-Z and `bg` leave the server running. Ctrl-C stops the server without saving anything; run `owngit serve` again to start over. When OwnGit listens only on this computer, the terminal does not ask about plain HTTP. If you later reach OwnGit from another device, confirm it on the Settings page, which asks until the confirmation is given, as for any installation without it. If you answer no to the plain HTTP question, OwnGit tells you how to keep it on this computer only: start again with `--listen 127.0.0.1:PORT`, or, when the network address comes from the saved [network settings](#network-settings), stop OwnGit, run `owngit network set --listen 127.0.0.1:PORT --base-url ""` and start it again, because a saved address applies at every start.

"Other devices" checks whether Tailscale runs on this computer. It finds the `tailscale` command as [sharing on the tailnet](#share-on-your-tailnet-over-https) does, including `owngit serve --tailscale PATH`, only runs `tailscale status --json`, and changes nothing. When Tailscale is running, the step shows this computer's Tailscale address and MagicDNS name and prints, on a line of its own, a command that saves them as [network settings](#network-settings), for example `owngit network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654`. Run it, then restart OwnGit after setup; the saved settings apply at every later start, a background service included. When you use a state directory other than the default, the command includes `--state-dir`. A path with a control or direction character, such as a tab, is written in ANSI-C quotes (`$'...'`), which zsh, bash and ksh read but a plain POSIX shell such as `dash` does not. Setup does not run or save that command. Tailscale encrypts the connection between devices, but OwnGit still reports plain HTTP because it cannot see that protection. To use an HTTPS address that OwnGit reports as encrypted, [share on your tailnet over HTTPS](#share-on-your-tailnet-over-https) instead. Press Enter to continue. If OwnGit runs as a service, leave `--listen` and `--base-url` out of the service definition; see [Options for a background service](#options-for-a-background-service).

### Setup in the browser, approved in the terminal

"Open the web dashboard" opens `http://127.0.0.1:7654/setup` in your browser; with `--no-open` the terminal prints the address instead. Nothing secret is in that address. In the browser, choose "Ask the terminal for approval". The page shows a short code, and the terminal shows "A browser wants to set up OwnGit" with the same code and the address the request came from. Answer `y` only when your browser shows that code. The approval applies to that one browser, which then continues setup on the web page.

- A request from another device is marked with a warning in the terminal.
- Only one browser can wait for approval at a time. Another browser is told to try again later.
- A rejected browser must wait a minute before asking again, and each address can ask at most five times in ten minutes.
- An unanswered request, or an approval the browser does not use, expires after ten minutes.
- Press T while waiting to set up in the terminal instead. A browser you already approved then loses its setup session.
- After you approved a browser, the terminal still shows a new request, for example from another browser of yours. Approving it ends the setup session of the browser approved before, and the request card says so.

When setup finishes in the browser, the terminal lists the saved answers and the server keeps running.

### Setup with a setup file

When OwnGit starts without a terminal, for example under `brew services`, a LaunchAgent, or systemd, with its output redirected, or as a background job of a shell (`owngit serve &`), it writes an owner-readable setup file inside the state directory and opens it in the installation owner's browser. With `--no-open`, or when the browser cannot be opened, the server log shows the file's path. The setup secret is not printed or passed in a browser command argument. `owngit setup-link` issues a new file, and it also works while setup waits in a terminal.

Before setup is finished, the setup link also works from another device by an address OwnGit was not started with, for example this computer's LAN address when OwnGit listens on every interface. `owngit setup-link --base-url http://192.168.1.20:7654` writes a setup file for that address. Until the link is used, that address shows only the page that uses it and refuses everything else. After the link is used, only that browser on that address can continue setup. The setup form then offers to keep accepting the address. If you leave it unticked, OwnGit refuses the address once setup is finished.

## Reaching the server from another device

OwnGit serves plain HTTP, so the connection is not encrypted, and it has no built-in TLS. For HTTPS, let Tailscale on this computer share it on your tailnet (see [Share on your tailnet over HTTPS](#share-on-your-tailnet-over-https)) or put a reverse proxy in front of it (see [Behind a reverse proxy](#behind-a-reverse-proxy)). Use Tailscale or your own VPN to reach its private-network address. A Tailscale-related name alone does not prove that the whole path is protected. Ordinary LAN HTTP also works: OwnGit shows a one-time warning before it accepts passwords, and the interface keeps the connection status visible. Do not expose OwnGit to the public Internet.

To use a LAN name:

```sh
./bin/owngit serve \
  --listen 0.0.0.0:7654 \
  --base-url http://gitbox.internal:7654 \
  --allowed-host gitbox.internal \
  --no-open
```

The server accepts only requests whose Host is `localhost`, `127.0.0.1`, `::1`, or an approved name. `--allowed-host` is repeatable. To approve another name permanently, run this on the installation host and restart the server:

```sh
./bin/owngit approve-host gitbox.internal
```

### Network settings

OwnGit can save the listen address, the base URL, the allowed Host names, and the trusted reverse proxies, so a server started without options, such as a background service, uses them at every start. Run these commands on the installation host. They work whether or not the server is running, and a change applies at the next start.

```sh
./bin/owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654 --allowed-host gitbox.internal
./bin/owngit network show
```

- `--listen` is `host:port`. An empty host, `0.0.0.0`, or `::` listens on every interface.
- `--base-url` is the address other devices use, an `http` or `https` origin with no path. OwnGit accepts its host name and shows it in clone addresses. Without a base URL, clone addresses on the pages use the address the browser connected to.
- `--allowed-host` and `--remove-allowed-host` change the stored list that `owngit approve-host` also adds to. Both are repeatable.
- `--trusted-proxy` and `--remove-trusted-proxy` change the reverse proxies whose forwarded headers OwnGit believes. Each takes an IP address or a CIDR range and is repeatable. See [Behind a reverse proxy](#behind-a-reverse-proxy).
- An empty value, such as `--base-url ""`, removes that saved value.

When you finish web setup from another device by a name that OwnGit accepts only for the current run, for example through a `--listen` or `--base-url` option, the setup form offers "Keep accepting this address after a restart". Ticking it saves the name as an allowed Host when setup finishes. Unticked, nothing is saved.

`set` prints a note when the listen address leaves this computer, because other devices then use plain HTTP. A reverse proxy with HTTPS or [Tailscale HTTPS](#share-on-your-tailnet-over-https) encrypts that connection. It also prints a note when the base URL uses `https` but no reverse proxy is trusted.

For each value, `owngit serve` uses its option if one is given, then the saved value, then the default (`127.0.0.1:7654`, with the base URL taken from the listen address). An option applies to that run only and does not change what is saved. `localhost`, `127.0.0.1`, and `::1` are always accepted, whatever is saved.

`owngit network show` lists the saved values. When a server is running on that state directory, it also shows what that server actually uses and whether a restart is needed for saved changes to apply. `--json` prints the same report as JSON.

The same settings are on the Settings page under Network. Everyone who can open Settings sees the saved values, the values the running server uses, and whether a restart is needed, as `network show` reports them, with the same notes that `network set` prints. Changing them there asks for the administrator password and uses the same checks as `network set`. A saved change applies at the next start, and the running server keeps its current values until then. If the new listen address reaches other devices and plain HTTP has not been accepted yet, the form also asks you to accept it, as setup does. If the settings changed after you opened the page, for example with `network set`, the save is refused and the page shows the current values. The page cannot reset the settings; use `owngit network reset` as described below.

If a saved value locks you out, for example a listen address that no longer exists on this computer, reset it on the installation host and restart the server:

```sh
./bin/owngit network reset
```

`reset` removes the saved listen address and base URL. It keeps the allowed Host names unless you add `--clear-allowed-hosts`, and it keeps the trusted proxies unless you add `--clear-trusted-proxies`. No web page can do this; it needs access to the state directory.

Network settings belong to this installation host. An offline backup does not carry them, and a restored installation starts with the defaults.

### Options for a background service

`--listen`, `--base-url`, `--allowed-host`, and `--trusted-proxy` are options of `owngit serve`, so they apply only to the command that starts the server. A service manager that passes them in the service definition (the `ProgramArguments` of a LaunchAgent, or the `ExecStart` line of a systemd unit) overrides the saved values at every start. To use saved settings, leave these options out of the service definition.

The Homebrew service (`brew services start owngit`) runs `owngit serve --no-open` without other options, so it uses the saved settings. To reach it from other devices:

```sh
owngit network set --listen 0.0.0.0:7654 --base-url http://gitbox.internal:7654
brew services restart owngit
```

### Share on your tailnet over HTTPS

When Tailscale runs on the computer that runs OwnGit, OwnGit can ask it to answer HTTPS for this computer's Tailscale name and pass the requests to OwnGit. Other devices signed in to your tailnet then open `https://NAME.TAILNET.ts.net/` and clone from addresses such as `https://NAME.TAILNET.ts.net/git/project.git`. Tailscale on this computer holds the certificate and encrypts the connection. When you open OwnGit at that address, the page header shows "Encrypted by Tailscale on this computer". Devices outside your tailnet cannot reach the address.

Tailscale must be installed and signed in on this computer, and the tailnet needs MagicDNS and HTTPS Certificates, both on the DNS page of the Tailscale admin console. On Linux, Tailscale changes its settings only for root or its operator. Allow your user once with `sudo tailscale set --operator=$USER`; OwnGit never runs `sudo`.

Turn sharing on in Settings, under "Share on your tailnet over HTTPS", with the administrator password, or on the installation host:

```sh
./bin/owngit tailscale on
./bin/owngit tailscale status
./bin/owngit tailscale off
```

Turning it on does the following:

1. OwnGit reads the current Tailscale Serve configuration first. If HTTPS port 443 of this computer already serves something else, including Tailscale Funnel, OwnGit changes nothing and shows what is there, with the command that removes it. The same applies to an address that already points at OwnGit but that OwnGit has no record of making, for example one left by an interrupted change, or one under an earlier name that this computer got back. The Settings page lists it, like addresses under earlier names and what Tailscale printed, only to an administrator session or right after the administrator password was entered; other viewers see only that the port is taken. `owngit tailscale status` always lists it.
2. It runs `tailscale serve --bg --https=443 http://127.0.0.1:PORT`, where PORT is OwnGit's port, and reads the configuration back to confirm that the address points at OwnGit.
3. It saves the HTTPS address as the base URL, the Tailscale name as an allowed Host, and `127.0.0.1` as a trusted proxy, each only if it is not saved yet. It also records what it made, so that turning off can take back exactly that.

Trusting `127.0.0.1` also trusts every other program on this computer, as [Behind a reverse proxy](#behind-a-reverse-proxy) describes. That includes forwarders that relay other devices from `127.0.0.1`, such as a reverse proxy running on this computer (for example a NAS's built-in one), an `ssh -L` tunnel, or another Tailscale Serve forward. While sharing is on, such a program can send forwarded headers to OwnGit, and a forwarder that passes on a client's `X-Forwarded-For` lets that client do the same: it can choose the client address that OwnGit uses for password lockouts, and make its request look like HTTPS. Tailscale Serve connects from `127.0.0.1` too, so OwnGit cannot tell them apart. If `127.0.0.1` was already trusted, turning sharing on changes nothing here.

Tailscale connects to OwnGit through `127.0.0.1`. When the listen address already accepts that, for example the default `127.0.0.1:7654` or `0.0.0.0:7654`, OwnGit keeps it. Otherwise, for example when OwnGit listens only on its Tailscale address, turning on saves `127.0.0.1:PORT`. OwnGit never opens home-network access on its own. The "Also allow on the home network (not encrypted)" checkbox, or `owngit tailscale on --home-network`, saves `0.0.0.0:PORT` so that devices on your home network can also connect over plain HTTP; when that opens the home network, it counts as accepting plain HTTP. When the running OwnGit was started with `--listen`, that option decides where it listens: turning on keeps it, and the page says so instead of offering the checkbox. `--home-network=false` keeps OwnGit on this computer only. A new listen address applies at the next start.

On the Settings page, the change applies at once: the running server accepts the name, trusts the proxy and uses the HTTPS address in clone addresses without a restart. `owngit tailscale on` and `off` save the same change but cannot reach a running server, so they tell you to restart OwnGit. `owngit tailscale status` says "on and ready", and the Settings page "On. Encrypted by Tailscale on this computer.", only after the running server accepts the name and trusts `127.0.0.1`, and Tailscale still has the address; otherwise they say what is missing, such as a restart. Changes run one at a time, also between the page and the command line, and a change runs to its end even if the browser tab that asked for it is closed. If turning on was interrupted anyway, for example because OwnGit stopped, the Settings page says so and offers to turn sharing on again. `owngit serve --tailscale PATH` and `owngit tailscale --tailscale PATH` use a `tailscale` command that OwnGit does not find on its own. `status --json` prints the report as JSON. The Settings page asks Tailscale for its state at most once every three seconds, however many people open the page at once, and again right after sharing is turned on or off, so a change made elsewhere can take a few seconds to show.

When Tailscale issues the certificate, the names of this computer and your tailnet, such as `gitbox.tail0000.ts.net`, are recorded in a public Certificate Transparency log. Only the fact that the address was opened is recorded, not your code, repositories, passwords or other content. The Settings page shows this notice next to the switch, and `owngit tailscale on` prints it before it asks Tailscale to serve the address (`--json` output has it as `certificate_log`). Command output other than terminal setup is in English. You can change this computer's name in the Tailscale admin console, or on this computer with `tailscale set --hostname NAME`; MagicDNS follows either change. A rename does not take the earlier name out of the log, and the new name is logged as well once Tailscale issues a certificate for it. Tailscale gets the certificate when the address is first opened, so the first HTTPS connection after turning sharing on, or after a rename, can take 25 to 50 seconds. Later connections do not wait for it.

After a rename, turn sharing on again to use the new name: OwnGit makes the address for the new name, saves the new base URL and allowed name, and takes back the old name if it had added it. Tailscale keeps the old address under the old name. It answers for nothing, because Tailscale answers only for the current name, but `tailscale serve` can remove it only while the computer has that name (Tailscale issue 16992). To remove it, rename the computer back, run `tailscale serve --https=443 --set-path=/ off`, and rename it again, or run `tailscale serve reset` if Tailscale serves nothing else on this computer. The Settings page and `owngit tailscale status` show such an address with these steps. Turning sharing off after a rename takes back OwnGit's settings and leaves the old address to you.

Turning off removes the Tailscale address only if OwnGit made it and it is still exactly as OwnGit made it. If someone changed it since, OwnGit changes nothing and explains; change it back or remove it with `tailscale serve`, then turn off again. The Settings page and `owngit tailscale status` give both commands, and offer neither turning on nor off until one of them has run. If the address is already gone, there is nothing to remove. Turning off asks Tailscale first, so it needs Tailscale to answer. OwnGit then restores the base URL that was saved before, if the HTTPS address is still saved, and removes the allowed Host and trusted proxy that it added. The listen address stays as it is. When you turn sharing off on a page opened at the HTTPS address, that address stops reaching OwnGit, so OwnGit answers with a short page instead of the Settings page. It confirms that sharing is off and gives OwnGit's address on this computer, such as `http://127.0.0.1:7654/`. OwnGit never runs `tailscale serve reset` or `tailscale funnel`.

OwnGit refuses every request that carries the `Tailscale-Funnel-Request` header, so the address cannot be opened to the Internet through Funnel. It ignores the `Tailscale-User-Login` and other `Tailscale-User-*` headers; passwords still decide who can read, write and administer.

With the Tailscale app for macOS (the App Store or standalone app, as opposed to Homebrew's `tailscaled`), Tailscale runs only while someone is logged in. After the Mac restarts, HTTPS does not work until someone logs in. Turn on automatic login, or use Homebrew's `tailscaled`, which runs without a login. The Settings page shows this line when it detects the app.

The sharing record belongs to this installation host, like the network settings. An offline backup does not carry it.

### Behind a reverse proxy

A reverse proxy such as Caddy, nginx, Traefik, or Nginx Proxy Manager can give OwnGit an HTTPS address. OwnGit must be at the root of its own host name, such as `https://git.example.internal`. A path below another site, such as `https://example.internal/git`, is not supported.

Behind a proxy, every request reaches OwnGit from the proxy over plain HTTP. Until you tell OwnGit that the proxy is trusted, it treats every client as the proxy: wrong passwords from one device lock out every device for 15 minutes, cookies are not marked `Secure`, and forms that the browser sends over HTTPS fail the Origin check. Save the proxy's address and the HTTPS address, then restart OwnGit:

```sh
owngit network set --base-url https://git.example.internal --trusted-proxy 127.0.0.1
owngit network show
```

`network show` lists the saved trusted proxies, and while OwnGit runs, the ones it uses. After the restart, when you open OwnGit through the proxy, the connection status in the page header says "Encrypted by the proxy in front of OwnGit".

`--trusted-proxy` takes the address the proxy connects from, such as `127.0.0.1` when the proxy runs on the same computer, or a CIDR range such as `172.18.0.0/16` for a Docker network. It is repeatable. OwnGit trusts no proxy by default, not even `127.0.0.1`. It refuses ranges wider than `/8` for IPv4 or `/32` for IPv6, such as `0.0.0.0/0`, `0.0.0.0/1`, and `::/0`, and the unspecified addresses `0.0.0.0` and `::`. A range trusts every computer in it, so keep it as small as you can. Trusting `127.0.0.1` also trusts every program on this computer, which can then choose the client address OwnGit sees. That includes forwarders on this computer that relay other devices, such as an `ssh -L` tunnel or another proxy. `owngit serve --trusted-proxy ADDRESS` replaces the saved list for one run, and `--trusted-proxy ""` trusts none for that run.

From a trusted proxy, and only from one, OwnGit reads three headers:

- `X-Forwarded-Proto`, when it is sent once and is exactly `https` or `http`. With `https`, OwnGit marks its cookies `Secure`, checks browser forms against the `https` address, shows the connection as encrypted by the proxy, does not ask for the plain-HTTP acknowledgement, and tells Git that the request came over HTTPS.
- `X-Forwarded-For`, when its last entry is an IP address. The last entry is the one the proxy added. OwnGit uses it for password lockouts and the setup approval warning, so two devices behind the proxy lock out separately. Entries before it came from the client and are ignored.
- `X-Forwarded-Host`, when it is sent once and OwnGit accepts both that Host and the Host of the request itself. It can only choose between names that already pass the Host check, never add one. The examples below pass the original Host instead, and the nginx example removes any `X-Forwarded-Host` that a client sends.

OwnGit ignores a repeated header, a list where one value belongs, or any other value, and uses what the connection itself shows. It ignores the `Forwarded` header. Requests from other addresses are treated as before, so a device that connects to OwnGit directly cannot set these headers. The proxy must add the client's address to `X-Forwarded-For` itself; a proxy that passes the client's header through unchanged lets clients choose their lockout address.

When the proxy runs on the same computer, keep OwnGit listening on `127.0.0.1:7654`, the default, so that other devices can reach it only through the proxy. A proxy in a container cannot reach `127.0.0.1` on the host unless it uses the host's network. Otherwise, let OwnGit listen on an address the proxy can reach, and trust the address the proxy connects from. A proxy in Docker on the same computer connects from its container's Docker network range, such as `172.18.0.0/16`. A proxy on another computer connects from that computer's address, even when it runs in Docker there, because Docker replaces the container's address with the computer's own.

When OwnGit listens on a network address, other devices can also connect to it directly over plain HTTP, without the proxy. `network set` and the Settings page say so. OwnGit does not believe forwarded headers from those devices, but their connection is not encrypted. To make every device go through the proxy, let only the proxy reach OwnGit's port, for example with a firewall rule.

Each Git request can send or receive up to 4 GiB and take up to 30 minutes (see [Git transfer limits](#git-transfer-limits)). The proxy's own limits must be at least as large, or large pushes and clones fail at the proxy.

#### Caddy

```caddyfile
git.example.internal {
	reverse_proxy 127.0.0.1:7654
}
```

By default `reverse_proxy` passes the original Host, sets `X-Forwarded-Proto`, and sets `X-Forwarded-For` to the client's address, ignoring any value the client sent. It has no request size limit and no timeout that would cut a long push. Caddy gets a certificate for a public name automatically. For a name such as `git.example.internal` it uses its own local certificate authority, which each device must trust. With Caddy's Debian package, the certificate of that authority is `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`. Only root and the `caddy` user can read it, so copy it as root, then add it to the trusted certificates on each device.

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

`client_max_body_size` and the two timeouts match OwnGit's limits. `proxy_request_buffering off` together with `proxy_http_version 1.1` lets nginx pass a push on as it arrives instead of storing all of it on disk first. `proxy_buffering off` does the same in the other direction: clones and archives go to the client as OwnGit sends them instead of into temporary files. `$proxy_add_x_forwarded_for` adds the client's address at the end of the header. nginx passes other client headers on unchanged, and an empty value removes one, so the `X-Forwarded-Host` line stops a client from sending its own. `$host` has no port, so if clients use a port other than 443, write `proxy_set_header Host $http_host;` instead, so that the Host OwnGit sees matches the address in the browser.

#### Traefik

Traefik passes the original Host and sets `X-Forwarded-Proto` and `X-Forwarded-For` with the client's address added, and it drops forwarded headers that clients send unless you configure `forwardedHeaders.trustedIPs`. Its entry points stop reading a request after 60 seconds by default, which cuts a long push with HTTP 504. Raise `readTimeout` on the HTTPS entry point. That setting belongs in the static configuration, and the router, service, and certificate go in a dynamic configuration file:

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

Start Traefik with `traefik --configFile=/etc/traefik/traefik.yml`. This example was tested with Traefik 3.7 installed from its release binary. When Traefik runs in Docker, trust the address it connects from, as described above.

#### Nginx Proxy Manager

With its default settings, Nginx Proxy Manager lets devices on your own network choose the address that OwnGit sees. Its `nginx.conf` accepts an `X-Real-IP` header from any address in `10.0.0.0/8`, `172.16.0.0/12`, and `192.168.0.0/16`, uses that value as the client's address, and adds it to `X-Forwarded-For`. If you trust Nginx Proxy Manager in OwnGit with these settings, a device on a home network can avoid the password lockout by sending a new address with every guess, lock out another device by sending that device's address, and make a setup approval request look as if it came from this computer, so the approval does not warn that another device asked.

The line `set_real_ip_from 127.0.0.1;` in the Advanced tab settings below turns this off for the OwnGit proxy host. Nginx Proxy Manager then accepts `X-Real-IP` only from its own container and reports the address each device connects from, so devices lock out separately. This was tested with Nginx Proxy Manager 2.16.0 on Docker Engine on Linux and IPv4 clients, with Websockets Support on and off. Docker Desktop, rootless Docker, and IPv6 clients were not tested. The line only stops Nginx Proxy Manager from believing `X-Real-IP`, so it cannot bring back a client address that Docker has already replaced.

Without that line, trust Nginx Proxy Manager only when every device that can reach it is yours. Use long passwords, because the lockout cannot slow down guesses from your network, and do not rely on the address shown when you approve a setup request.

To set it up, create a proxy host with the scheme `http`, OwnGit's address and port as Forward Hostname / IP and Forward Port, and an SSL certificate, and turn on Force SSL. Leave "Trust Upstream Forwarded Proto Headers" off. Since version 2.14.0, Nginx Proxy Manager passes on an `X-Forwarded-Proto` value that the client sent. With that option off, Force SSL redirects every plain-HTTP request, so only HTTPS requests reach OwnGit. A client that sends `X-Forwarded-Proto: http` over HTTPS only makes OwnGit treat its own request as plain HTTP. Nginx Proxy Manager does not set `X-Forwarded-Host`, so a value from the client reaches OwnGit, which uses it only as described above. By default, Nginx Proxy Manager limits a request body to 2000 MB, waits at most 90 seconds for OwnGit to send or accept data, and stores large responses in temporary files. Add these lines to Custom Nginx Configuration on the Advanced tab:

```nginx
client_max_body_size 4g;
proxy_request_buffering off;
proxy_read_timeout 30m;
proxy_send_timeout 30m;
set_real_ip_from 127.0.0.1;
```

They work with Websockets Support on or off. You can also add `proxy_buffering off;`, which passes clones and archives on as OwnGit sends them instead of writing them to temporary files first. Do not add `proxy_http_version`: Nginx Proxy Manager already sets it, and with Websockets Support on, a second value takes the proxy host offline.

Trust the address Nginx Proxy Manager connects from, as described above. That is its Docker network range when OwnGit runs on the same computer, and the address of the computer that runs Nginx Proxy Manager when OwnGit runs on another one.

## New-release notice

After setup, OwnGit asks GitHub once a day whether a newer release exists. The first check runs about 30 seconds after the server starts, or right after setup is finished on a new installation. It sends one HTTPS request to `https://api.github.com/repos/juliankang4/owngit/releases/latest` with a User-Agent that names OwnGit and its version. No repository data is sent, and GitHub sees the server's network address. Drafts and prereleases are ignored. Apart from [imports](#importing-from-another-git-host), which connect only to the source hosts you configure (including their scheduled refreshes), this is the only connection OwnGit opens to another host. The update-check setting and `--no-update-check` do not affect imports.

When a newer version exists, the dashboard shows a notice above the activity graph with links to the release notes and to the [Install](../README.md#install) section, which explains how to update. OwnGit never downloads or installs anything itself. Dismiss hides the notice for that version in the current browser; a later version shows it again. The result is kept in memory only, so a restart checks again. A failed check (no network, a rate limit, or an unexpected answer) shows nothing, writes at most one log line until a check succeeds again, and never affects Git or the pages.

To turn the check off, open Settings and use Update check, which asks for the administrator password. Turning it off stops further release checks and hides the notice at once; turning it on checks again within a few seconds. The setting belongs to this installation host: an offline backup does not carry it, and a restored installation starts with the check on.

For a deployment that must never check for new releases, start the server with `--no-update-check`. OwnGit then makes no release-check request whatever the saved setting says, and Settings reports that the start option disabled the check:

```sh
./bin/owngit serve --no-update-check
```

## Host-owner recovery

Before setup is complete, issue a replacement setup link with:

```sh
./bin/owngit setup-link --base-url http://127.0.0.1:7654 --no-open
```

To reset a forgotten administrator password, put the new password in an owner-readable file:

```sh
./bin/owngit reset-admin --password-file /path/to/owner-only-password-file
```

The password file must be a regular file. On Unix-like systems, it must not be readable by group or other users. On Windows, it must not inherit access entries from its folder and must give access only to your account (see [Password and token files](#password-and-token-files)). OwnGit never accepts a password as a command-line value. Resetting the administrator password signs out administrator sessions and leaves repositories unchanged.

OwnGit has no email or account recovery. Both procedures require access to the installation host.

### Password and token files

Every command that reads a password or token file you wrote yourself checks it this way, including `reset-admin`, `import`, `pr` and `repo`. When it refuses a file as not private, it says what is wrong, for example which accounts can also read the file, and gives a command that fixes it (on Windows, a PowerShell line).

On macOS and Linux, the file must not give its group or other users any access. Create it while `umask 077` is in effect, or fix an existing file with `chmod 600 FILE`.

On Windows, a file made with Notepad or `echo` inherits its folder's access entries, which usually let other accounts read it. In PowerShell, create the file, limit it to your account, and only then write the password into it:

```powershell
$file = "$HOME\owngit-password.txt"
$f = New-Item -ItemType File -Path $file
$io = if ($PSVersionTable.PSEdition -eq 'Core') { [IO.FileSystemAclExtensions] } else { [IO.File] }
$acl = $io::GetAccessControl($f, 'Access')
$acl.SetSecurityDescriptorSddlForm("D:P(A;;FA;;;$([Security.Principal.WindowsIdentity]::GetCurrent().User))", 'Access')
$io::SetAccessControl($f, $acl)
[IO.File]::WriteAllText($file, [Net.NetworkCredential]::new('', (Read-Host -AsSecureString 'Password')).Password)
```

The `$io` and `$acl` lines replace the file's access list with `D:P(A;;FA;;;SID)`: a list that inherits nothing (`P`) and allows (`A`) full access (`FA`) only to your account, named by its security identifier. They write only the access list, so the owner and any audit settings stay. `$io` picks the .NET class that has these methods: `[IO.File]` in Windows PowerShell 5.1, `[IO.FileSystemAclExtensions]` in PowerShell 7. `Read-Host -AsSecureString` keeps the password off the screen and out of the PowerShell history. The same commands work in Windows PowerShell 5.1 and PowerShell 7, in an ordinary window and in one opened with Run as administrator. In an administrator window, Windows makes the Administrators group the owner of the new file. OwnGit accepts that owner when the access entries name only your account, because administrators can take ownership of any file anyway.

## Restoring repository files

Start a restore from the repository's Overview (Start a restore, under Restore files), from a branch, tag, or kept-history line on the Overview or a commit page (Restore files from here), or from a file page (Restore this file). Choose a source commit and target branch, then preview the complete list of additions, changes, and deletions. OwnGit applies the reviewed tree only if the target branch still has the previewed tip.

An existing branch receives a new commit whose parent is its previous tip. A deleted branch is recreated at the selected commit. Selected-file restore keeps unselected files, file modes, binary files, and symbolic links as they are, and never follows links on the host. OwnGit refuses to restore a selected submodule, or a path whose replacement would remove unselected files beneath it.

Restore changes Git-tracked content in OwnGit only. It does not touch another computer's working tree or its uncommitted files.

## Changing the default branch

The default branch is the branch that OwnGit and `git clone` open first (the repository's `HEAD`). An imported repository whose only branch is `master` shows no default branch until you choose one. An administrator picks any existing branch in the repository's Settings tab. Changing it creates no branch and leaves every ref and kept history as it was.

## Deleting a repository

Deleting a repository removes it from OwnGit together with its pull requests, reviews, tasks, check settings, jobs and results, runner and helper credentials, import settings, run history and stored import credentials. Queued check jobs are dropped. An administrator deletes a repository with Delete repository, at the end of the repository's tabs, by typing its name and the administrator password. When the deletion finishes, the name is free for a new repository. You choose what happens to the files:

- Remove from OwnGit and keep the files moves the bare repository, unchanged, to `.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git` inside the repository folder. `ID` is the repository name in lowercase, as in its Git URL, and the time is UTC; a number is added if that name is taken. Its branches, tags and kept history stay in that folder until you remove it yourself.
- Delete the files too deletes the bare repository, including its kept history.

OwnGit refuses to delete a repository while an import is running, while a check job is claimed or running, while a check container still waits for OwnGit to confirm its removal, or while another Git operation (a push, clone, restore or merge) still holds the repository after a short wait. Try again once it finishes. A container cleanup that failed is retried when OwnGit starts, so restart OwnGit after Docker is available again. If the server log says the job belongs to another Docker daemon (for example after Docker was reset or reinstalled), OwnGit cannot confirm the cleanup, and it also skips removing old check workspaces at startup. Remove any leftover container labeled `com.owngit.check-job=JOB` on the daemon that ran it, or make sure that daemon no longer exists. Then release the record on the OwnGit computer:

```sh
./bin/owngit forget-check-container --job JOB --confirm-container-removed
```

`JOB` is the job identifier from the server log. Add `--state-dir` if you use a non-default state directory. The command works while OwnGit is running, removes no container, and prints the recorded container name, ID, daemon and label. It refuses a job without a record, and a record that belongs to the Docker daemon running now, because the next start of OwnGit removes that container itself. It also refuses a job that has not finished (pending, claimed or started), because a running OwnGit may still be using or removing that container. Wait for the job to finish or cancel it, or start OwnGit once so that it marks the interrupted job, and then run the command again. The repository can be deleted right away, and the next start cleans up the check workspaces.

OwnGit records the deletion in its state database before it moves or deletes the files. If OwnGit stops partway, or the files cannot be moved or deleted, the deletion reports that its files are not finished. The repository is already gone from the dashboard and from Git URLs, and creating or importing a repository with the same name reports that the name is in use. The files stay at `ID.git`, at their kept-folder path, or under a temporary `.owngit-delete-*` name. The next start of OwnGit finishes the move or deletion and frees the name; if it cannot, the reason is in the server log and the files stay where they are. OwnGit 1.0.0 cannot delete repositories and does not finish a deletion. If you go back to 1.0.0 while a deletion is unfinished, it stays unfinished until you start 1.0.1 or later again.

While a deletion is unfinished, the repository folder also holds a small `.owngit-deletion-ID` file with a random token for that deletion. It shows OwnGit that the folder is the storage the deletion began on. If the file is missing or holds another token at startup, for example because the storage is not mounted or an older copy of it is mounted, OwnGit keeps the deletion recorded, logs that the storage may be unavailable, and tries again at the next start. Do not remove this file while a deletion is unfinished. If you removed it by hand while the correct storage was mounted, recreate it in the repository folder with the `token ...` line quoted in the server log, then restart OwnGit. A leftover file after a finished deletion is harmless. OwnGit never follows a symbolic link while deleting and never removes anything outside that repository's directory. A Git request that was already waiting when the deletion started fails afterwards.

Earlier backups still contain a deleted repository, and the database space its records used is freed but not securely erased. Folders under `.owngit-removed` are never listed as repositories and are not included in backups.

After a deletion that kept the files, the dashboard shows the kept folder once, with a command that pushes its branches and tags to a new repository with the same name. Run it on the computer where OwnGit runs, because the folder is there. To bring the repository back, create an empty repository with the same name in the dashboard, then run the command. If the dashboard cannot confirm the kept folder, it shows no command; push from the folder yourself. To push into a repository with another name, use its URL:

```sh
git --git-dir /path/to/repositories/.owngit-removed/ID-YYYYMMDDTHHMMSSZ.git push http://HOST:7654/git/NEW-NAME.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
```

Kept history is not transferred. Commits that only kept history holds stay in the kept folder, and the new repository starts its own kept history. Pull requests, checks and other records do not come back either. If the kept repository's main branch is not `main`, change the default branch afterwards.

## Moving an existing repository into OwnGit

Create an empty repository in the dashboard. From a clone of the existing repository, add OwnGit as a remote and push branches and tags:

```sh
git remote add owngit http://HOST:7654/git/PROJECT.git
git push owngit --all
git push owngit --tags
```

OwnGit accepts pushes only to branches (`refs/heads/*`) and tags (`refs/tags/*`). A `git push --mirror` from a mirror clone of another host therefore fails for other refs, such as `refs/pull/*`.

Compare the branch and tag refs before you treat the move as complete:

```sh
git for-each-ref --format='%(refname) %(objectname)' refs/heads refs/tags
git ls-remote --heads --tags owngit
```

Pushing between two OwnGit installations does not carry kept history or repository records. Use an offline backup when those must move too. To keep pulling changes from a host that stays in use, see [Importing from another Git host](#importing-from-another-git-host).

## Keeping a copy on another host

OwnGit does not push to other hosts itself, but ordinary Git can keep a copy elsewhere.

To copy every branch and tag from OwnGit to another host, work from a mirror clone:

```sh
git clone --mirror http://HOST:7654/git/PROJECT.git
cd PROJECT.git
git push --mirror https://git.example.test/team/project.git
```

To update the copy later, run `git fetch --prune` and `git push --mirror` again in the same directory. `--mirror` makes the other host match the copy exactly: it overwrites refs there and deletes refs that the copy does not have. OwnGit does not share its kept history, so that history stays in OwnGit.

To update both hosts with every push from a working clone, give its remote two push URLs:

```sh
git remote set-url --add --push origin http://HOST:7654/git/PROJECT.git
git remote set-url --add --push origin https://git.example.test/team/project.git
```

Once a remote has a push URL, Git pushes only to its push URLs, so list OwnGit as well. Fetches still use the original URL. Git pushes to each URL in turn, and a rejection by one host does not undo the push to the other.

## Importing from another Git host

An import copies a repository from another Git host over HTTPS into a new OwnGit repository and can refresh it later. Imports are inbound only: OwnGit never writes to the source. Git LFS objects are not fetched or hosted.

Each import records a mode, `standalone` (**Standalone**) or `coexistence` (**Coexistence**), to note how you intend to use the copy: as the primary copy, or as a refreshed copy while the other host stays authoritative. The mode is only a label. It does not change how an import or refresh behaves: both modes follow the same [refresh rules](#what-an-import-publishes), and neither overwrites local work.

In the browser, the administrator uses Import a repository on the dashboard to start an import, and the repository's Import tab to set up or change its source and credentials, refresh, cancel, and set a schedule. Anyone who can read the repository sees the Import tab's status, run history and ref states; the source address, credential state and technical run messages are shown to administrators only. Every change asks for the current administrator password. Saving the credential form changes only what you enter: a new token or Basic credential keeps a stored source CA, and a CA alone (the credential form **No new sign-in (CA only)**) keeps the stored token or Basic credential. Only Clear credentials removes them. The browser form is limited to 1 MiB in total, so store a CA bundle close to that size with the command line.

The same operations are available from the command line, except changing the source URL or options of an existing import, which only the Import tab does. Import commands read the administrator password from a file with the same checks as `reset-admin`, and read a source token or Basic credential from a private file or an interactive prompt. They never accept a secret as an argument or environment variable.

```sh
./bin/owngit import add PROJECT https://example.invalid/team/project.git \
  --mode standalone \
  --token-file /path/to/owner-only-token \
  --ca-file /path/to/source-ca.pem \
  --server http://HOST:7654 --accept-insecure-http \
  --password-file /path/to/owner-only-admin-password
```

Every import command takes the same `--server`, `--accept-insecure-http`, and `--password-file` flags; they are omitted below:

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

- `--basic-file` replaces `--token-file` for a Basic credential; the file holds the username and password on separate lines. `--ca-file` stores a source certificate authority, up to 1 MiB. `import credentials` changes only what you pass: `--ca-file` alone keeps the stored token or Basic credential, and `--token-file` or `--basic-file` alone keeps the stored CA. `--clear` removes the stored credential and CA.
- `--allow-private-network` permits a source on a private LAN, CGNAT or Tailnet, or loopback address.
- `--git-only-consent` accepts a repository with Git LFS pointers; see [Git LFS](#git-lfs).
- `--accept-insecure-http` consents to reaching OwnGit over plain HTTP for that command only. The import source itself must use HTTPS.
- Output shows the credential type and whether one is stored, never the token, password, or CA.
- `import add` creates a new repository. For a repository that already exists it refuses with `repository_taken` and changes nothing; use `import refresh` to update it from its stored source.
- `import add` and `import refresh` wait for the whole run, up to about 62 minutes by default. When a finished run kept local refs that differ from the source, the command lists them and exits with status 3 instead of 0. A run cancelled before it finished, for example with `import cancel`, exits with status 130, as a cancelled `check run` does; other failures exit with 1. `import status` lists the last and active runs and every observed branch or tag that does not match the source, with its state.
- `import cancel NAME` also stops a first import that is still creating the repository NAME. A cancel stops a run only until its result is published, which for a first import is the moment the repository appears. A cancel that arrives later comes too late: the run finishes `complete`, and `import add` or `import refresh` says that the cancellation did not stop it.
- If the first import of a new repository fails or is cancelled, OwnGit removes the source and credentials it stored for that name, and the command result still reports the failed run. If OwnGit stopped during that import, for example after a crash, it removes them at its next start. A retry uses only what you supply. Creating a repository with that name also removes leftover import settings, or is refused while an earlier import for the name is still running or needs recovery. `import credentials NAME --clear` removes them for a name that has no repository.
- A schedule interval is between 60 seconds and 7 days. Another interval is refused with `invalid_schedule`. Scheduled refreshes run only while `owngit serve` is running.

### Source connections

The source URL must use HTTPS with TLS 1.2 or newer and must not contain a username, password, query, or fragment. Hostnames must be ASCII, and IPv6 zone identifiers are not supported. OwnGit does not follow redirects and ignores proxy environment variables, cookies, and Git credential helpers.

OwnGit resolves the hostname once and checks every returned address before it connects. Public addresses are allowed. Private LAN, CGNAT, Tailnet, and loopback addresses need `--allow-private-network`, including when a DNS answer mixes public and private addresses. Other special-purpose addresses are always refused. A custom CA adds to the system roots and never disables certificate or hostname checks. A run that fails because the source certificate is not trusted, does not match the host name, or fails the TLS handshake says so in its error message.

### What an import publishes

Each run fetches a full copy into a private staging area and checks it before anything reaches the repository: every advertised branch, tag, and HEAD must be present with the advertised object and a complete object graph. A new repository appears only when it is complete.

OwnGit publishes only branches and tags. Other refs, such as notes, replace refs, and pull request refs, are skipped. A source whose HEAD points outside `refs/heads/` is refused.

A refresh never overwrites local work. For each ref:

- a missing ref is created, and an identical ref is left alone;
- a branch follows the source only when it still holds the value OwnGit last saw from this source URL, or when it only moved forward from that value and the new source value includes it;
- a tag changes only when it is still the exact tag OwnGit last saw;
- anything else is divergent: the local ref is kept and the run reports it.

A source branch or tag whose name differs only by case from an existing local ref is not created and is reported as divergent; rename or remove one of the two if you want the source ref imported.

A branch or tag deleted at the source is never removed locally; the Import tab and `import status` show it as **Deleted at source**. Every replaced value stays in kept history. After you change the source URL, OwnGit has not yet seen the new source's refs, so refs that differ are reported as divergent instead of being replaced.

A refresh changes the repository's HEAD only when OwnGit set that HEAD on an earlier import from the same source and nothing changed it since. Otherwise HEAD stays as it is and is reported as divergent.

Repository hooks and configuration are not copied. A source with a different object format (SHA-1 or SHA-256) than the repository fails, and ref names that differ only by case are refused. A source whose branch, tag, or HEAD target name is longer than 417 bytes fails with `unsupported_refs` before anything is published; shorten that name at the source to import it.

### Git LFS

OwnGit scans the fetched objects for Git LFS pointer files, up to 200,000 objects, 100,000 candidate files, and 32 MiB of candidate content. If it finds a pointer, or cannot finish the scan within those limits, the run stops with `git_lfs_required`. With Git-only consent, the import proceeds, the pointer files are kept as they are, and the status says the content is incomplete. LFS objects themselves are never downloaded. OwnGit does not read `.gitattributes`, so a clean scan does not prove that a repository does not use LFS.

### Failures and cancellation

Only one run per repository is active at a time; another request returns `busy`. A run is limited to 60 minutes by default. A cancelled run is recorded as `cancelled`, and a run that reaches its time limit as `limit`. Other conflicts return `repository_taken`, `superseded`, `destination_changed`, `publication_unresolved`, or `nothing_to_resolve`.

When `owngit serve` stops, it cancels running imports and waits up to 45 seconds for each to record its outcome. At the next start, OwnGit marks interrupted runs, checks any publication that was in progress against the repository, and records what it finds. It never repeats or rolls back a write. If the import service cannot start, the Import page and `import status` say so, and ordinary Git service continues.

### Unresolved publications

A publication is unresolved when OwnGit cannot prove how it ended, for example when refs were written and HEAD was not. Refreshes are refused until the owner accepts the repository as it is:

1. Check the repository's branches, tags, and HEAD, and the reason on the last run. Fix anything you do not want to keep with ordinary Git operations.
2. Make sure no import is running. Resolution is refused with `busy` while a run or another Git operation holds the repository, and with `nothing_to_resolve` when nothing is unresolved.
3. Resolve with `owngit import resolve PROJECT` or the button on the Import tab. OwnGit records the current refs and HEAD as the accepted state. It writes nothing to Git and does not change the earlier run's history.
4. Refresh. Refs that match the source stay, refs that still hold the last confirmed source value follow the source, and anything else stays divergent.

If an initial import is unresolved and its repository does not exist yet, `import resolve` refuses it. Restart OwnGit. If the problem remains, move that import's `.owngit-create-*` directory out of the repository folder and restart again.

## Command-line pull requests

A normal push does not create a pull request. After pushing distinct source and target branches, create one. `--review` is optional:

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

Use `--review skip` when you intentionally omit review. A skip is recorded as skipped, not approved. Without `--review`, no review is requested, and `pr review request` can still be run later. Omit `--password-file` when general access is open. The file contains the shared general-access password, never the administrator password, and has the same owner-only checks as `reset-admin`.

Only one pull request can be open for a source and target branch pair. Creating a second one is refused with `pull_request_exists`, and `error.details.number` names the open pull request; the web page links to it. Pull requests from before this rule stay open and work as before.

A pull request you no longer need can be closed without merging, with Close pull request on its page or `pr close`. Closing changes no branch. A closed pull request stays in the list as Closed, keeps its history, cannot be reviewed or merged, and no longer holds its branch pair, so a new one can be opened. Reopen pull request or `pr reopen` opens it again; this is refused with `pull_request_exists` while another pull request is open for the same pair. A merged pull request cannot be closed or reopened (`pull_request_merged`). Closing and reopening need the same access as merging.

Plain HTTP exposes the password and pull request details to the network. `--accept-insecure-http` records your consent for that command only; omit it for HTTPS. The CLI rejects credentials embedded in the URL and does not follow redirects.

The other `pr` commands take the same `--server`, `--accept-insecure-http`, `--repository`, and `--password-file` flags; they are omitted below. Inside a clone of an OwnGit repository, `--server` and `--repository` can be left out because they come from the clone's `origin` remote, and a password file is then sent only when its first line names that server (see [Inside a clone](CODING_TOOLS.md#inside-a-clone) and [Credential files and the server line](CODING_TOOLS.md#credential-files-and-the-server-line)). `pr show` reports the current source and target object IDs, and every review decision and merge must supply both:

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

A submitted review is `approved` or `changes_requested`. The reviewer label records who supplied the review; it does not claim independence or that checks ran. A pending or changes-requested review does not hold a merge. When the source or target moves, earlier review and skip decisions no longer apply, so inspect the pull request again and decide for the new object IDs.

`pr diff` prints what the pull request changes, with the exact object IDs it compared; `--stat` leaves out the patch and `--patch` prints only the patch. [Pull request changes](CODING_TOOLS.md#pull-request-changes) describes pinning, limits, and the result.

Every command writes a JSON result. Failures include a stable `error.code` and a nonzero exit status. When the server cannot be reached, `connection_failed` names the cause, such as a refused connection or a TLS error. `checks` in a pull request result reports the evidence recorded for the current source revision, or `absent`. That evidence is `stale` when it ran other checks than those in the `.owngit/checks.json` committed in the source revision; configurations recorded for other branches do not affect it. A failed, stale, dirty, or incomplete check is advisory and never blocks a merge.

A pull request's changes, on its page and on the page that creates it, are what the source branch changed since it branched off the target: the difference from their merge base to the source, as on GitHub. Changes the target gained in the meantime are not shown. When the two branches share no commit, or have more than one merge base (for example after each was merged into the other), the page says so and shows no change list instead of comparing the branch tips or picking one base. A comparison too large to read in full is marked incomplete. Review and merge still apply to the exact source and target commits.

Merge makes a fast-forward or a new merge commit with the old target as first parent and the source as second parent, authored as `OwnGit <owngit@localhost>` with the pull request number and title in the message. It does not squash, rebase, force-update, delete the source branch, or change anyone's working tree. Merge requires Git 2.38 or newer on the OwnGit host; with an older Git it returns `unsupported_git`, and other Git use keeps working. A retried or interrupted merge never creates a second merge commit. When the target already contains the source, for example after the branch was merged some other way, merge writes no commit and leaves the target unchanged, like Git's "Already up to date". The pull request is recorded as merged with `merge.mode` `up_to_date` and `merge.oid` set to the unchanged target commit.

## Project checks

OwnGit records manual helper checks and runs owner-enabled automatic checks.

A manual helper runs in your working environment and uploads evidence tied to a revision. It inherits your environment and permissions, so it is not a sandbox: a check can read files and credentials your account can reach. OwnGit records the worktree state with every attempt and never reports a dirty or unknown worktree as a tested commit.

Automatic checks run as the OwnGit account, in a restricted local Docker container, or on a separately connected runner. They need a committed `.owngit/checks.json`, an owner policy, and current consent. See [Automatic checks](AUTOMATIC_CHECKS.md).

Create a repository-scoped helper credential with the administrator password. The token is written only to the owner-readable `--output` file and stored on the server only as a hash:

```sh
./bin/owngit helper-credential create \
  --server http://HOST:7654 --accept-insecure-http \
  --repository PROJECT --label laptop \
  --password-file /path/to/admin-password-file \
  --output ~/.owngit-helper-token
```

An existing file or symbolic link at `--output` is reported, not replaced. If creation or delivery fails, OwnGit leaves the output file in place instead of risking the removal of someone else's file; inspect and remove it before you retry. If the response is lost, the command revokes the new credential; if it cannot confirm the revoke, it prints the creation identity (never the token) so you can revoke it. `helper-credential list` and `helper-credential revoke --id ID` manage credentials, and a revoked token stops working immediately. An administrator can also issue and revoke helper credentials from the Helper credentials link on the repository's Checks tab. Issuing and revoking always ask for the administrator password, even in a signed-in browser.

Create a stable task, then run checks:

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

[Coding tools](CODING_TOOLS.md) is the reference for these commands, correction rounds, result fields, and exit codes. On Windows, `cmd.exe` returns exit code 1 for an unknown command, so OwnGit records that result as `failed`.

Raw check logs are stored in `owngit.sqlite`, limited to 256 KiB each, and kept for 30 days by default. Task and attempt records stay after a log expires. Reading an expired log returns `log_expired`, and a log that is missing earlier returns `log_missing`. A log that fails its integrity check is refused, and a truncated log is reported as truncated. If the database is full or reports an I/O error while storing a result, OwnGit stores the result without its raw log and records a log error on the attempt.

## Repositories being prepared

When `owngit serve` starts, it prepares each repository before serving it. It checks the repository's safety settings and retention hook and finishes pull request work that a previous run left unfinished. Up to 8 repositories are prepared at a time. Startup waits at most 10 seconds for this. Repositories that are not ready by then are served as soon as they are.

While OwnGit runs, a repository whose folder cannot be read when the dashboard lists it (for example because the folder was moved, its permissions changed, or its share is not mounted) is locked and prepared again in the same way. A repository whose folder can be read but whose Git data cannot is not locked: the dashboard lists it with an Unreadable label and leaves it out of the activity count, the server log records the cause once, and Git reports the error itself. Either way, the dashboard keeps listing the other repositories.

A repository whose preparation fails or does not finish stays locked, and the other repositories are served normally. While it is locked:

- Git clones, fetches and pushes get HTTP 503 with the message `repository is being prepared; try again later`.
- Its pages show that the repository is being prepared, and the API answers with the error code `repository_preparing`. The dashboard lists it with a Preparing label and leaves it out of the activity count.
- Scheduled imports and project checks for it wait. Nothing is recorded as failed, and they run once it is ready.
- An administrator can still delete it (except while a preparation attempt is running), and can still issue and revoke its helper credentials and runner tokens.

OwnGit retries a failed preparation by itself: 30 seconds after the attempt ended, then after twice the previous wait, up to 10 minutes. When the folder could not be read, OwnGit also checks it every 5 seconds and retries as soon as it can be read again. An attempt that does not return is never overlapped by another. The server log names the repository and the cause of each failure, and says when the repository is served again. The pages do not show the cause. Fix the cause (for example a disk that is not mounted, file permissions, or a conflicting Git setting that the log quotes) and wait for the next retry, or restart OwnGit to retry at once.

If OwnGit cannot read the list of repositories from its state database, it still refuses to start.

## Downloading an archive

The Code tab offers the selected branch or tag as a ZIP or tar.gz file from its top folder, and a commit page offers that commit. The archive holds the files of that revision, without Git history, in one folder named `PROJECT-REF`, such as `project-main`. Like `git archive`, it follows the `export-ignore` and `export-subst` attributes committed in that revision, so it can differ from the files the Code tab shows. The file has the same name. Any character other than a letter, a digit, `.`, `-`, or `_` becomes `-`, so `feature/login` gives `project-feature-login.zip`. Downloading needs the same access as reading the Code tab.

Without a browser, use the API route. `ref` is a branch or tag, as a short name or a full name such as `refs/heads/main`, or a full commit ID, and the default branch when it is left out. `format` is `zip` or `tar.gz`. An unknown ref or format answers 404. With shared-password protection, `--user owngit` makes curl ask for the password; leave it out when access is open:

```sh
curl --fail --remote-name --remote-header-name --user owngit \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive?ref=main&format=tar.gz'
```

With `--remote-header-name`, curl uses only the plain ASCII file name that OwnGit sends for clients that cannot read the full name. When the name has other letters, such as Korean, that ASCII name is the repository and the first 12 characters of the commit ID, for example `project-1a2b3c4d5e6f.zip`, and the folder inside keeps the full name. Browsers use the full name. To save the file under the full name with curl, give the name with `--output`; `--data-urlencode` encodes the ref:

```sh
curl --fail --get --user owngit \
  --data-urlencode 'ref=기능/로그인' --data format=zip \
  --output 'project-기능-로그인.zip' \
  'http://HOST:7654/api/v1/repositories/PROJECT/archive'
```

An archive download is a Git transfer with the limits below: at most 4 GiB and 30 minutes, including any wait for a place or for a push to the same repository, and one of the places for running Git requests. When the download cannot start in time, it answers HTTP 503 with `Retry-After`. OwnGit sends the archive while Git writes it. When Git fails, a limit is reached, or OwnGit stops before the end, OwnGit closes the connection without finishing the response, so the download fails instead of completing: curl exits with an error such as `(18) transfer closed with outstanding read data remaining`, and a browser marks the download as failed. The part received is not a valid archive either, because OwnGit sends the end of a ZIP file and the gzip trailer of a tar.gz file only after Git has finished successfully. When Git fails before it writes anything, the answer is an HTTP error instead.

## Git transfer limits

- Each Git request, such as a clone, fetch, or push, can send or receive at most 4 GiB and must finish within 30 minutes. A push over the size limit is refused with HTTP 413. A clone or fetch that passes either limit is cut off, and Git reports an incomplete transfer. The server log records the failure. A transfer is also stopped when its client moves no data for 60 seconds, for example a paused clone or an upload that stopped arriving; time that Git itself spends working does not count, but the client must keep data moving, so an extremely slow link of a few KB/s can also be cut. These limits are fixed in this version and no option changes them. OwnGit does not host Git LFS, so a repository whose complete history is larger than 4 GiB cannot be cloned through OwnGit. Keep large binary files out of Git history.
- At most 5 Git requests run at once. One repository can use up to 4 of them, and the fifth is only for a repository with no request running, so slow transfers of one repository never block the others. A request that finds no free place waits up to 90 seconds, then gets HTTP 503 with the message `Git service is busy with other transfers; try again shortly`, and the server log records it. Run the Git command again.
- With shared-password protection, 4 wrong passwords from one address within 10 minutes block that address for 15 minutes. Correct passwords never count, so several Git commands with the right password can run at the same time. The administrator password has the same limit, counted separately.
- When OwnGit is stopped, it waits up to 10 seconds for running requests, then ends the ones still running, such as a slow clone, and logs how many Git transfers it ended. This is a normal stop.

## Storage

- The state directory is the platform config directory joined with `owngit`, or `~/.owngit` when no config directory is available. It holds `owngit.sqlite` and, while the database is in use, its `-wal` and `-shm` files. Keep it on local storage, never on a network share used by other computers. Windows network (UNC) paths are refused.
- Import source credentials (tokens, Basic passwords, and source CAs) are stored unencrypted as JSON in `import-credentials/NAME.json` inside the state directory, one file per repository. OwnGit restricts that folder and its files to the account that runs OwnGit, and backups never include them. Anyone who can read the state directory as that account can read these secrets, so protect it like the credentials themselves.
- Choose the repository folder during setup. It can be on a separate disk or a mounted SMB or NFS share, with one OwnGit writer at a time. OwnGit leaves existing files in the folder alone and creates repositories there as bare repositories ending in `.git`.
- While it serves, OwnGit keeps a `.owngit-serve.lock` file in the repository folder locked. A second server on the same folder, for example one started from a copy of the state directory, refuses to start and names the folder, because it would rewrite the running server's repository hooks. Moving a stopped state directory works as before. If the folder is empty or missing when OwnGit starts, for example because the share is not mounted yet, OwnGit locks it before it first writes there. If the lock fails for another reason when OwnGit starts, the server log says that OwnGit could not check, and OwnGit starts anyway. On a network share, whether a server on another computer is detected depends on the share's file locking.
- When a Git operation holds a repository, for example a push waiting behind a long clone, the dashboard waits at most a second for it. It then shows the branch list it read last, or lists the repository as In use. A repository whose Git data could not be read the last time stays marked Unreadable, and one deleted in the meantime is left out. The repository's own pages wait until shortly before the request deadline and then answer that another Git operation is using the repository (HTTP 503 with `Retry-After`).
- The activity graph and recent activity are counted in the background when the server starts, and again when a page is opened after a branch changes. Counts are reused until the branches change. On a slow share the dashboard can appear before counting finishes; it then says that some repositories are still being counted, and reloading shows the full count.
- OwnGit remembers each repository's branches and tags between writes, so the dashboard does not read every repository on the share again at each visit. A push, merge, import, restore, deletion, or default-branch change made through OwnGit shows on the next page. If refs are changed directly in the repository folder without OwnGit, pages show the change after OwnGit next changes a ref in that repository or restarts. A push or scheduled import that changes no ref does not count.
- OwnGit also keeps recently read folder listings, files up to 4 MiB, commit diffs and pull request comparisons in memory, up to 64 MiB in total. They are stored by the commits and files they were read from, which never change, so a page opened again, such as a file you return to, starts no Git process. It also does not wait for a push that holds the repository, as long as the repository's branches and tags have not changed since OwnGit last read them. Deleting a repository drops its entries, and a restart drops all of them.
- OwnGit maintains each repository while nobody uses it, so reads stay fast as pushes accumulate. When a repository has changed and then gone five minutes without a push or request, OwnGit packs its loose refs and objects and updates its commit-graph. After a start, it does this once for every repository. Between 03:00 and 05:00, the OwnGit computer's local time, it also combines the packs of a repository that has more than 20 into one, once the repository has gone five minutes without a push or request inside that window. If the computer is asleep or OwnGit is not running during that window, this waits for the next night. Maintenance never deletes objects or kept history. It runs in three steps and holds the repository only while a step runs. A push, page or other Git operation on that repository that arrives during a step waits for that step, which can take tens of seconds for a large repository on a network share and longer for the nightly combining. Maintenance then stops and lets it run, and the remaining steps wait until the repository has again gone five minutes without use. Pages of other repositories do not wait, and the dashboard waits at most a second, as described above. Deleting a repository stops its maintenance. The server log has one line for each maintenance run. When OwnGit starts, it removes temporary pack files and maintenance lock files that a Git command interrupted before the start left in a repository, for example when the computer restarted during maintenance, and names them in the server log.
- A new repository is written under a temporary `.owngit-create-*` name and then renamed into place. On Windows, antivirus or search indexing can briefly lock the new directory. OwnGit retries for about 2 seconds; if the error persists, try again.
- A repository deleted with its files is first renamed to a temporary `.owngit-delete-*` name and then removed. Kept repositories go to the `.owngit-removed` folder. A `.owngit-deletion-*` file marks a deletion that is still unfinished. See [Deleting a repository](#deleting-a-repository).
- Repository names cannot end in `.git` or use Windows device names such as `CON`, `AUX`, `NUL`, `COM1`, or `LPT1`, with or without an extension. `new` and `new-import` are reserved. These rules apply on every platform.
- Expired logs free space inside the database for reuse, but the file does not shrink, the old bytes are not securely erased, and there is no overall size limit. OwnGit does not run `VACUUM`.
- When a `-wal` or `-shm` file is present at startup, OwnGit copies the database and its WAL to a private temporary directory to inspect them. The temporary volume needs about that much free space.
- On start, OwnGit upgrades a database from the earlier committed version or from any earlier OwnGit release in place, applying every later schema change in order in one transaction. It records the upgrade in one line, for example `state database upgraded from schema 14 to 15`, in the server log or, for an offline command such as `backup`, on standard error, so you can tell when older builds began to refuse it. It refuses a database from a newer or unknown version, or from an unreleased development build, and leaves its files unchanged. An older build refuses a database that a newer build has upgraded, so back up with the current executable before you replace it. Every command of the newer build that opens the state, including `backup`, upgrades the database first. To go back to OwnGit 1.0 after an upgrade, restore a backup that OwnGit 1.0 made: it refuses both the upgraded database and backups made by this version.
- OwnGit does not read or remove a `logs/` directory left by older versions. Remove it yourself once no older OwnGit process uses it.
- Removing the `owngit` executable leaves the state directory and repositories in place. Delete them yourself only when you no longer need them.
- A database from an unreleased development build that had built-in AI review may still hold review records and provider tokens. OwnGit does not use or erase them. Backup never copies the tokens, and it checks for review records and refuses to run while any remain.

## Offline backups

Kept history protects against force-pushes and deletions, but it is not a backup. A secret that was ever pushed stays visible in the browser and is included in every later backup, even after a force-push or branch deletion. Only [deleting the repository](#deleting-a-repository) with its files removes that history, and earlier backups still contain it. Rotate any secret you push by mistake. OwnGit does not schedule backups. Stop OwnGit before creating one. The output directory must not exist:

```sh
./bin/owngit backup \
  --state-dir /path/to/owngit-state \
  --output /path/to/new-backup
```

A backup holds a manifest and one Git bundle per nonempty repository. It includes:

- every ref, including OwnGit's kept history, and each repository's HEAD and metadata;
- pull requests, reviews, merge records, tasks, check configurations, check results, and automatic-check policies and jobs;
- import sources, run history, and publication records;
- the access mode and password hashes.

Raw logs, credentials and tokens of every kind, import schedules, consent, and the [update check](#new-release-notice) setting are not included. Keep backups private, because password hashes are sensitive.

Backup refuses to run when an import publication is still unsettled for a repository that does not exist yet. Start and stop OwnGit once so it can settle the record, then back up again. If the error remains, move the import's `.owngit-create-*` directory out of the repository folder, then start and stop OwnGit again and back up.

The manifest is limited to 64 MiB. A backup that would exceed it fails without writing output and never drops records to fit. Creating a backup holds the whole export in memory.

OwnGit restores backup versions 1, 2, 9, and 10 and refuses others, including the versions 3 through 8 that only unreleased development builds wrote. Older builds refuse a newer backup instead of dropping records they do not know. Restore into new paths that do not exist:

```sh
./bin/owngit restore \
  --input /path/to/backup \
  --state-dir /path/to/new-owngit-state \
  --repository-root /path/to/new-repositories
```

Restore checks every bundle, ref, object, and record before it publishes the new state. The SHA-256 hashes detect corruption but cannot detect a backup that someone replaced along with its manifest.

After a restore:

- sign-in sessions, setup links, approved Hosts, saved network settings, credentials, schedules, and every consent are gone;
- create new helper and runner credentials, and store import credentials again before refreshing an import that needs them;
- automatic checks stay off until the owner enables them again, and unfinished check jobs are marked `interrupted` instead of rerunning;
- unsettled import publications are closed without being applied;
- raw logs are absent, so a log reads as missing until its expiry and expired afterward;
- start `owngit serve` with the restored state before you use it in other ways, so that startup can settle interrupted records.

Backup and restore keep Git filenames exactly. A name Git accepts, such as one containing a backslash, may not check out on Windows. Rename it from a compatible working tree, or inspect the repository with a bare clone.

If restore is interrupted, do not start OwnGit from either target, and do not remove a `.owngit-restore-pending` marker. Move both targets and any `TARGET.owngit-restore-...` siblings to a separate quarantine location without merging or overwriting anything, then restore again into new paths. If a backup stops before finishing, its output directory does not exist; once no backup process is running, keep or quarantine its hidden `.OUTPUT.owngit-backup-...` sibling.

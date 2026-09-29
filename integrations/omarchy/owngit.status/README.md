# OwnGit for the Omarchy bar

A bar widget for the Omarchy shell that shows the OwnGit icon and, when you click it, a panel with whether OwnGit runs on this computer, the clone address with a Copy button, the three latest pushes, and what needs doing with one command to copy. Open dashboard opens OwnGit in the browser.

The widget asks OwnGit through the `owngit` command and runs nothing else except `wl-copy` for Copy:

- `owngit tray read --json` every 15 seconds, and every 5 seconds while the panel is open. The command reads OwnGit's status on this computer and checks that OwnGit sent it, so another program that takes OwnGit's address shows as "Status unavailable".
- `owngit tray open` when you choose Open dashboard. OwnGit is asked again at that moment, and the browser opens only when it answers.

The widget never reads OwnGit's files, has no install hook and needs no `sudo`. It shows the same states as the OwnGit icon: Running, Needs attention, Not running (only when the checkup finds that OwnGit is stopped) and Status unavailable.

## Install

It needs OwnGit 1.1.3 or later on this computer, installed for your account (not the `owngit` service account that a root install uses).

```sh
git clone https://github.com/juliankang4/owngit.git
cp -r owngit/integrations/omarchy/owngit.status ~/.config/omarchy/plugins/
omarchy plugin enable owngit.status
```

When the plugin is published as its own repository, `omarchy plugin add URL --enable` does the same.

## Settings

When `owngit` is not on the PATH of the Omarchy shell, or OwnGit uses another state directory, set them for the widget:

```sh
omarchy bar set owngit.status command /home/you/.local/bin/owngit
omarchy bar set owngit.status stateDir /path/to/state
omarchy bar set owngit.status language ko
```

`language` is `en` or `ko`; without it the widget follows the system language.

## Keyboard

Up and Down move between Copy and Open dashboard, Enter chooses, Esc closes the panel.

## The OwnGit icon in the tray

`owngit service install` also puts the OwnGit icon into the desktop's tray, which on Omarchy is the drawer behind the arrow in the bar. With this widget you may not need both: choose "Hide the icon" in the tray icon's menu or panel, or run `owngit tray off`. The widget keeps showing either way; remove it with `omarchy plugin disable owngit.status`.

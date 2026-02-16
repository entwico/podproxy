# macOS LaunchAgent install

Scripts to install podproxy as a macOS launchd user agent that starts automatically on login and restarts on crash.

## Prerequisites

Build the project first:

```sh
task build
```

This produces architecture-specific binaries in `dist/` (e.g. `podproxy_darwin_arm64`).

## Files

| File | Description |
|---|---|
| `install.sh` | Installs the binary, config, and launchd plist, then starts the agent |
| `uninstall.sh` | Stops the agent and removes the binary and plist |
| `com.github.entwico.podproxy.plist` | Launchd plist template with `__HOME__` placeholders |

## Install

```sh
./install/install.sh
```

The script performs the following steps:

1. Detects the current architecture (`arm64` or `amd64`) and locates the matching binary in `dist/`
2. Copies the binary to `/usr/local/bin/podproxy`3. Creates `~/.config/podproxy/` and copies `config.yaml` from the project root if one exists and the destination doesn't already have one (existing config is never overwritten)
4. Installs the plist to `~/Library/LaunchAgents/`, replacing `__HOME__` placeholders with the actual home directory (launchd does not expand `~`)
5. Unloads any previously loaded agent, then loads the new one

## Uninstall

```sh
./install/uninstall.sh
```

The script performs the following steps:

1. Unloads the launchd agent
2. Removes `~/Library/LaunchAgents/com.github.entwico.podproxy.plist`
3. Removes `/usr/local/bin/podproxy`4. Leaves `~/.config/podproxy/` and log files intact

## Plist configuration

The launchd plist template configures the agent with:

| Property | Value |
|---|---|
| Label | `com.github.entwico.podproxy` |
| Program | `/usr/local/bin/podproxy` |
| Arguments | `--config ~/.config/podproxy/config.yaml` |
| RunAtLoad | `true` — starts on login |
| KeepAlive | `true` — restarts on crash |
| WorkingDirectory | `$HOME` |
| stdout log | `~/Library/Logs/podproxy.stdout.log` |
| stderr log | `~/Library/Logs/podproxy.stderr.log` |

## Managing the agent

```sh
# check if loaded
launchctl list | grep podproxy

# stop
launchctl stop com.github.entwico.podproxy

# start
launchctl start com.github.entwico.podproxy

# unload (stop and remove from launchd until next load)
launchctl unload ~/Library/LaunchAgents/com.github.entwico.podproxy.plist

# reload after config changes
launchctl unload ~/Library/LaunchAgents/com.github.entwico.podproxy.plist
launchctl load ~/Library/LaunchAgents/com.github.entwico.podproxy.plist

# view logs
tail -f ~/Library/Logs/podproxy.stdout.log
tail -f ~/Library/Logs/podproxy.stderr.log
```

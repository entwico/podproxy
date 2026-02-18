# podproxy

Kubernetes-aware SOCKS5/HTTP proxy that routes traffic to pods and services via port-forwarding, with direct passthrough for non-Kubernetes destinations.

## Overview

podproxy runs a local proxy server that translates standard SOCKS5 and HTTP CONNECT requests into Kubernetes port-forward connections. Instead of running `kubectl port-forward` for each service, you point your tools at the proxy and address any pod or service across multiple clusters using a simple dot-separated naming convention.

The proxy resolves services to ready pod endpoints via the EndpointSlice API, then establishes SPDY port-forward connections directly to the target pod. Traffic addressed to non-Kubernetes hosts (e.g. `github.com`, internal DNS names) is passed through directly, so podproxy can serve as a general-purpose proxy for all traffic.

## Features

- **SOCKS5 proxy** for TCP connections to any Kubernetes pod or service
- **HTTP CONNECT proxy** for HTTPS tunneling and plain HTTP forwarding
- **Passthrough** for non-Kubernetes traffic — regular hostnames are dialed directly
- **PAC auto-configuration** server for automatic browser/OS proxy setup
- **Multi-cluster** support via multiple kubeconfig files or contexts
- **Structured logging** with configurable level and format (text/JSON)
- **Service resolution** via EndpointSlice API to find ready pod endpoints

## Address format

Addresses use a dot-separated naming convention where the **last segment is the cluster name** (derived from kubeconfig context names). The suffixes `.svc.cluster.local` and `.svc` are automatically stripped.

| Format | Description |
|---|---|
| `<service>.<cluster>:<port>` | Service in the cluster's default namespace |
| `<service>.<namespace>.<cluster>:<port>` | Service in a specific namespace |
| `<pod>.<service>.<namespace>.<cluster>:<port>` | Direct pod (e.g. StatefulSet member) |

**Examples** (assuming a cluster context named `staging`):

```
redis.staging:6379              → redis service, default namespace
postgres.db.staging:5432        → postgres service in "db" namespace
redis-0.redis.cache.staging:6379 → pod redis-0 in "cache" namespace
```

## Routing

The proxy decides how to handle each connection based on the destination hostname:

1. Strip Kubernetes DNS suffixes (`.svc.cluster.local`, `.svc`) from the hostname
2. Check if the last dot-separated segment matches a known cluster name (from kubeconfig contexts)
3. **Match** → route via Kubernetes port-forwarding to the target pod/service
4. **No match** → dial the destination directly (passthrough)

This means `redis.staging:6379` routes to Kubernetes (if `staging` is a known cluster), while `github.com:443` is dialed directly. Both SOCKS5 and HTTP proxy use the same routing logic.

## Project structure

```
cmd/podproxy/          Entry point
internal/
  config/              Configuration loading, defaults, and logger setup
  kube/                Kubernetes client, port-forward dialer, and service-to-pod resolver
  proxy/               HTTP CONNECT proxy, SOCKS5 handler, and PAC file server
install/               macOS launchd install/uninstall scripts and plist template
```

## Installation

### From source

```sh
go install github.com/entwico/podproxy/cmd/podproxy@latest
```

### Build with Task

Requires [Task](https://taskfile.dev) and [GoReleaser](https://goreleaser.com):

```sh
task build
```

### Docker

Multi-arch images (`linux/amd64`, `linux/arm64`) are published to GHCR:

```sh
docker run -d --name podproxy \
  -v ~/.kube:/home/podproxy/.kube:ro \
  -v ./config.yaml:/home/podproxy/config.yaml:ro \
  -p 9080:9080 \
  -p 8080:8080 \
  -p 8081:8081 \
  ghcr.io/entwico/podproxy:latest
```

Or with `docker compose`:

```yaml
# compose.yaml
services:
  podproxy:
    image: ghcr.io/entwico/podproxy:latest
    volumes:
      - ~/.kube:/home/podproxy/.kube:ro
      - ./config.yaml:/home/podproxy/config.yaml:ro
    ports:
      - "9080:9080"   # SOCKS5
      - "8080:8080"   # HTTP
      - "8081:8081"   # PAC
    restart: unless-stopped
```

> **Note:** When running in Docker, use `0.0.0.0` instead of `127.0.0.1` for listen addresses in your config so the ports are reachable from the host.

## Usage

```sh
podproxy --config config.yaml
```

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `--config` | `config.yaml` | Path to YAML config file |
| `--version` | | Print version information and exit |

## Kubeconfig discovery

podproxy discovers Kubernetes contexts using the same conventions as `kubectl`, in three phases:

1. **Default kubeconfig** (`~/.kube/config`) — loaded automatically if the file exists
2. **`KUBECONFIG` environment variable** — colon-delimited (Unix) or semicolon-delimited (Windows) list of paths
3. **Explicit paths and globs** from the `kubeconfigs` config field

Contexts from all phases are merged. If the same context appears in multiple sources, it is resolved from the first phase that provides it (duplicates are skipped). Each phase can be independently disabled via config fields.

## Configuration

Provide a YAML config file via `--config`:

```yaml
listenAddress: "127.0.0.1:1080"
httpListenAddress: "127.0.0.1:8080"
pacListenAddress: "127.0.0.1:8081"

# skipDefaultKubeconfig: true   # skip loading ~/.kube/config
# skipKubeconfigEnv: true       # skip reading KUBECONFIG env var

kubeconfigs:
  - ~/.kube/configs/*.yaml
  - ~/.kube/config-production

log:
  level: info    # debug, info, warn, error
  format: text   # text, json
```

| Field | Default | Description |
|---|---|---|
| `listenAddress` | `127.0.0.1:9080` | SOCKS5 proxy listen address |
| `httpListenAddress` | *(disabled)* | HTTP CONNECT proxy listen address |
| `pacListenAddress` | *(disabled)* | PAC file server listen address |
| `skipDefaultKubeconfig` | `false` | Skip loading the default `~/.kube/config` |
| `skipKubeconfigEnv` | `false` | Skip reading the `KUBECONFIG` environment variable |
| `kubeconfigs` | | List of kubeconfig file paths or glob patterns (supports `~`) |
| `log.level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `log.format` | `text` | Log format: `text`, `json` |

All contexts from all discovered kubeconfig files are available automatically. The context name becomes the cluster identifier used in addresses.

## PAC auto-configuration

When `--pac-listen` (or `pacListenAddress`) is set, the proxy serves a PAC file that routes `*.<cluster>` domains through the proxy and sends everything else `DIRECT`.

Configure your OS or browser to use the PAC URL:

```
http://127.0.0.1:8081/
```

- **macOS**: System Settings > Network > Wi-Fi > Proxies > Automatic Proxy Configuration
- **Firefox**: Settings > Network Settings > Automatic proxy configuration URL
- **Chrome/Edge**: Uses the system proxy settings

If the HTTP proxy is also enabled, the PAC file includes both `PROXY` and `SOCKS5` directives for maximum compatibility.

## Examples

### curl via SOCKS5

```sh
curl --socks5-hostname 127.0.0.1:1080 http://my-api.staging:8080/health
```

### curl via HTTP proxy

```sh
curl --proxy http://127.0.0.1:8080 http://my-api.staging:8080/health
```

### HTTPS via HTTP CONNECT

```sh
curl --proxy http://127.0.0.1:8080 https://my-api.production:8443/health
```

### Passthrough to the internet

Non-Kubernetes hostnames are dialed directly, so you can use podproxy as your only proxy:

```sh
curl --proxy http://127.0.0.1:8080 https://httpbin.org/get
curl --socks5-hostname 127.0.0.1:1080 https://example.com
```

### Environment variables

```sh
export http_proxy=http://127.0.0.1:8080
export https_proxy=http://127.0.0.1:8080
curl http://my-api.staging:8080/health   # routed via Kubernetes
curl https://github.com                   # passthrough (direct)
```

## Running as a macOS LaunchAgent

The `install/` directory contains scripts to set up podproxy as a launchd user agent that starts automatically on login.

```sh
./install/install.sh
```

The install script looks for the binary in the following order:

1. `dist/` directory (goreleaser build output from `task build`)
2. The same directory as `install.sh`
3. If not found, prompts for the full path to the binary

This installs the binary to `/usr/local/bin/podproxy`, sets up the launchd plist, and starts the agent. If a `config.yaml` exists in the project root, it is copied to `~/.config/podproxy/config.yaml` (existing config is not overwritten).

To uninstall:

```sh
./install/uninstall.sh
```

See the scripts for details. Logs are written to `~/Library/Logs/podproxy.std{out,err}.log`.

## Development

```sh
task test    # run tests with race detection and coverage
task lint    # run golangci-lint
task build   # build with goreleaser
```

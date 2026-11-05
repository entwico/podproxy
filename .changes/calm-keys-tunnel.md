---
bump: minor
---

add an SSH tunnel endpoint (`sshListenAddress`, off by default): `direct-tcpip` channels are routed like SOCKS5 connections, so tools that tunnel over SSH (ssh -L, ProxyJump, DBHub) reach cluster addresses without SOCKS5 support; public-key auth against `sshAuthorizedKeys`, shell/exec/sftp and remote forwarding refused

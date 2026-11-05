---
bump: patch
---

fix: a client that half-closes its connection (sends EOF, then waits for the reply) no longer loses the reply. HTTP CONNECT tunnels used to close the upstream on the client's EOF; they now only half-close it, and so do SOCKS5 and SSH connections to cluster targets

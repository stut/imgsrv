# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately rather than opening a public
issue. Use GitHub's [private vulnerability
reporting](https://github.com/stut/imgsrv/security/advisories/new) for this
repository, or email security@stut.net.

Please include enough detail to reproduce: the request(s) involved, the
configuration, and the observed versus expected behaviour. You'll get an
acknowledgement, and a fix or a decision, as promptly as is practical.

## Scope and trust model

imgsrv is designed to run behind a reverse proxy (nginx) that shape-gates
URLs, and to serve derivatives of a set of **trusted** original images.

- **Originals are trusted input.** Every source file is decoded by libvips and
  its codec dependencies (libheif/aom and others), a large native-code surface.
  HTTP clients can only select among files that already exist under
  `ORIGINALS_ROOT`; they never supply image bytes. Pointing `ORIGINALS_ROOT` at
  storage that untrusted parties can write to is outside the supported model.
- **Run behind the provided nginx configuration.** It rejects non-grammar and
  malformed URLs before they reach the service. The service still enforces its
  own token allowlist and path containment as defence in depth.
- **The health port is unauthenticated.** Keep `HEALTH_PORT` off any public
  interface.

Reports that depend on violating this trust model (e.g. attacker-writable
originals) are acknowledged but may be treated as configuration guidance rather
than vulnerabilities.

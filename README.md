# imgsrv

A small Go HTTP service that acts as an on-demand image resizer/transcoder
behind nginx. nginx serves everything it can from disk; only cache misses hit
the Go service, which generates the derivative, writes it atomically to disk,
and returns it — every subsequent request is a plain static file serve.

Highlights:

- **URL scheme:** `GET /holiday/photo-400x400.webp` — everything from the
  last dash in the basename to the extension is the size token; the extension
  selects the output format (`webp`, `jpeg`/`jpg`, `avif`). See
  [URL scheme](#url-scheme) for the full grammar.
- **Multi-domain:** the request's hostname is the top-level directory. nginx
  maps `images.example.com/portfolio/photo-400.webp` to
  `/portfolio/photo-400.webp` under `originals/images.example.com/` (and the
  cache mirrors it). The Go service is host-agnostic — the mapping lives
  entirely in nginx, so adding a domain is a DNS change plus a directory of
  originals; nothing is redeployed. See [Serving domains](#serving-domains).
- **Size tokens:** `400` (fit in square box), `400w`/`400h` (one edge),
  `1600x600` (fit in box), plus a mod letter: `z` fill/centre-crop, `s` smart
  crop (libvips attention), `t`/`b`/`w`/`g` pad
  (transparent/black/white/grey), `f` explicit fit. `original` re-encodes at
  native size. Never upscales.
- **Allowlist:** every dimension number must appear in the config
  `dimensions` list, else `400 Bad Request`. Bounded cache keyspace by
  construction.
- **Pipeline:** orient → resize → sRGB transform → strip all metadata →
  encode. Always; not configurable.
- **Concurrency:** singleflight per derivative path, generation capped at
  `NumCPU` libvips jobs, temp-file + atomic rename writes.

## URL scheme

```
GET /<path>/<basename>-<token>.<ext>
```

Everything from the **last dash** in the basename to the extension is the size
token; what precedes it is the original's basename (so `my-photo-400w.webp`
means original `my-photo`, token `400w`). The extension selects the output
format (`webp`, `jpeg`/`jpg`, `avif`) independently of the original's format.
The original is located by trying each configured input extension against
`<path>/<basename>.*`; no match is a `404`.

The token must match the grammar **and** every dimension number in it must be
in the config `dimensions` allowlist, else `400`. The allowlist bounds the
cache keyspace (`dims² × mods × formats × originals`), so a scanner can't fill
the disk with arbitrary sizes.

```
token := size mod? | "original"
size  := N        square box (shorthand for NxN)
       | Nw       width N, height follows aspect ratio
       | Nh       height N, width follows aspect ratio
       | WxH      box of W by H
mod   := f        fit inside the box (explicit form of the default)
       | z        fill: cover the box, centre-crop to exactly WxH
       | s        smart fill: as z, cropped around the focal point (libvips attention)
       | t        pad to exactly WxH, transparent background
       | b        pad, black background
       | w        pad, white background
       | g        pad, 50% grey background
```

| Request (basename `photo`) | Result |
|---|---|
| `photo-400.webp` | fits inside 400×400 (a 3:2 landscape → 400×267) |
| `photo-400w.webp` | 400 wide, height by aspect ratio |
| `photo-1600x600.webp` | fits inside 1600×600 |
| `photo-1600x600z.webp` | exactly 1600×600, cover + centre-crop |
| `photo-1600x600s.webp` | exactly 1600×600, cropped around the focal point |
| `photo-400x400t.webp` | exactly 400×400, fitted and centred, transparent pad |
| `photo-original.webp` | native dimensions, re-encoded (never the raw file) |

Never upscales: an original smaller than the box comes back at its own size
(fit), padded within the exact canvas (pad modes), or cropped only if genuinely
larger (fill). Transparent pad (`t`) on a non-alpha output (jpeg) is a `400`.
Quality is a global config setting, not part of the token.

## Layout

```
cmd/imgsrv/          main: env config, HTTP + health servers, shutdown
internal/token/      size-token grammar parser + allowlist validation
internal/config/     YAML config loading
internal/server/     HTTP handler, URL parsing, original resolution,
                     singleflight, atomic cache writes
internal/processor/  libvips pipeline (govips, cgo)
```

## Configuration

Environment (defaults shown):

```
ORIGINALS_ROOT=/originals        # mounted read-only
CACHE_ROOT=/cache                # mounted read-write; nginx's web root
PORT=8080
HEALTH_PORT=8081                 # GET /healthz
CONFIG=/etc/imgsrv/config.yaml
GENERATE_TIMEOUT=30s             # wall-clock bound per generation
ROOT_REDIRECT=                   # fallback URL for GET / (see Root redirects)
```

### Root redirects

A request for exactly `/` returns a 302. The destination is resolved per
domain: a `.root-redirect` file in the host's originals directory (e.g.
`/originals/imgsrv.net/.root-redirect`) containing a URL wins; the
`ROOT_REDIRECT` env var is the fallback for hosts without a file; with
neither, `/` returns 404. The file is read per request — drop, edit, or
remove it without a restart. All other non-derivative paths still 404
directly in nginx.

Config file: see [config.example.yaml](config.example.yaml).
nginx: see [nginx.conf.example](nginx.conf.example) — baked into the
container image as `/etc/nginx/imgsrv.conf`; also usable standalone if you
run nginx separately in front of the bare binary.

## Serving domains

Every served hostname is a directory directly under the roots, named as the
full lowercased hostname — no label splitting, no per-domain configuration:

```
/originals
    imgsrv.net/holiday/photo.jpg
    images.example.com/portfolio/photo.jpg
/cache
    imgsrv.net/holiday/photo-400.webp
    images.example.com/portfolio/photo-400.webp
```

`https://images.example.com/portfolio/photo-400.webp` serves
`cache/images.example.com/portfolio/photo-400.webp`, generated on first
request from `originals/images.example.com/portfolio/photo.*`.

nginx does the mapping: a `map` validates the hostname's shape (dot-separated
DNS labels — leading dots, empty labels, and `..` are impossible, so the
prefix can never traverse) and prefixes `/$host` onto the path for both
`try_files` and the proxied URI. The Go service just sees a first path
segment that happens to contain dots. A hostname with no matching originals
directory 404s naturally; a malformed Host gets a 404 straight from nginx.

To add a domain: point its DNS at the service and create the originals
directory. Behind a Cloudflare tunnel that means one DNS entry per hostname
(or a wildcard ingress rule per zone so new subdomains need only DNS).
TLS terminates at Cloudflare; note that Universal SSL covers only one
wildcard level (`*.example.com`), so deeper hostnames like `a.b.example.com`
need Advanced Certificate Manager or per-hostname certificates.

## Running

The image is a single deployment unit: nginx on port 80 (static cache
serving, URL shape gate, host→directory mapping) plus the imgsrv binary it
proxies misses to. Pull the published image (or `docker build -t imgsrv .`
to build locally):

```sh
docker run \
    -v /srv/images/originals:/originals:ro \
    -v /srv/images/cache:/cache \
    -v /srv/images/config.yaml:/etc/imgsrv/config.yaml:ro \
    -p 80:80 \
    ghcr.io/stut/imgsrv:latest
```

Port 80 is the only port to publish. Port 8081 (`/healthz`) is for
orchestrator probes and must stay off public interfaces; imgsrv itself
listens on 8080 inside the container and should never be exposed —
publishing it would bypass nginx's shape gate and host mapping.

The container exits if either process dies (let the orchestrator restart
it). `docker run --entrypoint imgsrv ghcr.io/stut/imgsrv:latest -version`
prints the build version and exits.

## Development

Requires Go ≥ 1.26 and libvips with headers (`brew install vips` /
`apk add vips-dev`).

```sh
go test ./...
go build ./cmd/imgsrv
```

The processor tests exercise the real libvips pipeline; everything else uses
stubs and needs no image library.

Releases are cut by the `Release` GitHub Actions workflow (manual dispatch,
minor/major bump). It tags the version, builds the `linux/amd64` image, and
pushes it to `ghcr.io/stut/imgsrv`. arm64 isn't built yet — add
`linux/arm64` to the workflow's `platforms` if it's needed (arm64 cgo builds
run under emulation and are slow).

## Security notes

See [SECURITY.md](SECURITY.md) for the trust model and how to report issues.
In short:

- **Originals are trusted input.** Every source file is decoded by libvips
  (and its codec dependencies — libheif/aom for AVIF, etc.), which is a large
  native-code surface with a history of memory-safety CVEs. HTTP clients can
  only select among files that already exist under `ORIGINALS_ROOT`; they
  never supply image bytes. Do not point `ORIGINALS_ROOT` at a directory that
  untrusted parties can write to, and rebuild the image periodically to pick
  up codec security fixes.
- **Run behind nginx.** The service assumes nginx shape-gates URLs and buffers
  responses; the published image bundles nginx, so this holds as long as only
  port 80 is exposed. `GENERATE_TIMEOUT` bounds the wall-clock time a single generation
  may occupy a worker (so a burst of expensive `original`/AVIF requests can't
  pin every slot indefinitely); the HTTP server also sets read/write/idle
  timeouts. The `HEALTH_PORT` endpoint is unauthenticated — keep it off any
  public interface.

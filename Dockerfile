# Build stage: needs vips-dev for the cgo bindings.
FROM golang:1.26-alpine AS build

RUN apk add --no-cache gcc musl-dev pkgconfig vips-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Stamp the binary with the release version (defaults to "dev" for local builds).
ARG VERSION=dev
RUN go build -ldflags "-X main.version=${VERSION}" -o /imgsrv ./cmd/imgsrv

# Runtime stage: alpine's vips package includes WebP natively and AVIF via
# libheif+aom. libvips is a shared-library dependency, so this can't be a
# distroless/static image — it needs the vips runtime.
#
# The container runs nginx and imgsrv together as one deployment unit:
# nginx on :80 serves cache hits from disk, gates URL shape, maps the
# hostname to the top-level directory, and proxies misses to imgsrv.
FROM alpine:3.22

RUN apk add --no-cache vips vips-heif nginx

# Links the GHCR package to the repo (shown on the repo page, and gives
# the package the repo's README/visibility context).
LABEL org.opencontainers.image.source="https://github.com/stut/imgsrv"

COPY --from=build /imgsrv /usr/local/bin/imgsrv
COPY docker/nginx.conf /etc/nginx/nginx.conf
COPY nginx.conf.example /etc/nginx/imgsrv.conf
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

# Rewritten by entrypoint.sh when ROOT_REDIRECT is set; the default keeps
# the include in imgsrv.conf valid so "/" falls through to its 404.
RUN printf '# set ROOT_REDIRECT to redirect requests for /\n' > /etc/nginx/imgsrv-root-redirect.conf

ENV ORIGINALS_ROOT=/originals \
    CACHE_ROOT=/cache \
    PORT=8080 \
    HEALTH_PORT=8081 \
    CONFIG=/etc/imgsrv/config.yaml \
    GENERATE_TIMEOUT=30s \
    ROOT_REDIRECT=""

# 80 is the public port (nginx). 8081 is imgsrv's health endpoint — keep it
# off any public interface. imgsrv itself listens on 8080; don't publish it.
EXPOSE 80 8081

ENTRYPOINT ["entrypoint.sh"]

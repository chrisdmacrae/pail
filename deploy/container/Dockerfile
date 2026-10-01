# Pail in one image: pail-server, with the web UI inside, and versitygw, the
# storage it keeps everything in. deploy/container/pail-container.sh runs it.
#
# Build it from the top of the repo:
#
#   docker build -f deploy/container/Dockerfile -t pail .
#
# Every stage that runs anything runs on the machine doing the build, and Go
# compiles for the machine the image is for. So an image for another
# architecture needs no emulation.

FROM --platform=$BUILDPLATFORM node:24-slim AS ui
RUN npm install --global pnpm@12
WORKDIR /src
COPY web web
RUN mkdir -p internal/webui/dist && cd web/ui && pnpm install --frozen-lockfile && pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETARCH
ARG VERSION=dev
ARG VERSITYGW_VERSION=v1.8.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
# The fonts pail-server's own pages use come straight from the design system.
COPY web/fonts.go web/fonts.go
COPY web/design-system/fonts web/design-system/fonts
COPY --from=ui /src/internal/webui/dist internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/pail-server ./cmd/pail-server
RUN curl -fsSL "https://github.com/versity/versitygw/releases/download/$VERSITYGW_VERSION/versitygw_${VERSITYGW_VERSION}_Linux_$([ "$TARGETARCH" = amd64 ] && echo x86_64 || echo arm64).tar.gz" \
      | tar -xz --no-same-owner -C /tmp \
    && install -m 0755 "$(find /tmp -type f -name versitygw | head -1)" /out/versitygw

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/pail-server /out/versitygw /usr/local/bin/
COPY deploy/container/entrypoint.sh /usr/local/bin/pail-entrypoint
# Everything Pail knows is under /data: mount a volume there to keep it.
VOLUME /data
EXPOSE 80 443
ENTRYPOINT ["/usr/local/bin/pail-entrypoint"]

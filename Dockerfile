# Stage 1: Build web UI
FROM node:22-alpine AS webui
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui /build/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -tags webui -ldflags="-s -w" -o sage-wiki ./cmd/sage-wiki

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache git tzdata ca-certificates && \
    adduser -D -u 1000 wiki
COPY --from=builder /build/sage-wiki /usr/local/bin/sage-wiki

USER wiki
WORKDIR /wiki
VOLUME /wiki

EXPOSE 3333

# The web UI binds 0.0.0.0 for container networking, which is non-loopback, so a
# token is REQUIRED — the server refuses to start without one. The same bind is
# subject to the DNS-rebind Host allowlist, so set SAGE_WIKI_ALLOWED_HOST to the
# hostname/IP you browse to (any non-loopback host, direct or via a proxy):
#   docker run -e SAGE_WIKI_TOKEN="$(openssl rand -hex 32)" \
#     -e SAGE_WIKI_ALLOWED_HOST=your-host -p 3333:3333 -v "$PWD:/wiki" <image>
# then open  http://your-host:3333/?token=<that token>  in a browser.
ENTRYPOINT ["sage-wiki"]
CMD ["serve", "--ui", "--bind", "0.0.0.0", "--port", "3333"]

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

ENTRYPOINT ["sage-wiki"]
CMD ["serve", "--ui", "--bind", "0.0.0.0", "--port", "3333"]

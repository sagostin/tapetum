# syntax=docker/dockerfile:1

# --- frontend build ---------------------------------------------------------
FROM node:25-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- backend build ----------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=web /app/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /tapetumd ./cmd/tapetumd

# --- runtime ----------------------------------------------------------------
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata ffmpeg
RUN adduser -D -u 1000 tapetum
COPY --from=build /tapetumd /usr/local/bin/tapetumd
USER tapetum
VOLUME /data
EXPOSE 8080
ENV TAPETUM_DATA_DIR=/data
ENTRYPOINT ["tapetumd", "-config", "/data/config.yaml"]

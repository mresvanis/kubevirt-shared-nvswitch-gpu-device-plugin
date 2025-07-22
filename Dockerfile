FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags='-s -w -extldflags "-static"' -o device-plugin ./cmd/device-plugin/

FROM alpine:3.20

RUN apk --no-cache add ca-certificates \
    && adduser -D -s /bin/sh -h /app device-plugin

WORKDIR /app

COPY --from=builder /app/device-plugin ./device-plugin
RUN chmod +x ./device-plugin

USER device-plugin

ENTRYPOINT ["./device-plugin"]

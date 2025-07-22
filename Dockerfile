FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o device-plugin ./cmd/device-plugin/

FROM alpine:3.18

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/device-plugin .

RUN adduser -D -s /bin/sh device-plugin

USER device-plugin

ENTRYPOINT ["./device-plugin"]

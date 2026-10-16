FROM golang:1.22-alpine

RUN apk add --no-cache ca-certificates git

WORKDIR /go/src/github.com/pierre-emmanuelJ/iptv-proxy
COPY . .
RUN git rev-parse HEAD > /app_commit.txt 2>/dev/null || (test -f .git/refs/heads/master && cat .git/refs/heads/master > /app_commit.txt) || echo "latest" > /app_commit.txt
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -o iptv-proxy .

FROM alpine:3
RUN apk add --no-cache ca-certificates util-linux ffmpeg && mkdir -p /data
ENV DATA_DIR=/data
ENV GOMEMLIMIT=512MiB
VOLUME ["/data"]
COPY --from=0 /app_commit.txt /
COPY --from=0  /go/src/github.com/pierre-emmanuelJ/iptv-proxy/iptv-proxy /
ENTRYPOINT ["/iptv-proxy"]

FROM golang:1.26.5-alpine AS builder
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build \
      -ldflags "-s -w" \
      -o miru .

FROM alpine:3.24
# yt-dlp is NOT installed here — it is downloaded and managed at runtime by
# internal/downloader, cached in the data volume. ca-certificates is required
# for the GitHub release fetch and all HTTPS outbound calls.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1000 miru \
    && adduser -D -u 1000 -G miru miru

WORKDIR /app
COPY --from=builder /build/miru /app/miru

USER miru
EXPOSE 8090
ENTRYPOINT ["/app/miru"]
CMD ["serve"]

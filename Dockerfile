# syntax is intentionally plain (classic BuildKit is enough)

# --- Build stage: full Go toolchain -----------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/shipyard ./cmd/shipyard

# --- Runtime stage: minimal Alpine, git + CA certs, non-root ----------------
FROM alpine:3.22
RUN apk add --no-cache ca-certificates git tzdata \
    && addgroup -S -g 65532 shipyard \
    && adduser -S -G shipyard -u 65532 shipyard \
    && mkdir -p /data \
    && chown -R shipyard:shipyard /data
COPY --from=build /out/shipyard /usr/local/bin/shipyard
USER shipyard
WORKDIR /data
ENTRYPOINT ["shipyard"]
CMD ["help"]
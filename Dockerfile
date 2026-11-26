# SubSlip container image: a static binary on Alpine with CA certificates.
#
#   docker build -t subslip --build-arg VERSION=0.1.0 .
#   docker run -d -p 5276:5276 -e UPSTREAM_URL=http://navidrome:4533 -v subslip-data:/data subslip
#
# Settings come from environment variables (see .env.example). The cache
# lives in /data; mount a volume there to keep it across restarts.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/subslip ./cmd/subslip

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 subslip \
 && mkdir /data && chown subslip /data
COPY --from=build /out/subslip /usr/local/bin/subslip
USER subslip
ENV LISTEN_ADDR=0.0.0.0:5276 \
    DB_PATH=/data/instant_mix_cache.db
VOLUME /data
EXPOSE 5276
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:5276/subslip/health >/dev/null || exit 1
ENTRYPOINT ["subslip"]
CMD ["serve"]

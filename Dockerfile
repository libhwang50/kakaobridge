# The bridge: cmd/kakaobridge.
#
# Why this recipe differs from a plain Go build:
#   * mautrix links mattn/go-sqlite3, so CGO must stay ON (a CGO_ENABLED=0 build fails in mxmain).
#   * -tags goolm selects mautrix's pure-Go olm; without it the build wants C libolm headers.
#   * The runtime base must carry glibc, because CGO links against it.
#   * The runtime user is `nonroot` (uid 65532); everything it writes under /data lands as that user.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -tags goolm -trimpath -ldflags "-s -w" -o /out/kakaobridge ./cmd/kakaobridge

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/kakaobridge /usr/local/bin/kakaobridge
EXPOSE 29315
USER nonroot:nonroot
# go.mau.fi/util/flag parses a single-dash "-config" as "-c onfig"; the long form needs --
ENTRYPOINT ["/usr/local/bin/kakaobridge", "--config", "/data/config.yaml"]

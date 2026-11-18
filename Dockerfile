FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
LABEL maintainer="fig0ut"
COPY . /go/src/github.com/fig0ut/rostra
WORKDIR /go/src/github.com/fig0ut/rostra
ARG TARGETOS TARGETARCH
ARG GOPROXY=""
ENV GOPROXY ${GOPROXY}
ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH
RUN set -ex \
    && apk add git build-base \
    && export COMMIT=$(git rev-parse --short HEAD) \
    && export VERSION=$(go run ./cmd/internal/read_tag) \
    && export TAGS=$(cat release/DEFAULT_BUILD_TAGS_OTHERS) \
    && export LDFLAGS_SHARED=$(cat release/LDFLAGS) \
    && go build -v -trimpath -tags "$TAGS" \
        -o /go/bin/rostra \
        -ldflags "-X \"github.com/fig0ut/rostra/constant.Version=$VERSION\" $LDFLAGS_SHARED -s -w -buildid=" \
        ./cmd/rostra
FROM --platform=$TARGETPLATFORM alpine AS dist
LABEL maintainer="fig0ut"
RUN set -ex \
    && apk add --no-cache --upgrade bash tzdata ca-certificates nftables
COPY --from=builder /go/bin/rostra /usr/local/bin/rostra
ENTRYPOINT ["rostra"]

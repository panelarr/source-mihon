# syntax=docker/dockerfile:1
#
# Image for running source-mihon standalone, next to mihon-source.
#
# Until source-api publishes tagged versions, go.mod points at a sibling
# source-api checkout. The build gets it as a named build context:
#
#   docker build --build-context source-api=../source-api -t panelarr/source-mihon .
#
# `make docker` does exactly that.

ARG GO_VERSION=1.27.4

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src/source-mihon
COPY --from=source-api go.mod go.sum /src/source-api/
COPY --from=source-api gen /src/source-api/gen
COPY --from=source-api sdk /src/source-api/sdk
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY gen ./gen
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/panelarr/source-mihon/internal/buildinfo.Version=${VERSION}" \
      -o /out/source-mihon ./cmd/source-mihon

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="source-mihon" \
      org.opencontainers.image.description="Mihon extensions as a Panelarr source plugin, through mihon-source" \
      org.opencontainers.image.source="https://github.com/panelarr/source-mihon" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/source-mihon /source-mihon
ENV PLUGIN_LISTEN=:8904
EXPOSE 8904
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/source-mihon", "healthcheck"]
ENTRYPOINT ["/source-mihon"]

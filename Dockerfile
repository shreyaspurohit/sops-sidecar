# Two ways to build the same image:
#   docker build .                 compiles from source (the default `runtime` stage)
#   --target prebuilt              copies a binary GoReleaser already built
# BuildKit skips stages the requested target does not need, so the `build`
# stage never runs under GoReleaser (whose context has no source).

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329 AS base
LABEL org.opencontainers.image.title="sops-sidecar" \
      org.opencontainers.image.description="Decrypts a SOPS file into a tmpfs of per-secret files for Docker Compose services" \
      org.opencontainers.image.source="https://github.com/shreyaspurohit/sops-sidecar" \
      org.opencontainers.image.licenses="MIT"
USER 65532:65532
ENTRYPOINT ["/sops-sidecar"]
CMD ["run"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/sops-sidecar", "health"]

FROM base AS prebuilt
ARG TARGETPLATFORM
COPY --chmod=0555 ${TARGETPLATFORM}/sops-sidecar /sops-sidecar

FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/sops-sidecar ./cmd/sops-sidecar

FROM base AS runtime
COPY --from=build --chmod=0555 /out/sops-sidecar /sops-sidecar

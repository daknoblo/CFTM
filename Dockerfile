# syntax=docker/dockerfile:1

# ---- Build stage ----
# Run the Go compiler on the runner's NATIVE architecture and cross-compile for
# the requested target platform. CGO is off and modernc SQLite is pure Go.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# Generated templ code and the compiled Tailwind stylesheet are committed, so
# the image needs neither the templ CLI nor Node.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w \
      -X github.com/daknoblo/CFTM/internal/version.Version=${VERSION} \
      -X github.com/daknoblo/CFTM/internal/version.Commit=${COMMIT} \
      -X github.com/daknoblo/CFTM/internal/version.Date=${DATE}" \
    -o /out/cftm ./cmd/cftm
RUN mkdir -p /out/appdata

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/cftm /cftm
COPY --from=build --chown=65532:65532 /out/appdata /appdata

ENV CFTM_ADDR=:8080 \
    CFTM_DATA_DIR=/appdata

VOLUME ["/appdata"]
EXPOSE 8080

# The binary implements its own healthcheck (distroless has no curl or wget).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/cftm", "-healthcheck"]

USER nonroot:nonroot
ENTRYPOINT ["/cftm"]

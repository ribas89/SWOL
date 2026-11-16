# Build on the runner's native arch and cross-compile to the target arch, so
# multi-arch builds don't emulate the whole Go toolchain under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine3.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN if [ "$TARGETARCH" = "arm" ]; then \
      export GOARM="${TARGETVARIANT#v}"; \
    fi; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -o swol .

FROM gcr.io/distroless/static-debian12

COPY --from=builder /app/swol /usr/local/bin/swol

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/swol"]

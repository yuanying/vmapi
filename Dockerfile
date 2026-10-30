# The vmapi server, built as a static binary, on a Debian base that carries
# the two programs it runs on the host's behalf: qemu-img makes each VM's
# qcow2 overlay, and xorriso makes its cloud-init seed ISO (docs/adr/0005, 0006).
# vmctl is not in the image; it is released as archives only.

FROM golang:1.27 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

# CGO off makes the result static, so it does not depend on the runtime
# image's libc. The caches are mounts rather than layers so that rebuilding
# after an edit does not start from an empty module cache.
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/vmapi ./cmd/vmapi

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates qemu-utils xorriso \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/vmapi /usr/local/bin/vmapi
# Who this runs as is the compose file's to say: it has to be able to open the
# libvirt socket and write the disk directory, and both belong to the host.
# Until it says, nothing runs as root.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/vmapi"]

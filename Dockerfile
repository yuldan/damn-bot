# Official Damn-Bot image (GAP-INF-03, GAP-SEC-22, ADR-033 Q35/Q49).
#
# Two stages: a Debian builder cross-compiles a static binary (CGO disabled,
# goolm tag) for the requested target platform, and a distroless runtime stage
# (FROM scratch: no distro, no shell, no compiler, no package manager) carries
# only the binary, the CA bundle, time/tzdata embedded in the binary, and an
# /etc/passwd entry for UID 65532. Image size stays within binary + 2 MB.
#
# Multi-arch: the builder runs on the build host ($BUILDPLATFORM) and emits
# $TARGETOS/$TARGETARCH, so `docker buildx build --platform linux/amd64,linux/arm64`
# needs no QEMU emulation (both stages are copies after the build).
FROM --platform=$BUILDPLATFORM golang:1.26.11-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Dependency layer first so source-only changes reuse the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static release flags match scripts/size-check.sh (CGO=0, -trimpath, -s -w).
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -tags goolm -ldflags="-s -w" -o /out/damnbot ./cmd/damnbot \
	&& printf 'nonroot:x:65532:65532:nonroot:/data:/sbin/nologin\n' > /out/passwd \
	&& printf 'nonroot:x:65532:\n' > /out/group

# Distroless runtime: FROM scratch, so there is no /bin/sh, no compiler and no
# package manager to exec. identity: 65532:65532 (distroless nonroot).
FROM scratch AS distroless

COPY --from=build /out/damnbot /damnbot
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENV HOME=/data \
	XDG_CONFIG_HOME=/data/.config \
	XDG_DATA_HOME=/data/.local/share \
	XDG_CACHE_HOME=/data/.cache \
	SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt

USER 65532:65532

# REL-17: the same readiness probe `serve` exposes on /readyz.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
	CMD ["/damnbot", "doctor", "health", "--ready"]

ENTRYPOINT ["/damnbot"]
CMD ["serve"]

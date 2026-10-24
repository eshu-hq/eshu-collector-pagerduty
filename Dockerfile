# PagerDuty collector image: one static Go binary on a distroless base with no
# shell. The image is also the component's delivery artifact: an init container
# runs `/collector seed --dest DIR` to copy the binary and its manifest into the
# Eshu component home, and the host's process runner then executes the seeded
# copy. The manifest cannot hold the digest of the image that carries it, so the
# deployer passes it with `--image-ref REPO@sha256:DIGEST` (or the
# COLLECTOR_IMAGE_REF environment variable) and seed stamps it into the seeded
# manifest. Run with no arguments, the binary reads one collector-sdk/v1alpha1
# Request from standard input.
#
# Both base images are pinned by digest. Re-resolve them (docker buildx
# imagetools inspect <ref>) when bumping either base.
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/collector ./cmd/collector

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/collector /collector
USER 65532:65532
ENTRYPOINT ["/collector"]

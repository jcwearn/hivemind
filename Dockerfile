# Two stages, and deliberately no Node stage.
#
# templ generates plain Go, and the generated *_templ.go files are committed, so
# the image build needs the templ CLI exactly as much as it needs a JavaScript
# toolchain: not at all. codegen.yml is what keeps the committed output honest.
#
# Digests are pinned rather than tags. Renovate bumps them as reviewable PRs,
# which is the difference between "the base image changed" being a commit and
# being a mystery.

FROM golang:1.27@sha256:65b6f280bf050ec5af12716857e8ea8439d694dbba8f31ceeb7630670071f2bb AS build

WORKDIR /src

# Dependencies first, in their own layer, so editing a handler does not
# re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY static/ static/

# CGO_ENABLED=0 is what makes the distroless static base below viable: no libc
# to link against at runtime. -trimpath keeps build paths out of the binary, and
# -s -w drop the symbol and DWARF tables, which is most of the difference
# between a 12MB image and a 20MB one.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hivemind ./cmd/hivemind

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/hivemind /hivemind

# Everything the browser needs is embedded in the binary, so there is nothing
# else to copy and no volume to mount.
EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/hivemind"]

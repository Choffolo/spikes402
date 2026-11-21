# Spikes402 proxy image. Config is mounted at runtime; secrets come from
# the environment. Do not bake passwords, seeds, or a live config.json in.
#
# Base images are pinned by digest (recorded 2026-11-21). Refresh the
# digests when you bump Go or the runtime.
#
#   docker build -t spikes402:local .

# Go 1.27.4 on Debian bookworm (matches the module toolchain).
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY LICENSE ./
COPY cmd/spikes402 ./cmd/spikes402
COPY client ./client
COPY internal ./internal
COPY pow ./pow
COPY webbotauth ./webbotauth

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/spikes402 ./cmd/spikes402
RUN mkdir -p /out/state && chmod 0700 /out/state

# Static, non-root, no shell. Includes CA certificates (needed to fetch
# crawler IP lists). The default user is 65532:65532.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build --chown=nonroot:nonroot /out/spikes402 /spikes402
COPY --from=build /src/LICENSE /LICENSE
# Empty state dir, owned by the process user, so a named volume starts writable.
COPY --from=build --chown=nonroot:nonroot /out/state /var/lib/spikes402
USER nonroot:nonroot
# Soft heap target for the Go runtime. Override at run time if the host
# has more (or less) memory, e.g. docker compose environment GOMEMLIMIT=512MiB.
# See docs/deploy.md.
ENV GOMEMLIMIT=256MiB
EXPOSE 8402
ENTRYPOINT ["/spikes402"]
CMD ["-config", "/etc/spikes402/config.json"]

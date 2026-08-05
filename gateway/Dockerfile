# dsgate — the free-tier gateway for deepseek-cli.
#
# Built here rather than on the target because the target is a 1 GiB box
# already running someone else's production, and a Go build wants most of
# that for itself.
#
# The result is a static binary on distroless: no shell, no package
# manager, nothing to exec into. That matters more than usual for this
# service — it holds a real API key in its environment, and the smallest
# possible image is the cheapest way to keep that boring.

FROM golang:1.26-alpine AS build

WORKDIR /src

# The gateway is its own module with no dependencies, so there is nothing
# to download and no cache mount worth the complexity.
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/dsgate ./cmd/dsgate

# Verify the thing we are about to ship actually runs. A binary built for
# the wrong architecture fails here rather than in a restart loop on the
# host.
RUN /out/dsgate --version

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/dsgate /usr/local/bin/dsgate

# State: the signing secret, the spend journal, the revocation list.
# Losing the secret invalidates every enrolment, and losing the journal
# refunds the credit pool — so this is the one directory that has to be a
# volume.
VOLUME ["/var/lib/dsgate"]
ENV DSGATE_STATE_DIR=/var/lib/dsgate \
    DSGATE_ADDR=:8787

EXPOSE 8787
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/dsgate"]

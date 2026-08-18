# The Flowy node as one image: the console built by vite, embedded into the Go
# binary, and nothing else shipped beside it.
#
# Three stages, because the two toolchains have nothing to say to each other.
# The console stage produces web/dist; the Go stage needs that directory to
# exist before it compiles, since console.go embeds it with `//go:embed
# all:web/dist` and a pattern matching nothing is a build error rather than an
# empty directory. web/dist is gitignored apart from its .gitkeep, so a clone
# has the placeholder and this build replaces it with the real thing.
#
# What is deliberately NOT here: cmd/handoff-runner. That binary drives Docker
# and so needs the host's socket, which is the whole privilege this image is
# built to not have. It is deployed on the trusted host instead - see
# deploy/README.md and deploy/handoff-runner.compose.yaml.

# ---------------------------------------------------------------- the console
FROM node:24-alpine AS console
WORKDIR /src/web
# The manifest and the lockfile first, on their own, so that `npm ci` is a
# cached layer for every build that only changed source. `npm ci` and not `npm
# install`: the lockfile is committed, and a deploy image that resolved its own
# dependency versions would be a different console from the one that was tested.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# `npm run build` is tsc then vite then scripts/keep-dist.mjs, which puts the
# .gitkeep back after vite empties the directory. The Go stage below copies the
# result over its own checkout of web/dist.
RUN npm run build

# ---------------------------------------------------------------- the binary
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
COPY --from=console /src/web/dist ./web/dist
# BUILD_STAMP is what `flowy version` reports. Pass the commit the image was
# built from - deploy/bootstrap.sh does - so that a node in the field can say
# which source it is, which is the first question asked of any node that
# misbehaves.
ARG BUILD_STAMP=""
# Vendored: the module's dependencies are committed under vendor/, so the build
# needs no network and cannot pick up a different version of anything than the
# gate ran against. CGO off because the runtime stage is a different libc from
# this one, and because nothing here needs cgo: lib/pq is pure Go.
#
# Everything under ./cmd is built as well as the node itself, so that the day
# cmd/handoff-runner lands it is already in this image and the trusted-host
# deploy has a binary to run.
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath \
    -ldflags "-s -w -X main.buildStamp=${BUILD_STAMP}" \
    -o /out/flowy . \
 && CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags "-s -w" \
    -o /out/ ./cmd/...

# ---------------------------------------------------------------- the runtime
#
# Named, and named in compose as `target: node`, because this is not the last
# stage in the file: the runner stage below it is. A build with no target takes
# the last stage, so leaving this unnamed silently shipped the runner image
# under the node's tag the first time the two lived in one Dockerfile.
FROM alpine:3.21 AS node
# ca-certificates because a node talks to its peers and to a forge over TLS, and
# a container with no root store fails that at the first request with an error
# that names the certificate rather than the missing store.
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10001 flowy
# The schema travels with the binary. A node cannot create its own tables - see
# BackfillProjects, which refuses a database with no projects table by telling
# the operator to apply schema.sql - so the file has to be somewhere the
# operator can reach without a checkout. deploy/compose.yaml mounts the repo's
# copy into Postgres's init directory; this one is for a hand-run psql against a
# database that was created some other way.
COPY --from=build /src/schema.sql /usr/share/flowy/schema.sql
COPY --from=build /out/ /usr/local/bin/
USER flowy
# 0.0.0.0 rather than the binary's own default of 127.0.0.1: inside a container
# the loopback default means "reachable by nothing", including by the other
# containers on the same compose network. The isolation that default was
# protecting is provided here by the network being internal and by publishing no
# host port, which is a stronger fence than a listen address.
ENV FLOWY_ADDR=0.0.0.0:8787
EXPOSE 8787
# busybox wget rather than curl, which is not installed: this is one GET against
# an endpoint that is deliberately open (see handleHealthz - a health check that
# needs a credential stops working at the worst moment).
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=6 \
    CMD wget -q -O- http://127.0.0.1:8787/healthz >/dev/null || exit 1
ENTRYPOINT ["flowy"]
CMD ["serve"]

# ------------------------------------------------- the trusted-host runner
# A SECOND image, for cmd/handoff-runner, and it exists because that binary
# cannot live in the image above.
#
# The runner drives Docker: it builds a system-under-test by shelling out to
# scripts/build-sut.sh, which needs bash, git and the docker CLI, and it brings
# up a generated compose package per run. So it needs a toolchain the node image
# deliberately does not have, and it needs the host's Docker socket, which is
# root on the host with extra steps. Those two facts are why it is a separate
# image on a separate deployment: keeping it out is what lets the node drop
# every capability and mount nothing.
#
# It is built explicitly (`--target runner`), never by a plain compose build of
# the store side. See deploy/handoff-runner.compose.yaml.
FROM docker:27-cli AS runner
# bash because build-sut.sh is a bash program and its arrays and [[ ]] are not
# POSIX sh; git because the build checks the source out at a sha through a
# worktree; ca-certificates for the registry and for the store's TLS.
RUN apk add --no-cache bash git ca-certificates
# Every ./cmd binary, not handoff-runner by name. cmd/handoff-runner is piece 9
# and may not have landed in the tree this image is built from; naming it here
# would make this stage unbuildable until it does, and copying the directory
# means the day it lands the image simply has it. deploy/handoff-runner.sh
# checks for it and says so rather than failing obscurely.
COPY --from=build /out/ /usr/local/bin/
# The build script and its per-project configuration travel with the binary. The
# runner shells out to the script by path, and a deployment that had the binary
# and not the script would fail at the first build with a missing file.
COPY --from=build /src/scripts/ /opt/flowy/scripts/
COPY --from=build /src/schema.sql /usr/share/flowy/schema.sql
ENV SUT_CONFIG_DIR=/opt/flowy/scripts/build-sut.d
# No USER and no read_only here, unlike the node: this container talks to the
# Docker socket and writes worktrees and build output into the scratch area, and
# pretending otherwise would only mean a set of permissions that do not hold.
# The isolation for this one is that it runs on a host that is already trusted
# with Docker, and that it publishes no port off that host.
ENTRYPOINT ["handoff-runner"]

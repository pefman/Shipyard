# Shipyard's built-in pi coding agent sandbox runtime.
#
# Builds a wrapper image around a language base image (ARG BASE, e.g.
# golang:1.22) with the pi coding agent runtime added: the Node.js
# runtime plus a preinstalled @earendil-works/pi-coding-agent. The npm
# tree is installed fully offline from the bundled cache
# (npm-cache.tar.gz, shipped in this directory with Shipyard), so
# building the wrapper never fetches the agent from the network — a
# fresh build is offline-complete with respect to the agent itself.
#
# Build (what shipyard does automatically):
#   docker build --build-arg BASE=golang:1.22 \
#                -t shipyard-sandbox/golang-1.22:pi-0.84.4 .
#
# The piagent stage uses a glibc-based Node image; for alpine-based
# base images pass --build-arg PIBASE=node:24-alpine (shipyard picks
# this automatically). The base image must provide /bin/sh and git
# (all auto-detected Shipyard language images do).

# BASE: the language image the wrapper is built on (docker build --build-arg BASE=…).
# PIBASE: the Node image for the piagent stage; glibc variant by default,
#         alpine variant for alpine-based base images.
ARG BASE
ARG PIBASE=node:24-bookworm-slim

FROM ${PIBASE} AS piagent
WORKDIR /build
COPY package.json package-lock.json ./
COPY npm-cache.tar.gz /tmp/npm-cache.tar.gz
RUN mkdir -p /root/.npm \
    && tar xzf /tmp/npm-cache.tar.gz -C /root/.npm \
    && npm ci --offline --ignore-scripts --no-audit --no-fund \
    && rm -rf /root/.npm /tmp/npm-cache.tar.gz package.json package-lock.json

FROM ${BASE}
COPY --from=piagent /usr/local/bin/node /usr/local/bin/node
COPY --from=piagent /build/node_modules /opt/shipyard-pi/node_modules
COPY pi-launcher.sh /usr/local/bin/pi
RUN chmod 0755 /usr/local/bin/pi
# The agent config directory for a run is created by shipyard inside
# the checkout; PI_CODING_AGENT_DIR is passed per container.
ENV PI_OFFLINE=1
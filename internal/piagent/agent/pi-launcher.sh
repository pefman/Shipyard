#!/bin/sh
# Shipyard's built-in pi coding agent launcher (installed at
# /usr/local/bin/pi in the sandbox image). The agent runtime lives in
# /opt/shipyard-pi (preinstalled from Shipyard's bundled vendor
# runtime; nothing is fetched at container start).
exec /usr/local/bin/node /opt/shipyard-pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js "$@"
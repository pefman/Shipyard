# Bundled pi agent runtime (offline vendor bundle)

This directory **is** the pi coding agent runtime that ships with
Shipyard. It is embedded into the shipyard binary (`go:embed`) and is
used to build the sandbox wrapper image for live/dry-run agent runs:

- `Dockerfile` — two stages: installs the pi agent offline (from the
  bundled npm cache, no network) on a Node base image, then copies the
  `node` runtime plus the installed agent tree into the language base
  image (`--build-arg BASE=…`).
- `package.json` / `package-lock.json` — pins
  `@earendil-works/pi-coding-agent` to the version in
  `internal/piagent` (`piagent.Version`).
- `npm-cache.tar.gz` — a relocatable npm cache holding every tarball
  the lock file needs, so `npm ci --offline` inside the image build
  never touches the network.
- `pi-launcher.sh` — the `pi` command installed at `/usr/local/bin/pi`
  in the wrapper image (runs the preinstalled agent with the bundled
  `node`).

After `docker build`, the wrapper image is ready to use offline:
nothing about the agent is downloaded at container start (pi itself is
also run with `PI_OFFLINE=1`).

## Updating the bundled agent

To bump to a newer pi release:

1. Change `piagent.Version` in `../piagent.go`.
2. In a scratch directory:
   ```sh
   npm init -y
   npm install --ignore-scripts \
       @earendil-works/pi-coding-agent@<new-version>
   cp package.json package-lock.json <this directory>/
   ```
   (the install was done with `--cache <scratch-cache>`; then)
   ```sh
   tar czf npm-cache.tar.gz -C <scratch-cache> _cacache
   ```
3. Verify offline: in a fresh directory with the lock file and an
   empty cache populated from `npm-cache.tar.gz`,
   `npm ci --offline` must succeed on Node ≥ 22.19 (the image uses
   node:24).
4. Run the Go tests (`go test ./...`) and rebuild a wrapper image to
   smoke-test the pinned release.
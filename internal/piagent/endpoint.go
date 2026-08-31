// Endpoint reachability from the sandbox container: detecting
// host-loopback AI endpoints, remapping them to the in-container name
// for the Docker host, and the from-sandbox reachability check that
// runs before the agent so a mis-bound local model server fails fast
// with an actionable message instead of the agent's first model call.
package piagent

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// HostGatewayName is the in-container name for the Docker host.
// Inside a sandbox container, localhost is the container itself — a
// host-loopback AI endpoint is only reachable under this name, which
// shipyard maps to the host via --add-host=host.docker.internal:
// host-gateway on every container run (Docker resolves host-gateway on
// all platforms; on Linux it is also what makes plain
// host.docker.internal names resolvable at all).
const HostGatewayName = "host.docker.internal"

// isLoopbackHost reports whether host names the host's own loopback
// interface: localhost (any casing, optional trailing dot), 127.0.0.1,
// any address in 127.0.0.0/8, or ::1.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// remapLoopbackEndpoint rewrites the host of a loopback endpoint to
// HostGatewayName, preserving scheme, port, and path
// (http://localhost:8080/v1 → http://host.docker.internal:8080/v1), so
// the same address works from inside the sandbox container. Endpoints
// that are not loopback — public URLs, LAN addresses, endpoints
// already on host.docker.internal — come back unchanged. The second
// result is true when a rewrite happened.
func remapLoopbackEndpoint(endpoint string) (string, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint, false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		host, port = u.Host, ""
	}
	if !isLoopbackHost(host) {
		return endpoint, false
	}
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	u.Host = HostGatewayName + ":" + port
	return u.String(), true
}

// probeTarget names the from-sandbox reachability check that runs
// before the agent step when a loopback endpoint was remapped.
type probeTarget struct {
	// host and port are the in-container address the model server
	// must answer at (HostGatewayName and the endpoint's port).
	host, port string
	// original is the endpoint the operator configured; remapped is
	// the address the sandbox actually uses.
	original, remapped string
}

// probeForRemap builds the probe target for a remapped endpoint:
// the in-container address (HostGatewayName plus the endpoint's
// port) the host-local model server must answer at.
func probeForRemap(original, remapped string) *probeTarget {
	p := &probeTarget{host: HostGatewayName, original: original, remapped: remapped}
	if u, err := url.Parse(remapped); err == nil {
		if host, port, err := net.SplitHostPort(u.Host); err == nil {
			p.host, p.port = host, port
		}
	}
	return p
}

// probeCommand builds the reachability check as one /bin/sh command
// line: a short-timeout TCP connect from inside the container to
// host:port, run with node (the wrapper image ships it in every base
// image; curl/wget are absent from slim bases). Exit 0 means the
// host-local AI server answers at that address.
func probeCommand(host, port string) string {
	return fmt.Sprintf(
		`node -e "const n=require('net');const s=n.connect('%s',%s);`+
			`const t=setTimeout(()=>process.exit(1),5000);`+
			`s.on('connect',()=>{clearTimeout(t);s.end();process.exit(0)});`+
			`s.on('error',()=>{clearTimeout(t);process.exit(1)})"`,
		strings.ReplaceAll(host, "'", ""), port)
}

// probeFailureMessage is the actionable error for a failed
// from-sandbox probe: the host-local AI endpoint is not reachable
// under the in-container address. It names that exact address, what
// shipyard remapped, and the bind requirement, so the operator does
// not have to guess why the host-side endpoint works but the sandbox
// cannot reach it.
func probeFailureMessage(original, remapped, address string) string {
	return fmt.Sprintf(
		"agent: cannot reach your local AI endpoint from the sandbox: nothing answers at %s. "+
			"Shipyard uses %s inside the sandbox for your --ai-endpoint %s (localhost inside the container is the container itself). "+
			"Start the model server on the host bound to a non-loopback interface — e.g. --host 0.0.0.0 instead of 127.0.0.1 — and retry. "+
			"If it already binds 0.0.0.0 and nothing still answers, check the host firewall (e.g. ufw/nftables) for rules blocking the docker bridge network (172.17.0.0/16) from reaching that port. "+
			"On rootless podman, reach the host via host.containers.internal instead of host.docker.internal, or run the sandbox with --net=host.",
		address, remapped, original)
}

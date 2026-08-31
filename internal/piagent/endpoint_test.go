package piagent

import (
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":                true,
		"LOCALHOST":                true,
		"localhost.":               true,
		"127.0.0.1":                true,
		"127.1.2.3":                true, // anywhere in 127.0.0.0/8
		"127.255.255.255":          true,
		"::1":                      true,
		"host.docker.internal":     false,
		"host.containers.internal": false,
		"10.0.0.5":                 false,
		"192.168.1.5":              false,
		"0.0.0.0":                  false,
		"example.com":              false,
		"":                         false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %t, want %t", host, got, want)
		}
	}
}

func TestRemapLoopbackEndpoint(t *testing.T) {
	cases := []struct {
		in           string
		want         string
		wantRemapped bool
	}{
		{"http://localhost:8080/v1", "http://host.docker.internal:8080/v1", true},
		{"http://localhost:8080", "http://host.docker.internal:8080", true},
		{"http://127.0.0.1:8765/v1", "http://host.docker.internal:8765/v1", true},
		{"http://127.5.6.7:9000", "http://host.docker.internal:9000", true},
		{"http://localhost/v1", "http://host.docker.internal:80/v1", true}, // scheme default made explicit
		{"https://[::1]:443/v1", "https://host.docker.internal:443/v1", true},
		{"http://example.com:8080/v1", "http://example.com:8080/v1", false},
		{"http://192.168.1.5:8080/v1", "http://192.168.1.5:8080/v1", false},
		{"http://host.docker.internal:8080/v1", "http://host.docker.internal:8080/v1", false},
	}
	for _, c := range cases {
		got, remapped := remapLoopbackEndpoint(c.in)
		if got != c.want {
			t.Errorf("remapLoopbackEndpoint(%q) = %q, want %q", c.in, got, c.want)
		}
		if remapped != c.wantRemapped {
			t.Errorf("remapLoopbackEndpoint(%q) remapped = %t, want %t", c.in, remapped, c.wantRemapped)
		}
	}
}

func TestProbeCommand(t *testing.T) {
	cmd := probeCommand("host.docker.internal", "8080")
	for _, want := range []string{
		"node -e",
		"n.connect('host.docker.internal',8080)",
		"setTimeout",
		"5000",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("probeCommand missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "'node") && !strings.HasPrefix(cmd, "node -e") {
		t.Errorf("probeCommand does not run node:\n%s", cmd)
	}
}

func TestProbeFailureMessage(t *testing.T) {
	msg := probeFailureMessage("http://localhost:8080/v1", "http://host.docker.internal:8080/v1", "host.docker.internal:8080")
	for _, want := range []string{
		"host.docker.internal:8080",
		"http://localhost:8080/v1",
		"0.0.0.0",
		"non-loopback",
		"firewall",
		"host.containers.internal",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message missing %q:\n%s", want, msg)
		}
	}
}

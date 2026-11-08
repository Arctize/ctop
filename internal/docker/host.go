package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostSpec describes how to reach a Docker daemon.
type HostSpec struct {
	Host   string // normalized URL, e.g. ssh://home, unix:///var/run/docker.sock
	Source string // where the host came from (flag, DOCKER_HOST, context name, default)
	TLS    *TLSFiles
}

// TLSFiles points at client certificates for tcp:// daemons.
type TLSFiles struct {
	CA, Cert, Key string
	SkipVerify    bool
}

// NormalizeHost turns a user supplied host into a URL. Anything without a
// scheme is treated as an ssh destination, so "home" becomes "ssh://home"
// and "me@box:2222" becomes "ssh://me@box:2222". Absolute paths are unix
// sockets.
func NormalizeHost(h string) string {
	h = strings.TrimSpace(h)
	switch {
	case h == "":
		return ""
	case strings.Contains(h, "://"):
		return h
	case strings.HasPrefix(h, "/"):
		return "unix://" + h
	default:
		return "ssh://" + h
	}
}

// Resolve figures out the daemon to talk to, mirroring the docker CLI's
// precedence: -H flag, --context flag, DOCKER_HOST, DOCKER_CONTEXT, the
// current context in the CLI config, then the default local socket.
func Resolve(flagHost, flagContext, runtime string) (HostSpec, error) {
	if flagHost != "" {
		return HostSpec{Host: NormalizeHost(flagHost), Source: "flag"}, nil
	}
	if flagContext != "" {
		return fromContext(flagContext, runtime)
	}
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		spec := HostSpec{Host: NormalizeHost(h), Source: "DOCKER_HOST"}
		if certPath := os.Getenv("DOCKER_CERT_PATH"); certPath != "" || os.Getenv("DOCKER_TLS_VERIFY") != "" {
			if certPath == "" {
				certPath = configDir()
			}
			spec.TLS = &TLSFiles{
				CA:         filepath.Join(certPath, "ca.pem"),
				Cert:       filepath.Join(certPath, "cert.pem"),
				Key:        filepath.Join(certPath, "key.pem"),
				SkipVerify: os.Getenv("DOCKER_TLS_VERIFY") == "",
			}
		}
		return spec, nil
	}
	if c := os.Getenv("DOCKER_CONTEXT"); c != "" {
		return fromContext(c, runtime)
	}
	if c := currentContext(); c != "" && c != "default" {
		spec, err := fromContext(c, runtime)
		if err == nil {
			return spec, nil
		}
	}
	return HostSpec{Host: defaultHost(runtime), Source: "default"}, nil
}

func configDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docker")
}

func currentContext() string {
	b, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return ""
	}
	return cfg.CurrentContext
}

func fromContext(name, runtime string) (HostSpec, error) {
	if name == "default" {
		return HostSpec{Host: defaultHost(runtime), Source: "context default"}, nil
	}
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	b, err := os.ReadFile(filepath.Join(configDir(), "contexts", "meta", id, "meta.json"))
	if err != nil {
		return HostSpec{}, fmt.Errorf("docker context %q not found", name)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host          string `json:"Host"`
			SkipTLSVerify bool   `json:"SkipTLSVerify"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return HostSpec{}, fmt.Errorf("docker context %q: %w", name, err)
	}
	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return HostSpec{}, fmt.Errorf("docker context %q has no docker endpoint", name)
	}
	spec := HostSpec{Host: NormalizeHost(ep.Host), Source: "context " + name}
	tlsDir := filepath.Join(configDir(), "contexts", "tls", id, "docker")
	if _, err := os.Stat(filepath.Join(tlsDir, "cert.pem")); err == nil {
		spec.TLS = &TLSFiles{
			CA:         filepath.Join(tlsDir, "ca.pem"),
			Cert:       filepath.Join(tlsDir, "cert.pem"),
			Key:        filepath.Join(tlsDir, "key.pem"),
			SkipVerify: ep.SkipTLSVerify,
		}
	}
	return spec, nil
}

func defaultHost(runtime string) string {
	const sys = "/var/run/docker.sock"
	var cands []string
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if runtime != "podman" {
		cands = append(cands, sys)
		if rt != "" {
			cands = append(cands, filepath.Join(rt, "docker.sock")) // rootless docker
		}
	}
	if runtime != "docker" {
		if rt != "" {
			cands = append(cands, filepath.Join(rt, "podman", "podman.sock"))
		}
		cands = append(cands, "/run/podman/podman.sock")
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return "unix://" + p
		}
	}
	if runtime == "podman" && len(cands) > 0 {
		return "unix://" + cands[0]
	}
	return "unix://" + sys
}

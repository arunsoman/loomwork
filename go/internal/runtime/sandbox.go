package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SandboxPolicy is the runtime's view of sandbox.json. The runtime enforces
// fs.read, fs.write and net.egress on the operations it performs itself
// (folder indexing, calls to the model server) and time.maxWall on each
// request. CPU and memory limits are declared but not applied.
type SandboxPolicy struct {
	FSRead      []string `json:"fs.read"`
	FSWrite     []string `json:"fs.write"`
	NetEgress   []string `json:"net.egress"`
	NetListen   []int    `json:"net.listen"`
	CPU         string   `json:"resources.cpu"`
	Memory      string   `json:"resources.memory"`
	TimeMaxWall string   `json:"time.maxWall"`
}

// DefaultSandboxPolicy returns a restrictive policy: read the home directory,
// write only to a workspace dir, talk only to localhost.
func DefaultSandboxPolicy(workspaceDir string) *SandboxPolicy {
	home, _ := os.UserHomeDir()
	if workspaceDir == "" {
		workspaceDir = filepath.Join(home, ".loomwork", "workspace")
	}
	return &SandboxPolicy{
		FSRead:      []string{home + "/**"},
		FSWrite:     []string{workspaceDir + "/**"},
		NetEgress:   []string{"localhost", "127.0.0.1", "::1"},
		NetListen:   []int{},
		CPU:         "1",
		Memory:      "1GiB",
		TimeMaxWall: "10m",
	}
}

// ParseSandboxPolicy parses sandbox.json and expands $HOME / ~ in paths.
func ParseSandboxPolicy(data []byte) (*SandboxPolicy, error) {
	var p SandboxPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse sandbox.json: %w", err)
	}
	p.FSRead = expandAll(p.FSRead)
	p.FSWrite = expandAll(p.FSWrite)
	if p.TimeMaxWall != "" {
		if _, err := time.ParseDuration(p.TimeMaxWall); err != nil {
			return nil, fmt.Errorf("sandbox.json time.maxWall: %w", err)
		}
	}
	return &p, nil
}

// MaxWall returns the per-request wall-clock limit (0 = none).
func (p *SandboxPolicy) MaxWall() time.Duration {
	d, _ := time.ParseDuration(p.TimeMaxWall)
	return d
}

func expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = expandHome(s)
	}
	return out
}

func expandHome(s string) string {
	home, _ := os.UserHomeDir()
	switch {
	case s == "~" || s == "$HOME":
		return home
	case strings.HasPrefix(s, "~/"):
		return filepath.Join(home, s[2:])
	case strings.HasPrefix(s, "$HOME/"):
		return filepath.Join(home, s[6:])
	}
	return s
}

// canonical resolves a path to an absolute, symlink-free form (as far as it exists).
func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	// Path (or its tail) does not exist yet: resolve the deepest existing parent.
	dir, base := filepath.Split(abs)
	if dir == "" || dir == abs {
		return abs, nil
	}
	parent, err := canonical(filepath.Clean(dir))
	if err != nil {
		return abs, nil
	}
	return filepath.Join(parent, base), nil
}

func matchAny(path string, globs []string) bool {
	for _, g := range globs {
		if matchGlob(path, g) {
			return true
		}
	}
	return false
}

// CheckRead returns nil if path may be read.
func (p *SandboxPolicy) CheckRead(path string) error {
	abs, err := canonical(path)
	if err != nil {
		return err
	}
	if matchAny(abs, p.canonGlobs(p.FSRead)) {
		return nil
	}
	return fmt.Errorf("sandbox: read of %s not in fs.read allowlist %v", abs, p.FSRead)
}

// CheckWrite returns nil if path may be written.
func (p *SandboxPolicy) CheckWrite(path string) error {
	abs, err := canonical(path)
	if err != nil {
		return err
	}
	if matchAny(abs, p.canonGlobs(p.FSWrite)) {
		return nil
	}
	return fmt.Errorf("sandbox: write to %s not in fs.write allowlist %v", abs, p.FSWrite)
}

// canonGlobs resolves symlinks in the fixed prefix of each path glob so it can
// be compared with canonical paths.
func (p *SandboxPolicy) canonGlobs(globs []string) []string {
	out := make([]string, len(globs))
	for i, g := range globs {
		if strings.HasSuffix(g, "/**") {
			if c, err := canonical(strings.TrimSuffix(g, "/**")); err == nil {
				out[i] = c + "/**"
				continue
			}
		}
		out[i] = g
	}
	return out
}

// CheckEgress returns nil if host may be contacted.
func (p *SandboxPolicy) CheckEgress(host string) error {
	if len(p.NetEgress) == 0 {
		return fmt.Errorf("sandbox: no egress allowed (host=%s)", host)
	}
	if matchAny(host, p.NetEgress) {
		return nil
	}
	return fmt.Errorf("sandbox: egress to %s not in net.egress allowlist %v", host, p.NetEgress)
}

// matchGlob matches a path or host against a pattern. "**" alone matches
// anything; "dir/**" matches dir and everything below it; otherwise
// filepath.Match semantics apply ("*" does not cross "/").
func matchGlob(path, pattern string) bool {
	if pattern == "**" {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	if path == pattern {
		return true
	}
	ok, _ := filepath.Match(pattern, path)
	return ok
}

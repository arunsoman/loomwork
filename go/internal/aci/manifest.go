// Package aci implements the Agent Container Image format.
//
// Spec: §4 of Loomwork PRD v0.1. Wire-compatible with the Python reference
// implementation at github.com/loomwork/loomwork (Python v0.1).
//
// An ACI is a gzipped tar archive with the layout:
//
//	my-agent.aci
//	├── manifest.json
//	├── persona/
//	│   └── system_prompt.md
//	├── skills/
//	│   └── graph.json
//	├── tools/
//	│   └── bindings.json
//	├── memory-schema.json
//	├── sandbox.json
//	└── signatures/
//	    ├── manifest.sig
//	    └── manifest.cert
//
// The manifest is signed with Ed25519 over its canonical-JSON serialization
// (RFC 8785 JCS). Every other file is referenced by SHA-256 digest from the
// manifest, so the signature transitively covers the entire ACI.
package aci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	APIVersion     = "aci.loomwork.dev/v0.1"
	Kind           = "Agent"
	digestPattern  = `^sha256:[0-9a-f]{64}$`
	namePattern    = `^[a-z0-9][a-z0-9-]*$`
	versionPattern = `^v?\d+\.\d+\.\d+`
)

// Manifest is the ACI manifest root. Matches the JSON Schema in PRD §4.3.
type Manifest struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   Metadata          `json:"metadata"`
	Persona    PersonaRef        `json:"persona"`
	Skills     SkillsRef         `json:"skills"`
	Tools      ToolsRef          `json:"tools"`
	Memory     MemoryRef         `json:"memory"`
	Sandbox    SandboxRef        `json:"sandbox"`
	Digests    map[string]string `json:"digests"`
}

type Metadata struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Description  string `json:"description,omitempty"`
	License      string `json:"license,omitempty"`
	Homepage     string `json:"homepage,omitempty"`
}

type PersonaRef struct {
	SystemPrompt string                 `json:"systemPrompt"`
	FewShot      string                 `json:"fewShot,omitempty"`
	ModelPrefs   map[string]interface{} `json:"modelPrefs,omitempty"`
	Capabilities map[string]bool        `json:"capabilities,omitempty"`
}

type SkillsRef struct {
	Graph string `json:"graph"`
}

type ToolsRef struct {
	Bindings string `json:"bindings"`
}

// MemoryRef uses the alias "schema" because Go's "schema" would collide with
// pydantic's reserved attribute. Wire-format identical to Python impl.
type MemoryRef struct {
	Schema string `json:"schema"`
}

type SandboxRef struct {
	Spec string `json:"spec"`
}

// Validate runs the manifest-level checks from PRD §4.3 + §4.11 (C3).
func (m *Manifest) Validate() error {
	if m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q, got %q", APIVersion, m.APIVersion)
	}
	if m.Kind != Kind {
		return fmt.Errorf("kind must be %q, got %q", Kind, m.Kind)
	}
	if !regexp.MustCompile(namePattern).MatchString(m.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s, got %q", namePattern, m.Metadata.Name)
	}
	if !regexp.MustCompile(versionPattern).MatchString(m.Metadata.Version) {
		return fmt.Errorf("metadata.version must match %s, got %q", versionPattern, m.Metadata.Version)
	}
	switch m.Metadata.Architecture {
	case "amd64", "arm64", "wasm":
	default:
		return fmt.Errorf("metadata.architecture must be amd64|arm64|wasm, got %q", m.Metadata.Architecture)
	}
	switch m.Metadata.OS {
	case "linux", "darwin", "windows", "any":
	default:
		return fmt.Errorf("metadata.os must be linux|darwin|windows|any, got %q", m.Metadata.OS)
	}

	// Validate digests format
	digestRe := regexp.MustCompile(digestPattern)
	for path, digest := range m.Digests {
		if err := ValidateEntryName(path); err != nil {
			return err
		}
		if !digestRe.MatchString(digest) {
			return fmt.Errorf("invalid digest for %q: %q", path, digest)
		}
	}

	// Every referenced file MUST have a digest entry (PRD §4.3 model_validator)
	required := []string{
		m.Persona.SystemPrompt,
		m.Skills.Graph,
		m.Tools.Bindings,
		m.Memory.Schema,
		m.Sandbox.Spec,
	}
	for _, path := range required {
		if path == "" {
			return fmt.Errorf("manifest is missing a required file reference")
		}
		if _, ok := m.Digests[path]; !ok {
			return fmt.Errorf("required file %q missing from digests map", path)
		}
	}

	return nil
}

// ParseManifest parses a manifest from JSON bytes.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest JSON parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// MarshalJSON serializes the manifest. We use a custom marshaler to ensure
// deterministic key order (matches Python's pydantic output).
func (m Manifest) MarshalJSON() ([]byte, error) {
	type alias Manifest
	return json.Marshal(alias(m))
}

// ToJSON returns pretty-printed JSON.
func (m *Manifest) ToJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// CanonicalJSON returns the RFC 8785 (JCS) canonical serialization: object
// keys sorted by UTF-16 code units, no whitespace, no nulls, numbers in
// shortest form.
func (m *Manifest) CanonicalJSON() ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var generic interface{}
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf strings.Builder
	if err := encodeCanonical(&buf, canonicalize(generic)); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// canonicalize recursively drops nil values.
func canonicalize(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = canonicalize(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = canonicalize(val)
		}
		return out
	default:
		return v
	}
}

var integerRe = regexp.MustCompile(`^-?[0-9]+$`)

func canonicalNumber(n json.Number) (string, error) {
	s := n.String()
	if integerRe.MatchString(s) {
		s = strings.TrimLeft(s, "0")
		neg := false
		if strings.HasPrefix(n.String(), "-") {
			neg = true
			s = strings.TrimLeft(n.String()[1:], "0")
		}
		if s == "" {
			return "0", nil
		}
		if neg {
			return "-" + s, nil
		}
		return s, nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("invalid number %q", s)
	}
	if f == 0 {
		return "0", nil
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	return strconv.FormatFloat(f, 'e', -1, 64), nil
}

func canonicalString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := strings.TrimSuffix(b.String(), "\n")
	// JCS does not escape U+2028/U+2029.
	out = strings.ReplaceAll(out, `\u2028`, "\u2028")
	out = strings.ReplaceAll(out, `\u2029`, "\u2029")
	return out
}

// encodeCanonical writes a canonical JSON value to buf.
func encodeCanonical(buf *strings.Builder, v interface{}) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		s, err := canonicalNumber(t)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case float64:
		s, err := canonicalNumber(json.Number(strconv.FormatFloat(t, 'g', -1, 64)))
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case string:
		buf.WriteString(canonicalString(t))
	case []interface{}:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		buf.WriteByte('{')
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStringsJCS(keys)
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(canonicalString(k))
			buf.WriteByte(':')
			if err := encodeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported value type %T", v)
	}
	return nil
}

// sortStringsJCS sorts strings by UTF-16 code unit order (RFC 8785 §3.2.3).
func sortStringsJCS(s []string) {
	sort.Slice(s, func(i, j int) bool {
		a, b := utf16.Encode([]rune(s[i])), utf16.Encode([]rune(s[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// Sha256File returns "sha256:<hex>" for a file.
func Sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Sha256Bytes(data), nil
}

// Sha256Bytes returns "sha256:<hex>" for a byte slice.
func Sha256Bytes(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

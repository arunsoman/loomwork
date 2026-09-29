// Package verify implements the verification gate.
//
// Per PRD §3 (Vibe→Verified pillar): every artifact an ACI produces carries
// a signed provenance attestation so that downstream consumers can audit
// who did what, when, with which capability.
//
// For the v0.1 MVP we implement:
//   - SBOM generation (simple file-inventory format, not full SPDX)
//   - Property tests (the agent's declared skills must each have a valid
//     entry point in the ACI)
//   - Signed attestation (Ed25519 over the SBOM + test results)
//
// Full TLA+/Apalache formal verification comes after the format has traction.
package verify

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loomwork.dev/loomwork/internal/aci"
)

// SBOM is a simple software bill of materials for an ACI.
type SBOM struct {
	ACI          string      `json:"aci"`
	GeneratedAt  string      `json:"generatedAt"`
	Files        []SBOMFile  `json:"files"`
	Skills       []SBOMSkill `json:"skills"`
	ToolBindings []SBOMTool  `json:"toolBindings"`
}

// SBOMFile is a single file entry.
type SBOMFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// SBOMSkill is a skill entry from the skills graph.
type SBOMSkill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Requires    []string `json:"requires"`
	HasImpl     bool     `json:"hasImpl"`
}

// SBOMTool is a tool binding entry.
type SBOMTool struct {
	Name         string   `json:"name"`
	MCPServer    string   `json:"mcpServer"`
	AllowedTools []string `json:"allowedTools"`
}

// BuildSBOM constructs an SBOM from an unpacked ACI directory.
func BuildSBOM(dir string, manifest *aci.Manifest) (*SBOM, error) {
	sbom := &SBOM{
		ACI:         manifest.Metadata.Name + "@" + manifest.Metadata.Version,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// Inventory exactly the files the manifest covers.
	paths := make([]string, 0, len(manifest.Digests))
	for p := range manifest.Digests {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		sbom.Files = append(sbom.Files, SBOMFile{
			Path:   rel,
			Size:   int64(len(data)),
			Digest: aci.Sha256Bytes(data),
		})
	}
	// Parse skills graph
	skillsPath := filepath.Join(dir, manifest.Skills.Graph)
	skillsData, err := os.ReadFile(skillsPath)
	if err == nil {
		var graph struct {
			Skills []struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Requires    []string `json:"requires"`
				Impl        *struct {
					Type  string `json:"type"`
					Entry string `json:"entry"`
				} `json:"impl"`
			} `json:"skills"`
		}
		if json.Unmarshal(skillsData, &graph) == nil {
			for _, s := range graph.Skills {
				sbom.Skills = append(sbom.Skills, SBOMSkill{
					Name:        s.Name,
					Description: s.Description,
					Requires:    s.Requires,
					HasImpl:     s.Impl != nil,
				})
			}
		}
	}
	// Parse tool bindings
	toolsPath := filepath.Join(dir, manifest.Tools.Bindings)
	toolsData, err := os.ReadFile(toolsPath)
	if err == nil {
		var bindings struct {
			Bindings []struct {
				Name         string   `json:"name"`
				MCPServer    string   `json:"mcpServer"`
				AllowedTools []string `json:"allowedTools"`
			} `json:"bindings"`
		}
		if json.Unmarshal(toolsData, &bindings) == nil {
			for _, b := range bindings.Bindings {
				sbom.ToolBindings = append(sbom.ToolBindings, SBOMTool{
					Name:         b.Name,
					MCPServer:    b.MCPServer,
					AllowedTools: b.AllowedTools,
				})
			}
		}
	}
	return sbom, nil
}

// PropertyTestResult is the result of running property tests on an ACI.
type PropertyTestResult struct {
	Passed bool    `json:"passed"`
	Checks []Check `json:"checks"`
}

// Check is a single property test.
type Check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// RunPropertyTests runs structural checks on an unpacked ACI directory:
//   - every file the manifest lists exists and matches its digest
//   - the skills graph parses, has no unknown requirements or cycles, and every
//     skill implementation file is present
//   - tool bindings parse, have unique names, and each MCP server is a valid URL
//   - sandbox.json and the memory schema are valid JSON
//   - the system prompt is not empty
func RunPropertyTests(dir string, manifest *aci.Manifest) *PropertyTestResult {
	result := &PropertyTestResult{Passed: true}
	add := func(name string, err error, okMsg string) {
		c := Check{Name: name, Passed: err == nil, Message: okMsg}
		if err != nil {
			c.Message = err.Error()
			result.Passed = false
		}
		result.Checks = append(result.Checks, c)
	}
	read := func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	}

	// 1. referenced files exist and match
	var bad []string
	for p, want := range manifest.Digests {
		data, err := read(p)
		switch {
		case err != nil:
			bad = append(bad, p+" missing")
		case aci.Sha256Bytes(data) != want:
			bad = append(bad, p+" digest mismatch")
		}
	}
	sort.Strings(bad)
	var err1 error
	if len(bad) > 0 {
		err1 = fmt.Errorf("%s", strings.Join(bad, "; "))
	}
	add("referenced_files_match", err1, fmt.Sprintf("%d files present and matching", len(manifest.Digests)))

	// 2. skills graph
	skillsData, err := read(manifest.Skills.Graph)
	if err != nil {
		add("skills_graph", err, "")
	} else {
		var g aci.SkillsGraph
		if err := json.Unmarshal(skillsData, &g); err != nil {
			add("skills_graph", err, "")
		} else if err := g.DetectCycles(); err != nil {
			add("skills_graph", err, "")
		} else {
			var missing error
			for _, e := range g.ImplEntries() {
				if _, ok := manifest.Digests[e]; !ok {
					missing = fmt.Errorf("skill implementation %q is not covered by the manifest", e)
					break
				}
			}
			add("skills_graph", missing, fmt.Sprintf("%d skills, acyclic", len(g.Skills)))
		}
	}

	// 3. tool bindings
	toolsData, err := read(manifest.Tools.Bindings)
	if err != nil {
		add("tool_bindings", err, "")
	} else {
		var b struct {
			Bindings []struct {
				Name      string `json:"name"`
				MCPServer string `json:"mcpServer"`
			} `json:"bindings"`
		}
		var terr error
		if err := json.Unmarshal(toolsData, &b); err != nil {
			terr = err
		} else {
			seen := map[string]bool{}
			for _, x := range b.Bindings {
				if x.Name == "" || seen[x.Name] {
					terr = fmt.Errorf("binding name %q is empty or duplicated", x.Name)
					break
				}
				seen[x.Name] = true
				if _, err := url.Parse(x.MCPServer); err != nil || x.MCPServer == "" {
					terr = fmt.Errorf("binding %q has an invalid mcpServer %q", x.Name, x.MCPServer)
					break
				}
			}
		}
		add("tool_bindings", terr, fmt.Sprintf("%d bindings declared", len(b.Bindings)))
	}

	// 4. sandbox + memory schema
	for name, rel := range map[string]string{"sandbox_spec": manifest.Sandbox.Spec, "memory_schema": manifest.Memory.Schema} {
		data, err := read(rel)
		if err == nil {
			var v map[string]interface{}
			err = json.Unmarshal(data, &v)
		}
		add(name, err, rel+" is valid JSON")
	}

	// 5. persona
	prompt, err := read(manifest.Persona.SystemPrompt)
	if err == nil && strings.TrimSpace(string(prompt)) == "" {
		err = fmt.Errorf("system prompt is empty")
	}
	add("persona", err, "system prompt present")
	return result
}

// Attestation is the signed verification gate result.
type Attestation struct {
	Type    string `json:"_type"`
	Subject string `json:"subject"`
	// ArchiveDigest binds the attestation to one exact .aci file.
	ArchiveDigest string              `json:"archiveDigest"`
	SBOM          *SBOM               `json:"sbom"`
	PropertyTests *PropertyTestResult `json:"propertyTests"`
	Signer        string              `json:"signer"`
	SignedAt      string              `json:"signedAt"`
	Signature     string              `json:"signature"`
}

// BuildAttestation signs the SBOM + property test results with an Ed25519 key.
func BuildAttestation(sbom *SBOM, tests *PropertyTestResult, archiveDigest, signerKeyID string, priv ed25519.PrivateKey) (*Attestation, error) {
	att := &Attestation{
		Type:          "loomwork.dev/attestation/v0.1",
		Subject:       sbom.ACI,
		ArchiveDigest: archiveDigest,
		SBOM:          sbom,
		PropertyTests: tests,
		Signer:        signerKeyID,
		SignedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	// Sign the canonical payload
	payload, err := att.canonicalPayload()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, payload)
	att.Signature = base64.StdEncoding.EncodeToString(sig)
	return att, nil
}

// Verify checks the attestation's signature.
func (a *Attestation) Verify(pub ed25519.PublicKey) bool {
	if a.Signature == "" {
		return false
	}
	payload, err := a.canonicalPayload()
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, payload, sig)
}

func (a *Attestation) canonicalPayload() ([]byte, error) {
	// Strip signature, marshal canonically
	att := *a
	att.Signature = ""
	data, err := json.Marshal(att)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// SaveAttestation writes the attestation as a sidecar file.
func SaveAttestation(att *Attestation, path string) error {
	data, err := json.MarshalIndent(att, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

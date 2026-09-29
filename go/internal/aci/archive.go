package aci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Archive is an in-memory representation of an unpacked ACI.
type Archive struct {
	Manifest   *Manifest
	Files      map[string][]byte // path -> raw bytes (non-signature files)
	Signatures map[string][]byte // signatures/* files
}

// Limits applied when reading an untrusted archive.
const (
	MaxArchiveFiles = 1024
	MaxFileBytes    = 16 << 20 // per file
	MaxArchiveBytes = 64 << 20 // total decompressed
	sigPath         = "signatures/manifest.sig"
	certPath        = "signatures/manifest.cert"
)

// allowedSignatureFiles are the only entries permitted under signatures/.
var allowedSignatureFiles = map[string]bool{sigPath: true, certPath: true}

// ValidateEntryName rejects names that could escape the extraction root or
// that are ambiguous (absolute, "..", backslashes, NULs, non-canonical).
func ValidateEntryName(name string) error {
	if name == "" || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("unsafe archive entry name %q", name)
	}
	if strings.HasPrefix(name, "/") || path.Clean(name) != name || name == "." ||
		name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("unsafe archive entry name %q", name)
	}
	return nil
}

// ArchiveFromDirectory builds an Archive from a source directory.
// Verifies that every file referenced by the manifest exists and that
// every digest in the manifest matches the file's actual SHA-256.
func ArchiveFromDirectory(dir string) (*Archive, error) {
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest.json: %w", err)
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Only files the manifest names are packed; anything else in the
	// directory (.env, keys, earlier builds) is left out.
	files := make(map[string][]byte)
	signatures := make(map[string][]byte)
	for rel, expected := range manifest.Digests {
		if err := ValidateEntryName(rel); err != nil {
			return nil, err
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("manifest references %q but file is missing", rel)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("manifest references %q which is not a regular file", rel)
		}
		if info.Size() > MaxFileBytes {
			return nil, fmt.Errorf("%q exceeds %d bytes", rel, MaxFileBytes)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		if Sha256Bytes(data) != expected {
			return nil, fmt.Errorf("digest mismatch for %q: manifest says %s, file is %s",
				rel, expected, Sha256Bytes(data))
		}
		files[rel] = data
	}
	for name := range allowedSignatureFiles {
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
			signatures[name] = data
		}
	}

	return &Archive{Manifest: manifest, Files: files, Signatures: signatures}, nil
}

// ToTarGz serializes the archive to a gzipped tarball (the .aci format).
func (a *Archive) ToTarGz() ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Add manifest first
	manifestJSON, err := a.Manifest.ToJSON()
	if err != nil {
		return nil, err
	}
	// Re-parse to ensure the JSON we sign is exactly what we ship
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	a.Manifest = manifest
	if err := writeTarFile(tw, "manifest.json", manifestJSON); err != nil {
		return nil, err
	}

	// Add all non-signature files in sorted order
	paths := make([]string, 0, len(a.Files))
	for p := range a.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := writeTarFile(tw, p, a.Files[p]); err != nil {
			return nil, err
		}
	}

	// Add signatures
	sigPaths := make([]string, 0, len(a.Signatures))
	for p := range a.Signatures {
		sigPaths = append(sigPaths, p)
	}
	sort.Strings(sigPaths)
	for _, p := range sigPaths {
		if err := writeTarFile(tw, p, a.Signatures[p]); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ArchiveFromTarGz parses a gzipped tarball into an Archive.
// Verifies digests. Does NOT verify signatures (use VerifySignature).
func ArchiveFromTarGz(data []byte) (*Archive, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	files := make(map[string][]byte)
	signatures := make(map[string][]byte)
	var total int64
	entries := 0

	tr := tar.NewReader(io.LimitReader(gz, MaxArchiveBytes+1))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported archive entry type for %q", hdr.Name)
		}
		if err := ValidateEntryName(hdr.Name); err != nil {
			return nil, err
		}
		entries++
		if entries > MaxArchiveFiles {
			return nil, fmt.Errorf("archive has more than %d files", MaxArchiveFiles)
		}
		if hdr.Size > MaxFileBytes {
			return nil, fmt.Errorf("%q exceeds %d bytes", hdr.Name, MaxFileBytes)
		}
		total += hdr.Size
		if total > MaxArchiveBytes {
			return nil, fmt.Errorf("archive exceeds %d bytes uncompressed", MaxArchiveBytes)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxFileBytes+1))
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(hdr.Name, "signatures/") {
			if !allowedSignatureFiles[hdr.Name] {
				return nil, fmt.Errorf("unexpected signature entry %q", hdr.Name)
			}
			if _, dup := signatures[hdr.Name]; dup {
				return nil, fmt.Errorf("duplicate archive entry %q", hdr.Name)
			}
			signatures[hdr.Name] = body
			continue
		}
		if _, dup := files[hdr.Name]; dup {
			return nil, fmt.Errorf("duplicate archive entry %q", hdr.Name)
		}
		files[hdr.Name] = body
	}

	manifestData, ok := files["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("manifest.json not found in archive")
	}
	delete(files, "manifest.json")

	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Verify digests, and reject any file the manifest does not cover.
	for p, expected := range manifest.Digests {
		actual, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("referenced file missing: %s", p)
		}
		if Sha256Bytes(actual) != expected {
			return nil, fmt.Errorf("digest mismatch for %q: expected %s, got %s",
				p, expected, Sha256Bytes(actual))
		}
	}
	for p := range files {
		if _, ok := manifest.Digests[p]; !ok {
			return nil, fmt.Errorf("archive contains %q which the manifest does not list", p)
		}
	}

	return &Archive{Manifest: manifest, Files: files, Signatures: signatures}, nil
}

// Unpack writes an archive to a directory. Every entry path is validated and
// confirmed to stay inside dir.
func (a *Archive) Unpack(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	write := func(name string, data []byte) error {
		if err := ValidateEntryName(name); err != nil {
			return err
		}
		full := filepath.Join(root, filepath.FromSlash(name))
		if !strings.HasPrefix(full, root+string(filepath.Separator)) {
			return fmt.Errorf("entry %q escapes the target directory", name)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, data, 0o644)
	}
	manifestJSON, err := a.Manifest.ToJSON()
	if err != nil {
		return err
	}
	if err := write("manifest.json", manifestJSON); err != nil {
		return err
	}
	for name, data := range a.Files {
		if err := write(name, data); err != nil {
			return err
		}
	}
	for name, data := range a.Signatures {
		if err := write(name, data); err != nil {
			return err
		}
	}
	return nil
}

// PackDirectory builds an .aci file from a source directory.
func PackDirectory(sourceDir, outputPath string) error {
	archive, err := ArchiveFromDirectory(sourceDir)
	if err != nil {
		return err
	}
	data, err := archive.ToTarGz()
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Size:     int64(len(data)),
		Mode:     0o644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

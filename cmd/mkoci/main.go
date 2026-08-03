// mkoci builds a multi-arch OCI layout for the proxy image, without docker
// buildx: each static binary becomes a single-layer image (binary + CA
// certs), all aggregated into a multi-platform index. The controller's
// built-in registry then serves this layout as-is.
//
// Usage:
//
//	mkoci --out /out/oci --cacert /etc/ssl/certs/ca-certificates.crt \
//	      --bin amd64=/out/meshcraft-proxy_linux_amd64 \
//	      --bin arm64=... --bin arm/v7=...
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	mtIndex    = "application/vnd.oci.image.index.v1+json"
	mtManifest = "application/vnd.oci.image.manifest.v1+json"
	mtConfig   = "application/vnd.oci.image.config.v1+json"
	mtLayerGz  = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type binFlag struct{ entries map[string]string }

func (b *binFlag) String() string { return "" }
func (b *binFlag) Set(v string) error {
	var platform, path, ok = strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("format expected: arch[/variant]=path, got %q", v)
	}
	b.entries[platform] = path
	return nil
}

type descriptor struct {
	MediaType string            `json:"mediaType"`
	Digest    string            `json:"digest"`
	Size      int64             `json:"size"`
	Platform  map[string]string `json:"platform,omitempty"`
}

func main() {
	var out = flag.String("out", "", "output directory for the OCI layout")
	var cacert = flag.String("cacert", "", "ca-certificates.crt file to embed")
	var bins = binFlag{entries: map[string]string{}}
	flag.Var(&bins, "bin", "arch[/variant]=path (repeatable)")
	flag.Parse()
	if *out == "" || len(bins.entries) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err = build(*out, *cacert, bins.entries)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkoci:", err)
		os.Exit(1)
	}
}

func build(out, cacert string, bins map[string]string) error {
	var blobsDir = filepath.Join(out, "blobs", "sha256")
	var err = os.MkdirAll(blobsDir, 0o755)
	if err != nil {
		return err
	}

	var caData []byte
	if cacert != "" {
		caData, err = os.ReadFile(cacert)
		if err != nil {
			return fmt.Errorf("cacert: %w", err)
		}
	}

	var manifests = []descriptor{}
	var platform string
	var binPath string
	for platform, binPath = range bins {
		var desc, buildErr = buildPlatform(blobsDir, platform, binPath, caData)
		if buildErr != nil {
			return fmt.Errorf("platform %s: %w", platform, buildErr)
		}
		manifests = append(manifests, desc)
	}

	var index = map[string]any{
		"schemaVersion": 2,
		"mediaType":     mtIndex,
		"manifests":     manifests,
		"annotations":   map[string]string{"org.opencontainers.image.ref.name": "latest"},
	}
	var indexJSON, marshalErr = json.MarshalIndent(index, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	if err = os.WriteFile(filepath.Join(out, "index.json"), indexJSON, 0o644); err != nil {
		return err
	}
	// The index is ALSO written as a blob, addressable by its own digest.
	//
	// Without this, `docker pull` resolves the tag, gets the index's digest
	// in Docker-Content-Digest, then requests the manifest again BY DIGEST —
	// and gets a 404. `docker pull` recovers via a deprecated "pull by tag"
	// fallback (with a warning), but `docker compose up` fails outright. A
	// registry must make its index content-addressable, like everything
	// else.
	var indexDigest, indexWriteErr = writeBlob(blobsDir, indexJSON)
	if indexWriteErr != nil {
		return indexWriteErr
	}
	if err = os.WriteFile(filepath.Join(out, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		return err
	}
	fmt.Printf("OCI layout written to %s (%d platform(s), index %s)\n",
		out, len(manifests), indexDigest)
	return nil
}

// buildPlatform produces the layer + config + manifest for one platform
// and returns the manifest descriptor (with platform) for the index.
func buildPlatform(blobsDir, platform, binPath string, caData []byte) (descriptor, error) {
	var arch, variant, _ = strings.Cut(platform, "/")

	var binData, err = os.ReadFile(binPath)
	if err != nil {
		return descriptor{}, err
	}

	// Layer: reproducible tar (epoch dates, uid/gid 0) then gzip.
	var rawTar bytes.Buffer
	var tw = tar.NewWriter(&rawTar)
	var epoch = time.Unix(0, 0)
	var files = []struct {
		name string
		mode int64
		data []byte
	}{
		{"etc/ssl/certs/ca-certificates.crt", 0o644, caData},
		{"usr/local/bin/meshcraft-proxy", 0o755, binData},
	}
	var f struct {
		name string
		mode int64
		data []byte
	}
	for _, f = range files {
		if f.data == nil {
			continue
		}
		if err = tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: f.mode, Size: int64(len(f.data)),
			ModTime: epoch, Uid: 0, Gid: 0, Typeflag: tar.TypeReg,
		}); err != nil {
			return descriptor{}, err
		}
		if _, err = tw.Write(f.data); err != nil {
			return descriptor{}, err
		}
	}
	if err = tw.Close(); err != nil {
		return descriptor{}, err
	}
	var diffID = "sha256:" + hexSum(rawTar.Bytes())

	var gzBuf bytes.Buffer
	var gz = gzip.NewWriter(&gzBuf)
	if _, err = gz.Write(rawTar.Bytes()); err != nil {
		return descriptor{}, err
	}
	if err = gz.Close(); err != nil {
		return descriptor{}, err
	}
	var layerDigest, writeErr = writeBlob(blobsDir, gzBuf.Bytes())
	if writeErr != nil {
		return descriptor{}, writeErr
	}

	// Image config.
	var configObj = map[string]any{
		"created":      "1970-01-01T00:00:00Z",
		"architecture": arch,
		"os":           "linux",
		"config": map[string]any{
			"Entrypoint": []string{"/usr/local/bin/meshcraft-proxy"},
			"Env":        []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	}
	if variant != "" {
		configObj["variant"] = variant
	}
	var configJSON, cfgErr = json.Marshal(configObj)
	if cfgErr != nil {
		return descriptor{}, cfgErr
	}
	var configDigest, cfgWriteErr = writeBlob(blobsDir, configJSON)
	if cfgWriteErr != nil {
		return descriptor{}, cfgWriteErr
	}

	// Platform manifest.
	var manifestObj = map[string]any{
		"schemaVersion": 2,
		"mediaType":     mtManifest,
		"config": descriptor{
			MediaType: mtConfig, Digest: configDigest, Size: int64(len(configJSON)),
		},
		"layers": []descriptor{
			{MediaType: mtLayerGz, Digest: layerDigest, Size: int64(gzBuf.Len())},
		},
	}
	var manifestJSON, manErr = json.Marshal(manifestObj)
	if manErr != nil {
		return descriptor{}, manErr
	}
	var manifestDigest, manWriteErr = writeBlob(blobsDir, manifestJSON)
	if manWriteErr != nil {
		return descriptor{}, manWriteErr
	}

	var platformDesc = map[string]string{"architecture": arch, "os": "linux"}
	if variant != "" {
		platformDesc["variant"] = variant
	}
	return descriptor{
		MediaType: mtManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestJSON)),
		Platform:  platformDesc,
	}, nil
}

func hexSum(data []byte) string {
	var h = sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func writeBlob(blobsDir string, data []byte) (string, error) {
	var hexDigest = hexSum(data)
	var err = os.WriteFile(filepath.Join(blobsDir, hexDigest), data, 0o644)
	return "sha256:" + hexDigest, err
}

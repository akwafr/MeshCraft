// mkoci construit un layout OCI multi-arch pour l'image du proxy, sans
// docker buildx : chaque binaire statique devient une image single-layer
// (binaire + CA certs), le tout agrege dans un index multi-plateforme.
// Le registre integrated du controleur sert ensuite ce layout tel quel.
//
// Usage :
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
		return fmt.Errorf("format expected: arch[/variant]=chemin, recu %q", v)
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
	var out = flag.String("out", "", "repertoire de sortie du layout OCI")
	var cacert = flag.String("cacert", "", "fichier ca-certificates.crt a embarquer")
	var bins = binFlag{entries: map[string]string{}}
	flag.Var(&bins, "bin", "arch[/variant]=chemin (repetable)")
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
			return fmt.Errorf("plateforme %s: %w", platform, buildErr)
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
	// L'index est AUSSI written comme blob, adressable par son propre digest.
	//
	// Sans ca, `docker pull` resout le tag, receives le digest de l'index dans
	// Docker-Content-Digest, puis redemande le manifeste PAR DIGEST — et prend
	// un 404. `docker pull` s'en sort par un repli « pull by tag » deprecie (avec
	// avertissement), mais `docker compose up` fails franchement. Un registre
	// doit rendre son index adressable par contenu, comme tout le reste.
	var indexDigest, indexWriteErr = writeBlob(blobsDir, indexJSON)
	if indexWriteErr != nil {
		return indexWriteErr
	}
	if err = os.WriteFile(filepath.Join(out, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		return err
	}
	fmt.Printf("layout OCI written dans %s (%d plateformes, index %s)\n",
		out, len(manifests), indexDigest)
	return nil
}

// buildPlatform produit layer + config + manifest pour une plateforme
// et renvoie le descripteur du manifest (avec platform) pour l'index.
func buildPlatform(blobsDir, platform, binPath string, caData []byte) (descriptor, error) {
	var arch, variant, _ = strings.Cut(platform, "/")

	var binData, err = os.ReadFile(binPath)
	if err != nil {
		return descriptor{}, err
	}

	// Layer : tar reproductible (dates epoch, uid/gid 0) puis gzip.
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

	// Config de l'image.
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

	// Manifest de la plateforme.
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

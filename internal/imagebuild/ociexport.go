// OCI-layout output — the second thing this package produces, beside a running
// container.
//
// A DRIVER image is built and then RUN on the controller host (RunContainer).
// An AGENT image is never run here: it is pulled by a proxy on a network the
// controller cannot reach, through the built-in registry (internal/distrib),
// which serves OCI layouts from disk. So an agent build must land as an OCI
// LAYOUT — oci-layout, index.json, blobs/sha256/<hex> — not as an image in the
// daemon.
//
// The daemon builds in its own image store; the only way out that keeps this
// package's "no host paths" rule is the export stream: GET /images/{tag}/get
// yields a docker-save archive, and dockerArchiveToOCI rewrites that stream
// into an OCI layout. The conversion is the compressed/uncompressed digest
// dance OCI requires: a manifest layer is addressed by its GZIPPED digest, the
// config's diff_ids by the UNCOMPRESSED one — docker-save already carries the
// latter, so the config crosses unchanged and only the layers are gzipped.

package imagebuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	ociMTIndex    = "application/vnd.oci.image.index.v1+json"
	ociMTManifest = "application/vnd.oci.image.manifest.v1+json"
	ociMTConfig   = "application/vnd.oci.image.config.v1+json"
	ociMTLayerGz  = "application/vnd.oci.image.layer.v1.tar+gzip"
)

// maxExport bounds the docker-save stream read into memory. Agent images are
// static-binary images (tens of MB); a build that produces hundreds is either
// a mistake or not an agent, and is refused with a legible error rather than
// an OOM.
const maxExport = 512 << 20

type ociDescriptor struct {
	MediaType string            `json:"mediaType"`
	Digest    string            `json:"digest"`
	Size      int64             `json:"size"`
	Platform  map[string]string `json:"platform,omitempty"`
}

// BuildOCI clones and builds like Build, then exports the resulting image as an
// OCI layout under outDir/<tag> is NOT assumed — outDir is the layout root the
// caller chose (e.g. OCIDir/meshcraft/agent-foo). ociTag is the ref the index
// answers to (the agent version). The build's docker tag is local and
// throwaway; what ships is the layout.
func (s *Service) BuildOCI(ctx context.Context, repoURL, subdir, ref, dockerTag,
	ociTag, outDir string, log io.Writer) (Result, error) {

	var res, err = s.Build(ctx, repoURL, subdir, ref, dockerTag, log)
	if err != nil {
		return res, err
	}
	var stream io.ReadCloser
	stream, err = s.eng.exportImage(ctx, dockerTag)
	if err != nil {
		return res, fmt.Errorf("export image: %w", err)
	}
	defer stream.Close()

	if err = dockerArchiveToOCI(outDir, stream, ociTag); err != nil {
		return res, fmt.Errorf("oci layout: %w", err)
	}
	_, _ = fmt.Fprintf(log, "\nOCI layout written to %s (tag %s)\n", outDir, ociTag)
	return res, nil
}

// dockerSaveManifest is the one entry of a docker-save archive's manifest.json
// this converter reads.
type dockerSaveManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// dockerArchiveToOCI rewrites a docker-save stream into an OCI layout at
// outDir. Single-arch by nature: an engine build produces one platform, which
// is the controller host's — the platform an agent it hosts will run on.
func dockerArchiveToOCI(outDir string, saveTar io.Reader, ociTag string) error {
	// Read the archive into memory: the manifest names its members and a tar is
	// not addressable by name without a full pass, so one pass into a map is the
	// simplest correct read. Bounded by maxExport.
	var members, err = readTarMembers(saveTar, maxExport)
	if err != nil {
		return err
	}
	var manifestRaw, ok = members["manifest.json"]
	if !ok {
		return fmt.Errorf("docker-save archive has no manifest.json " +
			"(a containerd/OCI export is not supported by this converter)")
	}
	var manifests []dockerSaveManifest
	if err = json.Unmarshal(manifestRaw, &manifests); err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	if len(manifests) == 0 {
		return fmt.Errorf("manifest.json is empty")
	}
	var dm = manifests[0]

	var blobsDir = filepath.Join(outDir, "blobs", "sha256")
	if err = os.MkdirAll(blobsDir, 0o755); err != nil {
		return err
	}

	// Config crosses unchanged: its diff_ids are the uncompressed layer digests
	// OCI wants there, and its architecture/os name the platform.
	var configRaw, hasConfig = members[dm.Config]
	if !hasConfig {
		return fmt.Errorf("config %q named by manifest is absent from the archive", dm.Config)
	}
	var cfg struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	}
	_ = json.Unmarshal(configRaw, &cfg)
	var configDigest, configSize, cwErr = writeOCIBlob(blobsDir, configRaw)
	if cwErr != nil {
		return cwErr
	}

	// Each layer is gzipped; the manifest addresses it by the gzipped digest.
	var layers = make([]ociDescriptor, 0, len(dm.Layers))
	var layerName string
	for _, layerName = range dm.Layers {
		var raw, present = members[layerName]
		if !present {
			return fmt.Errorf("layer %q named by manifest is absent from the archive", layerName)
		}
		var gz bytes.Buffer
		var zw = gzip.NewWriter(&gz)
		if _, err = zw.Write(raw); err != nil {
			return err
		}
		if err = zw.Close(); err != nil {
			return err
		}
		var digest, size, lwErr = writeOCIBlob(blobsDir, gz.Bytes())
		if lwErr != nil {
			return lwErr
		}
		layers = append(layers, ociDescriptor{MediaType: ociMTLayerGz, Digest: digest, Size: size})
	}

	// Image manifest.
	var manifest = map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociMTManifest,
		"config":        ociDescriptor{MediaType: ociMTConfig, Digest: configDigest, Size: configSize},
		"layers":        layers,
	}
	var manifestJSON, mErr = json.Marshal(manifest)
	if mErr != nil {
		return mErr
	}
	var manifestDigest, manifestSize, mwErr = writeOCIBlob(blobsDir, manifestJSON)
	if mwErr != nil {
		return mwErr
	}

	// Index — single manifest, its platform, and the tag it answers to.
	var platform = map[string]string{"architecture": cfg.Architecture, "os": cfg.OS}
	if cfg.Variant != "" {
		platform["variant"] = cfg.Variant
	}
	var index = map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociMTIndex,
		"manifests": []ociDescriptor{{
			MediaType: ociMTManifest, Digest: manifestDigest, Size: manifestSize, Platform: platform,
		}},
		"annotations": map[string]string{"org.opencontainers.image.ref.name": ociTag},
	}
	var indexJSON, iErr = json.MarshalIndent(index, "", "  ")
	if iErr != nil {
		return iErr
	}
	// The index is written both as index.json and as a blob addressable by its
	// own digest — `docker pull` requests it back by digest (cf. distrib).
	if _, _, err = writeOCIBlob(blobsDir, indexJSON); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(outDir, "index.json"), indexJSON, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644)
}

// readTarMembers reads every regular file of a tar into a name→bytes map,
// refusing past the cap. docker-save uses paths like "<hash>/layer.tar" and
// "<hash>.json"; the manifest references members by those exact names.
func readTarMembers(r io.Reader, cap int64) (map[string][]byte, error) {
	var out = map[string][]byte{}
	var tr = tar.NewReader(io.LimitReader(r, cap+1))
	var total int64
	for {
		var hdr, err = tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		var buf bytes.Buffer
		var n, cpErr = io.Copy(&buf, tr)
		if cpErr != nil {
			return nil, cpErr
		}
		total += n
		if total > cap {
			return nil, fmt.Errorf("image export exceeds %d bytes: too large to package as an agent", cap)
		}
		out[filepath.ToSlash(hdr.Name)] = buf.Bytes()
	}
}

// writeOCIBlob writes content-addressed and returns its OCI descriptor parts.
func writeOCIBlob(blobsDir string, data []byte) (digest string, size int64, err error) {
	var sum = sha256.Sum256(data)
	var hexSum = hex.EncodeToString(sum[:])
	err = os.WriteFile(filepath.Join(blobsDir, hexSum), data, 0o644)
	return "sha256:" + hexSum, int64(len(data)), err
}

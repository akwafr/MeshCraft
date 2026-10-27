// Package imagebuild turns a git repository into a local Docker image, with
// the whole build's log as its product — success or not.
//
// # No host paths, anywhere
//
// The controller itself runs in a container, so a bind mount would need the
// HOST's path for a directory the controller only knows by its own name —
// the classic docker-in-docker trap. Nothing here mounts anything:
//
//  1. the clone runs in a git container's own writable layer;
//  2. the resulting tree leaves through the daemon's archive endpoint as a
//     tar stream;
//  3. that stream — re-rooted, .git dropped — IS the build context POSTed
//     to /build.
//
// The daemon is the only filesystem this package touches.
package imagebuild

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// gitImage runs the clone. Pinned by name only: the daemon pulls it once and
// keeps it; an operator who cares pins a digest in their own mirror.
const gitImage = "alpine/git:latest"

// Service builds driver images through one Docker socket.
type Service struct {
	eng *engine
}

// New connects to the daemon socket (default /var/run/docker.sock).
func New(socketPath string) *Service {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	return &Service{eng: newEngine(socketPath)}
}

// Result is what a build leaves behind.
type Result struct {
	GitCommit string
	ImageID   string
}

// Build clones repoURL at ref (empty = default branch), then builds
// subdir/Dockerfile with the REPOSITORY ROOT as context — the official
// monorepo's driver images copy shared code (sdk/, vendor/) from outside
// their subdirectory, and a root context costs a single-driver repository
// nothing. Everything the clone and the build print goes to log, verbatim: a
// failed third-party build is diagnosed from that text or not at all.
func (s *Service) Build(ctx context.Context, repoURL, subdir, ref, tag string,
	log io.Writer) (Result, error) {

	var out Result
	fmt.Fprintf(log, "\" clone %s", repoURL)
	if ref != "" {
		fmt.Fprintf(log, " @ %s", ref)
	}
	fmt.Fprintln(log)

	var cloneID, commit, err = s.clone(ctx, repoURL, ref, log)
	if cloneID != "" {
		defer func() { _ = s.eng.removeContainer(context.Background(), cloneID) }()
	}
	if err != nil {
		return out, err
	}
	out.GitCommit = commit
	fmt.Fprintf(log, "\" commit %s\n\" build %s (context: %s)\n", commit, tag, orDot(subdir))

	// The clone container's /src, streamed out and re-rooted at the repo root.
	var archive, archErr = s.eng.do(ctx, "GET",
		"/containers/"+cloneID+"/archive", url.Values{"path": {"/src"}}, nil, "")
	if archErr != nil {
		return out, archErr
	}
	defer archive.Body.Close()

	var pr, pw = io.Pipe()
	go func() { pw.CloseWithError(rerootTar(pw, archive.Body, "src")) }()

	var dockerfile = "Dockerfile"
	if subdir != "" {
		dockerfile = strings.Trim(subdir, "/") + "/Dockerfile"
	}
	// The stamp is the version-ordering contract (never "dev"): drivers whose
	// Dockerfile takes DRIVER_VERSION get it; the arg is ignored otherwise.
	var args = map[string]string{"DRIVER_VERSION": time.Now().UTC().Format("20060102.150405")}
	if err = s.eng.buildImageFromTar(ctx, pr, dockerfile, tag, args, log); err != nil {
		return out, err
	}
	out.ImageID, err = s.eng.imageID(ctx, tag)
	if err != nil {
		return out, err
	}
	fmt.Fprintf(log, "\" image %s\n", out.ImageID)
	return out, nil
}

// clone runs the git container and returns its ID and the checked-out commit.
func (s *Service) clone(ctx context.Context, repoURL, ref string,
	log io.Writer) (string, string, error) {

	var err = s.eng.pullImage(ctx, gitImage)
	if err != nil {
		return "", "", fmt.Errorf("pull %s: %w", gitImage, err)
	}
	// One shell line: shallow clone first (history is not build input), FULL
	// clone as fallback — the dumb HTTP transport (a bare repo behind a plain
	// file server, the development case) has no shallow capability, and the
	// pipeline must not care which transport a URL turns out to speak. Then
	// print the commit LAST so it is the final log line.
	var branch = ""
	if ref != "" {
		branch = " --branch '" + strings.ReplaceAll(ref, "'", "") + "'"
	}
	var quoted = "'" + strings.ReplaceAll(repoURL, "'", "") + "'"
	var script = "(git clone --depth 1" + branch + " " + quoted + " /src" +
		" || git clone" + branch + " " + quoted + " /src)" +
		" && git -C /src rev-parse HEAD"

	var cfg containerConfig
	cfg.Image = gitImage
	cfg.Entrypoint = []string{"sh", "-c"}
	cfg.Cmd = []string{script}
	cfg.Labels = map[string]string{"meshcraft.role": "driver-clone"}
	var id string
	id, err = s.eng.createContainer(ctx, "", cfg)
	if err != nil {
		return "", "", err
	}
	if err = s.eng.startContainer(ctx, id); err != nil {
		return id, "", err
	}
	var code int
	code, err = s.eng.waitContainer(ctx, id)
	var logs, _ = s.eng.containerLogs(ctx, id)
	_, _ = io.WriteString(log, logs)
	if err != nil {
		return id, "", err
	}
	if code != 0 {
		return id, "", fmt.Errorf("git clone exited %d", code)
	}
	var lines = strings.Fields(strings.TrimSpace(logs))
	if len(lines) == 0 {
		return id, "", fmt.Errorf("clone produced no commit id")
	}
	return id, lines[len(lines)-1], nil
}

// rerootTar copies entries under root/ to the output tar with root stripped,
// dropping everything else (other subdirs, .git).
func rerootTar(w io.Writer, r io.Reader, root string) error {
	root = strings.Trim(root, "/") + "/"
	var tr = tar.NewReader(r)
	var tw = tar.NewWriter(w)
	for {
		var hdr, err = tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var name = strings.TrimPrefix(hdr.Name, "./")
		if !strings.HasPrefix(name, root) || strings.Contains(name, "/.git/") {
			continue
		}
		var rel = strings.TrimPrefix(name, root)
		if rel == "" || strings.HasPrefix(rel, ".git") {
			continue
		}
		hdr.Name = rel
		if err = tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			var _, copyErr = io.Copy(tw, tr)
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return tw.Close()
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

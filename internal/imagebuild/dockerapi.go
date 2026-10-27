package imagebuild

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// A THIN typed client over the Docker Engine HTTP API, unix socket, stdlib
// only.
//
// Deliberately not github.com/docker/docker/client: this package needs six
// endpoints, and the SDK brings a dependency tree larger than the rest of
// go.mod combined. The house precedent is cmd/mkoci, which builds multi-arch
// OCI images in 250 lines of stdlib — the same posture applies to driving
// the daemon. The API is stable (versioned paths, additive since forever),
// and a fake http.Handler tests every call site without a daemon.
//
// Everything here is CONTROLLER-HOST only. The customer-host stance
// (docs: "no Docker socket on the proxy machine, ever") is untouched: this
// socket is the operator's own machine, where the controller already runs
// as the most privileged thing there is.

// engine is the socket client.
type engine struct {
	http *http.Client
}

const apiPrefix = "/v1.43"

func newEngine(socketPath string) *engine {
	return &engine{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		// No global timeout: builds and waits are legitimately long; every
		// call carries a context.
	}}
}

// engineError is a non-2xx daemon answer, message included.
type engineError struct {
	Status  int
	Message string
}

func (e *engineError) Error() string {
	return fmt.Sprintf("docker daemon answered %d: %s", e.Status, e.Message)
}

func (e *engine) do(ctx context.Context, method, path string, q url.Values,
	body io.Reader, contentType string) (*http.Response, error) {

	var u = "http://docker" + apiPrefix + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var req, err = http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	var res *http.Response
	res, err = e.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		var raw, _ = io.ReadAll(io.LimitReader(res.Body, 1<<16))
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.Message == "" {
			msg.Message = strings.TrimSpace(string(raw))
		}
		return nil, &engineError{Status: res.StatusCode, Message: msg.Message}
	}
	return res, nil
}

func (e *engine) doJSON(ctx context.Context, method, path string, q url.Values,
	in, out any) error {

	var body io.Reader
	if in != nil {
		var buf bytes.Buffer
		var err = json.NewEncoder(&buf).Encode(in)
		if err != nil {
			return err
		}
		body = &buf
	}
	var res, err = e.do(ctx, method, path, q, body, "application/json")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// --- Images ---

// buildImageFromTar POSTs a tar build context and streams the build output
// into log. The stream is NDJSON; an entry carrying "error" fails the build
// — the daemon answers 200 before it knows.
func (e *engine) buildImageFromTar(ctx context.Context, contextTar io.Reader,
	dockerfile, tag string, buildArgs map[string]string, log io.Writer) error {

	var q = url.Values{"t": {tag}, "dockerfile": {dockerfile}, "rm": {"1"}}
	if len(buildArgs) > 0 {
		var raw, _ = json.Marshal(buildArgs)
		q.Set("buildargs", string(raw))
	}
	var res, err = e.do(ctx, http.MethodPost, "/build", q, contextTar, "application/x-tar")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var sc = bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var buildErr error
	for sc.Scan() {
		var entry struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &entry) != nil {
			continue
		}
		if entry.Stream != "" {
			_, _ = io.WriteString(log, entry.Stream)
		}
		if entry.Error != "" {
			_, _ = io.WriteString(log, entry.Error+"\n")
			buildErr = fmt.Errorf("build failed: %s", entry.Error)
		}
	}
	if sc.Err() != nil && buildErr == nil {
		return sc.Err()
	}
	return buildErr
}

// pullImage fetches an image, draining the progress stream — the call is
// complete only when the body ends.
func (e *engine) pullImage(ctx context.Context, ref string) error {
	var name, tag = ref, "latest"
	var i = strings.LastIndexByte(ref, ':')
	if i > 0 && !strings.Contains(ref[i:], "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	var q = url.Values{"fromImage": {name}, "tag": {tag}}
	var res, err = e.do(ctx, http.MethodPost, "/images/create", q, nil, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// exportImage streams a docker-save archive of a tag (GET /images/{tag}/get).
// The caller converts it to an OCI layout (ociexport.go); the stream must be
// fully read and closed, like every daemon body.
func (e *engine) exportImage(ctx context.Context, tag string) (io.ReadCloser, error) {
	var res, err = e.do(ctx, http.MethodGet, "/images/"+url.PathEscape(tag)+"/get", nil, nil, "")
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// imageID resolves a tag to its content-addressed identity — what the build
// history stores, because tags move and IDs do not.
func (e *engine) imageID(ctx context.Context, tag string) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	var err = e.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(tag)+"/json", nil, nil, &out)
	return out.ID, err
}

// --- Containers ---

// containerConfig is the subset of the create payload this package uses.
type containerConfig struct {
	Image string   `json:"Image"`
	Env   []string `json:"Env,omitempty"`
	// Entrypoint overrides the image's own — alpine/git ships `git` as its
	// entrypoint, which would turn a shell command into `git sh`.
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig struct {
		Binds          []string `json:"Binds,omitempty"`
		NetworkMode    string   `json:"NetworkMode,omitempty"`
		ReadonlyRootfs bool     `json:"ReadonlyRootfs,omitempty"`
		CapDrop        []string `json:"CapDrop,omitempty"`
		RestartPolicy  struct {
			Name string `json:"Name,omitempty"`
		} `json:"RestartPolicy,omitempty"`
	} `json:"HostConfig"`
}

func (e *engine) createContainer(ctx context.Context, name string,
	cfg containerConfig) (string, error) {

	var q = url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	var out struct {
		ID string `json:"Id"`
	}
	var err = e.doJSON(ctx, http.MethodPost, "/containers/create", q, cfg, &out)
	return out.ID, err
}

func (e *engine) startContainer(ctx context.Context, id string) error {
	return e.doJSON(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

// waitContainer blocks until exit and returns the exit code.
func (e *engine) waitContainer(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
	}
	var err = e.doJSON(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, nil, &out)
	return out.StatusCode, err
}

// containerLogs fetches stdout+stderr, demultiplexing the daemon's 8-byte
// frame headers.
func (e *engine) containerLogs(ctx context.Context, id string) (string, error) {
	var q = url.Values{"stdout": {"1"}, "stderr": {"1"}}
	var res, err = e.do(ctx, http.MethodGet, "/containers/"+id+"/logs", q, nil, "")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var raw, readErr = io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if readErr != nil {
		return "", readErr
	}
	// Multiplexed stream: [type 1][000][len 4][payload]. A TTY-less container
	// always frames; tolerate unframed output anyway.
	var out bytes.Buffer
	var rest = raw
	for len(rest) >= 8 {
		if rest[0] > 2 {
			return string(raw), nil
		}
		var n = int(rest[4])<<24 | int(rest[5])<<16 | int(rest[6])<<8 | int(rest[7])
		if 8+n > len(rest) {
			break
		}
		out.Write(rest[8 : 8+n])
		rest = rest[8+n:]
	}
	return out.String(), nil
}

func (e *engine) removeContainer(ctx context.Context, id string) error {
	var q = url.Values{"force": {"1"}}
	return e.doJSON(ctx, http.MethodDelete, "/containers/"+id, q, nil, nil)
}

// inspectIP returns the container's address on the named network (empty name
// = the first network found).
func (e *engine) inspectIP(ctx context.Context, id, network string) (string, error) {
	var out struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	var err = e.doJSON(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &out)
	if err != nil {
		return "", err
	}
	if network != "" {
		var n, ok = out.NetworkSettings.Networks[network]
		if ok {
			return n.IPAddress, nil
		}
		return "", fmt.Errorf("container %s is not on network %s", id, network)
	}
	var first string
	var netw struct {
		IPAddress string `json:"IPAddress"`
	}
	for _, netw = range out.NetworkSettings.Networks {
		first = netw.IPAddress
		break
	}
	if first == "" {
		return "", fmt.Errorf("container %s has no network address", id)
	}
	return first, nil
}

// ensureNetwork creates the named bridge network if absent.
func (e *engine) ensureNetwork(ctx context.Context, name string) error {
	var err = e.doJSON(ctx, http.MethodGet, "/networks/"+name, nil, nil, nil)
	if err == nil {
		return nil
	}
	var ee *engineError
	if !isStatus(err, 404, &ee) {
		return err
	}
	return e.doJSON(ctx, http.MethodPost, "/networks/create", nil,
		map[string]any{"Name": name, "Driver": "bridge"}, nil)
}

func isStatus(err error, status int, out **engineError) bool {
	var ee, ok = err.(*engineError)
	if ok && ee.Status == status {
		*out = ee
		return true
	}
	return false
}

package imagebuild

import (
	"context"
	"fmt"
	"os"
)

// Container-runtime primitives for the driver host — same engine client,
// run side. The lifecycle POLICY (when to start, what to register, how to
// reconcile) lives in internal/driverhost; this file only knows how to ask
// the daemon.

// RunSpec is what a driver container gets — and pointedly, what it does not:
// no published ports (the controller dials the bridge IP), no mounts, no
// capabilities, a read-only root. A driver that needs more than a socket to
// its own HTTP server and the SOCKS road out is asking for something the
// contract does not owe it.
type RunSpec struct {
	Name    string
	Image   string
	Network string
	Env     []string
	Labels  map[string]string
}

// RunContainer creates and starts the container, removing any earlier one
// holding the name — replacement is the ordinary path of a rebuild.
func (s *Service) RunContainer(ctx context.Context, spec RunSpec) (string, error) {
	_ = s.eng.removeByName(ctx, spec.Name)
	if spec.Network != "" {
		var err = s.eng.ensureNetwork(ctx, spec.Network)
		if err != nil {
			return "", err
		}
	}
	var cfg containerConfig
	cfg.Image = spec.Image
	cfg.Env = spec.Env
	cfg.Labels = spec.Labels
	cfg.HostConfig.NetworkMode = spec.Network
	cfg.HostConfig.ReadonlyRootfs = true
	cfg.HostConfig.CapDrop = []string{"ALL"}
	cfg.HostConfig.RestartPolicy.Name = "unless-stopped"
	var id, err = s.eng.createContainer(ctx, spec.Name, cfg)
	if err != nil {
		return "", err
	}
	if err = s.eng.startContainer(ctx, id); err != nil {
		_ = s.eng.removeContainer(context.Background(), id)
		return "", err
	}
	return id, nil
}

// ContainerIP answers the container's address on the named network.
func (s *Service) ContainerIP(ctx context.Context, id, network string) (string, error) {
	return s.eng.inspectIP(ctx, id, network)
}

// StopContainer force-removes a container. Removing what is already gone is
// the ordinary teardown path, not an error.
func (s *Service) StopContainer(ctx context.Context, id string) error {
	var err = s.eng.removeContainer(ctx, id)
	var ee *engineError
	if isStatus(err, 404, &ee) {
		return nil
	}
	return err
}

// removeByName removes a container by its name (404 = nothing to do).
func (e *engine) removeByName(ctx context.Context, name string) error {
	return e.removeContainer(ctx, name)
}

// AttachSelf connects the CONTROLLER's own container to the drivers network,
// so it can dial their bridge addresses. In Docker, a container's hostname
// is its ID; when the controller runs uncontainered (development) the
// lookup 404s and bridge IPs are host-routable anyway, so failure here is
// advice, not an error.
func (s *Service) AttachSelf(ctx context.Context, network string) error {
	var err = s.eng.ensureNetwork(ctx, network)
	if err != nil {
		return err
	}
	var self, hostErr = os.Hostname()
	if hostErr != nil {
		return hostErr
	}
	err = s.eng.doJSON(ctx, "POST", "/networks/"+network+"/connect", nil,
		map[string]any{"Container": self}, nil)
	var ee *engineError
	if isStatus(err, 404, &ee) {
		return fmt.Errorf("not running in a container (hostname %s unknown to the daemon)", self)
	}
	if err != nil && isForbiddenAlready(err) {
		return nil
	}
	return err
}

// isForbiddenAlready recognises "already attached", which the daemon answers
// as a 403.
func isForbiddenAlready(err error) bool {
	var ee, ok = err.(*engineError)
	return ok && ee.Status == 403
}

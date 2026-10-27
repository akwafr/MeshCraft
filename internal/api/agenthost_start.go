package api

import (
	"context"
	"log/slog"

	"meshcraft/internal/agenthost"
	"meshcraft/internal/imagebuild"
)

// StartAgentHost wires the agent-image build pipeline and reconciles it in the
// background. It needs only the Docker socket (to build) and OCIDir (where the
// registry serves layouts from) — NOT the driver SOCKS listener, which is why
// it is started on its own rather than inside StartDriverSocks.
//
// Off when no Docker socket is configured: agent builds simply do not exist for
// that instance, and the admin endpoints say so rather than failing obscurely.
func (s *Server) StartAgentHost(ctx context.Context) {
	if s.Cfg.DockerSocket == "" {
		return
	}
	s.AgentHost = agenthost.New(s.Store, imagebuild.New(s.Cfg.DockerSocket), s.Cfg.OCIDir)
	slog.Info("agent build pipeline enabled", "oci_dir", s.Cfg.OCIDir)
	// Build any enabled repo registered before this restart that never produced
	// a layout — startup must not wait on it.
	go s.AgentHost.Reconcile(ctx)
}

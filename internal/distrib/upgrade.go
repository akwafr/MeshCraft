package distrib

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Target version of the proxy and upgrade descriptor.
//
// The source of truth is what the controller actually SERVES: the
// `artifacts/VERSION` file (build timestamp, written by the Dockerfile) and
// the binaries themselves, hashed on demand. The original spec note talked
// about a `manifest.json` written at build time; hashing the served files is
// strictly more honest than a manifest sitting next to them, which could lie.
//
// See docs/en/01-Architecture/Proxy versioning and updates.md.

// Upgrade describes the upgrade offered to a proxy.
type Upgrade struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type versionCache struct {
	mu      sync.Mutex
	value   string
	mtime   time.Time
	checked time.Time
}

var artifactsVersion versionCache

// TargetVersion returns the version of the served binaries, or "" if the
// controller doesn't serve any (local build, directory absent). Empty means
// no upgrade will be offered: better to offer nothing than to offer the
// unknown.
func (s *Service) TargetVersion() string {
	var path = filepath.Join(s.Cfg.ArtifactsDir, "VERSION")
	artifactsVersion.mu.Lock()
	defer artifactsVersion.mu.Unlock()
	// Re-read at most once per second: the heartbeat of an entire fleet must
	// not turn into a burst of stat() calls.
	if time.Since(artifactsVersion.checked) < time.Second {
		return artifactsVersion.value
	}
	artifactsVersion.checked = time.Now()
	var info, err = os.Stat(path)
	if err != nil {
		artifactsVersion.value = ""
		return ""
	}
	if info.ModTime().Equal(artifactsVersion.mtime) && artifactsVersion.value != "" {
		return artifactsVersion.value
	}
	var data, readErr = os.ReadFile(path)
	if readErr != nil {
		artifactsVersion.value = ""
		return ""
	}
	artifactsVersion.mtime = info.ModTime()
	artifactsVersion.value = strings.TrimSpace(string(data))
	return artifactsVersion.value
}

// buildStamp recognizes a UTC build timestamp YYYYMMDD.HHMMSS, and NOTHING
// else.
//
// Recognizing the shape is not a validation nicety: it's what distinguishes
// "older" from "not comparable", and the two lead to opposite decisions.
var buildStamp = regexp.MustCompile(`^\d{8}\.\d{6}$`)

// IsNewer reports whether `candidate` must REPLACE `current`.
//
// The YYYYMMDD.HHMMSS format is fixed-width: between two timestamps,
// lexicographic comparison IS chronological comparison, with no parser
// needed.
//
// # THE TRAP THIS FUNCTION USED TO CARRY, AND WHAT IT COST
//
// The body used to be `candidate > current`, and its comment claimed that a
// version in a different format "is never newer — it's simply different,
// which is enough to decide". That was wrong, and wrong in the most
// expensive possible way:
//
//	"20260729.213000" > "dev"  →  FALSE
//
// Digits sort BEFORE letters in ASCII. A proxy whose version is "dev" is
// therefore "newer" than any build, past, present, or future: the
// controller offers it NOTHING, the UI displays it as "up to date", and it
// stays that way forever. This is exactly what was observed on
// `proxy-lab`, reporting `up_to_date` at version `dev` — a `docker
// compose pull` couldn't fix it either, since the embedded registry was
// serving the same build.
//
// And this contradicted this page's rule: "no intervention on the proxy's
// machine, ever." A proxy stuck at `dev` can only be fixed ON its machine.
//
// # THE RULE, IN BOTH DIRECTIONS
//
//   - a CANDIDATE that isn't a timestamp is never offered. This is the same
//     principle already applied to an empty target version: better to offer
//     nothing than to offer the unknown;
//   - a CURRENT that isn't a timestamp is to be REPLACED, without
//     comparison. It has no position in time, so there's nothing to
//     compare — and a proxy carrying a version that the build string no
//     longer produces (cf. the Dockerfile guard) is precisely the one that
//     needs to be moved off of it.
//
// The consequence worth knowing: a proxy built by hand, outside `make
// build`, will be offered the controller's packaged build. This is
// intentional. Refusing it requires `auto_upgrade = false` or pinning, i.e.
// an explicit decision, not a side effect of a string comparison.
func IsNewer(candidate, current string) bool {
	if !buildStamp.MatchString(candidate) {
		return false
	}
	if !buildStamp.MatchString(current) {
		return true
	}
	return candidate > current
}

// UpgradeFor builds the descriptor for the architecture reported by the
// proxy. Returns nil if nothing is served for that platform: an armv7 proxy
// talking to a controller that only publishes amd64 must not receive an
// offer.
func (s *Service) UpgradeFor(r *http.Request, version, osName, arch string) *Upgrade {
	if version == "" {
		return nil
	}
	if osName == "" {
		osName = "linux"
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	var arts, err = s.ListArtifacts()
	if err != nil {
		return nil
	}
	var a Artifact
	for _, a = range arts {
		if a.OS != osName || a.Arch != arch {
			continue
		}
		return &Upgrade{
			Version: version,
			URL:     s.externalBase(r).String() + a.URL,
			SHA256:  a.SHA256,
			Size:    a.Size,
		}
	}
	return nil
}

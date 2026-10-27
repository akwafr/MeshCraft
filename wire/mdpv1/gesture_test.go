package mdpv1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// The gesture table is what an EXTERNAL driver announces instead of a type
// assertion — an optional interface missing from it is a capability that
// exists in-process and vanishes over the wire, silently. This test makes the
// omission loud: it reads the package source, lists every exported interface,
// and requires each optional one to be mapped onto exactly one gesture.

// optionalInterfaceGestures is the human-maintained half of the check: which
// interface answers to which gesture. Adding an optional interface to the
// package without touching gesture.go fails the test below; updating this map
// without a matching probe row fails it too.
var optionalInterfaceGestures = map[string]Gesture{
	"AccountManager":        GestureAccounts,
	"Persister":             GesturePersist,
	"SNMPConfigurator":      GestureSNMPConfig,
	"TrapPusher":            GestureTrapReceiver,
	"MgmtMover":             GestureMgmtMove,
	"MgmtJumper":            GestureMgmtJump,
	"FaceplateReader":       GestureFaceplate,
	"IdentityReader":        GestureIdentity,
	"MgmtStaticOwnerProber": GestureMgmtStaticOwnerProbe,
	"RawReader":             GestureRawRead,
	"DHCPSnoopReader":       GestureDHCPSnoopRead,
	"DHCPSnoopApplier":      GestureDHCPSnoopApply,
	"StormControlReader":    GestureStormControlRead,
	"StormControlApplier":   GestureStormControlApply,
	"StormControlCapabler":  GestureStormControlCaps,
	"SpanningTreeReader":    GestureSpanningTreeRead,
	"SpanningTreeApplier":   GestureSpanningTreeApply,
	"PoEReader":             GesturePoERead,
	"PoEApplier":            GesturePoEApply,
	"PortAdminApplier":      GesturePortAdmin,
	"PortPowerCycler":       GesturePortPowerCycle,
	"LAGWriter":             GestureLAGs,
	"LAGFallbackWriter":     GestureLAGFallback,
	"HostnameSetter":        GestureHostname,
	"LLDPReader":            GestureLLDP,
	"FDBReader":             GestureFDB,
	"OpticsReader":          GestureOptics,
	"MgmtAddrReader":        GestureMgmtAddrRead,
	"MgmtAddrWriter":        GestureMgmtAddrApply,
	"PortNamer":             GesturePortName,
	"Identifier":            GestureIdentify,
	"Onboarder":             GestureOnboard,
	"WebUIAddresser":        GestureWebUI,
}

// The interfaces that are the contract itself, not optional capabilities.
var coreInterfaces = map[string]bool{
	"Driver":    true,
	"Connector": true,
	"Admin":     true,
	"Dialer":    true,

	// BulkReader is the composite read (POST sessions/{id}/read) in Go form:
	// the controller-side adapter implements it over the wire, and the endpoint
	// exists for EVERY driver — nothing to announce, so not a gesture.
	"BulkReader": true,
}

func exportedInterfaces(t *testing.T) []string {
	t.Helper()
	var fset = token.NewFileSet()
	var pkgs, err = parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	var pkg *ast.Package
	for _, pkg = range pkgs {
		var file *ast.File
		for _, file = range pkg.Files {
			var decl ast.Decl
			for _, decl = range file.Decls {
				var gen, isGen = decl.(*ast.GenDecl)
				if !isGen || gen.Tok != token.TYPE {
					continue
				}
				var spec ast.Spec
				for _, spec = range gen.Specs {
					var ts, isType = spec.(*ast.TypeSpec)
					if !isType || !ts.Name.IsExported() {
						continue
					}
					var _, isIface = ts.Type.(*ast.InterfaceType)
					if isIface {
						out = append(out, ts.Name.Name)
					}
				}
			}
		}
	}
	return out
}

func TestEveryOptionalInterfaceHasAGesture(t *testing.T) {
	var names = exportedInterfaces(t)
	if len(names) == 0 {
		t.Fatal("no interfaces found: the parser is looking at the wrong directory")
	}
	var name string
	for _, name = range names {
		if coreInterfaces[name] {
			continue
		}
		var _, mapped = optionalInterfaceGestures[name]
		if !mapped {
			t.Errorf("interface %s has no gesture: an external driver cannot announce it — add it to gesture.go AND to this map", name)
		}
	}
	if len(optionalInterfaceGestures) != len(names)-len(coreInterfaces) {
		t.Errorf("map has %d entries for %d optional interfaces: an entry names an interface that no longer exists",
			len(optionalInterfaceGestures), len(names)-len(coreInterfaces))
	}
}

func TestGestureProbesMatchTheMap(t *testing.T) {
	var probed = map[Gesture]bool{}
	var probe struct {
		gesture Gesture
		has     func(any) bool
	}
	for _, probe = range gestureProbes {
		if probed[probe.gesture] {
			t.Errorf("gesture %q probed twice", probe.gesture)
		}
		probed[probe.gesture] = true
	}
	var g Gesture
	for _, g = range optionalInterfaceGestures {
		if !probed[g] {
			t.Errorf("gesture %q mapped but never probed: GesturesOf can never return it", g)
		}
	}
	if len(probed) != len(optionalInterfaceGestures) {
		t.Errorf("%d probes for %d mapped gestures", len(probed), len(optionalInterfaceGestures))
	}
}

// A typed nil must be a pure method-set check: nothing dereferenced, and the
// answer identical to what a live session would give.
func TestGesturesOfTypedNilDoesNotPanicAndDetects(t *testing.T) {
	type onlyPersist struct{ Persister }
	var got = GesturesOf((*onlyPersist)(nil))
	if len(got) != 1 || got[0] != GesturePersist {
		t.Fatalf("expected exactly [%q], got %v", GesturePersist, got)
	}
	if len(GesturesOf(struct{}{})) != 0 {
		t.Fatal("a type with no methods must declare no gesture")
	}
}

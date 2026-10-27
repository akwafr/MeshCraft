// Command labparity reads ONE lab device twice and diffs what it saw: once
// through the in-tree driver package, once through the driver's container
// reached exactly as the controller reaches it (internal/restdriver over MDP,
// a minted SOCKS credential, the composite read). Empty diff = the external
// driver is the in-tree driver, on this hardware, for the read half of the
// contract.
//
// EXPLORATION ONLY — the Phase 4 parity gate, not a product feature. Nothing
// imports it; `go build ./...` covers it so it keeps compiling against the
// same restdriver and the same registry the controller uses.
//
// # Why the two reads share one code path
//
// The in-tree path and the wire path end in the same interface, [mdpv1.Admin]
// and its optional readers. readAll below asserts and calls exactly as
// readDeviceState's helpers do, on whichever Admin it is handed; the only
// thing that differs between the two runs is who answered. A parity tool with
// two bespoke readers would be comparing its own two readers.
//
// # Sequential by construction
//
// The registry holds ONE connector per name. The in-tree read happens first;
// restdriver.Load then REPLACES the registry entry with the wire connector,
// and the second read goes through it. Two sessions never coexist on the
// device — which is the lab rule (session budgets, instanton's five slots).
//
// # The road the container takes
//
// A driver container reaches its device through the controller's DRIVER SOCKS
// listener, with a credential minted for one host. This tool runs that
// listener itself: a [socks.Server] whose authenticator and resolver are a
// fresh [socks.Minter] (the real one), and whose dialer forwards through the
// OPERATOR SOCKS route with a socks-token — the same road labssh and labfwd
// take. The container therefore exercises the real minted-credential flow;
// only the last hop (operator route instead of the tunnel hub) is the lab's.
//
// Credentials come from the environment, never from flags: an argument is
// visible in `ps` to every user on the box. LAB_USER/LAB_PASS for the device,
// SOCKS_USER/SOCKS_PASS for the operator route.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
	"meshcraft/internal/restdriver"
	"meshcraft/internal/socks"
	"meshcraft/wire/mdpv1"

	// The in-tree drivers register through init(), as they do in the controller.
	_ "meshcraft/internal/driver/arista/eos"
	_ "meshcraft/internal/driver/cisco/ios"
	_ "meshcraft/internal/driver/fs/s3400"
	_ "meshcraft/internal/driver/fs/s3900"
	_ "meshcraft/internal/driver/hpe/procurve"
	_ "meshcraft/internal/driver/instanton"
	_ "meshcraft/internal/driver/mikrotik/routeros"
	_ "meshcraft/internal/driver/mikrotik/swos"
	_ "meshcraft/internal/driver/ubiquiti/edgeswitch"
)

// socksDialer adapts the operator SOCKS route to the contract's Dialer.
type socksDialer struct{ d proxy.ContextDialer }

func (s socksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return s.d.DialContext(ctx, network, addr)
}

// mintRoute is the last hop of the driver SOCKS listener: every minted
// credential's connection leaves through the operator route. The proxyID the
// minter recorded is empty here (the Target's dialer is not a tunnel dialer),
// which is exactly the "Local" case of the controller's driverSocksDialer.
type mintRoute struct{ d proxy.ContextDialer }

func (m mintRoute) Dial(ctx context.Context, _ string, host string, port int) (net.Conn, error) {
	return m.d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// partNames is the composite read, in the order readDeviceState asks for it.
var partNames = []string{
	mdpv1.PartState, mdpv1.PartIdentity, mdpv1.PartFaceplate,
	mdpv1.PartMgmtAddr, mdpv1.PartSpanningTree, mdpv1.PartPoE,
	mdpv1.PartOptics, mdpv1.PartLLDP, mdpv1.PartFDB,
}

func main() {
	var name = flag.String("driver", "", "connector name, e.g. cisco-ios")
	var host = flag.String("host", "", "device address, e.g. 192.0.2.64")
	var endpoint = flag.String("endpoint", "", "the driver container's MDP endpoint, e.g. http://127.0.0.1:9431")
	var socksAddr = flag.String("socks", "127.0.0.1:1080", "operator SOCKS route")
	var mintListen = flag.String("mint-listen", "127.0.0.1:1085", "where this tool serves the driver SOCKS listener (the container dials it)")
	var model = flag.String("model", "", "model as the inventory knows it")
	var prefix = flag.String("port-prefix", "", "standard-model port prefix, e.g. GE")
	var outDir = flag.String("out", ".", "directory for the three JSON documents")
	var skipIntree = flag.Bool("skip-intree", false, "external only (reuse a saved in-tree.json)")
	var timeout = flag.Duration("timeout", 6*time.Minute, "budget per side")
	flag.Parse()
	if *name == "" || *host == "" || *endpoint == "" {
		fmt.Fprintln(os.Stderr, "usage: labparity -driver NAME -host IP -endpoint URL [flags]")
		os.Exit(2)
	}
	var user, pass = os.Getenv("LAB_USER"), os.Getenv("LAB_PASS")
	if user == "" || pass == "" {
		fail("LAB_USER and LAB_PASS must be set")
	}
	var auth *proxy.Auth
	if os.Getenv("SOCKS_USER") != "" {
		auth = &proxy.Auth{User: os.Getenv("SOCKS_USER"), Password: os.Getenv("SOCKS_PASS")}
	}
	var route, err = proxy.SOCKS5("tcp", *socksAddr, auth, proxy.Direct)
	if err != nil {
		fail("socks: %v", err)
	}
	var ctxDialer = route.(proxy.ContextDialer)

	var tgt = mdpv1.Target{Host: *host, Dialer: socksDialer{ctxDialer}, Model: *model, PortPrefix: *prefix}
	// ONE candidate, on purpose: a refusal is read in the transcript, never
	// retried by a tool — that is how session budgets get burnt.
	var cands = []mdpv1.Candidate{{Username: user, Secret: pass, Why: "lab parity: the site credential"}}

	// --- 1. in-tree ---
	var intree map[string]any
	if *skipIntree {
		intree = load(filepath.Join(*outDir, "in-tree.json"))
	} else {
		var conn, cerr = driver.ConnectorFor(*name)
		if cerr != nil {
			fail("in-tree: %v", cerr)
		}
		fmt.Printf("in-tree connector: %T\n", conn)
		intree = readSide(ctxWith(*timeout), conn, tgt, cands, "in-tree", false)
		save(filepath.Join(*outDir, "in-tree.json"), intree)
	}

	// --- 2. the driver SOCKS listener, then the container ---
	var minter = socks.NewMinter()
	var ln, lerr = net.Listen("tcp", *mintListen)
	if lerr != nil {
		fail("mint listener: %v", lerr)
	}
	var srv = &socks.Server{
		Resolver: minter, Auth: minter, Dialer: mintRoute{ctxDialer},
		OnRefusal: func(label, h string, port int, reason string) {
			fmt.Printf("driver socks REFUSED %s -> %s:%d: %s\n", label, h, port, reason)
		},
	}
	var bg, stop = context.WithCancel(context.Background())
	defer stop()
	go func() { _ = srv.Serve(bg, ln) }()

	var loaded, lderr = restdriver.Load(bg, *endpoint, restdriver.Runtime{Minter: minter, SocksAdvertise: *mintListen})
	if lderr != nil {
		fail("load %s: %v", *endpoint, lderr)
	}
	var l restdriver.Loaded
	for _, l = range loaded {
		fmt.Printf("loaded %s (protocol %d, %d gestures)\n", l.Name, l.Protocol, len(l.Support.Gestures))
	}
	var conn2, c2err = driver.ConnectorFor(*name)
	if c2err != nil {
		fail("external: %v", c2err)
	}
	fmt.Printf("external connector: %T\n", conn2)
	if !strings.Contains(fmt.Sprintf("%T", conn2), "restdriver") {
		fail("the registry still answers the in-tree connector for %q: the container declares another name?", *name)
	}
	var single = readSide(ctxWith(*timeout), conn2, tgt, cands, "external-single", false)
	save(filepath.Join(*outDir, "external-single.json"), single)
	var bulk = readSide(ctxWith(*timeout), conn2, tgt, cands, "external-bulk", true)
	save(filepath.Join(*outDir, "external-bulk.json"), bulk)

	// --- 3. the verdict ---
	var d1 = report("in-tree vs external (single reads)", intree, single)
	var d2 = report("external single reads vs composite read", single, bulk)
	if d1+d2 == 0 {
		fmt.Println("\nPARITY: the three documents are identical")
		return
	}
	fmt.Printf("\n%d differing paths — classify each: volatile (counters, uptime, timestamps) or a defect\n", d1+d2)
}

// readSide authenticates ONE session on the given connector and reads the
// nine parts through it, then closes. bulk selects the composite read (the
// path the controller prefers when the Admin offers it).
func readSide(ctx context.Context, conn mdpv1.Connector, tgt mdpv1.Target,
	cands []mdpv1.Candidate, label string, bulk bool) map[string]any {

	var started = time.Now()
	var adm, err = conn.Authenticate(ctx, tgt, cands)
	if err != nil {
		if adm != nil {
			printTranscript(label, adm.Transcript())
		}
		fail("%s: authenticate: %v", label, err)
	}
	defer adm.Close()
	fmt.Printf("%s: authenticated as %q in %s (%d transcript steps)\n",
		label, adm.Account(), time.Since(started).Round(time.Millisecond), len(adm.Transcript()))

	var out map[string]any
	if bulk {
		var br, ok = adm.(mdpv1.BulkReader)
		if !ok {
			fail("%s: this Admin cannot bulk-read (%T)", label, adm)
		}
		out = readBulk(ctx, br)
	} else {
		out = readAll(ctx, adm)
	}
	fmt.Printf("%s: nine parts in %s\n", label, time.Since(started).Round(time.Millisecond))
	return out
}

// readAll is readDeviceState's nine reads, part by part, through the
// optional reader interfaces — on an in-tree Admin these are method calls, on
// a wire Admin nine HTTP round trips on the pinned session.
func readAll(ctx context.Context, adm mdpv1.Admin) map[string]any {
	var out = map[string]any{}
	var put = func(part string, v any, err error) { out[part] = normalize(v, err) }

	var st, err = adm.ReadState(ctx)
	put(mdpv1.PartState, st, err)

	var ir, okI = adm.(mdpv1.IdentityReader)
	if okI {
		var v, e = ir.ReadIdentity(ctx)
		put(mdpv1.PartIdentity, v, e)
	} else {
		put(mdpv1.PartIdentity, nil, mdpv1.ErrNotImplemented)
	}
	var fr, okF = adm.(mdpv1.FaceplateReader)
	if okF {
		var v, e = fr.ReadFaceplate(ctx)
		put(mdpv1.PartFaceplate, v, e)
	} else {
		put(mdpv1.PartFaceplate, nil, mdpv1.ErrNotImplemented)
	}
	var mr, okM = adm.(mdpv1.MgmtAddrReader)
	if okM {
		var v, e = mr.ReadMgmtAddressing(ctx)
		put(mdpv1.PartMgmtAddr, v, e)
	} else {
		put(mdpv1.PartMgmtAddr, nil, mdpv1.ErrNotImplemented)
	}
	var sr, okS = adm.(mdpv1.SpanningTreeReader)
	if okS {
		var v, e = sr.ReadSpanningTree(ctx)
		put(mdpv1.PartSpanningTree, v, e)
	} else {
		put(mdpv1.PartSpanningTree, nil, mdpv1.ErrNotImplemented)
	}
	var pr, okP = adm.(mdpv1.PoEReader)
	if okP {
		var v, e = pr.ReadPoE(ctx)
		put(mdpv1.PartPoE, v, e)
	} else {
		put(mdpv1.PartPoE, nil, mdpv1.ErrNotImplemented)
	}
	var or, okO = adm.(mdpv1.OpticsReader)
	if okO {
		var v, e = or.ReadOptics(ctx)
		put(mdpv1.PartOptics, v, e)
	} else {
		put(mdpv1.PartOptics, nil, mdpv1.ErrNotImplemented)
	}
	var lr, okL = adm.(mdpv1.LLDPReader)
	if okL {
		var v, e = lr.ReadLLDPObservation(ctx)
		put(mdpv1.PartLLDP, v, e)
	} else {
		put(mdpv1.PartLLDP, nil, mdpv1.ErrNotImplemented)
	}
	var dr, okD = adm.(mdpv1.FDBReader)
	if okD {
		var v, e = dr.ReadFDBObservation(ctx)
		put(mdpv1.PartFDB, v, e)
	} else {
		put(mdpv1.PartFDB, nil, mdpv1.ErrNotImplemented)
	}
	return out
}

// readBulk is the composite read, decoded part by part into the same shape.
func readBulk(ctx context.Context, br mdpv1.BulkReader) map[string]any {
	var out = map[string]any{}
	var parts, err = br.ReadAll(ctx, partNames)
	if err != nil {
		fail("composite read: %v", err)
	}
	var part string
	for _, part = range partNames {
		var got, ok = parts[part]
		if !ok {
			out[part] = map[string]any{"missing": true}
			continue
		}
		if got.Error != nil {
			// not_implemented over the wire is the failed assertion in process:
			// the same normalized marker on both sides.
			if got.Error.Code == "not_implemented" {
				out[part] = normalize(nil, mdpv1.ErrNotImplemented)
			} else {
				out[part] = map[string]any{"error": got.Error.Code + ": " + got.Error.Detail}
			}
			continue
		}
		var v any
		if json.Unmarshal(got.Result, &v) != nil {
			out[part] = map[string]any{"error": "undecodable result"}
			continue
		}
		out[part] = v
	}
	return out
}

// normalize turns a typed read into the generic JSON shape both sides are
// compared in; errors become a marker so an absent capability reads the same
// whichever side declared it.
func normalize(v any, err error) any {
	if err != nil {
		if errors.Is(err, mdpv1.ErrNotImplemented) {
			return map[string]any{"unsupported": true}
		}
		return map[string]any{"error": err.Error()}
	}
	var raw, merr = json.Marshal(v)
	if merr != nil {
		return map[string]any{"error": "unmarshalable: " + merr.Error()}
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// report prints every differing path between two documents and returns the count.
func report(title string, a, b map[string]any) int {
	var diffs []string
	var part string
	for _, part = range partNames {
		diffAny(part, a[part], b[part], &diffs)
	}
	fmt.Printf("\n=== %s: %d differing paths\n", title, len(diffs))
	var d string
	for _, d = range diffs {
		fmt.Println("  " + d)
	}
	return len(diffs)
}

// diffAny walks two generic JSON trees and records the paths that differ.
func diffAny(path string, a, b any, out *[]string) {
	if reflect.DeepEqual(a, b) {
		return
	}
	var ma, okA = a.(map[string]any)
	var mb, okB = b.(map[string]any)
	if okA && okB {
		var keys = map[string]bool{}
		var k string
		for k = range ma {
			keys[k] = true
		}
		for k = range mb {
			keys[k] = true
		}
		var sorted []string
		for k = range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k = range sorted {
			diffAny(path+"."+k, ma[k], mb[k], out)
		}
		return
	}
	var la, okLA = a.([]any)
	var lb, okLB = b.([]any)
	if okLA && okLB && len(la) == len(lb) {
		var i int
		for i = range la {
			diffAny(path+"["+strconv.Itoa(i)+"]", la[i], lb[i], out)
		}
		return
	}
	*out = append(*out, fmt.Sprintf("%s: %s -> %s", path, short(a), short(b)))
}

func short(v any) string {
	var raw, _ = json.Marshal(v)
	var s = string(raw)
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func printTranscript(label string, steps []mdpv1.Step) {
	var s mdpv1.Step
	for _, s = range steps {
		var raw, _ = json.Marshal(s)
		fmt.Printf("%s transcript: %s\n", label, raw)
	}
}

// ctxWith is a per-side budget; the cancel is kept until exit — a tool that
// runs three sessions in a row and exits has no earlier moment to release it.
var cancels []context.CancelFunc

func ctxWith(d time.Duration) context.Context {
	var ctx, cancel = context.WithTimeout(context.Background(), d)
	cancels = append(cancels, cancel)
	return ctx
}

func save(path string, doc map[string]any) {
	var raw, _ = json.MarshalIndent(doc, "", "  ")
	var err = os.WriteFile(path, raw, 0o600)
	if err != nil {
		fail("write %s: %v", path, err)
	}
}

func load(path string) map[string]any {
	var raw, err = os.ReadFile(path)
	if err != nil {
		fail("read %s: %v", path, err)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		fail("decode %s", path)
	}
	return out
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

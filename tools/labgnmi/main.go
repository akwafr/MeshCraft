// Command labgnmi subscribes to a lab switch with the PRODUCT's own collector
// and prints what comes back.
//
// EXPLORATION ONLY, and its value is precisely that it adds nothing: it builds a
// [gnmipoll.Target] and a [gnmipoll.Collector] and prints the
// [metricbuf.Sample] values they produce. The SubscribeRequest, the encoding,
// the sub-modes, the path table and the translation are all the shipped code —
// so a leaf that does not appear here does not appear in the product either, and
// a sample that is wrong here is wrong on the graph.
//
// That is what makes it worth writing rather than reaching for the gnmic CLI: a
// `gnmic get` proves the DEVICE serves a path, which is a weaker claim than the
// one that matters — that this collector's table names it correctly.
//
// Not part of the product; nothing imports it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/gnmipoll"
	"meshcraft/internal/metricbuf"
)

func main() {
	var addr = flag.String("addr", "127.0.0.1", "address to subscribe to")
	var port = flag.Int("port", 16030, "gNMI port")
	var metric = flag.String("metric", "if_counters", "if_state|if_counters|health|lldp")
	var window = flag.Duration("window", 40*time.Second, "how long to listen")
	var interval = flag.Duration("interval", 10*time.Second, "sample interval")
	// -raw subscribes an ARBITRARY path and prints every leaf that arrives,
	// mapped or not.
	//
	// The tool's most useful mode, and the reason is what the mapped modes cannot
	// tell you: a series that does not appear has two very different causes — the
	// device does not serve the leaf, or it serves it under a name this project's
	// table does not know. Those call for opposite fixes, and only the unfiltered
	// leaf list distinguishes them.
	var raw = flag.String("raw", "", "subscribe this path and dump every leaf, mapped or not")
	// -keys keeps the list keys (`[name=Ethernet49]`) in the -raw dump. The
	// default strips them so forty ports collapse into one line per leaf — the
	// right view for "which leaves exist", and exactly the wrong one for "what
	// are the components CALLED", which is the question a translation table's
	// attribution depends on (the transceiver work needed the component names,
	// and the stripped dump could not answer).
	var keys = flag.Bool("keys", false, "with -raw: keep list keys, one line per keyed instance")
	var plaintext = flag.Bool("plaintext", false, "no TLS at all")
	var skipVerify = flag.Bool("skip-verify", false, "TLS without checking the certificate")
	var caFile = flag.String("ca", "", "PEM file the target's certificate must chain to")
	flag.Parse()

	// DEBUG level, because that is where the transport reports a subscription
	// that ended — and a TLS failure ends one silently otherwise. Finding out
	// WHICH way a handshake was refused is the whole point of this tool.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{Level: slog.LevelDebug})))

	var tgt = gnmipoll.Target{
		DeviceID:   "lab-arista",
		IP:         *addr,
		Port:       *port,
		Username:   os.Getenv("LAB_USER"),
		Password:   os.Getenv("LAB_PASS"),
		Plaintext:  *plaintext,
		SkipVerify: *skipVerify,
	}
	if *caFile != "" {
		var pem, rerr = os.ReadFile(*caFile)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "ca: %v\n", rerr)
			os.Exit(2)
		}
		tgt.CAPEM = string(pem)
	}
	var ctx, cancel = context.WithTimeout(context.Background(), *window)
	defer cancel()

	if *metric == gnmipoll.MetricLLDP {
		runLLDP(ctx, tgt)
		return
	}
	if *raw != "" {
		runRaw(ctx, tgt, *raw, *interval, *keys)
		return
	}

	// Counts per series name rather than a firehose: forty ports times ten
	// counters at a ten second cadence is a thousand lines a minute, and what is
	// being checked is WHICH SERIES ARRIVE, not their values.
	var seen = map[string]int{}
	var example = map[string]metricbuf.Sample{}
	var coll = gnmipoll.Collector{Target: tgt, Metric: *metric, Interval: *interval}
	var err = coll.Run(ctx, gnmipoll.Transport(), func(batch []metricbuf.Sample) {
		var s metricbuf.Sample
		var had bool
		for _, s = range batch {
			seen[s.Name]++
			_, had = example[s.Name]
			if !had {
				example[s.Name] = s
			}
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe failed: %v\n", err)
		os.Exit(1)
	}
	report(*metric, seen, example)
}

func report(metric string, seen map[string]int, example map[string]metricbuf.Sample) {
	var names = make([]string, 0, len(seen))
	var n string
	for n = range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("=== %s: %d distinct series ===\n", metric, len(names))
	for _, n = range names {
		var e = example[n]
		var labels = make([]string, 0, len(e.Labels))
		var k string
		for k = range e.Labels {
			labels = append(labels, k+"="+e.Labels[k])
		}
		sort.Strings(labels)
		fmt.Printf("%-42s n=%-5d %v = %v\n", n, seen[n], strings.Join(labels, ","), e.Value)
	}
	if len(names) == 0 {
		fmt.Println("NOTHING ARRIVED — either the device does not serve these paths, or " +
			"the table in internal/gnmipoll/translate.go does not name them correctly.")
	}
}

// runLLDP exercises the neighbour table, which produces a snapshot rather than
// samples and therefore does not go through Collector.
func runLLDP(ctx context.Context, tgt gnmipoll.Target) {
	var table = gnmipoll.NewTable(tgt.DeviceID)
	var st, err = gnmipoll.Transport().Subscribe(ctx, tgt,
		gnmipoll.SubscriptionsFor(gnmipoll.MetricLLDP, 0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe failed: %v\n", err)
		os.Exit(1)
	}
	var n gnmipoll.Notification
	var open bool
	for {
		select {
		case <-ctx.Done():
			print(table)
			return
		case n, open = <-st.C:
			if !open {
				// The reason the stream ended, which is the whole point of running
				// this tool against a lab box: "nothing arrived" and "the device
				// refused the login" look identical without it.
				var reason = st.Reason()
				if reason != nil {
					fmt.Fprintf(os.Stderr, "subscription ended: %v (classified as %s)\n",
						reason, gnmipoll.ClassifyEnd(reason))
				}
				print(table)
				return
			}
			// Fold, and NOT Apply-then-Delete: this tool is what the proxy's table
			// gets checked against, so it has to fold a notification exactly as the
			// proxy does. Applying updates before deletes inverts the order gNMI
			// mandates and loses a neighbour the device has just re-published —
			// which would make the reference disagree with the thing it references.
			table.Fold(n)
		}
	}
}

func print(table *gnmipoll.Table) {
	var r = table.Report()
	fmt.Printf("=== lldp: served=%v chassis=%q sysname=%q neighbours=%d ===\n",
		r.Served, r.ChassisID, r.SysName, len(r.Neighbors))
	var i int
	for i = range r.Neighbors {
		var nb = r.Neighbors[i]
		fmt.Printf("%-14s -> chassis=%-24s port=%-16s sys=%q\n",
			nb.LocalPort, nb.ChassisID, nb.PortID, nb.SysName)
	}
	if len(r.Neighbors) == 0 {
		fmt.Println("NO NEIGHBOURS — the device shows four in \"show lldp neighbors\", so " +
			"an empty table means the OpenConfig path or the leaf names are wrong.")
	}
}

// runRaw subscribes one arbitrary path and prints the DISTINCT leaf suffixes
// that arrive, with an example value.
//
// Distinct suffixes rather than every notification: a components subtree on a
// 64-port switch is thousands of updates a minute, and the question being asked
// is which LEAVES exist, not how often they tick.
func runRaw(ctx context.Context, tgt gnmipoll.Target, path string,
	interval time.Duration, keepKeys bool) {
	var p, perr = gnmipoll.ParsePath(path)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "bad path: %v\n", perr)
		os.Exit(2)
	}
	var st, err = gnmipoll.Transport().Subscribe(ctx, tgt, []gnmipoll.Subscription{
		{Path: p, Mode: gnmipoll.SubSample, SampleInterval: interval},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe failed: %v\n", err)
		os.Exit(1)
	}
	var seen = map[string]string{}
	var n gnmipoll.Notification
	var open bool
	for {
		select {
		case <-ctx.Done():
			dumpLeaves(path, seen)
			return
		case n, open = <-st.C:
			if !open {
				var reason = st.Reason()
				if reason != nil {
					fmt.Fprintf(os.Stderr, "subscription ended: %v (classified as %s)\n",
						reason, gnmipoll.ClassifyEnd(reason))
				}
				dumpLeaves(path, seen)
				return
			}
			var u gnmipoll.Update
			for _, u = range gnmipoll.Expand(n.Update) {
				var full = gnmipoll.Join(n.Prefix, u.Path)
				// Keyed on the ELEMENT NAMES with keys stripped, so forty ports do
				// not produce forty entries for one leaf. With -keys, the full
				// spelling instead: one line per keyed instance.
				var key = strings.Join(full.Names(), "/")
				if keepKeys {
					key = full.String()
				}
				var _, had = seen[key]
				if had {
					continue
				}
				seen[key] = describe(u.Value)
			}
		}
	}
}

func describe(v gnmipoll.Value) string {
	switch v.Kind {
	case gnmipoll.ValueNumber:
		return fmt.Sprintf("num %v", v.Num)
	case gnmipoll.ValueString:
		return fmt.Sprintf("str %q", v.Str)
	case gnmipoll.ValueJSON:
		return fmt.Sprintf("json %s", truncate(string(v.JSON), 80))
	}
	return "none"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func dumpLeaves(path string, seen map[string]string) {
	var keys = make([]string, 0, len(seen))
	var k string
	for k = range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("=== %s: %d distinct leaves ===\n", path, len(keys))
	for _, k = range keys {
		fmt.Printf("%-64s %s\n", k, seen[k])
	}
}

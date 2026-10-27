package mdpv1

// The COLLECTION-CHANNEL vocabulary, and the one kind that is a promise.
//
// [MonitorChannel.Kind] has always been a bare string, compared with `==` by
// [MonitoringCapabilities.BestOfKind] and written as a literal in the two places
// that resolve a cadence (internal/api/watch_handlers.go). That worked while
// there was exactly one collector in the product; it stops working the moment
// there are two, because a kind that is a typo does not fail to compile — it
// fails to match, and a device whose connector declares `snpm` is simply never
// collected, with no error anywhere.
//
// So the kinds live here, as constants, next to the prose that says what each
// one COMMITS the project to.
//
// # The kind names the PROTOCOL, not the family
//
// `snmp` and `gnmi` are not two flavours of "polling": they are two wire
// protocols, collected by two different programs, with two different credential
// shapes and two different sample label sets. BestOfKind matches the exact
// string precisely because one collector implements one protocol — a caller
// asking "how fast may I poll this device over SNMP" must not be answered with
// the floor of a channel nobody polls over SNMP. That bug already happened once
// (RouterOS, a 5 s `web` channel answering an SNMP question) and is the whole
// reason BestOfKind exists alongside Best.
//
// # A `gnmi` channel is a PROMISE, and declaring one without a collector is the
// same fault as WritesExtra on a form nobody ran
//
// The write side of this contract has a rule everyone in this codebase knows:
// [Connector.WritesExtra] stays false until the command forms have been proven
// on hardware, because a declared write puts a button on screen whose first
// exercise would be on a customer's switch.
//
// The read side has the same rule and it is easier to break, because nothing
// visibly fails. A connector that declares a channel of a kind no collector
// implements produces: a UI that says the metric is available, a device that is
// never subscribed to, and a graph that stays empty for ever — with no error, no
// log line, and no way for the operator to tell "this platform cannot" from
// "this build does not". That is strictly worse than declaring nothing, which at
// least says the truth.
//
// So: DECLARE A `gnmi` CHANNEL ONLY WHEN A COLLECTOR SUBSCRIBES. In this build
// no collector does — internal/gnmipoll owns the paths and the translation, and
// its transport returns an explicit "not built" error — and a registry-wide test
// (internal/api/gnmi_declaration_test.go) fails if any connector declares one
// while that remains true.
//
// # gNMI samples carry `device` and `port`, and deliberately no `ifindex`
//
// gNMI names an interface by its OpenConfig key (`name=Ethernet49`); there is no
// interface index in the subscription and no ifType. That is not a gap to be
// filled by a side lookup — it is the same position the MikroTik binary-API and
// SwOS page channels are in, and internal/metricbuf/names.go states what it buys:
// a sample with no `iftype` label feeds curves and can never reach inventory, so
// the rule "telemetry never CREATES a port" holds by construction rather than by
// a guardrail somebody has to remember.
//
// The consequence to plan for, stated here because it is invisible until it
// bites: a device collected over BOTH kinds would publish the same series name
// twice with two different label sets, which VictoriaMetrics stores as two
// adjacent half-populated series — exactly the failure names.go was written to
// prevent. Whoever wires the controller half must make the two kinds EXCLUSIVE
// per device and per metric, not merely additive.
const (
	// KindSNMP: the proxy's SNMP poller (internal/snmppoll). The only kind any
	// periodic collector implements in this build.
	KindSNMP = "snmp"
	// KindWeb: the platform's own HTTP interface, scraped by the connector.
	KindWeb = "web"
	// KindAPI: a vendor API the connector speaks (the RouterOS binary API).
	KindAPI = "api"
	// KindGNMI: gNMI Subscribe, streamed to the proxy. Read the file header
	// before declaring one: this kind is a promise that something subscribes.
	KindGNMI = "gnmi"
)

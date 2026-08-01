package instanton

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: Instant On Admin reads spanning tree from the sections the
// product UI itself loads (pages1.js STP.initWcd).
var _ driver.SpanningTreeReader = (*Admin)(nil)

// Live JL685A (Instant On 1930) — confirmed via web-probe of pages1.js and
// authenticated RawSection:
//
//	initWcd = wcd?{SpanningTreeGlobalParam}{STP}{RSTP}
//
// SpanningTreeGlobalParam carries admin enable + protocol version
// (STPOperationMode: 0=STP, 2=RSTP, 3=MSTP). STP carries bridge identity and
// the CST InterfaceList. RSTP carries rapid-protocol port extras.
const (
	sectionSTPGlobalParam = "SpanningTreeGlobalParam"
	sectionSTP            = "STP"
	sectionRSTP           = "RSTP"
)

// ReadSpanningTree reads global mode/priority and CST port roles from the
// Instant On wcd sections the local UI uses.
func (a *Admin) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState

	var paramBody, err = a.RawSection(ctx, sectionSTPGlobalParam)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", sectionSTPGlobalParam, err)
	}
	if sectionMissing(paramBody, sectionSTPGlobalParam) {
		return out, fmt.Errorf("section %s not found on device", sectionSTPGlobalParam)
	}

	out.Enabled = ionTrue(xmlField(paramBody, "enabled"))
	out.Mode = ionSTPOperationMode(xmlField(paramBody, "STPOperationMode"))
	if !out.Enabled {
		out.Mode = ""
		a.step(true, "spanning tree read", "admin off")
		return out, nil
	}

	var stpBody string
	stpBody, err = a.RawSection(ctx, sectionSTP)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", sectionSTP, err)
	}
	if sectionMissing(stpBody, sectionSTP) {
		return out, fmt.Errorf("section %s not found on device", sectionSTP)
	}

	var bridge = sectionOf(stpBody, "BridgeSetting")
	if bridge == "" {
		bridge = stpBody
	}
	out.BridgePriority = xmlInt(bridge, "bridgePriority", "BridgePriority")
	var addr = xmlField(bridge, "address")
	if out.BridgePriority > 0 && addr != "" {
		out.BridgeID = fmt.Sprintf("%d-%s", out.BridgePriority, strings.ToLower(addr))
	} else {
		out.BridgeID = xmlFieldAny(stpBody, "bridgeID", "BridgeIdentifier")
	}

	var root = sectionOf(stpBody, "DesignatedRoot")
	if root == "" {
		root = stpBody
	}
	out.RootBridgeID = xmlFieldAny(root, "rootBridgeID", "RootBridgeID", "DesignatedRoot")
	out.RootPathCost = xmlInt(root, "rootPathCost", "RootPathCost", "RootCost")
	if out.BridgeID != "" && out.RootBridgeID != "" {
		out.IsRoot = ionBridgeIDsEqual(out.BridgeID, out.RootBridgeID)
	}

	out.Ports = parseSTPPorts(stpBody, a.portPrefix())

	a.step(true, "spanning tree read",
		fmt.Sprintf("mode %s, enabled=%v, %d port(s)", out.Mode, out.Enabled, len(out.Ports)))
	return out, nil
}

func sectionMissing(body, name string) bool {
	if body == "" {
		return true
	}
	if strings.Contains(body, "Cannot find the section") {
		return true
	}
	if strings.Contains(body, "statusCode>-1") || strings.Contains(body, "<statusCode>-1</statusCode>") {
		return true
	}
	return !strings.Contains(body, "<"+name)
}

// ionSTPOperationMode maps SpanningTreeGlobalParam.STPOperationMode.
//
// Product radios (pages1.js rboProtocolVersion): 0=STP, 2=RSTP, 3=MSTP.
// (There is no "1" — that was a wrong ForceVersion guess from other Marvell UIs.)
func ionSTPOperationMode(raw string) string {
	switch strings.TrimSpace(raw) {
	case "0":
		return "stp"
	case "2", "1": // tolerate legacy/mis-set "1" as RSTP
		return "rstp"
	case "3":
		return "mstp"
	}
	return ""
}

// ionOperationModeCode is the write-side inverse of ionSTPOperationMode.
func ionOperationModeCode(mode string) string {
	switch driver.NormalizeSTPMode(mode) {
	case "stp":
		return "0"
	case "mstp":
		return "3"
	default:
		return "2" // rstp
	}
}

// ionTrue: Instant On _TRUE='1', _FALSE='2' (modules.js).
func ionTrue(v string) bool {
	var s = strings.ToLower(strings.TrimSpace(v))
	return s == "1" || s == "true" || s == "enable" || s == "enabled"
}

func ionBridgeIDsEqual(a, b string) bool {
	return strings.EqualFold(normalizeBridgeID(a), normalizeBridgeID(b))
}

func normalizeBridgeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func xmlFieldAny(body string, keys ...string) string {
	var k string
	for _, k = range keys {
		var v = xmlField(body, k)
		if v != "" {
			return v
		}
	}
	return ""
}

func xmlBool(body string, keys ...string) bool {
	var k string
	for _, k = range keys {
		var v = strings.ToLower(xmlField(body, k))
		if v == "" {
			continue
		}
		if ionTrue(v) {
			return true
		}
		if v == "2" || v == "0" || v == "false" || v == "disable" || v == "disabled" {
			return false
		}
	}
	return false
}

func xmlInt(body string, keys ...string) int {
	var k string
	for _, k = range keys {
		var v = xmlField(body, k)
		if v == "" {
			continue
		}
		var n, err = strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

var reInterfaceEntry = regexp.MustCompile(`(?s)<InterfaceEntry>(.*?)</InterfaceEntry>`)

// parseSTPPorts reads STP.InterfaceList entries (CST port table on Instant On).
func parseSTPPorts(body, prefix string) []driver.SpanningTreePort {
	var matches = reInterfaceEntry.FindAllStringSubmatch(body, -1)
	var out []driver.SpanningTreePort
	var m []string
	for _, m = range matches {
		var block = m[1]
		var raw = xmlFieldAny(block, "interfaceName", "InterfaceName", "interfaceID")
		if raw == "" {
			continue
		}
		var p = driver.SpanningTreePort{Port: facadePortName(raw, prefix)}
		p.Role = ionPortRole(xmlFieldAny(block, "portRole", "PortRole"))
		p.State = ionPortState(xmlFieldAny(block, "portState", "PortState"))
		p.PathCost = xmlInt(block, "pathCost", "PathCost")
		if p.PathCost == 0 {
			p.PathCost = xmlInt(block, "pathCostConfigured")
		}
		p.EdgeMode, p.Edge = parsePortFastEdge(block)
		p.BPDUGuard = ionTrue(xmlFieldAny(block, "BPDUGuardEnabled", "BpduGuardMode", "BPDUGuard"))
		out = append(out, p)
	}
	return out
}

// parsePortFastEdge maps Instant On Edge Port radios:
//
//	portFastEnabled 3=auto, 1=enable, 2=disable (pages1.js chkEditEdgePort)
//	portFastOperEnabled is the operative edge flag (_TRUE/_FALSE).
func parsePortFastEdge(block string) (mode string, operative bool) {
	operative = ionTrue(xmlFieldAny(block, "portFastOperEnabled", "fastLinkOperStatus", "EdgePort", "OperEdgePort"))
	switch strings.TrimSpace(xmlFieldAny(block, "portFastEnabled", "fastLinkStatusEnabled", "AdminEdgePort")) {
	case "3":
		return driver.STPEdgeAuto, operative
	case "1":
		return driver.STPEdgeYes, true
	case "2":
		return driver.STPEdgeNo, false
	}
	// Legacy AdminEdge/AutoEdge pair from older guesses — keep for fixtures.
	return parseCSTEdge(block)
}

// parseCSTEdge maps Instant On Edge Port (Auto / Enable / Disable) onto
// EdgeMode + operative Edge. Kept for fixtures that still use AdminEdgePort.
func parseCSTEdge(block string) (mode string, operative bool) {
	var adminRaw = strings.TrimSpace(xmlFieldAny(block, "AdminEdgePort", "adminEdgePort", "EdgePortMode"))
	var adminLower = strings.ToLower(adminRaw)
	switch adminLower {
	case "auto", "2":
		return driver.STPEdgeAuto, xmlBool(block, "EdgePort", "OperEdgePort", "OperationalEdgePort")
	case "enable", "enabled", "yes", "1", "true":
		return driver.STPEdgeYes, true
	case "disable", "disabled", "no", "0", "false":
		if adminLower == "0" || adminLower == "false" {
			break
		}
		return driver.STPEdgeNo, false
	}

	var admin = xmlBool(block, "AdminEdgePort")
	var autoPresent = xmlFieldAny(block, "AutoEdgePort", "autoEdgePort") != ""
	var auto = true
	if autoPresent {
		auto = xmlBool(block, "AutoEdgePort", "autoEdgePort")
	}
	operative = xmlBool(block, "EdgePort", "OperEdgePort", "OperationalEdgePort")
	if !operative {
		operative = admin
	}
	switch {
	case admin:
		return driver.STPEdgeYes, true
	case autoPresent && !auto:
		return driver.STPEdgeNo, false
	default:
		return driver.STPEdgeAuto, operative
	}
}

// portFastCode encodes Edge Port for STP InterfaceList writes.
func portFastCode(edge string) string {
	switch driver.NormalizeSTPEdge(edge) {
	case driver.STPEdgeYes:
		return "1"
	case driver.STPEdgeNo:
		return "2"
	case driver.STPEdgeAuto:
		return "3"
	}
	return ""
}

// cstEdgeFields encodes Auto / Enable / Disable as AdminEdgePort + AutoEdgePort
// for older write shapes; live Instant On CST edits use portFastEnabled instead.
func cstEdgeFields(edge string) string {
	switch driver.NormalizeSTPEdge(edge) {
	case driver.STPEdgeYes:
		return "<AdminEdgePort>1</AdminEdgePort><AutoEdgePort>0</AutoEdgePort>"
	case driver.STPEdgeNo:
		return "<AdminEdgePort>0</AdminEdgePort><AutoEdgePort>0</AutoEdgePort>"
	case driver.STPEdgeAuto:
		return "<AdminEdgePort>0</AdminEdgePort><AutoEdgePort>1</AutoEdgePort>"
	}
	return ""
}

// facadePortName maps Instant On interfaceName ("1", "GE1") onto meshcraft names.
func facadePortName(raw, prefix string) string {
	var s = strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	var _, err = strconv.Atoi(s)
	if err == nil {
		if prefix == "" {
			prefix = defaultPortPrefix
		}
		return prefix + s
	}
	return s
}

// devicePortName strips the facade prefix so wcd writes use numeric interfaceName.
func devicePortName(facade, prefix string) string {
	var s = strings.TrimSpace(facade)
	if prefix == "" {
		prefix = defaultPortPrefix
	}
	if strings.HasPrefix(strings.ToUpper(s), strings.ToUpper(prefix)) {
		var rest = s[len(prefix):]
		var _, err = strconv.Atoi(rest)
		if err == nil {
			return rest
		}
	}
	return s
}

// ionPortRole maps numeric Instant On / RADLAN portRole codes.
// Observed on JL685A: 1=disabled, 4=root (rootPortID), 5=designated.
func ionPortRole(s string) string {
	switch strings.TrimSpace(s) {
	case "1":
		return driver.STPRoleDisabled
	case "2":
		return driver.STPRoleAlternate
	case "3":
		return driver.STPRoleBackup
	case "4":
		return driver.STPRoleRoot
	case "5":
		return driver.STPRoleDesignated
	case "6":
		return driver.STPRoleMaster
	}
	return normalizePortRole(s)
}

// ionPortState maps numeric Instant On portState codes.
// Observed: 1=disabled, 5=forwarding.
func ionPortState(s string) string {
	switch strings.TrimSpace(s) {
	case "1":
		return "" // disabled / not participating
	case "2", "3":
		return driver.STPStateDiscarding // blocking / listening
	case "4":
		return driver.STPStateLearning
	case "5":
		return driver.STPStateForwarding
	case "6":
		return driver.STPStateDiscarding // broken
	}
	return normalizePortState(s)
}

func normalizePortRole(s string) string {
	var v = strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "root"):
		return driver.STPRoleRoot
	case strings.Contains(v, "design"):
		return driver.STPRoleDesignated
	case strings.Contains(v, "alter"):
		return driver.STPRoleAlternate
	case strings.Contains(v, "back"):
		return driver.STPRoleBackup
	case strings.Contains(v, "master"):
		return driver.STPRoleMaster
	case strings.Contains(v, "disable"):
		return driver.STPRoleDisabled
	}
	return ""
}

func normalizePortState(s string) string {
	var v = strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "forward"):
		return driver.STPStateForwarding
	case strings.Contains(v, "learn"):
		return driver.STPStateLearning
	case strings.Contains(v, "discard") || strings.Contains(v, "block"):
		return driver.STPStateDiscarding
	case strings.Contains(v, "disable"):
		return driver.STPRoleDisabled
	}
	return ""
}

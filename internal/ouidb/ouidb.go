// Package ouidb answers "which vendor made this network card" from a MAC
// address, offline.
//
// The data is the IEEE MA-L registry (standards-oui.ieee.org/oui/oui.csv,
// fetched 2026-08-13), trimmed to assignment + organisation name and gzipped:
// 39,926 entries in ~380 KiB embedded, decoded once into a map on first use
// (~4 MB resident). Embedded rather than fetched at runtime because the
// controller may serve a site with no internet at all — the PWA and this
// registry follow the same rule: everything the product needs, it carries.
//
// # What a lookup can and cannot say
//
// An OUI names the card's MANUFACTURER, not the machine: "Intel Corporate" on
// a laptop's dock, "Proxmox Server Solutions GmbH" on a VM's virtual NIC. And
// a randomised address (the privacy default of every phone OS since ~2020)
// carries the locally-administered bit and matches NO registry entry — the
// lookup reports that case as its own answer rather than an empty string,
// because "this machine hides its identity" and "the registry has a gap" call
// for different reactions from whoever reads the page.
package ouidb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
)

//go:embed data/oui.tsv.gz
var ouiData []byte

var loadOnce sync.Once
var table map[string]string

// load decodes the embedded registry. Failure leaves an empty table: a
// corrupt embed is a build defect, and lookups degrade to "unknown" rather
// than panicking a controller over a cosmetic column.
func load() {
	table = map[string]string{}
	var zr, err = gzip.NewReader(bytes.NewReader(ouiData))
	if err != nil {
		return
	}
	var sc = bufio.NewScanner(zr)
	for sc.Scan() {
		var line = sc.Text()
		var tab = strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		table[line[:tab]] = line[tab+1:]
	}
}

// hexOnly keeps the hex digits of a MAC spelling, lowercased.
func hexOnly(mac string) string {
	var out = make([]byte, 0, 12)
	var r rune
	for _, r = range strings.ToLower(mac) {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
			out = append(out, byte(r))
		case r == ':' || r == '-' || r == '.':
			// separators of the three common spellings
		default:
			return ""
		}
	}
	if len(out) != 12 {
		return ""
	}
	return string(out)
}

// LocallyAdministered reports the U/L bit of the first byte — a randomised
// (privacy) or software-assigned address, which by construction matches no
// registry entry.
func LocallyAdministered(mac string) bool {
	var hex = hexOnly(mac)
	if hex == "" {
		return false
	}
	var hi = strings.IndexByte("0123456789abcdef", hex[0])
	var lo = strings.IndexByte("0123456789abcdef", hex[1])
	return ((hi*16+lo)>>1)&1 == 1
}

// Vendor returns the organisation that registered the MAC's OUI, and whether
// the registry knows it. Accepts the three common spellings (colon, dash,
// dotted). A locally-administered address returns ("", false) here — callers
// that want to SAY so use [LocallyAdministered], which names that case.
func Vendor(mac string) (string, bool) {
	loadOnce.Do(load)
	var hex = hexOnly(mac)
	if hex == "" {
		return "", false
	}
	var name, known = table[hex[:6]]
	return name, known
}

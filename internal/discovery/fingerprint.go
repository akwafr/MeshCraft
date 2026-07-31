package discovery

import (
	"regexp"
	"strings"
)

// Matcher relie un indice textuel a un driver. L'ordre du tableau compte : le
// premier motif qui accroche gagne, les entries les plus specific d'abord.
type Matcher struct {
	Driver string
	Vendor string
	Re     *regexp.Regexp
	// PageCmd desactive la pagination interactive, Ident identifie, Config
	// exporte la configuration courante. Vides pour les drivers generiques.
	PageCmd    string
	IdentCmd   string
	ConfigCmd  string
	ConfigNote string
}

func re(p string) *regexp.Regexp { return regexp.MustCompile("(?i)" + p) }

// matchers : reconnaissance des plateformes courantes.
//
// Ces entries servent a IDENTIFIER the device et a lire sa configuration.
// Elles ne constituent pas encore des connecteurs write : piloter la
// configuration d'un Cisco ou d'un Aruba demande le travail de la phase 2
// (cf. docs/01-Architecture/Connecteurs).
var matchers = []Matcher{
	// Aruba Instant On : AVANT les entries HPE/Aruba generiques, sinon
	// « hpe-procurve » attraperait la gamme. Ces switchs sont WEB-ONLY (ni SSH ni
	// Telnet) : aucune commande CLI a leur envoyer, hence les champs vides.
	// Connecteur : internal/driver/instanton.
	{Driver: "aruba-instant-on-1930", Vendor: "Aruba (HPE)",
		Re:         re(`Instant\s?On`),
		ConfigNote: "Gamme web-only : la configuration se lit par l'interface web, pas par CLI."},
	{Driver: "cisco-nxos", Vendor: "Cisco", Re: re(`NX-OS|Nexus`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "cisco-ios", Vendor: "Cisco", Re: re(`Cisco IOS|IOS-XE|IOS Software|Catalyst|WS-C\d|C9[23]00`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "aruba-cx", Vendor: "Aruba", Re: re(`ArubaOS-CX|AOS-CX|Aruba JL\d`),
		PageCmd: "no page", IdentCmd: "show system", ConfigCmd: "show running-config"},
	{Driver: "hpe-procurve", Vendor: "HPE", Re: re(`ProCurve|Aruba|HP J\d|HPE OfficeConnect`),
		PageCmd: "no page", IdentCmd: "show system-information", ConfigCmd: "show running-config"},
	{Driver: "juniper-junos", Vendor: "Juniper", Re: re(`JUNOS|Juniper`),
		PageCmd: "set cli screen-length 0", IdentCmd: "show version",
		ConfigCmd: "show configuration | display set"},
	// SwOS AVANT RouterOS, et ce n'est pas un detail d'ordre : le titre de sa page
	// est « MikroTik SwOS », que le motif de RouterOS attrapait par son
	// alternative `MikroTik`. Un CSS was donc range sous `mikrotik-routeros`, et
	// le connecteur lui parlait REST — une API que SwOS n'a pas. Ces deux systemes
	// partagent un constructeur et presque rien d'autre.
	// Connecteur : internal/driver/mikrotik/swos.
	{Driver: "mikrotik-swos", Vendor: "MikroTik", Re: re(`SwOS`),
		ConfigNote: "Gamme web-only : la configuration se lit par les pages `*.b` de " +
			"l'interface web (authentification HTTP Digest), pas par CLI."},
	// Connecteur : internal/driver/mikrotik/routeros.
	{Driver: "mikrotik-routeros", Vendor: "MikroTik", Re: re(`RouterOS|ROSSSH|MikroTik`),
		IdentCmd: "/system resource print", ConfigCmd: "/export terse"},
	{Driver: "ubiquiti", Vendor: "Ubiquiti", Re: re(`UniFi|EdgeSwitch|EdgeOS|Ubiquiti`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "extreme-exos", Vendor: "Extreme", Re: re(`ExtremeXOS|EXOS|Extreme Networks`),
		PageCmd: "disable clipaging", IdentCmd: "show version", ConfigCmd: "show configuration"},
	{Driver: "huawei-vrp", Vendor: "Huawei", Re: re(`Huawei|VRP \(|Versatile Routing`),
		PageCmd: "screen-length 0 temporary", IdentCmd: "display version",
		ConfigCmd: "display current-configuration"},
	{Driver: "h3c-comware", Vendor: "H3C", Re: re(`H3C|Comware`),
		PageCmd: "screen-length disable", IdentCmd: "display version",
		ConfigCmd: "display current-configuration"},
	{Driver: "dell-os10", Vendor: "Dell", Re: re(`Dell EMC|OS10|PowerConnect|Force10`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-configuration"},
	{Driver: "fortiswitch", Vendor: "Fortinet", Re: re(`FortiSwitch|FortiOS`),
		IdentCmd: "get system status", ConfigCmd: "show"},
	{Driver: "zyxel", Vendor: "Zyxel", Re: re(`Zyxel|ZyNOS`),
		IdentCmd: "show system-information", ConfigCmd: "show running-config"},
	{Driver: "netgear", Vendor: "Netgear", Re: re(`Netgear|NETGEAR|GS[13]\d{2}|M4[13]00`),
		IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "tplink", Vendor: "TP-Link", Re: re(`TP-?Link|T[12][56]00G`),
		IdentCmd: "show system-info", ConfigCmd: "show running-config"},
	{Driver: "dlink", Vendor: "D-Link", Re: re(`D-Link|DGS-|DES-`),
		IdentCmd: "show switch", ConfigCmd: "show config current_config"},
	{Driver: "cumulus-sonic", Vendor: "Linux", Re: re(`Cumulus|SONiC`),
		IdentCmd: "cat /etc/os-release", ConfigCmd: "cat /etc/network/interfaces"},
}

// genericSSH et genericHTTP servent quand rien n'accroche : the device
// apparait quand same dans la file d'adoption, marque non identifie.
var genericSSH = Matcher{Driver: "generic-ssh", Vendor: "",
	IdentCmd: "show version", ConfigCmd: "show running-config"}
var genericHTTP = Matcher{Driver: "generic-http", Vendor: ""}

// Confiance attribuee selon la source de l'indice : ce que the device dit de
// lui-same after login vaut mieux qu'un en-tete HTTP.
const (
	// ConfDeviceSelfReport : the device s'est identifie lui-same par son API
	// ou ses champs de page — mieux qu'une sortie de commande interpretee.
	ConfDeviceSelfReport = 96
	ConfShellOutput      = 92
	ConfSSHBanner        = 72
	ConfHTTPHeader       = 55
	ConfPortsOnly        = 20
)

// Identify choisit un driver a partir des indices collectes. Renvoie aussi la
// confiance et la source retenue, que l'UI affiche telle quelle.
func Identify(shellOut, sshBanner, httpServer, httpTitle string) (Matcher, int, string) {
	type source struct {
		text  string
		conf  int
		label string
	}
	var sources = []source{
		{shellOut, ConfShellOutput, "sortie de commande"},
		{sshBanner, ConfSSHBanner, "banniere SSH"},
		{httpServer + " " + httpTitle, ConfHTTPHeader, "en-tete/titre HTTP"},
	}
	var src source
	var m Matcher
	for _, src = range sources {
		if strings.TrimSpace(src.text) == "" {
			continue
		}
		for _, m = range matchers {
			if m.Re.MatchString(src.text) {
				return m, src.conf, src.label
			}
		}
	}
	// Rien d'identifie : on garde un driver generique selon le transport.
	if strings.TrimSpace(sshBanner) != "" || strings.TrimSpace(shellOut) != "" {
		return genericSSH, ConfPortsOnly, "non identifie (SSH joignable)"
	}
	if strings.TrimSpace(httpServer+httpTitle) != "" {
		return genericHTTP, ConfPortsOnly, "non identifie (web joignable)"
	}
	return genericHTTP, ConfPortsOnly, "non identifie"
}

// MatcherFor retrouve un matcher par nom de driver (choix manuel du type dans
// l'UI, ou candidat recharge depuis la base).
func MatcherFor(driver string) Matcher {
	var m Matcher
	for _, m = range matchers {
		if m.Driver == driver {
			return m
		}
	}
	if driver == genericSSH.Driver {
		return genericSSH
	}
	return genericHTTP
}

// Drivers liste les drivers proposables dans l'UI (choix explicite du type).
func Drivers() []map[string]string {
	var out = []map[string]string{}
	var m Matcher
	for _, m = range matchers {
		out = append(out, map[string]string{"driver": m.Driver, "vendor": m.Vendor})
	}
	out = append(out,
		map[string]string{"driver": genericSSH.Driver, "vendor": "generique"},
		map[string]string{"driver": genericHTTP.Driver, "vendor": "generique"})
	return out
}

var hostnameRe = regexp.MustCompile(`(?m)^\s*(?:hostname|sysname|system name)\s+"?([A-Za-z0-9_.\-]+)"?`)

// Le separateur « : » ou « = » est OBLIGATOIRE dans ces deux motifs.
//
// Sans lui, « System Serial Number : FOC1841X0AB » capture le mot « Number » :
// l'alternative « system serial » accroche avant « serial number », et comme
// (?i) rend aussi la classe [A-Z0-9] insensible a la casse, le mot suivant fait
// un serial parfaitement credible. Exiger le separateur supprime la classe
// entire de ce default, au prix des rares sorties alignees sans ponctuation —
// where l'on prefers un champ vide a une valeur invented.
var serialRe = regexp.MustCompile(`(?i)serial(?:[ -]?(?:number|no\.?))?\s*[:=]\s*"?([A-Za-z0-9\-]{5,24})`)
var modelRe = regexp.MustCompile(`(?i)(?:model(?:[ -]?(?:number|name))?|hardware(?:[ -]?model)?)\s*[:=]\s*"?([A-Za-z0-9\-/+]{3,32})`)

var hasDigitRe = regexp.MustCompile(`[0-9]`)

// ExtractFacts peche hostname, serial et model dans une sortie de commande.
// Volontairement conservateur : mieux vaut un champ vide qu'une valeur fausse
// qui donnerait a l'utilisateur l'illusion d'une identification sure.
func ExtractFacts(text string) (hostname, serial, model string) {
	var m = hostnameRe.FindStringSubmatch(text)
	if len(m) > 1 {
		hostname = m[1]
	}
	m = serialRe.FindStringSubmatch(text)
	// Un serial number contient au moins un chiffre : ce garde-fou ecarte les
	// mots ordinaires qu'un motif trop permissif ramenerait.
	if len(m) > 1 && hasDigitRe.MatchString(m[1]) {
		serial = strings.ToUpper(m[1])
	}
	m = modelRe.FindStringSubmatch(text)
	if len(m) > 1 {
		model = m[1]
	}
	return hostname, serial, model
}

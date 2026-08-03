package swos

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Les capabilities declarees, et surtout ce que SwOS NE SAIT PAS FAIRE.
//
// Ces tests ne paraphrasent pas une structure : chacun verrouille une declaration
// dont le renversement changerait une decision d'exploitation. Un `Commit` errone fait
// croire a un rollback qui n'existe pas ; un `SerialPort` a vrai fait promettre une
// console de secours a quelqu'un qui devra se deplacer ; une procedure de remise a
// zero presented comme verified alors qu'elle est deduced se paie au pire moment,
// celui where access est already perdu.

// TestCommitImmediatSansFilet : la contrainte la plus lourde de ce connecteur.
//
// L'interface de the device n'a aucun « Save Configuration » : son seul « Discard
// Changes » RELIT la page depuis the device, i.e. n'annule rien de ce qui
// est parti. Ce qui est poste est written, et un reboot ne l'annulera pas.
//
// Basculer cette declaration en `apply_then_save` ferait croire a un rollback gratuit
// qui n'existe pas, et la pose cesserait de prendre la sauvegarde qui est le seul
// filet reel.
func TestCommitImmediatSansFilet(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	if caps.Commit != driver.CommitImmediate {
		t.Errorf("Commit = %q ; SwOS persiste d'un seul geste, il n'y a pas de "+
			"configuration de demarrage distincte et donc aucun retour arriere gratuit",
			caps.Commit)
	}
	// Une seule session : SwOS n'a aucune notion de session, et le parallelisme n'a
	// pas been verified sur du hardware en service.
	if caps.MaxSessions != 1 {
		t.Errorf("MaxSessions = %d ; faute d'avoir pu eprouver le parallelisme, le "+
			"connecteur doit serialiser ses access", caps.MaxSessions)
	}
}

// TestAucuneCLINiConsole : ce qui rend chaque garde-fou necessary.
//
// Sur l'reference device, un seul port TCP repond : le 80. L'adoption par
// console serial — l'une des trois portes ingress de la discovery — est donc
// IMPOSSIBLE, et une erreur d'adressage de management se repare sur site.
func TestAucuneCLINiConsole(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	var f string
	for _, f = range []string{"SSHMgmt", "TelnetMgmt", "SerialCLI", "SerialPort"} {
		if caps.Has(f) {
			t.Errorf("%s declared disponible : sur l'reference device, ni 22, "+
				"ni 23, ni serial port", f)
		}
	}
	if !caps.Has("HTTPMgmt") {
		t.Error("l'interface web est le SEUL chemin d'administration")
	}
	// Le transport declared : « web » ET « snmp », et rien d'autre.
	//
	// CETTE ASSERTION A CHANGED DE FORME, DELIBEREMENT. Elle exigeait `[web]` tout court,
	// avec pour motif « annoncer SNMP sans avoir vu l'agent repondre ferait planifier
	// une collecte qui n'arriverait jamais ». Le motif was juste et sa condition est
	// now REMPLIE : l'agent a repondu, en v2c, sur l'reference device
	// (cf. le observed dans SNMPVersions). Garder l'ancienne assertion aurait fait
	// echouer le test sur un fait verified.
	//
	// Ce que ce test protege vraiment n'was PAS l'absence de SNMP — c'est l'absence
	// de CLI et de console, qui est le fait dur de cette plateforme et la raison de
	// toute sa prudence write. La liste est donc verified par ce qu'elle ne doit
	// PAS contenir, ce qui est plus fidele a l'intention et resistera au prochain
	// canal de lecture qu'on relevera.
	var transport = (&Driver{}).Info().Transport
	var interdits = map[string]string{
		"ssh":    "aucun 22 ouvert sur l'reference device",
		"telnet": "aucun 23 ouvert",
		"serial": "ce model n'a pas de connecteur serial",
		"api":    "SwOS n'a aucune API : ses pages *.b SONT son interface web",
	}
	var tr string
	for _, tr = range transport {
		var pourquoi, interdit = interdits[tr]
		if interdit {
			t.Errorf("Transport annonce %q : %s", tr, pourquoi)
		}
	}
	if !slices.Contains(transport, "web") {
		t.Error("Transport doit annoncer « web » : c'est le seul chemin d'WRITE")
	}
	if !slices.Contains(transport, "snmp") {
		t.Error("Transport doit annoncer « snmp » : l'agent v2c a been observed sur " +
			"l'reference device, et c'est par lui que la collecte passe")
	}
}

// TestSansRoutageNiHorloge : ce que SwOS ne sait pas faire, et qui doit se voir.
func TestSansRoutageNiHorloge(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	var absentes = []string{
		"StaticRouting", // SwOS ne route pas du tout : c'est la difference de fond
		"SNTP",          // aucune horloge, donc aucun horodatage possible
		"LLDP",          // aucun voisinage normalise
		"Dot1X", "RADIUS", "VoiceVLAN", "DHCPRelay", "JumboFrames",
		"LoopProtection", "EEE", "RMON", "ARPProtection", "DoSProtection",
	}
	var f string
	for _, f = range absentes {
		if caps.Has(f) {
			t.Errorf("%s declared disponible alors qu'aucun champ de the device ne "+
				"l'establishes", f)
		}
	}
	var presentes = []string{
		"Trunk8021Q", "PVID", "IGMPSnooping", "LACP", "DHCPSnooping",
		"PortSecurity", "ProtectedPorts", "PortMirroring", "IngressRate",
		"StormControl", "StormControlPort",
	}
	for _, f = range presentes {
		if !caps.Has(f) {
			t.Errorf("%s est established par un champ de the device et devrait be "+
				"declared", f)
		}
	}
	// Le control de storm est PAR PORT sur SwOS (`srt` est un tableau), there where
	// l'Instant On ne sait le faire que globalement. C'est une force de cette
	// plateforme et elle ne doit pas se perdre.
	if !caps.Has("StormControlPort") {
		t.Error("`srt` est un tableau de 26 valeurs : le control de storm est par port")
	}
	// Aucune route, aucune instance MSTP.
	if caps.MaxStaticRoutes != 0 || caps.MSTInstances != 0 {
		t.Errorf("routes = %d, instances MSTP = %d ; SwOS n'a ni l'un ni l'autre",
			caps.MaxStaticRoutes, caps.MSTInstances)
	}
	// RSTP seulement : le champ qui affiche « STP » est en lecture seule, c'est un
	// repli automatique et non un mode qu'on choisit.
	if !reflect.DeepEqual(caps.STPModes, []string{"rstp"}) {
		t.Errorf("STPModes = %v ; l'interface n'offre aucun selecteur de protocole",
			caps.STPModes)
	}
}

// TestResetProcedureHonnete : la procedure de factory reset, et son degre de
// certitude.
//
// `Confirmed` doit be FAUX : la voie logicielle est observed dans le code de
// l'interface de the device, mais le geste HARDWARE vient de la documentation et n'a
// pas been exerce. Comme c'est precisely le geste qu'un operator tentera quand il
// aura perdu access — au pire moment — le present  comme verified serait la faute que
// ce champ existe pour empecher.
func TestResetProcedureHonnete(t *testing.T) {
	var p = (&Driver{}).ResetProcedure("CSS326-24G-2S+")
	if p.Confirmed {
		t.Error("le geste hardware n'a pas been exerce sur ce model : Confirmed doit " +
			"rester faux, et la source doit dire laquelle des deux halves est established")
	}
	if p.Hardware == "" || p.Software == "" {
		t.Error("les deux voies doivent be decrites")
	}
	// Le geste hardware de MikroTik n'est pas une pression longue sur un device
	// allume : il exige de couper puis retablir l'alimentation. Le dire evite un
	// deplacement inutile.
	if !strings.Contains(strings.ToLower(p.Hardware), "power") {
		t.Errorf("le geste hardware doit dire qu'il passe par l'alimentation ; obtenu : %q",
			p.Hardware)
	}
	// La source doit distinguer ce qui est observed de ce qui est deduit.
	var expected string
	for _, expected = range []string{"software", "documentation"} {
		if !strings.Contains(strings.ToLower(p.Source), expected) {
			t.Errorf("la source doit mentionner %q ; obtenu : %q", expected, p.Source)
		}
	}
	// Et le trap qui compte le plus : aucune console de secours.
	var joined = strings.ToLower(strings.Join(p.Pitfalls, " "))
	if !strings.Contains(joined, "serial") {
		t.Errorf("les pieges doivent dire qu'il n'y a pas de serial port : %v", p.Pitfalls)
	}
}

// TestNotesDeManagementPortentLOrdre : la capability la plus dangereuse a supposer.
//
// Se tromper rend the device injoignable, et ce model n'a pas de serial port. Les
// notes doivent porter l'ORDRE des operations et les champs qui coupent access —
// c'est ce qui fait la difference entre un apply qui marche et un deplacement.
func TestNotesDeManagementPortentLOrdre(t *testing.T) {
	var mgmt = (&Driver{}).Capabilities("CSS326-24G-2S+").Mgmt
	if !mgmt.TaggedOK {
		t.Error("le management tagged est OBSERVED sur l'reference device : il est " +
			"administre en VLAN 99 alors que son seul port avec lien a un VLAN natif de 1")
	}
	if mgmt.VlanIDs != driver.VlanAny {
		t.Errorf("VlanIDs = %q ; « Allow From VLAN » est libre", mgmt.VlanIDs)
	}
	// La borne haute est celle de la TABLE des VLAN (4094), pas celle du champ
	// `avln` (4095) : un VLAN de management doit exister dans la table.
	if mgmt.VlanRange != [2]int{1, 4094} {
		t.Errorf("VlanRange = %v, expected [1 4094] — la borne la plus basse des deux",
			mgmt.VlanRange)
	}
	var joined = strings.ToLower(strings.Join(mgmt.Notes, " "))
	var expected = []string{
		"before", // l'ordre des operations
		"allp",   // les champs qui coupent access
		"serial", // pas de console de secours
		"strict", // le mode VLAN qui fait tomber les trames
		"endian", // l'ordre des octets de l'adresse
	}
	var a string
	for _, a = range expected {
		if !strings.Contains(joined, a) {
			t.Errorf("les notes de management doivent parler de %q : c'est une way "+
				"documentee de se couper la main", a)
		}
	}
}

// TestSNMPv2cReleve : l'agent v2c, et le fait qu'il ne soit PAS un v3 deguise.
//
// Ce test remplace un test qui verrouillait l'INVERSE — `SNMPVersions` devait be vide,
// « faute d'avoir vu l'agent repondre ». Le motif was bon et sa condition est levee :
// le walk du proxy, qui parle v2c en dur, a obtenu sysDescr, sysObjectID, sysName et
// ifNumber de l'reference device le 29 juillet 2026. Ce qui est verrouille ici est
// donc le observed, et son EXACTITUDE — annoncer plus que ce qui a repondu serait retomber
// dans la faute que l'ancien test evitait.
func TestSNMPv2cReleve(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	if !reflect.DeepEqual(caps.SNMPVersions, []string{"v2c"}) {
		t.Errorf("SNMPVersions = %v ; seul v2c a been OBSERVED. v1 est probable mais n'a "+
			"jamais repondu a personne — le seul canal d'interrogation du projet est v2c — "+
			"et v3 n'existe pas sur cette plateforme.", caps.SNMPVersions)
	}
	// v3 est une ABSENCE ESTABLISHED par enumeration : /snmp.b tient en quatre champs, et il
	// n'y a ni utilisateur, ni algorithme, ni chiffrement, dans aucun onglet ni dans la
	// sauvegarde de configuration. Declare un algorithme ferait engendrer une identity
	// v3 que rien ne pourrait poser.
	if len(caps.SNMPv3Auth) != 0 || len(caps.SNMPv3Priv) != 0 {
		t.Errorf("SNMPv3Auth = %v, SNMPv3Priv = %v ; SwOS n'a AUCUN v3",
			caps.SNMPv3Auth, caps.SNMPv3Priv)
	}
}

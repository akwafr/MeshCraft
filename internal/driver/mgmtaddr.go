package driver

import "context"

// L'adressage de management TEL QUE THE DEVICE LE PORTE, et l'interface qui le lit.
//
// # Pourquoi ce n'est pas dans [State]
//
// [State] is serialise in `devices.observed` and serves au calcul de derive des ports
// (cf. state.go). Deux reasons de nature differente tiennent l'adressage a l'ecart :
//
//   - un fait de plateforme : l'adressage de management ne se lit pas au same endroit
//     que la configuration des ports — /sys.b contre /vlan.b et /fwd.b sur SwOS,
//     `ip/address` contre `interface/bridge/vlan` sur RouterOS. Une lecture peut
//     reussir when l'other fails, and les fondre en une only structure ferait passer
//     failure de l'une pour l'absence de l'autre ;
//   - un does de produit : ce qui between in `observed` is compare a l'intention and
//     declenche des poses. L'adressage de management, lui, ne se apply NEVER by le
//     chemin de la reconciliation des ports (cf. docs/01-Architecture/Adressage de
//     management §5, « le management ne se touche jamais par le chemin des ports »).
//
// # Ce que ce type serves a decider
//
// Une only chose, and elle is petite : display la verite. Un device en address
// statique et un device en bail DHCP se present  identiquement dans un
// inventaire qui ne observes que l'adresse — et pourtant ce qui se passe le jour where le
// serveur DHCP change does not have rien de commun between les two. It is also ce qui allows a
// meshcraft de CONSERVER une configuration statique trouvee en place rather que de la
// take for un ecart au plan du site.

// MgmtAddrState is l'adressage de management observe on un device.
type MgmtAddrState struct {
	// Mode : « static », « dhcp », ou VIDE quand la plateforme ne le dit pas.
	//
	// Le vide est une valeur de plein droit, et il ne se remplace pas par un default.
	// « On ne sait pas comment cet device a obtenu son adresse » est une response
	// exacte ; « il is en DHCP puisque le plan du site says DHCP » is une deduction
	// qui deviendra fausse sur le premier device statique du parc — et il y en a.
	Mode string `json:"mode,omitempty"`
	// Address porte le PREFIXE when la platform le gives (« 10.99.0.36/24 »), and
	// the address nue sinon. Ce que le connector has lu, never ce that it en deduit.
	Address string `json:"address,omitempty"`
	// Gateway / DNS : vides quand la plateforme n'en porte pas.
	//
	// Un vide ne se lit donc pas tout seul : c'est [MgmtCapabilities.Gateway] et
	// [MgmtCapabilities.DNS] qui disent s'il faut le read « non configure » or
	// « cette platform does not have ce reglage ». Les two menent a des gestes opposes,
	// et un UI qui les confondrait ferait chercher un champ inexistant.
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	// Interface : le nom que the device gives a the interface qui porte cette
	// adresse (« vlan99 », « VLAN1 »). Vide sur les plateformes qui n'en nomment
	// no — un switch SwOS does not have has address and ne l'attache a rien.
	Interface string `json:"interface,omitempty"`
	// Notes porte ce que le connecteur a compris et que les champs ci-dessus ne
	// savent pas dire : une second address trouvee, un mode lu in une value
	// inconnue, une limite de la platform. Ecrites for un operator and affichees
	// telles quelles, comme [State.Notes].
	Notes []string `json:"notes,omitempty"`
}

// Les valeurs de [MgmtAddrState.Mode].
const (
	// MgmtAddrModeStatic : the address is configuree a la main on the device.
	MgmtAddrModeStatic = "static"
	// MgmtAddrModeDHCP : l'adresse vient d'un bail DHCP.
	MgmtAddrModeDHCP = "dhcp"
)

// MgmtAddrReader : le connecteur sait dire COMMENT the device a obtenu son adresse
// d'administration.
//
// FACULTATIF, et en LECTURE SEULE. Un connecteur qui ne l'implements pas n'est pas en
// retard sur les autres : il dit qu'il n'a pas observed, sur cette plateforme, de quoi
// distinguer une adresse statique d'un bail. L'appelant l'apprend AVANT de proposer un
// UI (cf. [NotSupported]), et affiche « mode d'adressage non observed sur cette
// plateforme » au lieu d'un choix qui ne repose sur rien.
//
// Read-only. Writes go through [MgmtAddrWriter] (same VLAN), [MgmtMover] (dual-path
// VLAN migrate), or [MgmtJumper] (single-address leap) — never through this reader.
type MgmtAddrReader interface {
	ReadMgmtAddressing(ctx context.Context) (MgmtAddrState, error)
}

// AsMgmtAddrReader returns the admin as an addressing reader when it is one.
func AsMgmtAddrReader(a Admin) (MgmtAddrReader, bool) {
	var r, ok = a.(MgmtAddrReader)
	return r, ok
}

// MgmtAddrIntent is what meshcraft wants to APPLY on the management interface,
// WITHOUT changing management VLAN.
//
// Distinct from [MgmtMover] (VLAN migrate with dual path) and from [MgmtJumper]
// (single-address leap). Same-VLAN apply still moves the address under the
// commanding session — connectors must prove dual-address or accept an honest
// jump with backup before setting [MgmtCapabilities.ApplySameVLAN].
type MgmtAddrIntent struct {
	// Mode: [MgmtAddrModeStatic] or [MgmtAddrModeDHCP]. Inherit is never applied.
	Mode string `json:"mode"`
	// Address carries PREFIX when static ("10.99.0.36/24"). Empty for DHCP.
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// MgmtAddrResult is what apply left on the device, or refused to do.
type MgmtAddrResult struct {
	// Observed is a re-read AFTER write when the connector could obtain one.
	Observed MgmtAddrState `json:"observed,omitempty"`
	// Notes carry platform facts the caller must surface (partial apply, etc.).
	Notes []string `json:"notes,omitempty"`
	// Backup is a pre-write copy when the platform offers one (SwOS). Empty is
	// normal on dual-path platforms that never needed it for this gesture.
	Backup []byte `json:"-"`
}

// MgmtAddrWriter applies management addressing on the CURRENT management VLAN.
//
// FACULTATIVE. Must NOT change management VLAN — that is [MgmtMover] or
// [MgmtJumper]. Declaring this interface without having exercised the writes on
// hardware is forbidden: flip [MgmtCapabilities.ApplySameVLAN] only after proof.
type MgmtAddrWriter interface {
	ApplyMgmtAddressing(ctx context.Context, want MgmtAddrIntent) (MgmtAddrResult, error)
}

// AsMgmtAddrWriter returns the admin as a same-VLAN addressing writer when it is one.
func AsMgmtAddrWriter(a Admin) (MgmtAddrWriter, bool) {
	var w, ok = a.(MgmtAddrWriter)
	return w, ok
}

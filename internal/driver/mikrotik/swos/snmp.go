package swos

import (
	"context"
	"fmt"
)

// L'agent SNMP de SwOS : ce que la plateforme expose actually, et ce qu'elle
// n'expose pas.
//
// /snmp.b tient en quatre champs, et c'est TOUT ce que SwOS a de SNMP :
//
//	{en:0x01, com:'<community, hex>', ci:'<contact>', loc:'<localisation>'}
//
// Ce que cette page ESTABLISHES, et qui est solide :
//
//   - un agent existe et il est actif sur l'reference device ;
//   - il n'a QU'UNE community, 64 octets au plus (`ga:64` dans l'interface) ;
//   - il n'y a AUCUN SNMPv3 sur cette plateforme. Pas d'utilisateur, pas
//     d'algorithme d'authentification, pas de chiffrement — aucun champ, dans aucun
//     onglet, ni dans la sauvegarde de configuration complete de the device. Ce
//     n'est pas une page masquee comme sur la gamme Instant On : l'interface web
//     entire d'un SwOS tient en un fichier, et elle ne connait pas v3 ;
//   - il n'y a AUCUN destinataire de trap, donc rien a pousser.
//
// Ce que cette page n'establishes PAS, et n'etablira jamais : a quelle version du
// protocole l'agent repond. Il n'y a pas de selecteur de version, et une community
// ne dit pas si l'agent sert du v1, du v2c, ou les deux. Cette page a donc longtemps
// justified un `SNMPVersions` VIDE — non par prudence de principe, mais parce que ces
// listes decident du secret actually pousse et enregistre en base, et qu'un
// identifiant present en base et nulle part ailleurs est pire qu'un identifiant
// absent : the UI le montre.
//
// La response n'est pas venue de la page, elle est venue du network. Le walk du
// 29 juillet 2026 sur 10.99.0.36 a obtenu `sysName.0` = `lan-m6` (le champ `id` de
// /sys.b) et `ifNumber.0` = 26 (le `prt:0x1a` de /link.b) : un agent qui repondrait
// pour un autre hardware ne rendrait ni ce nom-there ni ce compte de ports-there.
// `Capabilities().SNMPVersions` vaut now `["v2c"]` (cf. swos.go). v1 n'est pas
// annonce — probable, jamais vu repondre — et v3 reste l'absence established ci-dessus.
//
// Le code de LECTURE et d'WRITE de la community est ici, verified sur la capture
// reelle de la page. Il avait been written avant d'avoir un emploi, sur l'argument qu'un
// jour la version se leverait d'une ligne ; ce jour est passe, et l'emploi existe.
// Ce qui n'est pas affirme ici : qu'une write ait been exercee sur le hardware de
// reference. Elle ne l'a pas been.

// SNMPConfig est ce que the device porte comme configuration SNMP.
type SNMPConfig struct {
	// Enabled : l'agent repond-il.
	Enabled bool `json:"enabled"`
	// Community : la community, UNE seule. En lecture comme en write — SwOS ne
	// distingue pas lecture et write, il n'y a qu'une string.
	Community string `json:"community"`
	Contact   string `json:"contact,omitempty"`
	Location  string `json:"location,omitempty"`
}

// maxSNMPTextLen borne les champs texte de /snmp.b : 64 octets, la borne que
// l'interface de the device s'impose (`ga:64`). Elle REFUSE au-dela, sans message.
const maxSNMPTextLen = 64

// ReadSNMP lit la configuration SNMP. LECTURE SEULE.
func (a *Admin) ReadSNMP(ctx context.Context) (SNMPConfig, error) {
	var out SNMPConfig
	var page, err = a.Get(ctx, PageSNMP)
	if err != nil {
		return out, err
	}
	return snmpFrom(page)
}

// snmpFrom traduit la page. Fonction pure, donc verifiable sur une capture.
//
// Un champ absent n'est pas une string vide : /snmp.b n'ayant que quatre champs, en
// manquer un veut dire qu'on ne lit pas la page qu'on croit — et une write qui
// suivrait renverrait une community vide, coupant la supervision en silence.
func snmpFrom(page *Page) (SNMPConfig, error) {
	var out SNMPConfig
	var en, ok = page.Num(FieldSNMPEnabled)
	if !ok {
		return out, fmt.Errorf("/snmp.b ne porte pas le champ « %s »", FieldSNMPEnabled)
	}
	out.Enabled = en != 0

	out.Community, ok = page.Text(FieldSNMPCommunity)
	if !ok {
		return out, fmt.Errorf("/snmp.b ne porte pas le champ « %s »", FieldSNMPCommunity)
	}
	// Contact et localisation sont bien present sur le firmware de reference, mais
	// ils ne portent aucune consequence : leur absence ne justifie pas de refuser la
	// lecture.
	out.Contact, _ = page.Text(FieldSNMPContact)
	out.Location, _ = page.Text(FieldSNMPLocation)
	return out, nil
}

// SetCommunity pose la community SNMP.
//
// NON VERIFIED SUR HARDWARE : aucune write n'a pu be menee depuis l'hote de
// developpement (cf. l'encadre de apply.go). Le corps produit est verified sur la
// real capture de la page.
//
// LIRE-MODIFIER-REWRITE, comme toute write sur cette plateforme : la page part
// avec ses quatre champs, contact et localisation compris. Ne renvoyer que la
// community laisserait the device libre d'effacer les deux autres.
//
// L'agent est enabled au passage. Poser une community sur un agent off donnerait
// une supervision configuree et muette, ce qui est le pire des deux mondes : rien ne
// repond, et rien ne dit pourquoi.
func (a *Admin) SetCommunity(ctx context.Context, community string) error {
	if community == "" {
		return fmt.Errorf("community vide refused : elle rendrait l'agent SNMP " +
			"inutilisable sans le desactiver, donc muet sans que rien ne le dise")
	}
	if len(community) > maxSNMPTextLen {
		return fmt.Errorf("community de %d octets : the device en accepte %d au plus, "+
			"et refuse au-dela sans message", len(community), maxSNMPTextLen)
	}
	var page, err = a.Get(ctx, PageSNMP)
	if err != nil {
		return err
	}
	page.SetText(FieldSNMPCommunity, community)
	page.SetNum(FieldSNMPEnabled, 1)

	var fields, ok = WritableFields(PageSNMP)
	if !ok {
		return fmt.Errorf("jeu de champs inscriptible de %s inconnu : write refused",
			PageSNMP)
	}
	var body *Page
	var missing []string
	body, missing = page.Only(fields)
	if len(missing) > 0 {
		var e = fmt.Errorf("/snmp.b : ce firmware ne sert pas les champs %v attendus par "+
			"le connecteur — write refused, renvoyer la page sans eux laisserait "+
			"the device les effacer", missing)
		a.step(false, "jeu de champs de /snmp.b inattendu", e.Error())
		return e
	}
	// La community ne part PAS dans le transcript : c'est un secret, et un
	// transcript est fait pour be lu et conserve.
	return a.postRawSecret(ctx, PageSNMP, body.Encode(),
		"community SNMP applied et agent enabled")
}

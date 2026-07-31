package discovery

import (
	"errors"
	"fmt"
	"net/netip"
)

// DECLARER une plage et savoir la BALAYER sont deux questions distinctes.
//
// On accepte de declare ce qu'un operator a actually dans son network — un
// /16 IPv4, un /64 IPv6 — parce que c'est la verite de son adressage. Ce qui est
// borne, c'est l'ENUMERATION : elle a un cost network et, en IPv6, elle n'a
// arithmetiquement pas de sens. La strategie de discovery se choisit donc au
// moment du balayage, pas a la saisie.

// MaxHosts borne enumeration d'une plage IPv4 : un /16 (65 536 adresses) est
// le plus large qu'on parcourt. Au-dela, c'est un balayage qui se compte en
// heures, et ca se fait par tranches declarees.
const MaxHosts = 65536

// MinIPv6Bits : au-dela (prefix plus large qu'un /112), on n'enumerates pas.
//
// Ce n'est pas une limite d'implementation mais une realite arithmetique : un
// /64 contient 1,8·10¹⁹ adresses. Un tel prefix reste DECLARABLE — c'est le
// sous-network reel du site — mais sa discovery passera par le voisinage (NDP,
// multicast all-nodes) et non par enumeration. Tant que le proxy ne remonte pas
// sa table de voisinage, un balayage sur un tel prefix est refused avec sa
// raison, ce qui vaut mieux que de le lancer et de ne rien trouver.
const MinIPv6Bits = 112

var ErrPrefixTooLarge = errors.New("plage trop large")

// ErrNotEnumerable : la plage est valide et declarable, mais on ne la parcourt
// pas adresse par adresse.
var ErrNotEnumerable = errors.New("plage non enumerable")

// ValidPrefix verifies qu'une plage est DECLARABLE : format correct, rien de plus.
// C'est ce que la saisie control.
func ValidPrefix(cidr string) error {
	var _, err = NormalizePrefix(cidr)
	return err
}

// Enumerable dit si la plage peut be parcourue adresse par adresse, et sinon
// pourquoi. L'appelant s'en sert pour refuser un balayage avec une raison
// lisible, rather que de lancer l'impossible.
func Enumerable(cidr string) error {
	var p, err = netip.ParsePrefix(cidr)
	if err != nil {
		// Une adresse seule est toujours enumerable.
		var _, addrErr = netip.ParseAddr(cidr)
		return addrErr
	}
	p = p.Masked()
	if p.Addr().Is6() && !p.Addr().Is4In6() && p.Bits() < MinIPv6Bits {
		return fmt.Errorf(
			"%w : %s. En IPv6 enumeration n'a pas de sens (un /64 = 1,8·10¹⁹ adresses). "+
				"La discovery de ce prefix passera par la table de voisinage du proxy "+
				"(NDP), qui n'est pas encore livree : en attendant, declarez un prefix "+
				"plus etroit (/112 ou plus specific) ou ajoutez the device par son adresse",
			ErrNotEnumerable, cidr)
	}
	var bits = p.Addr().BitLen() - p.Bits()
	if bits > 16 || 1<<uint(bits) > MaxHosts {
		return fmt.Errorf("%w : %s produirait plus de %d adresses ; decoupez-la en tranches",
			ErrPrefixTooLarge, cidr, MaxHosts)
	}
	return nil
}

// ExpandPrefix enumerates les adresses a sonder pour une plage CIDR.
//
// En IPv4 l'adresse de network et celle de diffusion sont exclues from que le
// masque est plus large qu'un /31.
func ExpandPrefix(cidr string) ([]string, error) {
	var p, err = netip.ParsePrefix(cidr)
	if err != nil {
		// Une adresse seule est accepted : c'est le cas de l'ajout manuel.
		var addr netip.Addr
		addr, err = netip.ParseAddr(cidr)
		if err != nil {
			return nil, fmt.Errorf("prefix invalide : %s", cidr)
		}
		return []string{addr.String()}, nil
	}
	p = p.Masked()

	if p.Addr().Is6() && !p.Addr().Is4In6() {
		if p.Bits() < MinIPv6Bits {
			return nil, fmt.Errorf(
				"%w : %s. En IPv6 enumeration n'a pas de sens au-dela d'un /%d "+
					"(un /64 = 1,8·10¹⁹ adresses). Declarez un prefix plus specific "+
					"ou ajoutez the device par son adresse",
				ErrPrefixTooLarge, cidr, MinIPv6Bits)
		}
	}

	var bits = p.Addr().BitLen() - p.Bits()
	if bits > 16 || 1<<uint(bits) > MaxHosts {
		return nil, fmt.Errorf("%w : %s produirait plus de %d adresses",
			ErrPrefixTooLarge, cidr, MaxHosts)
	}

	var total = 1 << uint(bits)
	var skipEdges = p.Addr().Is4() && total > 2
	var out = make([]string, 0, total)
	var cur = p.Addr()
	var i int
	for i = 0; i < total; i++ {
		if !(skipEdges && (i == 0 || i == total-1)) {
			out = append(out, cur.String())
		}
		cur = cur.Next()
		if !cur.IsValid() {
			break
		}
	}
	return out, nil
}

// NormalizePrefix valide une saisie utilisateur et la renvoie sous forme
// canonique (network masque), pour eviter les doublons 10.0.0.5/24 vs 10.0.0.0/24.
func NormalizePrefix(cidr string) (string, error) {
	var p, err = netip.ParsePrefix(cidr)
	if err == nil {
		return p.Masked().String(), nil
	}
	var addr netip.Addr
	addr, err = netip.ParseAddr(cidr)
	if err != nil {
		return "", fmt.Errorf("prefix ou adresse invalide : %s", cidr)
	}
	return addr.String(), nil
}

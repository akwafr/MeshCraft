package driver

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// Pre-vol de joignabilite : est-ce qu'un proxy atteint vraiment un network ?
//
// C'est la verification qu'exige la bascule d'adressage de management
// (docs/01-Architecture/Adressage de management §5) : envoyer un device dans
// un sous-network que personne ne joint, c'est le perdre.
//
// Le point subtil, et c'est tout l'interet de ce code : **un port closed est une
// bonne nouvelle**. Un `connection refused` signifie que le paquet est arrive et
// que l'hote a repondu par un RST — donc la route existe. Un `timeout`, lui, ne
// distingue pas « pas de route » de « filtre en silence ». Confondre les deux
// ferait echouer un pre-vol parfaitement valide.

// ReachOutcome : ce qu'une tentative de connexion apprend.
type ReachOutcome string

const (
	// ReachOpen : quelque chose ecoute. L'hote existe.
	ReachOpen ReachOutcome = "open"
	// ReachRefused : l'hote a repondu RST. Il existe, la route existe.
	ReachRefused ReachOutcome = "refused"
	// ReachUnreachable : ICMP unreachable — the device network a repondu que
	// personne n'est there. La route existe, l'hote non.
	ReachUnreachable ReachOutcome = "unreachable"
	// ReachTimeout : silence. Pas de route, ou filtrage muet — indecidable.
	ReachTimeout ReachOutcome = "timeout"
)

// ReachResult describes le result d'un pre-vol vers un hote.
//
// DEUX questions distinctes, qu'il ne faut pas confondre — et c'est un trap dans
// lequel ce code est tombe avant d'be corrige :
//
//	Reachable   : l'hote LUI-SAME a repondu (open ou refused).
//	RouteExists : un device du chemin a repondu, same pour dire que l'hote
//	              est absent (ICMP unreachable). Le network est route ; l'hote,
//	              non. Utile pour valider un sous-network, jamais pour conclure
//	              qu'une adresse repond.
type ReachResult struct {
	Host string `json:"host"`
	// Probes : le detail par port, pour que le diagnostic soit lisible.
	Probes      map[string]ReachOutcome `json:"probes"`
	Reachable   bool                    `json:"reachable"`
	RouteExists bool                    `json:"route_exists"`
	// Verdict est la phrase a afficher : elle doit suffire a decider.
	Verdict string `json:"verdict"`
}

// ReachPorts : des ports courants sur une passerelle ou un device network. On
// n'en attend aucun ouvert — seule la NATURE de failure compte.
var ReachPorts = []int{443, 80, 22, 179}

// CheckReach teste la joignabilite d'un hote depuis le dialer fourni (donc
// depuis le proxy, quand c'est celui d'un tunnel).
func CheckReach(ctx context.Context, host string, d Dialer, timeout time.Duration) ReachResult {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	var res = ReachResult{Host: host, Probes: map[string]ReachOutcome{}}
	var port int
	for _, port = range ReachPorts {
		var outcome = probeReach(ctx, host, port, d, timeout)
		res.Probes[strconv.Itoa(port)] = outcome
		switch outcome {
		case ReachOpen, ReachRefused:
			// L'hote a parle : presence et route established.
			res.Reachable = true
			res.RouteExists = true
		case ReachUnreachable:
			// Un routeur a parle A LA PLACE de l'hote : la route existe, pas l'hote.
			res.RouteExists = true
		}
		// Une response de l'hote suffit ; un ICMP unreachable, non — on continue
		// d'essayer les autres ports au cas where seul celui-ci soit filtre.
		if res.Reachable {
			break
		}
	}
	switch {
	case res.Reachable:
		res.Verdict = "hote joignable depuis ce proxy : il a repondu, la route existe"
	case res.RouteExists:
		res.Verdict = "un device du chemin a repondu que cet hote est absent " +
			"(ICMP unreachable) : le network est route depuis ce proxy, mais rien ne " +
			"repond a cette adresse"
	default:
		res.Verdict = "aucune response : soit il n'y a pas de route depuis ce proxy, " +
			"soit le trafic est filtre en silence. Impossible de conclure que le network " +
			"est joignable."
	}
	return res
}

func probeReach(ctx context.Context, host string, port int, d Dialer, timeout time.Duration) ReachOutcome {
	var dctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()

	var conn net.Conn
	var err error
	var addr = net.JoinHostPort(host, strconv.Itoa(port))
	if d != nil {
		conn, err = d.DialContext(dctx, "tcp", addr)
	} else {
		var dialer = net.Dialer{Timeout: timeout}
		conn, err = dialer.DialContext(dctx, "tcp", addr)
	}
	if err == nil {
		_ = conn.Close()
		return ReachOpen
	}
	return classify(err)
}

// classify traduit une erreur de connexion en verdict. Le tunnel remonte le
// message du proxy sous forme de texte : on reconnait donc aussi les labels,
// pas seulement les erreurs typees.
func classify(err error) ReachOutcome {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ReachRefused
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ReachUnreachable
	}
	var msg = err.Error()
	switch {
	case contains(msg, "refused"):
		return ReachRefused
	case contains(msg, "unreachable"):
		return ReachUnreachable
	}
	return ReachTimeout
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	var n = len(needle)
	var i int
	for i = 0; i+n <= len(haystack); i++ {
		if haystack[i:i+n] == needle {
			return i
		}
	}
	return -1
}

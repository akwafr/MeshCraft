package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"meshcraft/internal/discovery"
	"meshcraft/internal/tunnel"
)

// Surveillance locale : c'est le PROXY qui pingue.
//
// Le controleur lui envoie la liste des devices a surveiller et les plages a
// balayer ; le proxy pingue en continu et ne remonte que les **transitions**. Un
// aller-retour par seconde et par device a travers le WAN livrerait la same
// information pour un cost sans rapport — et le proxy est l'executeur local par
// construction (docs/01-Architecture/Proxy).
//
// ICMP demande une socket brute, donc CAP_NET_RAW : already accordee par le
// docker-compose genere comme par unit systemd. Sans elle, la surveillance se
// desactive proprement en le disant, rather que de faire semblant.

// Monitor tient state de surveillance du proxy.
type Monitor struct {
	mu       sync.Mutex
	cfg      tunnel.WatchConfig
	states   map[string]*targetState // key : IP
	events   chan tunnel.Event
	sock     *icmp.PacketConn
	echoID   int
	seq      int
	pending  map[string]time.Time // IP → date d'envoi du dernier echo
	sweeping bool
}

type targetState struct {
	deviceID string
	name     string
	// up : dernier etat CONNU. nil = aucun avis encore.
	//
	// Ce nil n'est PAS silencieux, et le commentaire qui vivait ici pretendait le
	// contraire (« evite d'annoncer une transition au tout premier ping ») : `onReply`
	// comme `evaluate` traitent un nil comme une bascule et l'annoncent, ce qui est le
	// comportement voulu — un device qu'on vient d'ajouter a la surveillance doit
	// produire un premier constat, sans quoi son statut resterait `unknown` jusqu'a sa
	// first panne. C'est exactement l'invariant sur lequel on raisonne pour
	// diagnostiquer un statut manquant, donc il valait d'be dit juste.
	//
	// Ce qu'un nil evite est autre chose : conclure avant d'avoir pinged. Une cible qui
	// n'a pas encore been interrogee n'est ni en ligne ni injoignable, et `Snapshot` la
	// passe pour cette raison.
	up     *bool
	misses int
}

func NewMonitor() *Monitor {
	return &Monitor{
		states:  map[string]*targetState{},
		events:  make(chan tunnel.Event, 256),
		pending: map[string]time.Time{},
		echoID:  os.Getpid() & 0xffff,
	}
}

// Events est le flux a pousser au controleur.
func (m *Monitor) Events() <-chan tunnel.Event { return m.events }

// emit depose un evenement sans jamais bloquer : un controleur injoignable ne
// doit pas figer la surveillance.
func (m *Monitor) emit(ev tunnel.Event) {
	ev.TS = time.Now().UTC()
	select {
	case m.events <- ev:
	default:
		slog.Warn("file d'evenements pleine — evenement abandonne", "type", ev.Type)
	}
}

// SetConfig remplace la configuration de surveillance. Une config complete
// remplace la previous : rien a reconcilier, donc aucune divergence possible.
func (m *Monitor) SetConfig(cfg tunnel.WatchConfig) {
	// La collecte SNMP suit la same config : une seule source de verite cote
	// controleur, donc aucune divergence possible entre ce qui est pinged et ce
	// qui est interroge.
	if snmpCollector != nil {
		snmpCollector.SetWatches(cfg.SNMP)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	var seen = map[string]bool{}
	var t tunnel.WatchTarget
	for _, t = range cfg.Targets {
		if t.IP == "" {
			continue
		}
		seen[t.IP] = true
		var st, exists = m.states[t.IP]
		if !exists {
			st = &targetState{}
			m.states[t.IP] = st
		}
		st.deviceID, st.name = t.DeviceID, t.Name
	}
	// Une cible retiree de la liste cesse d'be surveillee, sans evenement : ce
	// n'est pas une panne, c'est une decision d'exploitation.
	var ip string
	for ip = range m.states {
		if !seen[ip] {
			delete(m.states, ip)
		}
	}
	slog.Info("surveillance configuree", "devices", len(cfg.Targets),
		"plages", len(cfg.Prefixes))
}

// Snapshot rend state COURANT de chaque cible surveillee, sous forme d'evenements
// marques `Resync`.
//
// POURQUOI CE N'EST PAS REDONDANT AVEC LES TRANSITIONS. Le proxy ne remonte que des
// bascules, et une bascule perdue ne se rejoue jamais : la file de 256 entries jette
// quand elle est pleine, l'evenement en cours write est perdu si le tunnel tombe
// a cet instant, et une reconnexion ne reannonce rien puisque `SetConfig` conserve
// state memory des cibles already connues. Constate sur un CSS326 reste `unknown`
// dix-neuf heures alors qu'il repondait au ping sans faute : son unique transition
// avait disparu dans une coupure, et seule une panne reelle — un reboot manuel —
// a fini par rewrite son statut. Le cliche ferme ce trou par construction, sans rien
// changer au regime permanent : il ne part only l'ouverture du flux.
//
// Ce qui est PASSE, et pourquoi :
//
//   - une cible sans avis (`up == nil`) n'a rien a dire. Elle n'a pas encore been
//     pingee, et annoncer « en ligne » ou « injoignable » serait inventer un constat
//     que personne n'a fait ;
//   - une cible sans `deviceID` ne concerne aucun device connu du controleur, qui
//     l'ignorerait de toute way (cf. applyDeviceState).
//
// L'ordre est STABLE, par adresse. Deux cliches du same etat doivent produire la same
// suite d'octets : c'est ce qui rend un flux de reconnexion comparable d'une fois sur
// l'autre, et un test reproductible.
func (m *Monitor) Snapshot() []tunnel.Event {
	m.mu.Lock()
	var ips = make([]string, 0, len(m.states))
	var ip string
	for ip = range m.states {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	var now = time.Now().UTC()
	var out = make([]tunnel.Event, 0, len(ips))
	for _, ip = range ips {
		var st = m.states[ip]
		if st.up == nil || st.deviceID == "" {
			continue
		}
		var ev = tunnel.Event{
			Type: tunnel.EventDeviceUp, TS: now, DeviceID: st.deviceID, IP: ip,
			Detail: st.name, Resync: true,
		}
		if !*st.up {
			ev.Type = tunnel.EventDeviceDown
			ev.Misses = st.misses
		}
		out = append(out, ev)
	}
	m.mu.Unlock()
	return out
}

// Run demarre la surveillance. Ne rend la main only l'arret du contexte.
func (m *Monitor) Run(ctx context.Context) {
	var conn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		// Sans CAP_NET_RAW, on le dit clairement au lieu de rester muet.
		slog.Error("surveillance ICMP indisponible : socket brute refused "+
			"(CAP_NET_RAW manquante ?) — state des devices ne sera pas suivi",
			"err", err)
		return
	}
	m.sock = conn
	defer conn.Close() //nolint:errcheck
	slog.Info("surveillance ICMP active")

	go m.readReplies(ctx)
	go m.sweepLoop(ctx)

	var interval = m.pingInterval()
	var ticker = time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pingRound()
		}
	}
}

func (m *Monitor) pingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.PingIntervalMS > 0 {
		return time.Duration(m.cfg.PingIntervalMS) * time.Millisecond
	}
	return tunnel.DefaultPingInterval
}

func (m *Monitor) downAfter() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.DownAfter > 0 {
		return m.cfg.DownAfter
	}
	return tunnel.DefaultDownAfter
}

// pingRound envoie un echo a chaque cible, puis juge celles qui n'ont pas
// repondu depuis assez longtemps.
func (m *Monitor) pingRound() {
	m.mu.Lock()
	var targets = make([]string, 0, len(m.states))
	var ip string
	for ip = range m.states {
		targets = append(targets, ip)
	}
	m.mu.Unlock()

	for _, ip = range targets {
		m.sendEcho(ip)
	}

	// On laisse le temps aux responses d'arriver avant de conclure.
	time.AfterFunc(m.pingInterval()*3/4, m.evaluate)
}

func (m *Monitor) sendEcho(ip string) {
	var addr = net.ParseIP(ip)
	if addr == nil || addr.To4() == nil {
		return // IPv6 : a traiter avec ip6:ipv6-icmp, pas encore livre
	}
	m.mu.Lock()
	m.seq = (m.seq + 1) & 0xffff
	var seq = m.seq
	m.pending[ip] = time.Now()
	m.mu.Unlock()

	var msg = icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: m.echoID, Seq: seq, Data: []byte("meshcraft")},
	}
	var data, err = msg.Marshal(nil)
	if err != nil {
		return
	}
	_, _ = m.sock.WriteTo(data, &net.IPAddr{IP: addr.To4()})
}

// readReplies lit les responses en continu. Une seule socket pour tout le parc :
// surveiller cent devices costs cent paquets par seconde, pas cent sockets.
func (m *Monitor) readReplies(ctx context.Context) {
	var buf = make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = m.sock.SetReadDeadline(time.Now().Add(2 * time.Second))
		var n, peer, err = m.sock.ReadFrom(buf)
		if err != nil {
			continue // timeout de lecture : normal
		}
		var msg, parseErr = icmp.ParseMessage(1, buf[:n])
		if parseErr != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		var echo, ok = msg.Body.(*icmp.Echo)
		if !ok || echo.ID != m.echoID {
			continue // la response d'un autre processus
		}
		m.onReply(peer.String())
	}
}

func (m *Monitor) onReply(ip string) {
	m.mu.Lock()
	var sentAt, wasPending = m.pending[ip]
	delete(m.pending, ip)
	var st, watched = m.states[ip]
	var latency int
	if wasPending {
		latency = int(time.Since(sentAt).Milliseconds())
	}
	if !watched {
		// Reponse d'une adresse qu'on balaye, pas d'un device suivi.
		m.mu.Unlock()
		m.noteSweepHit(ip)
		return
	}
	var wasUp = st.up
	st.misses = 0
	var up = true
	st.up = &up
	var deviceID, name = st.deviceID, st.name
	m.mu.Unlock()

	// TRANSITION seulement : un device qui va bien depuis une heure n'a rien
	// a dire chaque seconde.
	if wasUp == nil || !*wasUp {
		m.emit(tunnel.Event{
			Type: tunnel.EventDeviceUp, DeviceID: deviceID, IP: ip,
			LatencyMS: latency, Detail: name,
		})
	}
}

// evaluate declares injoignables les cibles silencieuses depuis assez de rondes.
func (m *Monitor) evaluate() {
	var limit = m.downAfter()
	type fallen struct {
		deviceID, ip, name string
		misses             int
	}
	var down []fallen

	m.mu.Lock()
	var ip string
	var st *targetState
	for ip, st = range m.states {
		var _, stillPending = m.pending[ip]
		if !stillPending {
			continue // une response est arrivee
		}
		delete(m.pending, ip)
		st.misses++
		if st.misses < limit {
			continue
		}
		if st.up != nil && !*st.up {
			continue // already annonce
		}
		var no = false
		st.up = &no
		down = append(down, fallen{st.deviceID, ip, st.name, st.misses})
	}
	m.mu.Unlock()

	var f fallen
	for _, f = range down {
		m.emit(tunnel.Event{
			Type: tunnel.EventDeviceDown, DeviceID: f.deviceID, IP: f.ip,
			Misses: f.misses, Detail: f.name,
		})
	}
}

// --- Balayage continu des plages locales ---

var sweepHits = struct {
	sync.Mutex
	seen map[string]bool
}{seen: map[string]bool{}}

func (m *Monitor) noteSweepHit(ip string) {
	sweepHits.Lock()
	defer sweepHits.Unlock()
	if sweepHits.seen == nil {
		sweepHits.seen = map[string]bool{}
	}
	sweepHits.seen[ip] = true
}

// sweepLoop balaye en continu les plages confiees au proxy. Le rythme est lent
// par construction : decouvrir un device nouveau n'est pas urgent, et un
// balayage permanent a plein regime ressemblerait a une attaque.
func (m *Monitor) sweepLoop(ctx context.Context) {
	var period = tunnel.DefaultSweepInterval
	m.mu.Lock()
	if m.cfg.SweepIntervalS > 0 {
		period = time.Duration(m.cfg.SweepIntervalS) * time.Second
	}
	m.mu.Unlock()

	var ticker = time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepOnce(ctx)
		}
	}
}

func (m *Monitor) sweepOnce(ctx context.Context) {
	m.mu.Lock()
	if m.sweeping {
		m.mu.Unlock()
		return
	}
	m.sweeping = true
	var prefixes = append([]string{}, m.cfg.Prefixes...)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.sweeping = false
		m.mu.Unlock()
	}()

	var prefix string
	for _, prefix = range prefixes {
		if ctx.Err() != nil {
			return
		}
		var addrs, err = discovery.ExpandPrefix(prefix)
		if err != nil {
			m.emit(tunnel.Event{Type: tunnel.EventSweepDone, Prefix: prefix,
				Detail: "non enumerable : " + err.Error()})
			continue
		}
		sweepHits.Lock()
		sweepHits.seen = map[string]bool{}
		sweepHits.Unlock()

		// Cadence maitrisee : 200 echos/s suffisent a couvrir un /24 en une
		// seconde et un /16 en cinq minutes, sans saturer un lien lent.
		var ip string
		var sent int
		for _, ip = range addrs {
			if ctx.Err() != nil {
				return
			}
			m.sendEcho(ip)
			sent++
			if sent%200 == 0 {
				time.Sleep(time.Second)
			}
		}
		// Laisser rentrer les dernieres responses.
		time.Sleep(2 * time.Second)

		sweepHits.Lock()
		var found = make([]string, 0, len(sweepHits.seen))
		for ip = range sweepHits.seen {
			found = append(found, ip)
		}
		sweepHits.Unlock()

		for _, ip = range found {
			m.emit(tunnel.Event{Type: tunnel.EventHostSeen, IP: ip, Prefix: prefix})
		}
		m.emit(tunnel.Event{Type: tunnel.EventSweepDone, Prefix: prefix,
			Scanned: len(addrs), Found: len(found)})
	}
}

// --- Cablage avec le tunnel ---

// handleWatchStream lit les configurations de surveillance poussees par le
// controleur sur un stream dedie.
func (m *Monitor) handleWatchStream(dec *json.Decoder) error {
	for {
		var cfg tunnel.WatchConfig
		var err = dec.Decode(&cfg)
		if err != nil {
			return err
		}
		m.SetConfig(cfg)
	}
}

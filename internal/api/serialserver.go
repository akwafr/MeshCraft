package api

// SHARED SERIAL LINES: one operator's Web Serial port, reachable by another.
//
// # The problem this solves
//
// Web Serial only works in the browser that is physically cabled to the switch.
// That is the right security model and it is also a hard operational limit: the
// person holding the console cable is not always the person who knows what to
// type, and a switch being provisioned over serial is usually a switch that
// cannot be reached any other way — that is why somebody is on a console cable
// in the first place.
//
// So this relays a serial line through the controller: a HOST browser publishes
// the port it already has open, and another operator ATTACHES to it. The
// controller never touches a serial device; it moves bytes between two
// WebSockets and remembers who is offering what.
//
// # THIS IS A REMOTE SHELL ON A DEVICE, AND IT IS TREATED AS ONE
//
// A serial console is the most privileged access a switch has. It survives a
// wrong VLAN, a wrong address and a lost password; on most of this fleet it is
// how a factory reset is performed. Publishing one is therefore not a
// convenience toggle, and the rules below are not ceremony:
//
//   - IT IS NEVER IMPLICIT. A host publishes only by pressing a button, per
//     session. Nothing auto-publishes, nothing persists across a reload, and
//     closing the tab ends it — the registry is in memory precisely so that a
//     controller restart cannot resurrect an offer nobody is standing next to;
//   - IT IS SITE-SCOPED AND ROLE-GATED. Publishing and attaching both require
//     `site_admin` on the site the host names, the same bar the device console
//     already sets. A shared console is not a lesser thing than a direct one;
//   - IT IS AUDITED ON BOTH ENDS, at warning severity, with both operators
//     named. A shell somebody else opened through your cable must be findable
//     afterwards, beside the configuration changes it may explain;
//   - ONE ATTACHER AT A TIME. Two people typing into one console interleave
//     their keystrokes into a single command line, which on a device being
//     factory-reset is a genuinely dangerous way to discover you are not alone.
//
// # What the controller deliberately does NOT do
//
// It does not buffer, replay or record the stream. A serial console carries
// passwords being typed — that is what provisioning IS — and a relay that kept
// scrollback would turn a transient shell into a stored credential. The existing
// Web Serial wizard transcripts (serial_log.go) are a separate, deliberate,
// opt-in mechanism with its own redaction; this path stores nothing at all.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

const (
	// serialOfferTTL bounds how long an offer survives without a sign of life
	// from its host.
	//
	// # WHAT "A SIGN OF LIFE" MUST NOT MEAN
	//
	// It used to mean "bytes arrived from the device", because the stamp was
	// refreshed in the relay's read loop and nowhere else. That is wrong in the
	// most ordinary case there is: a serial console sitting at a prompt sends
	// NOTHING. So a healthy, connected, idle line disappeared from the list after
	// two minutes — the host's browser still open, the socket still up, the
	// switch still cabled — and the only symptom was another operator not
	// finding it.
	//
	// Liveness is now the SOCKET, proven by a keepalive ping (see
	// [keepSerialAlive]), which is the thing the offer actually depends on.
	serialOfferTTL = 2 * time.Minute
	// serialPingEvery refreshes the stamp and proves the socket. Comfortably
	// inside the TTL so one lost ping does not reap a live line.
	serialPingEvery = 30 * time.Second
	// serialReadLimit bounds one frame. Console traffic is keystrokes and screen
	// paint; anything larger is not a terminal.
	serialReadLimit = 1 << 20
	// serialMaxLifetime caps a published offer. A console left open for a day is
	// an access nobody is watching any more.
	serialMaxLifetime = 4 * time.Hour
)

// serialOffer is one published line.
type serialOffer struct {
	ID     string `json:"id"`
	SiteID string `json:"site_id"`
	// Label is what the host typed: which switch, which rack. Free text, shown to
	// whoever is choosing a line to attach to, and the only thing distinguishing
	// two offers from the same operator.
	Label string `json:"label"`
	// HostUser is the operator publishing. Displayed, and audited.
	HostUser string    `json:"host_user"`
	Baud     int       `json:"baud,omitempty"`
	Since    time.Time `json:"since"`
	// AttachedBy is the operator currently typing, empty when nobody is. ONE at a
	// time — see the file header.
	AttachedBy string `json:"attached_by,omitempty"`

	// mu guards everything below, which is the live plumbing rather than the
	// description above.
	mu sync.Mutex
	// host is the publisher's socket. Nil once it goes away.
	host *websocket.Conn
	// client is the attacher's socket, nil when nobody is attached.
	client *websocket.Conn
	seen   time.Time
	// sawHost / sawClient: a frame has arrived from that side at least once.
	// Diagnostic only — see relaySerial.
	sawHost   bool
	sawClient bool
}

// serialRegistry holds the offers. IN MEMORY, deliberately: an offer is only
// meaningful while a browser is holding a serial port open, so it must not
// outlive the process that holds the socket. Persisting it would advertise
// consoles that cannot be reached.
type serialRegistry struct {
	mu     sync.Mutex
	offers map[string]*serialOffer
}

func (s *Server) serialOffers() *serialRegistry {
	s.serialOnce.Do(func() {
		s.serialReg = &serialRegistry{offers: map[string]*serialOffer{}}
	})
	return s.serialReg
}

// list returns the live offers for a site, oldest first.
func (g *serialRegistry) list(siteID string) []*serialOffer {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*serialOffer
	var now = time.Now()
	var id string
	var o *serialOffer
	for id, o = range g.offers {
		if now.Sub(o.seen) > serialOfferTTL {
			// Reaped on read rather than by a timer: the only thing that cares is
			// somebody listing, and a goroutine per offer would be a lot of
			// machinery for a map that is usually empty.
			delete(g.offers, id)
			continue
		}
		if o.SiteID == siteID {
			out = append(out, o)
		}
	}
	return out
}

func (g *serialRegistry) get(id string) (*serialOffer, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var o, ok = g.offers[id]
	return o, ok
}

func (g *serialRegistry) put(o *serialOffer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.offers[o.ID] = o
}

func (g *serialRegistry) drop(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.offers, id)
}

// emitSiteEvent journals a site-level fact. The device-scoped sibling lives in
// devicecreds_handlers.go; a published serial line belongs to no single device —
// the host names it with free text and may not have adopted it yet, which is
// often exactly why a console cable is plugged in.
func (s *Server) emitSiteEvent(r *http.Request, siteID, evType string, sev int, msg string) {
	var sid = siteID
	s.emit(r, model.Event{
		SiteID: &sid, Type: evType, Severity: sev, Message: msg,
	})
}

// newSerialOfferID mints the handle an attacher names.
//
// UNGUESSABLE, although the authorization does not rest on it: `site_admin` on
// the offer's own site is checked before the id is even looked at (cf.
// handleAttachSerialServer). The randomness is defence in depth for a handle
// that will appear in a URL and therefore in a fronting proxy's access log.
func newSerialOfferID() string {
	var b = make([]byte, 16)
	var _, err = rand.Read(b)
	if err != nil {
		// crypto/rand failing is not a condition this can paper over with a
		// counter: a predictable id in a log is exactly what the randomness is
		// for. A timestamp-shaped fallback would be worse than a refusal, so this
		// returns empty and the caller's offer is never findable.
		return ""
	}
	return hex.EncodeToString(b)
}

// handleListSerialServers answers "which lines can I attach to?".
//
// `site_admin`, the same bar as attaching. Listing is not harmless here: the
// label names a switch somebody is standing in front of with a console cable,
// which is operational information about work in progress.
func (s *Server) handleListSerialServers(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var out = s.serialOffers().list(siteID)
	if out == nil {
		out = []*serialOffer{}
	}
	respond(w, http.StatusOK, map[string]any{"servers": out})
}

// handleHostSerialServer is the PUBLISHING half: the browser that holds the
// serial port keeps this socket open for as long as it offers the line.
//
// The socket IS the offer. There is no separate "unregister" call to forget, no
// row to clean up, and a host that crashes stops offering by the same mechanism
// as one that presses the button again — which is the property that makes this
// safe to leave running.
func (s *Server) handleHostSerialServer(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var me = auth.UserFrom(r.Context())
	var label = r.URL.Query().Get("label")
	if label == "" {
		label = "serial line"
	}
	// The DETECTED line speed, so whoever attaches knows what they are typing
	// into. Advisory only — the host owns the port and the controller relays
	// bytes; a wrong number here misleads a reader, it does not misconfigure
	// anything.
	var baud int
	var b, convErr = strconv.Atoi(r.URL.Query().Get("baud"))
	if convErr == nil && b > 0 {
		baud = b
	}

	var wsConn, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin checking left ON — same reason the device console leaves it on:
		// the alternative hands a privileged shell to any tab the operator has
		// open. Compression off: keystrokes gain nothing and buffer badly.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept already answered
	}
	wsConn.SetReadLimit(serialReadLimit)

	var offer = &serialOffer{
		ID:       newSerialOfferID(),
		SiteID:   siteID,
		Label:    label,
		HostUser: me.Email,
		Baud:     baud,
		Since:    time.Now().UTC(),
		host:     wsConn,
		seen:     time.Now(),
	}
	s.serialOffers().put(offer)
	// AUDITED. Offering a console is a grant of the most privileged access a
	// switch has, and it must be findable afterwards.
	s.emitSiteEvent(r, siteID, "serial.published", model.SevWarning,
		"Serial line \""+label+"\" published by "+me.Email+
			" — any site administrator can now type on it")

	var ctx, cancel = context.WithTimeout(r.Context(), serialMaxLifetime)
	defer cancel()

	// Liveness, independent of whether the device is saying anything. See
	// [serialOfferTTL] for the bug this fixes.
	go keepSerialAlive(ctx, offer, wsConn)
	defer func() {
		s.serialOffers().drop(offer.ID)
		s.emitSiteEvent(r, siteID, "serial.unpublished", model.SevNotice,
			"Serial line \""+label+"\" withdrawn by "+me.Email)
	}()

	// Pump host → attached client. Reading also refreshes the liveness stamp,
	// which is what makes `list` able to reap an offer whose browser died.
	relaySerial(ctx, offer, wsConn, true)
}

// keepSerialAlive pings the host socket and refreshes the offer's stamp.
//
// The ping is what makes the stamp mean something: a browser that was closed, a
// laptop that slept, a tunnel that died all fail it, and the offer ages out. A
// timer that merely refreshed the stamp on a schedule would keep a dead line
// listed for ever — it would be measuring that this process is still running,
// which nobody was in doubt about.
func keepSerialAlive(ctx context.Context, offer *serialOffer, conn *websocket.Conn) {
	var tick = time.NewTicker(serialPingEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Bounded: a ping that never answers must not hold this goroutine for
			// the offer's whole lifetime.
			var pingCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
			var err = conn.Ping(pingCtx)
			cancel()
			if err != nil {
				// The socket is gone. The relay's Read will fail too and its defer
				// drops the offer; nothing to do here but stop.
				return
			}
			offer.mu.Lock()
			offer.seen = time.Now()
			offer.mu.Unlock()
		}
	}
}

// handleAttachSerialServer is the CONSUMING half.
func (s *Server) handleAttachSerialServer(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var offer, found = s.serialOffers().get(chi.URLParam(r, "serverID"))
	if !found || offer.SiteID != siteID {
		// Refused BEFORE the upgrade: an ordinary status is readable by the fetch
		// the UI already made, where a socket that accepts then closes is a much
		// worse way to say "that line is gone".
		//
		// The site check is what stops an offer id being a cross-site handle: ids
		// are unguessable, but an authorization that rests on unguessability is
		// not an authorization.
		respondErr(w, http.StatusNotFound, "no such serial line on this site")
		return
	}
	var me = auth.UserFrom(r.Context())

	offer.mu.Lock()
	if offer.client != nil {
		offer.mu.Unlock()
		// ONE AT A TIME. Two people typing into one console interleave keystrokes
		// into a single command line — on a switch mid-factory-reset, a genuinely
		// dangerous way to find out you are not alone.
		respondErr(w, http.StatusConflict,
			"somebody is already attached to this serial line ("+offer.AttachedBy+")")
		return
	}
	offer.mu.Unlock()

	var wsConn, err = websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	wsConn.SetReadLimit(serialReadLimit)

	offer.mu.Lock()
	if offer.client != nil {
		// Lost the race between the check above and the upgrade. Rare, and the
		// close reason has to say why, because the UI cannot read a status here.
		offer.mu.Unlock()
		_ = wsConn.Close(websocket.StatusPolicyViolation, "already attached")
		return
	}
	offer.client = wsConn
	offer.AttachedBy = me.Email
	offer.mu.Unlock()

	s.emitSiteEvent(r, siteID, "serial.attached", model.SevWarning,
		me.Email+" attached to serial line \""+offer.Label+"\" published by "+offer.HostUser)

	defer func() {
		offer.mu.Lock()
		if offer.client == wsConn {
			offer.client = nil
			offer.AttachedBy = ""
		}
		offer.mu.Unlock()
		s.emitSiteEvent(r, siteID, "serial.detached", model.SevNotice,
			me.Email+" detached from serial line \""+offer.Label+"\"")
	}()

	relaySerial(r.Context(), offer, wsConn, false)
}

// relaySerial pumps one side of the pair into the other until either goes away.
//
// `fromHost` says which direction this call owns, and the asymmetry is
// deliberate rather than a symmetry that could be factored out: a host with no
// attacher keeps running — its offer stands and the switch's output is simply
// dropped — whereas an attacher whose host disappears has nothing left to talk
// to and must end. Collapsing the two would either close the offer whenever
// nobody is watching, or leave attachers connected to a dead line.
func relaySerial(ctx context.Context, offer *serialOffer, self *websocket.Conn, fromHost bool) {
	for {
		var typ, data, err = self.Read(ctx)
		if err != nil {
			return
		}
		offer.mu.Lock()
		offer.seen = time.Now()
		var peer = offer.client
		if !fromHost {
			peer = offer.host
		}
		var hasClient = offer.client != nil
		// FIRST FRAME ONLY, at INFO. The rest at DEBUG, which this build does not
		// emit.
		//
		// Not instrumentation for its own sake: "the socket is open" and "bytes
		// cross" are different claims, and when the relay delivered nothing there
		// was no way to tell WHICH SIDE was silent — a host that never sends, or
		// a client whose frames never reach the device. One line per direction
		// answers that from the log alone, which is the only vantage point there
		// is when both ends are browsers on somebody else's desk. Per-frame at
		// INFO would flood a console session; once per direction costs nothing
		// and is the diagnostic.
		var first bool
		if fromHost && !offer.sawHost {
			offer.sawHost, first = true, true
		} else if !fromHost && !offer.sawClient {
			offer.sawClient, first = true, true
		}
		offer.mu.Unlock()
		if first {
			slog.Info("serial relay: first frame", "offer", offer.ID,
				"from_host", fromHost, "bytes", len(data),
				"peer_attached", peer != nil, "has_client", hasClient)
		}
		if peer == nil {
			// Nobody on the other end. For a host that is the normal idle state:
			// the bytes are DISCARDED rather than buffered, because a buffer here
			// would be scrollback of a console carrying typed passwords (see the
			// file header).
			if fromHost {
				continue
			}
			return
		}
		if err = peer.Write(ctx, typ, data); err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Debug("serial relay write failed", "offer", offer.ID, "err", err.Error())
			}
			return
		}
	}
}

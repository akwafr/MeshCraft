package unifi

// The write half of this package: three gestures, one door (ADR-0025).
//
// # Why a type and not three methods
//
// ADR-0015 guaranteed "this package cannot write" by having no write method,
// and said so explicitly: the promise lived in the method set, not in a
// comment. ADR-0025 reopened that, so the guarantee owed a replacement of the
// same nature — structural, checkable by reading, impossible to erode by
// forgetting.
//
// `Gesture` is that replacement. Its only field is unexported and it is a
// pointer, so:
//
//   - no caller outside this file can write a Gesture literal, which means the
//     set of writes is closed at compile time and is exactly the three `var`
//     declarations below;
//   - the zero Gesture carries a nil pointer, so a forgotten argument or a
//     zeroed struct field refuses at the door instead of guessing;
//   - `Do` is the single exported method of Client that writes, so "what can
//     meshcraft change on somebody's controller" is answered by reading one
//     function.
//
// Adding a fourth gesture is therefore a visible act in review. `restart`,
// `upgrade` and `set-locate` are recognised by the controller and deliberately
// absent (ADR-0025, Consequences).
//
// # Two APIs again, and they are not equally trustworthy
//
// Forgetting goes through the INTEGRATION API: `DELETE` on the device
// resource. It is undocumented — it appears in no action enumeration — but it
// answers with a real HTTP status, and it has no inert form: the identifier IS
// the whole payload, so the request that would probe whether the route exists
// is already the request that destroys. That is not a hypothesis; it was
// learned by removing a production access point that had to be re-adopted by
// hand (ADR-0025, Context).
//
// Adopting and reprovisioning exist ONLY on the LEGACY API, `cmd/devmgr` —
// the same uncontracted half that serves the SSIDs, and it can break at a
// UniFi upgrade. Worse, IT FAILS OPEN: a command name it does not recognise
// answers `{"meta":{"rc":"ok"}}`. So `rc:ok` is never taken as proof that
// anything happened; every gesture is confirmed by a read (ADR-0025 §6).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- Errors a caller must be able to tell apart ---

// ErrNoGesture is the zero Gesture reaching Do. It means a caller passed a
// struct field that was never set, which is a programming error and not an
// operator's: refusing loudly is the whole point of the type.
var ErrNoGesture = errors.New("unifi: no gesture (a Gesture must come from this package)")

// ErrConfirmMismatch is a destructive gesture whose confirmation name did not
// match what the controller reports RIGHT NOW. The caller's fix is to re-read
// the object, which is exactly what the check exists to force (ADR-0025 §5).
var ErrConfirmMismatch = errors.New("unifi: the confirmation name does not match the controller's")

// ErrGestureRefused is the controller saying no — an unknown device, a device
// that cannot be adopted. Distinct from a transport failure because the
// operator's next move is different.
var ErrGestureRefused = errors.New("unifi: the controller refused the gesture")

// ErrUnconfirmed means the gesture was ACCEPTED and the confirming read did not
// show its effect. Deliberately neither a success nor a failure: on an API that
// answers "ok" to commands it does not know, "I asked and cannot prove it
// landed" is the honest answer and the operator must see it (ADR-0025 §6).
var ErrUnconfirmed = errors.New("unifi: the gesture was accepted but could not be confirmed by a read")

// --- The closed set of writes ---

// Gesture is one write meshcraft is willing to ask a UniFi controller to
// perform. It cannot be constructed outside this package: see the file comment.
type Gesture struct{ g *gesture }

// Name is the gesture's stable identifier, for a log line or an API payload.
// The zero Gesture answers "" rather than panicking, because a log statement
// is not where a nil check should first bite.
func (gs Gesture) Name() string {
	if gs.g == nil {
		return ""
	}
	return gs.g.name
}

// Destructive reports whether this gesture needs a confirmation name. It is
// read by the API layer to shape its contract, never to decide whether to
// check: the check itself lives in Do, where it cannot be skipped.
func (gs Gesture) Destructive() bool { return gs.g != nil && gs.g.destructive }

type gesture struct {
	name        string
	destructive bool
	// ask performs the gesture. It must report a refusal as ErrGestureRefused.
	ask func(ctx context.Context, c *Client, t Target) error
	// confirm re-reads and returns nil when the effect is visible. A gesture
	// whose effect is not observable at once leaves this nil and says why.
	confirm func(ctx context.Context, c *Client, t Target) error
}

// Target says which box, on which site, a gesture is about.
//
// Both site identifiers are here because the two APIs disagree on what a site
// is: the integration API wants the UUID, the legacy `cmd/devmgr` wants the
// short internalReference. That is the same split that made SiteExtension store
// both (ADR-0015 §6).
type Target struct {
	// SiteID is the integration API's site UUID.
	SiteID string
	// SiteRef is the legacy short reference, needed by cmd/devmgr.
	SiteRef string
	// DeviceID is the integration API's device UUID. Needed to forget.
	DeviceID string
	// MAC is what cmd/devmgr addresses a box by. Needed to adopt: a box that
	// is not adopted yet has no device UUID on this site.
	MAC string
	// ConfirmName is the box's exact name as the CONTROLLER reports it, which a
	// destructive gesture requires and Do re-reads and compares. Ignored by the
	// others.
	ConfirmName string
	// SSIDID is the service set an edit is about — wlanconf's `_id`. Needed by
	// EditSSID and ignored by the three box gestures (ADR-0029).
	SSIDID string
	// SSIDEdit carries where each setting came from and where it should go. Its
	// `Was` half is the PRECONDITION, re-checked against the controller at the
	// instant of the write; see ErrSSIDMoved.
	SSIDEdit SSIDEdit
}

// Adopt takes a pending box into the paired site.
//
// Only cmd/devmgr can do this, and it addresses the box by MAC — a box that is
// not adopted has no device UUID on this site yet. Unlike most of devmgr this
// one does not fail open on a box it does not know: it answers
// `api.err.CanNotAdoptUnknownDevice`.
var Adopt = Gesture{&gesture{
	name: "adopt",
	ask: func(ctx context.Context, c *Client, t Target) error {
		return c.devmgr(ctx, t.SiteRef, "adopt", t.MAC)
	},
	// Adoption takes a minute or more to reach ONLINE, and a handler must not
	// wait for that. What is confirmed is that the site now KNOWS the box —
	// which is the part the gesture was responsible for; the rest is the
	// controller provisioning at its own pace and the mirror will show it.
	confirm: func(ctx context.Context, c *Client, t Target) error {
		return c.expectPresence(ctx, t, true)
	},
}}

// Forget removes a box from the site. IRREVERSIBLE on UniFi's side: the box
// factory-resets, loses its name and its per-box settings, and comes back only
// as a candidate for adoption — if it can still reach the controller at all.
//
// Hence `destructive`, hence the confirmation name, and hence the integration
// API rather than devmgr's `delete-device`: the latter answers `rc:ok` for a
// box that does not exist, so it cannot tell "forgotten" from "never seen",
// which is precisely the distinction that matters here (ADR-0025 §8).
var Forget = Gesture{&gesture{
	name:        "forget",
	destructive: true,
	ask: func(ctx context.Context, c *Client, t Target) error {
		if strings.TrimSpace(t.DeviceID) == "" {
			return fmt.Errorf("%w: forgetting needs the device's id", ErrGestureRefused)
		}
		var err = c.do(ctx, http.MethodDelete, devicePath(t.SiteID, t.DeviceID), nil, nil)
		if err != nil {
			return asRefusal(err)
		}
		return nil
	},
	confirm: func(ctx context.Context, c *Client, t Target) error {
		return c.expectPresence(ctx, t, false)
	},
}}

// Reprovision asks the controller to push ITS OWN configuration to the box
// again. This is what "reconfigure" reduces to when meshcraft holds no
// intention (ADR-0025 §2): meshcraft never says what the configuration should
// be, only "make it match what you already decided".
//
// Harmless on an adopted box — it rewrites the configuration the box is
// already meant to have — which is why it is the one gesture that can be
// rehearsed against live hardware.
//
// NOTE ON ITS CONFIRMATION, which is the weakest of the three. Nothing
// observable changes at once: `provisionedAt` moves when the controller gets
// round to it, not when the command is accepted. So there is no `confirm`
// here. What rules out devmgr's fail-open instead is Do's PRE-check: this
// command DOES answer `api.err.UnknownDevice` for a box it has never heard of,
// so an accepted call on a box we just read is as much as this API can be made
// to promise.
var Reprovision = Gesture{&gesture{
	name: "reprovision",
	ask: func(ctx context.Context, c *Client, t Target) error {
		return c.devmgr(ctx, t.SiteRef, "force-provision", t.MAC)
	},
}}

// --- The door ---

// Do performs one gesture. It is the ONLY method of Client that writes.
//
// The order of operations is the decision, not an implementation detail:
//
//  1. the gesture must be real (a zero Gesture refuses);
//  2. a destructive gesture has its confirmation name re-read FROM THE
//     CONTROLLER and compared — never from meshcraft's mirror, which may be
//     hours old, and confirming against a stale copy is a gate that opens on
//     an object that is no longer the one that was read (ADR-0025 §5);
//  3. only then is the controller asked;
//  4. the effect is confirmed by a read, because `rc:ok` proves nothing on the
//     legacy API (ADR-0025 §6). A gesture that cannot be confirmed comes back
//     as ErrUnconfirmed — not as a success.
//
// The confirmation lives HERE rather than in the API handler on purpose: a
// check a caller has to remember is a check that will be forgotten, and this
// one guards the only gesture in the product that cannot be undone.
func (c *Client) Do(ctx context.Context, gs Gesture, t Target) error {
	if gs.g == nil {
		return ErrNoGesture
	}
	if strings.TrimSpace(t.SiteID) == "" {
		return errors.New("unifi: a gesture needs the site id")
	}

	if gs.g.destructive {
		var err = c.checkName(ctx, t)
		if err != nil {
			return err
		}
	}

	var err = gs.g.ask(ctx, c, t)
	if err != nil {
		return err
	}
	if gs.g.confirm == nil {
		return nil
	}
	err = gs.g.confirm(ctx, c, t)
	if err != nil {
		return err
	}
	return nil
}

// checkName re-reads the box's current name and compares it with the one the
// caller claims to have read. Any failure to READ is a refusal: not being able
// to confirm what is about to be destroyed is exactly when to stop, which is
// the same rule `delete_object` follows in cmd/mcp (ADR-0012).
func (c *Client) checkName(ctx context.Context, t Target) error {
	if strings.TrimSpace(t.ConfirmName) == "" {
		return fmt.Errorf("%w: this gesture requires the box's exact current name", ErrConfirmMismatch)
	}
	if strings.TrimSpace(t.DeviceID) == "" {
		return fmt.Errorf("%w: the box's name cannot be re-read without its id", ErrConfirmMismatch)
	}
	var d, err = c.GetDevice(ctx, t.SiteID, t.DeviceID)
	if err != nil {
		return fmt.Errorf("%w: the current name could not be read, so nothing was done: %v",
			ErrConfirmMismatch, err)
	}
	// Compared verbatim apart from surrounding blanks. Not case-folded and not
	// accent-folded: a name that differs in case is a name somebody edited, and
	// the point of the check is to notice that the object moved under us.
	if strings.TrimSpace(t.ConfirmName) != strings.TrimSpace(d.Name) {
		return fmt.Errorf("%w: it is named %q and you passed %q",
			ErrConfirmMismatch, d.Name, t.ConfirmName)
	}
	return nil
}

// --- Gesture plumbing ---

func devicePath(siteID, deviceID string) string {
	return "/proxy/network/integration/v1/sites/" + url.PathEscape(siteID) +
		"/devices/" + url.PathEscape(deviceID)
}

// legacyReply is cmd/devmgr's envelope. `msg` carries the refusal code.
type legacyReply struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
		MAC string `json:"mac"`
	} `json:"meta"`
}

// devmgr issues one cmd/devmgr command.
//
// It treats a missing `rc` as a refusal rather than as success. That is not
// defensive habit: this endpoint answers `{"meta":{"rc":"ok"}}` to command
// names it has never heard of, so the only safe reading of a reply that is not
// explicitly `ok` is "no".
func (c *Client) devmgr(ctx context.Context, siteRef, cmd, mac string) error {
	if strings.TrimSpace(siteRef) == "" {
		return fmt.Errorf("%w: %s needs the site's short reference", ErrGestureRefused, cmd)
	}
	if strings.TrimSpace(mac) == "" {
		return fmt.Errorf("%w: %s addresses a box by MAC and none was given", ErrGestureRefused, cmd)
	}
	var body, err = json.Marshal(map[string]string{
		"cmd": cmd,
		// Lowercased: devmgr matches the MAC as a string, and the mirror stores
		// it lowercased already (ADR-0016 §3). A mixed-case MAC would come back
		// as an unknown device.
		"mac": strings.ToLower(strings.TrimSpace(mac)),
	})
	if err != nil {
		return err
	}
	var reply legacyReply
	err = c.do(ctx, http.MethodPost,
		"/proxy/network/api/s/"+url.PathEscape(siteRef)+"/cmd/devmgr", body, &reply)
	if err != nil {
		return asRefusal(err)
	}
	if reply.Meta.RC != "ok" {
		var why = reply.Meta.Msg
		if why == "" {
			why = "the controller answered " + strconv.Quote(reply.Meta.RC)
		}
		return fmt.Errorf("%w: %s: %s", ErrGestureRefused, cmd, why)
	}
	return nil
}

// asRefusal keeps ErrUnauthorized distinguishable while turning a 4xx into a
// refusal: a token the controller rejects is the operator's to fix, and a 404
// on a device is a refusal rather than an outage.
func asRefusal(err error) error {
	if errors.Is(err, ErrUnauthorized) {
		return err
	}
	var he *HTTPError
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
		return fmt.Errorf("%w: HTTP %d: %s", ErrGestureRefused, he.Status, strings.TrimSpace(he.Body))
	}
	return err
}

// confirmAttempts / confirmPause bound the confirming read.
//
// Generous enough to usually succeed, because the integration device list is
// eventually consistent with a gesture the controller just accepted and a
// false "unconfirmed" on every single adoption would train people to ignore
// the warning that matters. Still far short of gestureTimeout, and still
// nowhere near "has the box finished rebooting" — adoption reaches ONLINE in
// minutes, and this only waits for the site to KNOW the box.
const (
	confirmAttempts = 6
	confirmPause    = 1500 * time.Millisecond
)

// expectPresence re-reads the site's boxes until the target's presence matches
// what the gesture promised. A few short tries, because the controller's own
// list is eventually consistent with a gesture it just accepted.
func (c *Client) expectPresence(ctx context.Context, t Target, want bool) error {
	var attempt int
	var lastErr error
	for attempt = 0; attempt < confirmAttempts; attempt++ {
		if attempt > 0 {
			var timer = time.NewTimer(confirmPause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("%w: %v", ErrUnconfirmed, ctx.Err())
			case <-timer.C:
			}
		}
		var present, err = c.devicePresent(ctx, t)
		if err != nil {
			lastErr = err
			continue
		}
		if present == want {
			return nil
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w: %v", ErrUnconfirmed, lastErr)
	}
	return ErrUnconfirmed
}

// devicePresent asks whether the site still lists the target, by MAC and by id.
//
// Both keys are tried because the two gestures know the box by different ones:
// an adoption has only a MAC (the box had no id on this site yet), a forget has
// the id. Matching on either is what lets one helper serve both.
func (c *Client) devicePresent(ctx context.Context, t Target) (bool, error) {
	var listed, err = c.listDevicesShallow(ctx, t.SiteID)
	if err != nil {
		return false, err
	}
	var wantMAC = strings.ToLower(strings.TrimSpace(t.MAC))
	var d Device
	for _, d = range listed {
		if t.DeviceID != "" && d.ID == t.DeviceID {
			return true, nil
		}
		if wantMAC != "" && strings.ToLower(d.MACAddress) == wantMAC {
			return true, nil
		}
	}
	return false, nil
}

// listDevicesShallow is the list call WITHOUT the per-box detail request that
// ListDevices makes. A confirmation needs identities, not radios, and paying
// one request per box to answer "is it there" would make a gesture slower than
// the import it precedes.
func (c *Client) listDevicesShallow(ctx context.Context, siteID string) ([]Device, error) {
	var out []Device
	var err = c.pages(ctx,
		"/proxy/network/integration/v1/sites/"+url.PathEscape(siteID)+"/devices",
		func(raw json.RawMessage) (int, error) {
			var batch []Device
			var decErr = json.Unmarshal(raw, &batch)
			if decErr != nil {
				return 0, decErr
			}
			out = append(out, batch...)
			return len(batch), nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetDevice reads one box. One request, unlike ListDevices, because the two
// callers that need it — the confirmation check and a gesture's pre-read — want
// an identity and not an inventory.
func (c *Client) GetDevice(ctx context.Context, siteID, deviceID string) (Device, error) {
	var d Device
	var err = c.get(ctx, devicePath(siteID, deviceID), &d)
	if err != nil {
		return Device{}, err
	}
	return d, nil
}

// --- What there is to adopt ---

// Candidate is a box the controller can see but has not taken in.
type Candidate struct {
	MAC   string `json:"mac"`
	Model string `json:"model"`
	// Kind is UniFi's legacy type — "uap", "usw", "ugw". Carried raw because it
	// is the only class information this endpoint gives, and translating it
	// here would duplicate ClassOf on a different vocabulary.
	Kind string `json:"kind"`
}

// ListAdoptable returns the boxes awaiting adoption.
//
// THEY BELONG TO NO SITE YET, and that surprised us: the same pending MAC is
// listed by every site's endpoint, because a box that has not been adopted has
// not been assigned anywhere. So this list is controller-wide however it is
// fetched, and adopting is what attaches a box to the paired site. A screen
// must say that, or an operator will think they are looking at this site's
// hardware.
//
// Read from the LEGACY API: the integration API's device list only ever shows
// adopted boxes, which is exactly why a forgotten box vanishes from the mirror
// instead of reappearing there as adoptable.
func (c *Client) ListAdoptable(ctx context.Context, siteRef string) ([]Candidate, error) {
	if strings.TrimSpace(siteRef) == "" {
		return nil, errors.New("unifi: no site reference; the pending list is on the legacy API")
	}
	var out struct {
		Meta struct {
			RC string `json:"rc"`
		} `json:"meta"`
		Data []struct {
			MAC     string `json:"mac"`
			Model   string `json:"model"`
			Type    string `json:"type"`
			Adopted bool   `json:"adopted"`
		} `json:"data"`
	}
	var err = c.get(ctx, "/proxy/network/api/s/"+url.PathEscape(siteRef)+"/stat/device-basic", &out)
	if err != nil {
		return nil, err
	}
	if out.Meta.RC != "" && out.Meta.RC != "ok" {
		return nil, fmt.Errorf("unifi: device-basic refused: rc=%s", out.Meta.RC)
	}
	var list = make([]Candidate, 0, len(out.Data))
	var d struct {
		MAC     string `json:"mac"`
		Model   string `json:"model"`
		Type    string `json:"type"`
		Adopted bool   `json:"adopted"`
	}
	for _, d = range out.Data {
		if d.Adopted {
			continue
		}
		list = append(list, Candidate{
			MAC:   strings.ToLower(d.MAC),
			Model: d.Model,
			Kind:  d.Type,
		})
	}
	return list, nil
}

// --- What the token is allowed to do ---

// Rights is what the controller says about the administrator behind the token.
//
// It exists so the product can state what it can really do instead of finding
// out at the worst moment (ADR-0025 §7). The honesty is in `Scoped`: UniFi
// exposes `is_super` and `is_owner` plainly, and a token that is neither has
// per-site rights this endpoint does not describe. We report that we do not
// know, rather than guessing either way.
type Rights struct {
	// Admin is the account behind the key, for the screen to name it.
	Admin string `json:"admin"`
	// Writes is "granted" when the account is owner or super-admin, and
	// "unproven" otherwise. Never "denied": a right we cannot read is not a
	// right we can deny.
	Writes string `json:"writes"`
	// Scoped is true when the account answered but is neither owner nor super,
	// so its gestures may still be refused per site.
	Scoped bool `json:"scoped"`
}

// Rights values.
const (
	WritesGranted  = "granted"
	WritesUnproven = "unproven"
)

// Rights reads the administrator behind the token.
//
// On the LEGACY API, because the integration API has no `self` at all
// (`/integration/v1/self` is a 404). No site reference is needed, which is why
// this works before any pairing exists.
func (c *Client) Rights(ctx context.Context) (Rights, error) {
	var out struct {
		Data []struct {
			Email   string `json:"email"`
			Name    string `json:"name"`
			IsOwner bool   `json:"is_owner"`
			IsSuper bool   `json:"is_super"`
		} `json:"data"`
	}
	var err = c.get(ctx, "/proxy/network/api/self", &out)
	if err != nil {
		return Rights{}, err
	}
	if len(out.Data) == 0 {
		return Rights{}, errors.New("unifi: the controller described no administrator for this token")
	}
	var me = out.Data[0]
	var who = me.Email
	if who == "" {
		who = me.Name
	}
	if me.IsOwner || me.IsSuper {
		return Rights{Admin: who, Writes: WritesGranted}, nil
	}
	return Rights{Admin: who, Writes: WritesUnproven, Scoped: true}, nil
}

package socks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Per-driver-session SOCKS credentials.
//
// # Why drivers get minted credentials instead of a shared secret
//
// An external driver reaches its device THROUGH this SOCKS server — that is
// the whole transport story of the MDP contract: the driver container holds no
// tunnel, no inventory, no route, only a SOCKS endpoint and a username and
// password handed to it in the session-create request. Handing every driver
// one shared credential would make each driver able to reach EVERY device the
// instance knows, which is precisely the door internal/socks's header refuses
// to open ("reach my switches" and "reach anything on the customer LAN" are
// not the same door — and neither are "reach your device" and "reach the
// fleet").
//
// A minted credential is scoped THREE ways, none negotiable:
//
//   - ONE HOST: the device the session was opened for, any port — the ranges
//     mix 22, 80 and 443 on one box, and the driver knows which its platform
//     speaks, not the controller (that is the Target.Host contract);
//   - ONE ROUTE: the proxy that reaches the device is resolved from the
//     inventory AT MINT TIME and frozen into the credential. A device
//     relocated mid-session keeps its old route until the session ends, which
//     is the correct behaviour: the session's transcript belongs to one
//     conversation with one box;
//   - ONE LIFETIME: the driver session's lease. Renewals travel through
//     [Minter.Renew] alongside the session's own; revocation is immediate on
//     session close, and expiry needs no janitor to be safe — every use
//     checks the clock.
//
// The Minter implements [Authenticator] and [Resolver], so a dedicated
// [Server] on the drivers' network is composed from it directly. It is NOT
// mounted on the operator listener: the two populations authenticate from
// different worlds (operator tokens live in Postgres, driver credentials live
// here, in memory), and a credential that survives a controller restart would
// outlive the session table that justifies it — losing both together is the
// point.
type Minter struct {
	mu    sync.Mutex
	creds map[string]minted
}

// minted is one live credential. The secret is kept as minted, not hashed:
// it is 32 bytes from crypto/rand living only in this map, and the comparison
// stays constant-time either way.
type minted struct {
	secret     string
	host       string
	proxyID    string
	deviceName string
	expires    time.Time
}

// MintedCredential is what the caller forwards to the driver, verbatim, in
// the session-create body.
type MintedCredential struct {
	Username string
	Password string
}

// NewMinter builds an empty Minter.
func NewMinter() *Minter {
	return &Minter{creds: map[string]minted{}}
}

// Mint issues a credential for ONE driver session: reach `host` (any port),
// through `proxyID` (empty = from the controller machine), for `ttl`.
//
// `sessionID` seeds the username so an audit line can be joined back to the
// driver session that spent the route; `deviceName` is what that line calls
// the destination.
func (m *Minter) Mint(sessionID, host, proxyID, deviceName string,
	ttl time.Duration) (MintedCredential, error) {

	var raw = make([]byte, 32)
	var _, err = rand.Read(raw)
	if err != nil {
		return MintedCredential{}, err
	}
	var cred = MintedCredential{
		Username: "drv-" + sessionID,
		Password: hex.EncodeToString(raw),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds[cred.Username] = minted{
		secret:     cred.Password,
		host:       host,
		proxyID:    proxyID,
		deviceName: deviceName,
		expires:    time.Now().Add(ttl),
	}
	return cred, nil
}

// Renew extends a live credential's lifetime. False when the credential is
// unknown or already expired — the caller must then treat the driver session
// as gone, not re-mint under the same name.
func (m *Minter) Renew(username string, ttl time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c, ok = m.creds[username]
	if !ok || time.Now().After(c.expires) {
		delete(m.creds, username)
		return false
	}
	c.expires = time.Now().Add(ttl)
	m.creds[username] = c
	return true
}

// Revoke forgets a credential. Idempotent: revoking what expired or never
// existed is the ordinary path on session teardown, not an error.
func (m *Minter) Revoke(username string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creds, username)
}

// Sweep drops expired credentials. Expiry is already enforced on every use;
// this only bounds the map's memory, so calling it from any periodic hook is
// enough.
func (m *Minter) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var now = time.Now()
	var user string
	var c minted
	for user, c = range m.creds {
		if now.After(c.expires) {
			delete(m.creds, user)
		}
	}
}

// Authenticate implements [Authenticator]: the subject is the minted
// username, the audit label names the driver session and its device.
func (m *Minter) Authenticate(_ context.Context, user, pass string) (string, string, bool) {
	m.mu.Lock()
	var c, ok = m.creds[user]
	m.mu.Unlock()
	if !ok || time.Now().After(c.expires) {
		// Spend the comparison anyway: an expired or unknown username must not
		// answer measurably faster than a wrong password.
		_ = ConstantTimeMatch(pass, "")
		return "", "", false
	}
	if !ConstantTimeMatch(pass, c.secret) {
		return "", "", false
	}
	return user, user + " → " + c.deviceName, true
}

// ProxyFor implements [Resolver]: the subject may reach exactly the host its
// credential was minted for, through the route frozen at mint time. Everything
// else is refused — including the credential's own device on a DIFFERENT
// address, because a relocation mid-session belongs to the NEXT session.
func (m *Minter) ProxyFor(_ context.Context, subject, host string) (string, string, bool) {
	m.mu.Lock()
	var c, ok = m.creds[subject]
	m.mu.Unlock()
	if !ok || time.Now().After(c.expires) || host != c.host {
		return "", "", false
	}
	return c.proxyID, c.deviceName, true
}

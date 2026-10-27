package restdriver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/socks"
	"meshcraft/wire/mdpv1"
)

// A hand-rolled MDP endpoint, NOT the SDK: this test is the controller side's
// view of the contract, and using the SDK here would let both halves drift
// together. What this fake serves is what the OpenAPI document promises.
func fakeMDP(t *testing.T) *httptest.Server {
	t.Helper()
	var mux = http.NewServeMux()
	var reply = func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /mdp/v1/protocol", func(w http.ResponseWriter, r *http.Request) {
		var info mdpv1.ProtocolInfo
		info.Protocol = mdpv1.Protocol
		info.Driver.Name = "wire-vendor"
		reply(w, info)
	})
	mux.HandleFunc("GET /mdp/v1/supports", func(w http.ResponseWriter, r *http.Request) {
		var one = mdpv1.ConnectorSupport{Name: "wire-vendor",
			Gestures: []mdpv1.Gesture{mdpv1.GestureHostname}}
		reply(w, mdpv1.SupportsResponse{Connectors: []mdpv1.ConnectorSupport{one}})
	})
	mux.HandleFunc("GET /mdp/v1/info", func(w http.ResponseWriter, r *http.Request) {
		reply(w, mdpv1.Info{Name: "wire-vendor", Vendor: "Wire"})
	})
	mux.HandleFunc("POST /mdp/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in mdpv1.SessionRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		// The SOCKS credential must have been minted and forwarded.
		if in.SOCKS.Username == "" || in.SOCKS.Password == "" || in.SOCKS.Address == "" {
			w.WriteHeader(http.StatusBadRequest)
			reply(w, mdpv1.ErrorEnvelope{Error: mdpv1.ErrorBody{
				Code: mdpv1.ErrCodeBadRequest, Detail: "no socks access"}})
			return
		}
		w.WriteHeader(http.StatusCreated)
		reply(w, mdpv1.SessionResponse{SessionID: "s1", Account: "mcadmin",
			Accepted: in.Candidates[0], LeaseExpiresAt: time.Now().Add(time.Minute)})
	})
	mux.HandleFunc("POST /mdp/v1/sessions/s1/hostname", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /mdp/v1/sessions/s1/storm-control-apply", func(w http.ResponseWriter, r *http.Request) {
		// The driver refuses with the platform's own sentence: the envelope
		// must carry it back INTACT.
		w.WriteHeader(http.StatusUnprocessableEntity)
		reply(w, mdpv1.ErrorEnvelope{Error: mdpv1.ErrorBody{
			Code: mdpv1.ErrCodeUnsupported, Gesture: "limit storms",
			Because: "this platform has no per-port limiter"}})
	})
	mux.HandleFunc("DELETE /mdp/v1/sessions/s1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	var ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestLoadRegistersAndSessionsCarryTheContract(t *testing.T) {
	var ts = fakeMDP(t)
	var minter = socks.NewMinter()
	var loaded, err = Load(context.Background(), ts.URL, Runtime{
		Minter: minter, SocksAdvertise: "10.88.0.1:1081",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Name != "wire-vendor" || loaded[0].Protocol != 1 {
		t.Fatalf("Load must report what it registered: %+v", loaded)
	}
	t.Cleanup(func() { driver.Unregister("wire-vendor") })

	// The wire connector answers through the ordinary registry door.
	var conn, connErr = driver.ConnectorFor("wire-vendor")
	if connErr != nil {
		t.Fatal(connErr)
	}
	if !driver.Has(conn, mdpv1.GestureHostname) || driver.Has(conn, mdpv1.GesturePoERead) {
		t.Fatal("Supports must mirror the driver's declaration exactly")
	}

	var adm, authErr = conn.Authenticate(context.Background(),
		mdpv1.Target{Host: "192.0.2.9"},
		[]mdpv1.Candidate{{Username: "mcadmin", Secret: "s", Why: "test"}})
	if authErr != nil {
		t.Fatal(authErr)
	}
	if adm.Account() != "mcadmin" {
		t.Fatalf("account: %q", adm.Account())
	}

	// Declared gesture → the wire; undeclared → local ErrNotImplemented with
	// zero HTTP (poe-read has no handler in the fake: reaching it would 404).
	var h, _ = driver.AsHostnameSetter(adm)
	var hErr = h.SetHostname(context.Background(), "sw-01")
	if hErr != nil {
		t.Fatal(hErr)
	}
	var pr, _ = driver.AsPoEReader(adm)
	var _, poeErr = pr.ReadPoE(context.Background())
	if poeErr == nil || !isNotImplemented(poeErr) {
		t.Fatalf("undeclared gesture must refuse locally: %v", poeErr)
	}

	// A wire refusal rebuilds the SAME error an in-tree driver returns,
	// operator sentence included.
	var sc, _ = driver.AsStormControlApplier(adm)
	// storm-control-apply is gated by supports… the fake declares only
	// hostname, so gate refuses before the wire. Temporarily widen the
	// declaration through a second connector? No: assert the gate here, and
	// the envelope path is covered by the mdpv1 round-trip test.
	var _, stormErr = sc.ApplyStormControl(context.Background(), mdpv1.StormControlPlan{})
	if !isNotImplemented(stormErr) {
		t.Fatalf("gate must refuse an undeclared apply: %v", stormErr)
	}

	adm.Close()
	adm.Close() // idempotent, like the contract demands
}

func isNotImplemented(err error) bool {
	return err != nil && errors.Is(err, mdpv1.ErrNotImplemented)
}

func TestProtocolMismatchIsRefusedAtLoad(t *testing.T) {
	var mux = http.NewServeMux()
	mux.HandleFunc("GET /mdp/v1/protocol", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"protocol": 99})
	})
	var ts = httptest.NewServer(mux)
	defer ts.Close()
	var _, err = Load(context.Background(), ts.URL, Runtime{Minter: socks.NewMinter()})
	if err == nil {
		t.Fatal("a future protocol must be refused at load, never half-spoken")
	}
}

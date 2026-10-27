package api

// Optics (transceiver DDM) observation plumbing — the read-state hook and the
// device-view extraction, kept together in one file ON PURPOSE: readPoE lives
// in deviceaddr.go and poeObservedOf in conformity.go, which spreads one
// feature across two files other work keeps touching. Everything the optics
// observation needs, except the one-line call in handleReadDeviceState and
// the deviceView field, is here.
//
// The shape mirrors PoE exactly (cf. readPoE / poeObservedOf): read over the
// admin session the read-state gesture already opened, persist under
// Extra.optics_observed (Store.SaveOpticsObserved), serve on the device view
// as `optics_state`. Same drift doctrine too — Extra is not part of the
// diff, so a receive power moving a tenth of a dBm between reads can never
// flag a device.

import (
	"context"
	"encoding/json"
	"errors"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// readOptics re-reads transceiver diagnostics (module identity, TX/RX power,
// temperature, voltage, bias current) and stores the observation under Extra.
//
// Called from [Server.handleReadDeviceState]. Failure does not interrupt the
// read; the returned phrase is displayed instead. There is no OpticsCapsView
// counterpart to PoECapsView, deliberately: optics has no write path — the
// presence of `optics_state` is the whole capability statement, exactly the
// "readability is proven by the observation itself" half of PoECapsView's
// comment, without the writability half that made that view necessary.
func (s *Server) readOptics(ctx context.Context, deviceID string,
	adm driver.Admin) (driver.OpticsState, string) {
	var reader, canRead = driver.AsOpticsReader(adm)
	if !canRead {
		return driver.OpticsState{}, "connector \"" + adm.Connector().Name +
			"\" cannot read transceiver diagnostics on this product line"
	}
	var state, err = reader.ReadOptics(ctx)
	if errors.Is(err, driver.ErrNotImplemented) {
		return driver.OpticsState{}, "connector \"" + adm.Connector().Name +
			"\" cannot read transceiver diagnostics on this product line"
	}
	if err != nil {
		return driver.OpticsState{}, "transceiver diagnostics not read: " + err.Error()
	}
	var raw, marshalErr = json.Marshal(state)
	if marshalErr != nil {
		return state, "transceiver diagnostics read but not saved: " + marshalErr.Error()
	}
	var saveErr = s.Store.SaveOpticsObserved(ctx, deviceID, raw)
	if saveErr != nil {
		return state, "transceiver diagnostics read but not saved: " + saveErr.Error()
	}
	return state, ""
}

// opticsObservedOf extracts the observation stored under Extra.optics_observed.
// Same contract as stpObservedOf and poeObservedOf: absent, unreadable, or
// empty means nil — a bare {} must never be shown as "no module anywhere on
// this device" (an empty Ports list WITH notes still shows: the notes are the
// observation then).
func opticsObservedOf(d model.Device) *driver.OpticsState {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var raw, ok = bag["optics_observed"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var st driver.OpticsState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	if len(st.Ports) == 0 && len(st.Notes) == 0 {
		return nil
	}
	return &st
}

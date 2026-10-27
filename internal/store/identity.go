package store

import (
	"context"
	"encoding/json"

	"meshcraft/internal/model"
)

// SetDeviceIdentity records what a re-read established about WHAT the device IS:
// the `model` column, and the identity facts (serial, firmware, source…) under
// `extra.identity`.
//
// Identity is a FACT of the hardware, not intent — same nature as the faceplate
// (cf. SetDeviceFaceplate), and same reason to have a dedicated method: it must
// not touch name/location/intent, and UpdateDevice would let a stale form value
// ride along.
//
// TWO GUARDS LIVE IN THE SQL, deliberately, so no caller can hold them wrong:
//
//   - `coalesce(nullif($2, ”), model)` — an EMPTY model observation NEVER blanks
//     a stored model. A connector that could not read the model this time (a
//     partial identity, cf. driver.Identity) passes "" and the column keeps what
//     it knew. This is the same invariant api.identityHealDecision applies one
//     level up; it is repeated here because this is the layer that survives the
//     next caller.
//   - `jsonb_set(coalesce(extra, '{}'::jsonb), …)` — a device whose extra is
//     NULL (entered by hand, pre-0001 rows) gains the identity instead of the
//     update silently producing NULL, which is what jsonb_set on NULL does.
//
// Both guard expressions were exercised on the deployment's PostgreSQL (SELECT
// replay: empty-model keep, model replace, NULL-extra fill) before being written
// here; the decision table is transcribed in identity_test.go.
func (s *Store) SetDeviceIdentity(ctx context.Context, deviceID, deviceModel string,
	identity json.RawMessage) (model.Device, error) {
	return scanDevice(s.pool.QueryRow(ctx,
		`update devices
		    set model = coalesce(nullif($2, ''), model),
		        extra = jsonb_set(coalesce(extra, '{}'::jsonb), '{identity}', $3::jsonb, true)
		  where id = $1::uuid returning `+deviceCols,
		deviceID, deviceModel, jsonOr(identity, `{}`)))
}

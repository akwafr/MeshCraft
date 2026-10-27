// Notification channels, over HTTP. ADR-0024.
//
// # The three rules this file exists to keep
//
// THE LIST IS SECRET-FREE. `notifyChanCols` never selects `token_enc`, only
// `octet_length(token_enc) > 0`, and `model.NotifyChannel` has no field that
// could hold it. So the list route is safe by construction and not by the
// vigilance of whoever edits it next — the same device as SiteExtension.
//
// AN ABSENT TOKEN LEAVES THE STORED ONE ALONE. The UI cannot read the token
// back, so it cannot resubmit it; without this rule, renaming a channel would
// destroy its credential.
//
// REVELATION IS TRACED BEFORE IT HAPPENS. The journal line is written before
// the plaintext reaches the response, so a reader of the audit log cannot be
// shown fewer revelations than occurred.
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/notify"
)

const notifyBodyLimit = 16 << 10

// handleListNotifyChannels: the site's channels plus the instance-wide ones.
//
// `operator`, like the extension list and for the same reason: an operator must
// be able to see THAT a channel exists and whether its last send failed,
// without being able to read its token. A channel nobody can see is a channel
// nobody notices is broken.
func (s *Server) handleListNotifyChannels(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var list, err = s.Store.ListNotifyChannels(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, list)
}

type notifyChannelInput struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Label       string   `json:"label"`
	Target      string   `json:"target"`
	Token       string   `json:"token"`
	Classes     []string `json:"classes"`
	MinSeverity *int     `json:"min_severity"`
	Enabled     *bool    `json:"enabled"`
}

// validateChannelInput normalises and refuses. Shared by upsert and test so a
// channel cannot be tested under rules the upsert would reject.
func validateChannelInput(in *notifyChannelInput) string {
	in.Kind = strings.TrimSpace(in.Kind)
	in.Target = strings.TrimSpace(in.Target)
	in.Token = strings.TrimSpace(in.Token)
	switch in.Kind {
	case model.NotifyKindTelegram:
		// The chat id is NOT the secret and lives in `target`. Refused when
		// empty rather than stored, because a Telegram channel without one
		// cannot work, and a configuration that cannot work must fail where it
		// is typed and not at the first incident.
		if in.Target == "" {
			return "a Telegram channel needs a chat_id in \"target\""
		}
	case model.NotifyKindSlack:
		// The webhook URL is addressing and credential at once, so it belongs
		// entirely in the vault. A `target` here would be a second, unsealed
		// copy of half the secret.
		if in.Target != "" {
			return "a Slack channel carries no \"target\": the webhook URL is the token"
		}
	default:
		return "unknown channel kind \"" + in.Kind + "\": expected \"" +
			model.NotifyKindTelegram + "\" or \"" + model.NotifyKindSlack + "\""
	}
	if len(in.Classes) == 0 {
		// The default is the three classes, i.e. the policy as it was asked
		// for. A channel subscribed to nothing is a channel that looks
		// configured and delivers nothing — the exact failure ADR-0024's
		// health fields exist to expose, so better not to create it.
		in.Classes = []string{model.AlertClassDevice,
			model.AlertClassInterco, model.AlertClassDescribed}
	}
	var c string
	for _, c = range in.Classes {
		if c != model.AlertClassDevice && c != model.AlertClassInterco &&
			c != model.AlertClassDescribed {
			return "unknown alert class \"" + c + "\""
		}
	}
	if in.MinSeverity != nil && (*in.MinSeverity < 0 || *in.MinSeverity > 5) {
		return "min_severity must be between 0 and 5"
	}
	return ""
}

// handleUpsertNotifyChannel creates or edits a channel. `site_admin`: it holds
// a secret, and a bad one sends this site's outages to a stranger's chat.
func (s *Server) handleUpsertNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in notifyChannelInput
	if readJSON(w, r, &in, notifyBodyLimit) != nil {
		return
	}
	var why = validateChannelInput(&in)
	if why != "" {
		respondErr(w, http.StatusBadRequest, why)
		return
	}
	if in.ID == "" && in.Token == "" {
		respondErr(w, http.StatusBadRequest, "a new channel needs a token")
		return
	}
	// Tenancy: editing by id must not reach across sites. Checked BEFORE the
	// write, unlike the reveal route which checks after the read, because here
	// there is nothing to read first.
	if in.ID != "" {
		var existing, getErr = s.Store.GetNotifyChannel(r.Context(), in.ID)
		if getErr != nil {
			respondErr(w, http.StatusNotFound, "no such channel")
			return
		}
		if existing.SiteID != siteID {
			// Covers the instance channel too: it is not a site admin's to
			// edit, and saying 404 rather than 403 avoids confirming it exists.
			respondErr(w, http.StatusNotFound, "no such channel")
			return
		}
	}

	var ch = model.NotifyChannel{
		ID: in.ID, SiteID: siteID, Kind: in.Kind,
		Label: strings.TrimSpace(in.Label), Target: in.Target,
		Classes: in.Classes, MinSeverity: model.SevNotice, Enabled: true,
	}
	if in.MinSeverity != nil {
		ch.MinSeverity = *in.MinSeverity
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}

	var tokenEnc []byte
	var err error
	if in.Token != "" {
		tokenEnc, err = s.Secrets.Seal(in.Token)
		if err != nil {
			fail(w, err)
			return
		}
	}
	var out model.NotifyChannel
	out, err = s.Store.UpsertNotifyChannel(r.Context(), ch, tokenEnc)
	if err != nil {
		fail(w, err)
		return
	}
	var verb = "created"
	if in.ID != "" {
		verb = "updated"
	}
	s.emitSite(r, siteID, "notify.channel_"+verb, model.SevInfo,
		"Notification channel \""+out.Kind+"\" "+verb+" by "+
			auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, out)
}

func (s *Server) handleDeleteNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var id = chi.URLParam(r, "chanID")
	var ch, err = s.Store.GetNotifyChannel(r.Context(), id)
	if err != nil || ch.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "no such channel")
		return
	}
	if err = s.Store.DeleteNotifyChannel(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	// SevWarning, not SevInfo: deleting a channel is how a site goes quiet, and
	// "why did nobody hear about the outage" is answered by this line.
	s.emitSite(r, siteID, "notify.channel_deleted", model.SevWarning,
		"Notification channel \""+ch.Kind+"\" deleted by "+
			auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleRevealNotifyToken returns the clear token, after journalling.
func (s *Server) handleRevealNotifyToken(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var id = chi.URLParam(r, "chanID")
	var ch, enc, err = s.Store.RevealNotifyToken(r.Context(), id)
	if err != nil {
		respondErr(w, http.StatusNotFound, "no such channel")
		return
	}
	// Tenancy re-check AFTER the read: without it a site-admin would read
	// another site's token by guessing a UUID.
	if ch.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "no such channel")
		return
	}
	var token string
	token, err = s.Secrets.Open(enc)
	if err != nil {
		respondErr(w, http.StatusUnprocessableEntity,
			"this token cannot be decrypted: the vault key has changed since it was stored. "+
				"Re-enter the token to replace it.")
		return
	}
	// Journalled BEFORE the response. Same event type as every other vault
	// access, deliberately: the audit trail must be readable without knowing
	// which screen a secret came from.
	s.emitSite(r, siteID, "credential.revealed", model.SevWarning,
		"Notification token \""+ch.Kind+"\" revealed by "+
			auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, map[string]any{"id": ch.ID, "token": token})
}

// handleTestNotifyChannel sends a message now, and reports what the far end said.
//
// This route is not a convenience. A notification integration you cannot try
// before the incident is one whose misconfiguration you discover DURING the
// incident — and the failure modes here are all silent from this side: a bot
// never started by the user, a bot removed from the group, a revoked webhook, a
// chat id off by a minus sign. Each returns a different diagnostic from the
// API, and this is what puts that diagnostic on the operator's screen.
//
// It accepts an inline token so a channel can be tested BEFORE being saved,
// which is the order an operator actually works in. With no token in the body
// it tests the stored one.
func (s *Server) handleTestNotifyChannel(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in notifyChannelInput
	if readJSON(w, r, &in, notifyBodyLimit) != nil {
		return
	}
	var ch model.NotifyChannel
	var token = in.Token
	if in.ID != "" {
		var err error
		var enc []byte
		ch, enc, err = s.Store.RevealNotifyToken(r.Context(), in.ID)
		if err != nil || ch.SiteID != siteID {
			respondErr(w, http.StatusNotFound, "no such channel")
			return
		}
		if token == "" {
			// A TEST of a stored channel decrypts its token but never returns
			// it, and is NOT journalled as a revelation: nothing leaves towards
			// the operator, only towards the channel they already configured.
			token, err = s.Secrets.Open(enc)
			if err != nil {
				respondErr(w, http.StatusUnprocessableEntity,
					"this token cannot be decrypted: the vault key has changed since it was stored.")
				return
			}
		}
	} else {
		var why = validateChannelInput(&in)
		if why != "" {
			respondErr(w, http.StatusBadRequest, why)
			return
		}
		if token == "" {
			respondErr(w, http.StatusBadRequest, "a test needs a token")
			return
		}
		ch = model.NotifyChannel{Kind: in.Kind, Target: in.Target, SiteID: siteID}
	}

	var sender, err = senderOf(ch, token)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A fresh budget, not the request's: the send must be bounded by the
	// channel's timeout rather than by whatever the browser allows.
	var ctx, cancel = context.WithTimeout(r.Context(), notifySendTimeout)
	defer cancel()
	var site, _ = s.Store.GetSite(ctx, siteID)
	err = sender.Send(ctx, notify.Message{
		Title:    "test",
		Body:     "meshcraft test message. If you are reading this, alerts will arrive here.",
		Severity: model.SevNotice,
		Site:     site.Name,
	})
	// The attempt is journalled either way. A test that failed is exactly as
	// worth recording as one that worked: it is the evidence that somebody
	// checked, and what the far end answered at that moment.
	if err != nil {
		s.emitSite(r, siteID, "notify.test_failed", model.SevWarning,
			"Notification test on \""+ch.Kind+"\" failed: "+err.Error(), nil)
		// 200, not 502: the REQUEST succeeded and its answer is "the channel
		// refused, here is what it said". A 5xx would be read by the UI as
		// "meshcraft is broken" and hide the diagnostic, which is the one thing
		// this route exists to deliver.
		respond(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if ch.ID != "" {
		// A successful test is real evidence of health and updates it, so the
		// channel does not read as "never worked" until the first alert.
		var noteErr = s.Store.NoteChannelResult(ctx, ch.ID, "")
		if noteErr != nil {
			fail(w, noteErr)
			return
		}
	}
	s.emitSite(r, siteID, "notify.test_sent", model.SevInfo,
		"Notification test on \""+ch.Kind+"\" sent by "+
			auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

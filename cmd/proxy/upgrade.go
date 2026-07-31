package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Mise a jour automatique du proxy : le binaire est remplace, pas le conteneur.
//
// Le proxy telecharge le binaire de son architecture depuis le controleur,
// verifies son SHA-256, le range dans son volume state et se reexecute. L'image
// Docker n'est pas touchee — donc aucun access au socket Docker, et le same code
// marche pour une installation systemd.
//
// Cf. docs/01-Architecture/Versionnage et mise a jour du proxy.

// binDir : les binaires telecharges vivent dans le volume state, seul chemin
// inscriptible d'une installation durcie (StateDirectory cote systemd).
func binDir() string { return filepath.Join(stateDir, "bin") }

func binPath(version string) string {
	return filepath.Join(binDir(), "meshcraft-proxy-"+version)
}

// upgradeOffer est ce que le controleur propose dans la response au heartbeat.
type upgradeOffer struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// report est ce que le proxy renvoie au heartbeat suivant. Un failure muet serait
// pire que pas de mise a jour du tout.
type upgradeReport struct {
	State   string `json:"state"` // downloading | applied | failed
	Version string `json:"version"`
	Error   string `json:"error,omitempty"`
}

// pendingReport porte le rapport jusqu'au prochain heartbeat. Apres un exec
// reussi, c'est le NOUVEAU processus qui rapporte — sa seule preuve etant la
// version qu'il annonce, ce qui suffit au controleur.
var pendingReport *upgradeReport

// applyUpgrade execute la mise a jour proposee. Elle ne rend la main que si
// quelque chose a echoue : en cas de success, le processus est remplace.
func applyUpgrade(st State, offer upgradeOffer) error {
	if offer.Version == "" || offer.URL == "" || offer.SHA256 == "" {
		return errors.New("offre de mise a jour incomplete")
	}
	if offer.Version == version {
		return nil // already a jour : rien a faire
	}
	// L'URL doit be SUR LE CONTROLEUR de cet etat local. Le controleur est de
	// confiance par construction, mais rien ne justifie d'executer un binaire
	// telecharge ailleurs.
	var err = sameHost(offer.URL, st.Controller)
	if err != nil {
		return err
	}

	slog.Info("mise a jour : telechargement", "version", offer.Version, "url", offer.URL)
	pendingReport = &upgradeReport{State: "downloading", Version: offer.Version}

	var path string
	path, err = download(offer)
	if err != nil {
		return err
	}

	// L'enregistrement dans state.json AVANT l'exec : c'est lui, et non la
	// presence du fichier, qui autorisera l'execution au prochain demarrage.
	var newState = st
	newState.Binary = &BinaryRecord{
		Version:      offer.Version,
		SHA256:       offer.SHA256,
		BootAttempts: 1,
		Confirmed:    false,
	}
	if err = saveState(newState); err != nil {
		return fmt.Errorf("etat non persiste: %w", err)
	}

	slog.Info("mise a jour : exec du nouveau binaire", "version", offer.Version, "path", path)
	return execBinary(path, offer.Version)
}

// download recupere le binaire et REFUSE de l'installer si le SHA-256 ne
// correspond pas. Le fichier temporaire est written a cote de la cible puis
// renomme : pas de binaire a half written executable.
func download(offer upgradeOffer) (string, error) {
	var err = os.MkdirAll(binDir(), 0o750)
	if err != nil {
		return "", err
	}
	var tmp *os.File
	tmp, err = os.CreateTemp(binDir(), ".dl-*")
	if err != nil {
		return "", err
	}
	var tmpName = tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck

	var resp, reqErr = client.Get(offer.URL)
	if reqErr != nil {
		_ = tmp.Close()
		return "", reqErr
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		return "", fmt.Errorf("telechargement HTTP %d", resp.StatusCode)
	}

	var h = sha256.New()
	var written int64
	written, err = io.Copy(tmp, io.TeeReader(resp.Body, h))
	_ = tmp.Close()
	if err != nil {
		return "", err
	}
	if offer.Size > 0 && written != offer.Size {
		return "", fmt.Errorf("taille inattendue : %d octets recus, %d annonces", written, offer.Size)
	}
	var got = hex.EncodeToString(h.Sum(nil))
	if got != offer.SHA256 {
		return "", fmt.Errorf("SHA-256 inattendu : %s (expected %s)", got, offer.SHA256)
	}
	if err = os.Chmod(tmpName, 0o750); err != nil {
		return "", err
	}
	var final = binPath(offer.Version)
	if err = os.Rename(tmpName, final); err != nil {
		return "", err
	}
	return final, nil
}

// sameHost verifies que l'URL de telechargement est bien sur le controleur
// courant (same schema et same hote).
func sameHost(rawURL, controller string) error {
	var u, err = url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL de mise a jour illisible: %w", err)
	}
	var c, cErr = url.Parse(controller)
	if cErr != nil {
		return fmt.Errorf("URL de controleur illisible: %w", cErr)
	}
	if !strings.EqualFold(u.Host, c.Host) || u.Scheme != c.Scheme {
		return fmt.Errorf("URL de mise a jour hors du controleur (%s ≠ %s)", u.Host, c.Host)
	}
	return nil
}

// execBinary remplace le processus courant. MESHCRAFT_UPGRADED_FROM est un
// garde-fou : si le binaire execute annoncait une version incoherente avec
// l'enregistrement, l'amorcage refuserait de recommencer indefiniment.
func execBinary(path, toVersion string) error {
	var env = append(os.Environ(), "MESHCRAFT_UPGRADED_FROM="+version)
	var err = syscall.Exec(path, []string{path}, env)
	// syscall.Exec ne revient qu'en cas failure.
	return fmt.Errorf("exec %s (→ %s): %w", path, toVersion, err)
}

// bootstrapBinary est appele au DEMARRAGE, avant toute autre chose.
//
// Apres un reboot de conteneur, l'entrypoint relance le binaire de l'image
// — l'ancienne version. C'est donc ici qu'on rebascule sur le binaire mis a
// jour, et ici qu'on refuse de le faire quand il ne demarre pas.
func bootstrapBinary(st State) State {
	if st.Binary == nil || st.Binary.Version == "" {
		return st
	}
	// On est already le bon binaire : on confirmera au premier heartbeat reussi.
	if st.Binary.Version == version {
		return st
	}
	var record = st.Binary

	// Garde-fou : un binaire qui n'annonce pas la version enregistree ne doit pas
	// provoquer une boucle d'exec.
	if os.Getenv("MESHCRAFT_UPGRADED_FROM") != "" {
		slog.Error("mise a jour : le binaire execute n'annonce pas la version attendue — abandon",
			"expected", record.Version, "annonce", version)
		return fallback(st, "binaire incoherent : "+version+" au lieu de "+record.Version)
	}

	var path = binPath(record.Version)
	var err = verifyFile(path, record.SHA256)
	if err != nil {
		slog.Error("mise a jour : binaire enregistre inutilisable", "err", err)
		_ = os.Remove(path)
		return fallback(st, err.Error())
	}
	if record.BootAttempts >= 2 {
		// Deux demarrages sans confirmation : le binaire ne tient pas debout.
		// On repart sur celui de l'image et on le fait savoir.
		slog.Error("mise a jour : deux demarrages sans confirmation — retour au binaire de l'image",
			"version", record.Version)
		_ = os.Remove(path)
		return fallback(st, "le binaire "+record.Version+" ne demarre pas (2 tentatives)")
	}

	record.BootAttempts++
	st.Binary = record
	if err = saveState(st); err != nil {
		slog.Error("mise a jour : compteur de demarrage non persiste", "err", err)
		return st
	}
	slog.Info("mise a jour : bascule sur le binaire enregistre",
		"version", record.Version, "tentative", record.BootAttempts)
	if err = execBinary(path, record.Version); err != nil {
		slog.Error("mise a jour : exec impossible", "err", err)
		return fallback(st, err.Error())
	}
	return st // inatteignable
}

// fallback abandonne le binaire enregistre, revient a celui de l'image et
// prepare le rapport failure pour le prochain heartbeat.
func fallback(st State, reason string) State {
	var failed = ""
	if st.Binary != nil {
		failed = st.Binary.Version
	}
	st.Binary = nil
	var err = saveState(st)
	if err != nil {
		slog.Error("etat non persiste after repli", "err", err)
	}
	pendingReport = &upgradeReport{State: "failed", Version: failed, Error: reason}
	return st
}

// verifyFile relit le fichier et compare son empreinte a celle enregistree.
func verifyFile(path, wantSHA string) error {
	var f, err = os.Open(path)
	if err != nil {
		return fmt.Errorf("binaire absent: %w", err)
	}
	defer f.Close() //nolint:errcheck
	var h = sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	var got = hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		return fmt.Errorf("SHA-256 du binaire enregistre ne correspond pas (%s ≠ %s)", got, wantSHA)
	}
	return nil
}

// confirmBinary : un heartbeat a reussi, la version tient. On remet le compteur
// a zero pour que le prochain reboot ne consomme pas de tentative.
func confirmBinary(st *State) {
	if st.Binary == nil || st.Binary.Confirmed && st.Binary.BootAttempts == 0 {
		return
	}
	if st.Binary.Version != version {
		return
	}
	st.Binary.Confirmed = true
	st.Binary.BootAttempts = 0
	var err = saveState(*st)
	if err != nil {
		slog.Error("confirmation de version non persistee", "err", err)
	}
	slog.Info("mise a jour confirmee", "version", version)
}

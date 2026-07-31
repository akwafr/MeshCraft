package main

// Les tests du refus de demarrer.
//
// Ce qu'ils protegent : la garde de key n'a de valeur que si elle refuse
// vraiment. Un garde-fou qui laisse passer est pire que pas de garde-fou, parce
// qu'on cesse de se mefier. Les deux sens du trap sont couverts :
//
//   - MESHCRAFT_SECRET_KEY definie (ou changee) sur une base scellee autrement ;
//   - variable RETIREE after une bascule, ce qui autrefois tirait une key neuve
//     et rescellait par-dessus.
//
// Aucune base n'est necessary : c'est tout l'interet de l'interface keyStore.
// Un test de refus de demarrer qu'on n'execute pas faute de PostgreSQL sous la
// main ne protege rien.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// fakeKeyStore imite le strict necessary : la ligne instance_secrets, la ligne
// d'empreinte, et un sample de valeurs scellees.
type fakeKeyStore struct {
	instance    map[string][]byte
	fingerprint []byte
	algoErr     error
	sealed      [][]byte

	// setCalls compte les enregistrements d'empreinte : un refus qui would write
	// quand same l'empreinte serait un desastre deguise en refus.
	setCalls int
	// putCalls compte les creations de key : la garde ne doit JAMAIS tirer une
	// key neuve sur une base already scellee.
	putCalls int
}

func newFake() *fakeKeyStore {
	return &fakeKeyStore{instance: map[string][]byte{}}
}

func (f *fakeKeyStore) CredentialsKeyFingerprint(ctx context.Context) ([]byte, error) {
	if f.algoErr != nil {
		return nil, f.algoErr
	}
	return f.fingerprint, nil
}

func (f *fakeKeyStore) SetCredentialsKeyFingerprint(ctx context.Context, fp []byte) error {
	f.setCalls++
	f.fingerprint = fp
	return nil
}

func (f *fakeKeyStore) InstanceSecretValue(ctx context.Context, name string) ([]byte, error) {
	var v, ok = f.instance[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyStore) PutInstanceSecret(ctx context.Context, name string, value []byte) error {
	f.putCalls++
	// `on conflict do nothing` du vrai store : on ne remplace pas.
	var _, exists = f.instance[name]
	if !exists {
		f.instance[name] = value
	}
	return nil
}

func (f *fakeKeyStore) SealedSample(ctx context.Context, perColumn int) ([][]byte, error) {
	if perColumn >= len(f.sealed) {
		return f.sealed, nil
	}
	return f.sealed[:perColumn], nil
}

// hexKey rend la forme hexadecimale attendue dans MESHCRAFT_SECRET_KEY.
func hexKey(t *testing.T, key []byte) string {
	t.Helper()
	var out = ""
	var b byte
	for _, b = range key {
		out += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	return out
}

// sealWith remplit l'sample avec des valeurs scellees par `key`.
func sealWith(t *testing.T, f *fakeKeyStore, key []byte, clairs ...string) {
	t.Helper()
	var box, err = secretbox.New(key, false)
	if err != nil {
		t.Fatal(err)
	}
	var c string
	for _, c = range clairs {
		var enc []byte
		enc, err = box.Seal(c)
		if err != nil {
			t.Fatal(err)
		}
		f.sealed = append(f.sealed, enc)
	}
}

func newKey(t *testing.T) []byte {
	t.Helper()
	var k, err = secretbox.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// --- Sens 1 : on present une key d'environnement qui n'a pas scelle ---

// Le trap central du lot : une instance en service, dont le coffre est scelle
// par la key rangee en base, et sur laquelle on definit MESHCRAFT_SECRET_KEY.
// Avant l'empreinte, le controleur demarrait, le produit avait l'air de
// fonctionner, et la supervision disparaissait sans un mot.
func TestRefuseWhenEnvSetOnSealedDB(t *testing.T) {
	var f = newFake()
	var enBase = newKey(t)
	f.instance[store.CredentialsKeyName] = enBase
	sealWith(t, f, enBase, "snmp-community", "MotDePasseAdmin42")
	// Cas reel de la mise a jour : la base n'a pas encore d'empreinte.
	f.fingerprint = nil

	var autre = newKey(t)
	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, autre))
	if err == nil {
		t.Fatal("demarrage accepted avec une key qui n'ouvre pas le coffre")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("mauvaise erreur : %v", err)
	}
	// Le message doit nommer la cause ET le geste : « ca ne demarre pas » sans
	// mode d'emploi laisse l'exploitant retirer la variable au hasard.
	assertMentions(t, err.Error(), "-rekey", "scelles avec une AUTRE key", "Sauvegardez")
	// Et surtout : l'empreinte de la MAUVAISE key ne doit pas avoir been gravee.
	if f.setCalls != 0 {
		t.Fatal("l'empreinte a been enregistree despite le refus")
	}
}

// Meme trap, une etape plus tard : l'empreinte existe, et on change la valeur
// de la variable (rotation manuelle, copier-coller d'un autre environnement,
// restauration d'un .env d'une autre instance).
func TestRefuseWhenFingerprintDiffers(t *testing.T) {
	var f = newFake()
	var sealing = newKey(t)
	f.instance[store.CredentialsKeyName] = sealing
	f.fingerprint = secretbox.Fingerprint(sealing)
	sealWith(t, f, sealing, "snmp-community")

	var autre = newKey(t)
	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, autre))
	if err == nil {
		t.Fatal("demarrage accepted despite une empreinte differente")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("mauvaise erreur : %v", err)
	}
	// Les deux empreintes doivent apparaitre : c'est ce qui rend le diagnostic
	// possible sans deviner.
	assertMentions(t, err.Error(),
		secretbox.ShortID(f.fingerprint), secretbox.KeyShortID(autre),
		"MESHCRAFT_SECRET_KEY", "-rekey")
	if f.setCalls != 0 {
		t.Fatal("l'empreinte a been rewritten despite le refus")
	}
}

// --- Sens 2 : la variable est RETIREE after la bascule ---

// Le cas inverse, aussi dangereux : after une bascule reussie, la key n'est plus
// en base. Un reboot sans la variable tirait autrefois une key NEUVE, la
// rangeait, et rescellait par-dessus au premier secret enregistre — melange
// irreparable. Ici : refus, et AUCUNE key neuve tiree.
func TestRefuseWhenEnvRemovedAfterSwitch(t *testing.T) {
	var f = newFake()
	var switched = newKey(t)
	// Etat laisse par le rechiffrement : empreinte enregistree, plus de key en
	// base, coffre scelle avec la key d'environnement.
	f.fingerprint = secretbox.Fingerprint(switched)
	sealWith(t, f, switched, "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("demarrage accepted sans key alors que le coffre est scelle")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("mauvaise erreur : %v", err)
	}
	assertMentions(t, err.Error(), "MESHCRAFT_SECRET_KEY", "plus de key en base")
	// LE point du test : pas de key bidon written en base. Une key neuve laissee
	// there serait prise pour la vraie par le lecteur suivant.
	if f.putCalls != 0 {
		t.Fatal("une key neuve a been tiree et rangee en base")
	}
	if f.setCalls != 0 {
		t.Fatal("l'empreinte a been rewritten despite le refus")
	}
}

// Variante sans empreinte : quelqu'un a supprime la ligne de key a la main sur
// une base dont le coffre n'est pas vide. Il n'y a plus rien pour lire, et tirer
// une key neuve condamnerait les secrets.
func TestRefuseWhenKeyGoneWithoutFingerprint(t *testing.T) {
	var f = newFake()
	sealWith(t, f, newKey(t), "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("demarrage accepted sans key sur un coffre non vide")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("mauvaise erreur : %v", err)
	}
	if f.putCalls != 0 {
		t.Fatal("une key neuve a been tiree sur une base dont le coffre est plein")
	}
}

// --- Les chemins qui doivent PASSER, sans bruit ---

// Une base already scellee par la key en base, sans empreinte : c'est state de
// toute instance existante au moment de la mise a jour. L'empreinte est
// enregistree, silencieusement, after verification que la key ouvre le coffre.
func TestRecordFingerprintWhenAbsent(t *testing.T) {
	var f = newFake()
	var enBase = newKey(t)
	f.instance[store.CredentialsKeyName] = enBase
	sealWith(t, f, enBase, "snmp-community", "MotDePasseAdmin42")

	var key, fromEnv, err = resolveVaultKey(context.Background(), f, "")
	if err != nil {
		t.Fatalf("demarrage refused sur une base saine : %v", err)
	}
	if fromEnv {
		t.Fatal("fromEnv devrait be faux")
	}
	if string(key) != string(enBase) {
		t.Fatal("mauvaise key retenue")
	}
	if f.setCalls != 1 {
		t.Fatalf("empreinte enregistree %d fois, expected 1", f.setCalls)
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(enBase)) {
		t.Fatal("l'empreinte enregistree n'est pas celle de la key active")
	}
}

// Premier demarrage d'une base vierge, sans variable : la key est tiree et
// rangee, l'empreinte enregistree. C'est le comportement historique, il ne doit
// pas regresser.
func TestFirstStartEmptyDB(t *testing.T) {
	var f = newFake()
	var key, fromEnv, err = resolveVaultKey(context.Background(), f, "")
	if err != nil {
		t.Fatalf("premier demarrage refused : %v", err)
	}
	if fromEnv || len(key) != 32 {
		t.Fatalf("key inattendue : fromEnv=%v len=%d", fromEnv, len(key))
	}
	if f.putCalls != 1 {
		t.Fatalf("key rangee %d fois, expected 1", f.putCalls)
	}
	if f.setCalls != 1 {
		t.Fatalf("empreinte enregistree %d fois, expected 1", f.setCalls)
	}
}

// Premiere installation AVEC la variable : coffre vide, rien a contredire. La
// garde ne doit pas transformer le mode recommande en obstacle.
func TestFirstStartWithEnvVar(t *testing.T) {
	var f = newFake()
	var env = newKey(t)
	var key, fromEnv, err = resolveVaultKey(context.Background(), f, hexKey(t, env))
	if err != nil {
		t.Fatalf("first installation avec la variable refused : %v", err)
	}
	if !fromEnv || string(key) != string(env) {
		t.Fatal("la key de l'environnement n'a pas been retenue")
	}
	if f.putCalls != 0 {
		t.Fatal("une key a been rangee en base alors que l'environnement en fournit une")
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(env)) {
		t.Fatal("empreinte incorrecte")
	}
}

// Apres la bascule, avec la bonne key : demarrage normal, et l'empreinte n'est
// pas rewritten (rien a changer).
func TestAfterSwitchWithCorrectKey(t *testing.T) {
	var f = newFake()
	var switched = newKey(t)
	f.fingerprint = secretbox.Fingerprint(switched)
	sealWith(t, f, switched, "snmp-community")

	var key, fromEnv, err = resolveVaultKey(context.Background(), f, hexKey(t, switched))
	if err != nil {
		t.Fatalf("demarrage refused avec la bonne key : %v", err)
	}
	if !fromEnv || string(key) != string(switched) {
		t.Fatal("key incorrecte")
	}
	if f.setCalls != 0 {
		t.Fatal("empreinte rewritten inutilement")
	}
}

// Une base rechiffree a la main hors de meshcraft — le coffre s'ouvre avec la
// key d'environnement, mais l'ancienne ligne de key traine encore et il n'y a
// pas d'empreinte. Le coffre est le juge : il repond oui, on demarre.
func TestManualSwitchRecognized(t *testing.T) {
	var f = newFake()
	var env = newKey(t)
	f.instance[store.CredentialsKeyName] = newKey(t) // ancienne key oubliee
	sealWith(t, f, env, "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, env))
	if err != nil {
		t.Fatalf("demarrage refused alors que la key ouvre le coffre : %v", err)
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(env)) {
		t.Fatal("empreinte incorrecte")
	}
}

// Une key hexadecimale invalide doit be refused AVANT tout access a la base :
// une variable mal collee (63 caracteres, un espace) ne doit pas declencher un
// diagnostic de coffre illisible, qui enverrait l'exploitant sur une fausse
// piste.
func TestInvalidHexKeyRejected(t *testing.T) {
	var f = newFake()
	var _, _, err = resolveVaultKey(context.Background(), f, "not-a-key")
	if err == nil {
		t.Fatal("key invalide accepted")
	}
	if !errors.Is(err, secretbox.ErrKeySize) {
		t.Fatalf("mauvaise erreur : %v", err)
	}
	if errors.Is(err, ErrKeyMismatch) {
		t.Fatal("une key mal formee ne doit pas se present  comme une key incompatible")
	}
}

// Une empreinte written par une derivation inconnue (base venue d'une version
// plus recente) doit produire un message franc, pas un refus « key
// incompatible » qui enverrait rechiffrer une base parfaitement saine.
func TestUnknownDerivationSurfaces(t *testing.T) {
	var f = newFake()
	f.algoErr = errors.New("empreinte de key calculee par une derivation inconnue")
	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("erreur d'algorithme avalee")
	}
	if !strings.Contains(err.Error(), "derivation inconnue") {
		t.Fatalf("message perdu : %v", err)
	}
}

func assertMentions(t *testing.T, s string, fragments ...string) {
	t.Helper()
	var frag string
	for _, frag = range fragments {
		if !strings.Contains(s, frag) {
			t.Errorf("le message ne mentionne pas %q.\nMessage :\n%s", frag, s)
		}
	}
}

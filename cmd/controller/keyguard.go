package main

// La garde de la key du coffre : decider QUELLE key sert, et refuser de demarrer
// quand ce n'est pas la bonne.
//
// LE TRAP QU'ON DESARME ICI. Le controleur prenait MESHCRAFT_SECRET_KEY from
// qu'elle was definie et ne regardait plus jamais la key rangee en base. Sur
// une instance neuve, tres bien. Sur une instance EN SERVICE, definir cette
// variable rendait tout le coffre illisible — et en silence, ce qui est le vrai
// probleme : le controleur demarrait, servait ses pages, repondait a son
// healthcheck, et le produit avait l'air de marcher. Ce qui s'arretait ne se
// voyait pas tout de suite :
//
//   - la supervision SNMP disparaissait sans un message, parce que
//     snmpWatchesFor (internal/api/watch_handlers.go) ecarte silencieusement
//     the device dont la community ne se dechiffre pas ;
//   - les mots de passe d'administration du parc devenaient inaccessibles, y
//     compris ceux que meshcraft a lui-same applied sur les devices — donc,
//     sur une gamme sans serial port, un retour aux reglages d'usine ;
//   - l'historique des generations, seul moyen de joindre un device reste en
//     arriere, partait avec.
//
// Et la discovery se faisait UI par UI, des jours plus tard, souvent
// after que les sauvegardes contenant encore l'ancienne key aient tourne.
//
// LE RAISONNEMENT. Le controleur enregistre en base une EMPREINTE a sens unique
// de la key qui a scelle (jamais la key, jamais de quoi la retrouver — cf.
// secretbox.Fingerprint), puis compare a chaque demarrage :
//
//   - egales → rien a dire, on demarre ;
//   - differentes → on REFUSE de demarrer, en nommant la cause et le geste.
//     Demarrer serait PIRE que refuser : un refus costs une indisponibilite
//     bruyante de quelques minutes, un demarrage costs la supervision, les
//     access au parc et le contenu du coffre, decouverts un UI a la fois ;
//   - absente (instance anterieure a la migration 0024) → on l'enregistre, sans
//     bruit… mais seulement after avoir VERIFIED que la key active ouvre bien ce
//     qui est already scelle. Enregistrer l'empreinte sans cette verification
//     rejouerait exactement le trap d'origine : quelqu'un qui met a jour le
//     code ET definit la variable dans le same geste ferait graver l'empreinte
//     de la mauvaise key, et la garde benirait la catastrophe.
//
// LE CAS INVERSE, aussi dangereux, que la same comparaison attrape : une
// instance basculee vers l'environnement puis redemarree SANS la variable
// tirerait une key neuve et rescellerait par-dessus. Ici on va plus loin que
// refuser — on ne tire same pas la key neuve (cf. InstanceSecretValue) : laisser
// une key bidon dans instance_secrets, c'est laisser le prochain lecteur la
// prendre pour la vraie.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"meshcraft/internal/config"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// keyStore est le strict necessary que la garde demande a la base.
//
// L'interface existe pour une raison precise : la decision de REFUSER est la
// partie qu'il faut absolument tester, et la tester contre un vrai PostgreSQL
// ferait un test qu'on n'execute pas. Avec elle, les quatre chemins de refus se
// verify en memory.
type keyStore interface {
	CredentialsKeyFingerprint(ctx context.Context) ([]byte, error)
	SetCredentialsKeyFingerprint(ctx context.Context, fingerprint []byte) error
	InstanceSecretValue(ctx context.Context, name string) ([]byte, error)
	PutInstanceSecret(ctx context.Context, name string, value []byte) error
	SealedSample(ctx context.Context, perColumn int) ([][]byte, error)
}

// ErrKeyMismatch : la key presented n'est pas celle qui a scelle cette base.
// C'est un refus de demarrer, pas un avertissement.
var ErrKeyMismatch = errors.New("key de chiffrement incompatible avec cette base")

// sampleSize : combien de valeurs scellees on sonde par colonne quand la base
// ne porte pas encore d'empreinte. Trois suffisent — il s'agit de savoir si la
// key ouvre le coffre, pas de le valider ligne a ligne (c'est le travail du
// rechiffrement, qui relit tout).
const sampleSize = 3

// resolveVaultKey choisit la key du coffre et verifies qu'elle est la bonne.
//
// Renvoie la key (32 octets) et si elle vient de l'environnement. Toute erreur
// renvoyee ici doit faire echouer le demarrage : c'est le sens de la garde.
func resolveVaultKey(ctx context.Context, ks keyStore, envKey string) ([]byte, bool, error) {
	var stored, err = ks.CredentialsKeyFingerprint(ctx)
	if err != nil {
		return nil, false, err
	}

	var key []byte
	var fromEnv bool
	if envKey != "" {
		if key, err = secretbox.ParseHexKey(envKey); err != nil {
			return nil, false, err
		}
		fromEnv = true
	} else {
		key, err = resolveKeyFromDB(ctx, ks, stored != nil)
		if err != nil {
			return nil, false, err
		}
	}

	var fingerprint = secretbox.Fingerprint(key)

	if stored != nil {
		if secretbox.SameFingerprint(stored, fingerprint) {
			return key, fromEnv, nil
		}
		return nil, false, mismatchError(ctx, ks, stored, fingerprint, fromEnv)
	}

	// Pas d'empreinte enregistree : il n'y a rien a comparer, donc on interroge le
	// coffre lui-same. C'est le seul juge fiable — si la key active ouvre ce qui
	// est already scelle, elle est bien celle qui a scelle.
	if err = probeVault(ctx, ks, key, fromEnv); err != nil {
		return nil, false, err
	}
	if err = ks.SetCredentialsKeyFingerprint(ctx, fingerprint); err != nil {
		return nil, false, err
	}
	slog.Info("empreinte de la key du coffre enregistree : un demarrage avec une "+
		"autre key sera now refused au lieu de rendre les secrets illisibles",
		"key_id", secretbox.ShortID(fingerprint), "key_external", fromEnv)
	return key, fromEnv, nil
}

// resolveKeyFromDB lit la key rangee en base, et ne la CREATES que si son absence
// est legitime : premier demarrage d'une base vierge.
//
// `hasFingerprint` dit que cette base a already been scellee par une key connue. Si
// la ligne manque alors qu'une empreinte existe, la conclusion est nette : la
// bascule vers l'environnement a eu lieu (le rechiffrement supprime la key) et
// on vient de reboot sans la variable. Tirer une key neuve ici serait le
// scenario inverse du trap d'origine, avec le same result.
func resolveKeyFromDB(ctx context.Context, ks keyStore, hasFingerprint bool) ([]byte, error) {
	var key, err = ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if hasFingerprint {
		return nil, fmt.Errorf(`%w

MESHCRAFT_SECRET_KEY n'est pas definie, et il n'y a plus de key en base.

Cette instance a been basculee vers une key fournie par l'environnement : le
rechiffrement a retire l'ancienne key de la table instance_secrets, parce qu'une
key devenue inutile n'est plus qu'un secret qui attend de fuiter.

Le controleur REFUSE de demarrer rather que de tirer une key neuve : cette key
neuve ne dechiffrerait rien, et le premier secret enregistre ensuite rescellerait
par-dessus l'empreinte — la base deviendrait un melange irreparable.

CE QU'IL FAUT FAIRE : redefinissez MESHCRAFT_SECRET_KEY (64 caracteres
hexadecimaux) avec la key used lors de la bascule, puis redemarrez. Elle est
dans le .env de l'instance, et dans le coffre where vous l'avez rangee. Sans elle,
seule une restauration de sauvegarde anterieure a la bascule recupere les
secrets.`, ErrKeyMismatch)
	}

	// Ni empreinte, ni key. Base vierge ? On verifies avant de l'affirmer : un
	// coffre non vide sans key pour l'ouvrir n'est pas un premier demarrage, et
	// tirer une key au hasard le condamnerait.
	var sample [][]byte
	if sample, err = ks.SealedSample(ctx, 1); err != nil {
		return nil, err
	}
	if len(sample) > 0 {
		return nil, fmt.Errorf(`%w

MESHCRAFT_SECRET_KEY n'est pas definie, la table instance_secrets ne contient
aucune key, et le coffre contient pourtant des secrets scelles.

Aucune key de cette base ne peut donc les ouvrir. Tirer une key neuve les
condamnerait definitivement, le controleur refuse donc de demarrer.

CE QU'IL FAUT FAIRE : definissez MESHCRAFT_SECRET_KEY avec la key qui a scelle
ces valeurs, ou restaurez une sauvegarde contenant la ligne
instance_secrets/credentials-key.`, ErrKeyMismatch)
	}

	// Premier demarrage reel : on tire la key et on la range. La relecture after
	// write n'est pas une coquetterie — `on conflict do nothing` veut dire que
	// deux demarrages simultanes en write une seule, et c'est CELLE-LA qu'il
	// faut utiliser.
	var fresh []byte
	if fresh, err = secretbox.NewRandomKey(); err != nil {
		return nil, err
	}
	if err = ks.PutInstanceSecret(ctx, store.CredentialsKeyName, fresh); err != nil {
		return nil, err
	}
	return ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
}

// probeVault verifies que `key` ouvre au moins une valeur already scellee.
//
// Un coffre vide passe : il n'y a rien a contredire. Un coffre plein dont rien
// ne s'ouvre est le trap d'origine pris la main dans le sac — typiquement une
// mise a jour du code et une definition de MESHCRAFT_SECRET_KEY dans le same
// geste, sur une instance en service.
func probeVault(ctx context.Context, ks keyStore, key []byte, fromEnv bool) error {
	var sample, err = ks.SealedSample(ctx, sampleSize)
	if err != nil {
		return err
	}
	if len(sample) == 0 {
		return nil
	}
	var box *secretbox.Service
	if box, err = secretbox.New(key, fromEnv); err != nil {
		return err
	}
	var enc []byte
	var probeErr error
	for _, enc = range sample {
		_, probeErr = box.Open(enc)
		if probeErr == nil {
			return nil
		}
	}
	return fmt.Errorf(`%w

Les secrets de cette base ont been scelles avec une AUTRE key que celle fournie.
Aucune des %d valeurs sondees ne s'ouvre avec la key active (empreinte %s).

Cette base n'avait pas encore d'empreinte de key enregistree, le controleur a
donc interroge le coffre lui-same — et le coffre repond non.

CE QUI SE PASSE PROBABLEMENT : MESHCRAFT_SECRET_KEY vient d'be definie sur une
instance existante dont les secrets were scelles par la key rangee en base
(instance_secrets/credentials-key). Demarrer ainsi ferait bien plus de degats que
refuser : le produit fonctionnerait EN APPARENCE, puis perdrait la supervision
SNMP (les devices dont la community ne se dechiffre pas sont ecartes sans
message), les mots de passe d'administration du parc et tout le contenu du
coffre — un UI a la fois, sur plusieurs jours.

CE QU'IL FAUT FAIRE, l'une des deux :

 1. BASCULER POUR DE BON — rechiffrer le coffre avec la nouvelle key. Sauvegardez
    d'abord la base, arretez le controleur, puis lancez le rechiffrement avec la
    NOUVELLE key dans l'environnement :

        docker compose stop controller
        docker compose run --rm controller -rekey

    Le rechiffrement lit chaque valeur avec l'ancienne key (celle de la base),
    la rescelle avec la nouvelle, relit tout pour verify, et does not write qu'en une
    seule transaction : une coupure au milieu ne laisse rien a half fait.

 2. REVENIR EN ARRIERE — retirer MESHCRAFT_SECRET_KEY de l'environnement et
    reboot. L'instance repart sur la key en base, exactement comme avant, et
    rien n'a been perdu : le controleur a refused de demarrer AVANT to write quoi
    que ce soit.`,
		ErrKeyMismatch, len(sample), secretbox.KeyShortID(key))
}

// mismatchError construit le refus quand une empreinte EXISTE et differe.
//
// Le message dit dans quel sens ca s'est passe, parce que le geste de
// reparation n'est pas le same : si la key est encore en base, la bascule reste
// possible ; sinon, il faut retrouver la key.
func mismatchError(ctx context.Context, ks keyStore, stored, active []byte, fromEnv bool) error {
	var keyInDB = true
	var _, err = ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if errors.Is(err, store.ErrNotFound) {
		keyInDB = false
	}

	var source = "la key rangee en base (instance_secrets/credentials-key)"
	if fromEnv {
		source = "MESHCRAFT_SECRET_KEY"
	}

	var remedy string
	switch {
	case fromEnv && keyInDB:
		remedy = `L'ancienne key est TOUJOURS en base : la bascule est donc encore possible et
sans risque de perte. Sauvegardez la base, arretez le controleur, puis rechiffrez
le coffre avec la nouvelle key dans l'environnement :

    docker compose stop controller
    docker compose run --rm controller -rekey

Ou, pour repartir immediatement en service : retirez MESHCRAFT_SECRET_KEY et
redemarrez. Rien n'aura been perdu.`
	case fromEnv:
		remedy = `Il n'y a plus de key en base : cette instance a already been basculee, et la valeur
de MESHCRAFT_SECRET_KEY n'est pas celle qui a servi.

Remettez la key de la bascule (celle notee au moment du rechiffrement) et
redemarrez. Si elle est definitivement perdue, seule une restauration de
sauvegarde anterieure a la bascule recupere les secrets — le rechiffrement est
impossible sans key de lecture.`
	default:
		remedy = `MESHCRAFT_SECRET_KEY n'est pas definie : le controleur est reparti sur la key de
la base, alors que le coffre a been scelle avec une autre — typiquement une
bascule vers l'environnement, suivie d'un reboot sans la variable.

Redefinissez MESHCRAFT_SECRET_KEY avec la key de la bascule et redemarrez.`
	}

	return fmt.Errorf(`%w

Empreinte enregistree en base : %s
Empreinte de la key active   : %s   (source : %s)

Les secrets de cette base ont been scelles avec une autre key que celle que le
controleur vient de recevoir. Il REFUSE de demarrer.

Ce refus est deliberate, et il est le contraire d'une regression : demarrer
laisserait le produit fonctionner en apparence tout en perdant, un UI a la
fois, la supervision SNMP (les devices dont la community ne se dechiffre pas
sont ecartes sans message), les mots de passe d'administration du parc et le
contenu du coffre. Pire encore, le premier secret enregistre ensuite serait
scelle avec la nouvelle key : la base deviendrait un melange des deux, que plus
aucun rechiffrement ne peut demeler.

%s`,
		ErrKeyMismatch, secretbox.ShortID(stored), secretbox.ShortID(active), source, remedy)
}

// initSecrets prepare le chiffrement des identifiants du coffre.
//
// MESHCRAFT_SECRET_KEY (64 caracteres hexadecimaux) garde la key HORS de la
// base : c'est le mode a privilegier. Sans elle, une key est tiree au premier
// demarrage et rangee dans la base — le controleur le dit clairement rather que
// de laisser croire a une protection qu'il n'offre pas.
//
// Le choix de la key et le refus de demarrer sur la mauvaise sont dans
// resolveVaultKey ci-dessus, avec le raisonnement complet.
func initSecrets(ctx context.Context, cfg config.Config, st *store.Store) (*secretbox.Service, error) {
	var key, fromEnv, err = resolveVaultKey(ctx, st, cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	if !fromEnv {
		slog.Warn("MESHCRAFT_SECRET_KEY absente : la key de chiffrement des " +
			"identifiants est stockee dans la base. Une fuite du dump exposerait " +
			"les secrets. Definissez MESHCRAFT_SECRET_KEY (64 caracteres hex) en " +
			"production, puis rechiffrez le coffre avec « controller -rekey » — " +
			"definir la variable SEULE ne bascule rien et sera refused au demarrage.")
	}
	return secretbox.New(key, fromEnv)
}

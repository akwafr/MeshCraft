package main

// Le mode `-rekey` du controleur : bascule la key du coffre de la base vers
// l'environnement, en rechiffrant tout ce qui est scelle.
//
// POURQUOI UN MODE DU BINAIRE, ET PAS AUTRE CHOSE. Trois formes were
// possibles, et le choix n'est pas cosmetique :
//
//   - Une route d'API serait le PIRE choix. Elle laisserait en permanence, sur
//     un produit expose, un point ingress capable de rewrite tout le coffre en
//     une request. La bascule est un acte qui arrive une fois dans la vie d'une
//     instance ; lui dedier une surface permanente, c'est payer un risque
//     permanent pour un usage unique. Elle ne pourrait pas non plus garantir ce
//     qui compte le plus ici : que personne d'autre does not write pendant l'operation,
//     puisqu'elle serait justement servie par le processus qui written.
//
//   - Un binaire de plus (cmd/rekey) obligerait a toucher le Dockerfile, a
//     distribuer et versionner un outil supplementaire, et surtout a DUPLIQUER
//     la logique de key. Le jour where les deux copies divergent, l'outil de
//     bascule bascule autre chose que ce que le controleur lit.
//
//   - Un mode du controleur, retenu : la logique de key est already ici
//     (keyguard.go), il n'y a rien a distribuer, l'image existante sait le faire
//     (`docker compose run --rm controller -rekey`), et le processus qui
//     rechiffre est exactement celui qui verifiera l'empreinte au demarrage
//     suivant. Le mode ne demarre PAS le serveur HTTP : il fait son travail,
//     imprime son compte rendu et sort.
//
// La sortie est volontairement bavarde et chiffree : un exploitant doit pouvoir
// comparer ligne a ligne avec l'inventaire des colonnes scellees. « Termine »
// sans compte n'est pas verifiable.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"meshcraft/internal/config"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// runRekey execute la bascule. Renvoie une erreur ; l'appelant sort en code 1.
//
// force : passe outre le garde-fou « d'autres connexions a la base sont
// ouvertes ». Il existe parce que ce garde-fou peut se tromper (un pgbouncer, un
// psql d'observation), mais son default est de refuser : la faute qu'il attrape —
// un controleur reste allume, qui garde l'ancienne key en memory et continuera
// de sceller avec elle after le commit — costs des secrets perdus.
func runRekey(ctx context.Context, cfg config.Config, st *store.Store, force bool) error {
	fmt.Println("=== meshcraft — rechiffrement du coffre a identifiants ===")
	fmt.Println()

	// La NOUVELLE key vient de l'environnement. Refuser tot et clairement :
	// lancer le rechiffrement sans savoir vers quoi on bascule n'a aucun sens.
	if cfg.SecretKey == "" {
		return errors.New("MESHCRAFT_SECRET_KEY est vide.\n\n" +
			"Le rechiffrement bascule le coffre de la key rangee en base VERS une key\n" +
			"fournie par l'environnement : la nouvelle key doit donc be dans\n" +
			"MESHCRAFT_SECRET_KEY (64 caracteres hexadecimaux).\n\n" +
			"    openssl rand -hex 32\n\n" +
			"NOTEZ-LA AVANT DE LANCER L'OPERATION, dans votre coffre a secrets. Apres la\n" +
			"bascule, elle est le SEUL moyen de lire les secrets de cette base : le\n" +
			"rechiffrement retire l'ancienne key, et aucune copie ne reste cote serveur.")
	}
	var newKey, err = secretbox.ParseHexKey(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("MESHCRAFT_SECRET_KEY : %w", err)
	}

	// L'ANCIENNE key se lit en base, et sans la create : si elle n'y est plus,
	// c'est soit une bascule already faite, soit une base qu'on ne peut plus lire.
	// Les deux se traitent, mais pas de la same way.
	var oldKey []byte
	oldKey, err = st.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if errors.Is(err, store.ErrNotFound) {
		return alreadySwitched(ctx, st, newKey)
	}
	if err != nil {
		return err
	}

	if secretbox.SameFingerprint(secretbox.Fingerprint(oldKey), secretbox.Fingerprint(newKey)) {
		return errors.New("MESHCRAFT_SECRET_KEY contient ALREADY la key rangee en base.\n\n" +
			"Rechiffrer n'apporterait rien : la key serait la same de chaque cote, et la\n" +
			"sortir de la base sans la changer laisserait une copie de la key de\n" +
			"production dans les sauvegardes existantes.\n\n" +
			"Engendrez une key NEUVE (openssl rand -hex 32) et relancez.")
	}

	// Garde-fou : un controleur reste allume garde l'ancienne key en memory.
	// L'operation elle-same resterait atomique (les verrous s'en chargent), mais
	// tout secret enregistre AFTER le commit par ce processus-there serait scelle
	// avec une key que la base ne reconnait plus.
	var others int
	if others, err = st.OtherConnections(ctx); err != nil {
		return err
	}
	if others > 0 && !force {
		return fmt.Errorf(`%d autre(s) connexion(s) sont ouvertes sur cette base.

Le controleur est probablement encore en service. Il garde l'ANCIENNE key en
memory : after le rechiffrement, chaque secret qu'il enregistrerait serait
scelle avec une key que la base ne reconnait plus — et perdu.

    docker compose stop controller
    docker compose run --rm controller -rekey

Si vous savez que ces connexions sont inoffensives (psql d'observation,
pgbouncer), relancez avec -rekey-force.`, others)
	}
	if others > 0 {
		fmt.Printf("ATTENTION : %d autre(s) connexion(s) sur la base, ignorees (-rekey-force).\n\n", others)
	}

	// Inventaire AVANT. C'est lui que l'exploitant compare au compte rendu : un
	// total qui tombe juste par rapport a un inventaire pris after coup ne prouve
	// rien.
	var before []store.RekeyCount
	if before, err = st.CountSealed(ctx); err != nil {
		return err
	}
	fmt.Println("Inventaire AVANT (valeurs scellees non vides) :")
	printCounts(before, false)
	fmt.Println()
	fmt.Printf("Ancienne key (base)      : empreinte %s\n", secretbox.KeyShortID(oldKey))
	fmt.Printf("Nouvelle key (env)       : empreinte %s\n", secretbox.KeyShortID(newKey))
	fmt.Println()

	var report store.RekeyReport
	report, err = st.RekeyCredentials(ctx, oldKey, newKey, store.RekeyOptions{
		// 30 s : assez pour laisser passer une write normale qui trainait, trop
		// peu pour rester pendu sans explication si le controleur written encore.
		LockTimeout: 30 * time.Second,
	})
	if err != nil {
		// Le compte rendu partiel est imprime quand same : savoir where ca s'est
		// stopped aide, same si rien n'a been written.
		if len(report.Columns) > 0 {
			fmt.Println("Progression au moment de failure (RIEN N'A ETE WRITES) :")
			printCounts(report.Columns, true)
			fmt.Println()
		}
		return fmt.Errorf(`le rechiffrement a echoue : %w

LA TRANSACTION A ETE ANNULEE : la base est exactement dans state where elle was
avant l'operation, entirely lisible avec l'ancienne key. L'instance redemarre
sans MESHCRAFT_SECRET_KEY comme avant.`, err)
	}

	fmt.Println("Compte rendu (transaction validee) :")
	printCounts(report.Columns, true)
	var rows, resealed, already = report.Total()
	fmt.Println()
	fmt.Printf("  total          : %d valeur(s) scellee(s)\n", rows)
	fmt.Printf("  rescellees     : %d\n", resealed)
	fmt.Printf("  already a jour    : %d\n", already)
	fmt.Printf("  RELUES ET DECHIFFREES avec la nouvelle key avant validation : %d\n", report.Verified)
	if report.OldKeyDeleted {
		fmt.Println("  ancienne key   : SUPPRIMEE de instance_secrets " +
			"(une key devenue inutile n'est plus qu'une fuite en attente)")
	}
	fmt.Printf("  empreinte enregistree : %s\n", secretbox.KeyShortID(newKey))
	fmt.Println()

	// La comparaison avant/after est faite ICI, pas laissee a l'œil de
	// l'exploitant : un total qui ne tombe pas juste doit se voir tout seul.
	var mismatch = compareInventories(before, report.Columns)
	if mismatch != "" {
		fmt.Println("ANOMALIE DE COMPTE : " + mismatch)
		fmt.Println("La transaction est validee et verified ; signalez tout de same cet ecart.")
		fmt.Println()
	}

	fmt.Println("Termine. MESHCRAFT_SECRET_KEY est now INDISPENSABLE au demarrage :")
	fmt.Println("elle est le seul moyen de lire ce coffre. Verifiez qu'elle est bien dans le")
	fmt.Println(".env de l'instance ET dans votre coffre a secrets, puis redemarrez :")
	fmt.Println()
	fmt.Println("    docker compose up -d controller")
	return nil
}

// alreadySwitched traite le cas « plus de key en base ».
//
// Rejouer la bascule doit be sans danger : c'est la property qui permet de
// relancer sereinement after une interruption. Si l'empreinte enregistree est
// celle de la key fournie, il n'y a rien a faire et on le dit. Sinon, il n'y a
// aucune key de LECTURE disponible et le rechiffrement est impossible — refuser
// en le disant vaut mille fois mieux que tenter quelque chose.
func alreadySwitched(ctx context.Context, st *store.Store, newKey []byte) error {
	var stored, err = st.CredentialsKeyFingerprint(ctx)
	if err != nil {
		return err
	}
	if stored != nil && secretbox.SameFingerprint(stored, secretbox.Fingerprint(newKey)) {
		fmt.Println("Rien a faire : cette base est ALREADY basculee sur la key fournie.")
		fmt.Printf("  empreinte en base = empreinte de MESHCRAFT_SECRET_KEY = %s\n",
			secretbox.KeyShortID(newKey))
		fmt.Println("  aucune key ne subsiste dans instance_secrets.")
		fmt.Println()
		var counts []store.RekeyCount
		if counts, err = st.CountSealed(ctx); err != nil {
			return err
		}
		fmt.Println("Inventaire (valeurs scellees, lisibles avec cette key) :")
		printCounts(counts, false)
		return nil
	}
	var what = "  aucune empreinte n'est enregistree dans cette base."
	if stored != nil {
		what = fmt.Sprintf("  empreinte enregistree en base : %s\n"+
			"  empreinte de MESHCRAFT_SECRET_KEY : %s",
			secretbox.ShortID(stored), secretbox.KeyShortID(newKey))
	}
	return fmt.Errorf(`aucune key a lire.

La table instance_secrets ne contient plus de ligne « %s », et :

%s

Le rechiffrement a besoin de DEUX keys : celle qui lit et celle qui written. Sans
key de lecture, il n'y a rien a rechiffrer — et surtout rien a tenter : write
quoi que ce soit ici transformerait un coffre illisible en coffre detruit.

CE QU'IL FAUT FAIRE : retrouvez la key avec laquelle ces secrets ont been scelles
et mettez-la dans MESHCRAFT_SECRET_KEY — le demarrage normal du controleur
suffira alors, sans rechiffrement. Si elle est perdue, restaurez une sauvegarde
anterieure a la bascule.`, store.CredentialsKeyName, what)
}

// printCounts imprime l'inventaire, une ligne par colonne scellee.
func printCounts(counts []store.RekeyCount, detail bool) {
	var c store.RekeyCount
	for _, c = range counts {
		if detail {
			fmt.Printf("  %-24s %-12s  %3d trouvee(s)  %3d rescellee(s)  %3d already a jour\n",
				c.Table, c.Column, c.Rows, c.Resealed, c.Already)
			continue
		}
		fmt.Printf("  %-24s %-12s  %3d\n", c.Table, c.Column, c.Rows)
	}
}

// compareInventories confronte le compte d'avant au compte rendu. Rend une
// description de l'ecart, vide si tout tombe juste.
func compareInventories(before, after []store.RekeyCount) string {
	if len(before) != len(after) {
		return "l'inventaire d'avant et le compte rendu ne portent pas sur le same " +
			"nombre de colonnes"
	}
	var i int
	for i = range before {
		if before[i].Table != after[i].Table || before[i].Column != after[i].Column {
			return "les colonnes de l'inventaire et du compte rendu ne sont pas les memes"
		}
		if before[i].Rows != after[i].Rows {
			return fmt.Sprintf("%s.%s : %d valeur(s) avant, %d traitee(s)",
				before[i].Table, before[i].Column, before[i].Rows, after[i].Rows)
		}
		if before[i].Rows != after[i].Resealed+after[i].Already {
			return fmt.Sprintf("%s.%s : %d valeur(s) mais %d rescellee(s) + %d already a jour",
				before[i].Table, before[i].Column, before[i].Rows,
				after[i].Resealed, after[i].Already)
		}
	}
	return ""
}

// exitRekey enrobe runRekey pour le main : imprime l'erreur sur la sortie
// d'erreur telle quelle (les messages sont multilignes et written pour be lus
// par un humain, pas indexes par un agregateur de logs) et sort.
func exitRekey(ctx context.Context, cfg config.Config, st *store.Store, force bool) {
	var err = runRekey(ctx, cfg, st, force)
	if err != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "FAILURE : "+err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

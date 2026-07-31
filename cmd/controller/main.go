// Le controleur meshcraft : API, SPA embarquee, registre docker du proxy.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"meshcraft/internal/api"
	"meshcraft/internal/auth"
	"meshcraft/internal/config"
	"meshcraft/internal/debuglog"
	"meshcraft/internal/distrib"
	"meshcraft/internal/relocate"
	"meshcraft/internal/renumber"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
	"meshcraft/internal/tsdb"
	"meshcraft/internal/tunnel"
)

// Injectes au build par -ldflags (voir Dockerfile et Makefile) :
//
//	version = version produit (fichier VERSION, ex. 0.1.0) ;
//	build   = horodatage de build UTC AAAAMMJJ.HHMMSS, ce qui identifie
//	          actually l'image deployee
//	          (cf. docs/01-Architecture/Versionnage et mise a jour du proxy).
var (
	version = "dev"
	build   = "dev"
)

func main() {
	// Drapeaux. Le controleur n'en avait aucun ; `-rekey` en introduit un parce
	// que la bascule de key du coffre est un ACTE D'EXPLOITATION, pas une route
	// d'API : lui donner une surface HTTP permanente serait laisser en
	// permanence, sur un produit expose, de quoi rewrite tout le coffre en une
	// request. Cf. l'en-tete de rekey.go pour l'argumentaire complet.
	var doRekey = flag.Bool("rekey", false,
		"rechiffre le coffre a identifiants avec MESHCRAFT_SECRET_KEY, puis sort "+
			"(le serveur HTTP ne demarre pas)")
	var rekeyForce = flag.Bool("rekey-force", false,
		"avec -rekey : passe outre le refus quand d'autres connexions a la base "+
			"sont ouvertes (le controleur DOIT be stopped)")
	flag.Parse()

	// Le journal part sur la sortie standard COMME AVANT, et en plus dans un
	// anneau en memory que l'administration peut consulter en direct
	// (cf. docs/01-Architecture/Monitoring unifie et pipelines §6). « En plus »,
	// jamais « a la place » : detourner le flux ferait perdre les journaux du
	// conteneur au moment where on en a le plus besoin.
	var debugRing = debuglog.NewRing(3000)
	slog.SetDefault(slog.New(debuglog.NewHandler(
		slog.NewJSONHandler(os.Stdout, nil), debugRing)))
	var cfg = config.Load()
	var ctx = context.Background()

	// La base peut mettre quelques secondes a accepter les connexions
	// (demarrage docker compose) : on retente brievement.
	var st *store.Store
	var err error
	var attempt int
	for attempt = 0; attempt < 30; attempt++ {
		st, err = store.New(ctx, cfg.DatabaseURL)
		if err == nil {
			break
		}
		slog.Info("attente de la base de data", "attempt", attempt+1, "err", err.Error())
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		slog.Error("connexion base impossible", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err = st.Migrate(ctx); err != nil {
		slog.Error("migrations", "err", err)
		os.Exit(1)
	}

	// Le mode rechiffrement stops ici : il ne creates pas de compte admin, ne
	// rattrape aucun identifiant et ne sert pas une seule request. Il fait la
	// bascule, imprime son compte rendu et sort. Les migrations, en revanche, ont
	// bien been appliquees — la table d'empreinte (0024) lui est necessary.
	if *doRekey {
		exitRekey(ctx, cfg, st, *rekeyForce)
	}

	if err = bootstrapAdmin(ctx, cfg, st); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	var secrets *secretbox.Service
	secrets, err = initSecrets(ctx, cfg, st)
	if err != nil {
		// Sur la sortie d'erreur EN CLAIR, pas seulement dans le journal JSON :
		// le refus de demarrer sur une key incompatible est un message multiligne
		// written pour be lu par un humain qui regarde `docker compose logs`, et
		// qui doit y trouver le geste de reparation. Encapsule en JSON, il
		// devient une ligne de 2 000 caracteres avec des \n litteraux — i.e.
		// illisible, au moment precise where il faut le lire.
		if errors.Is(err, ErrKeyMismatch) {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "REFUS DE DEMARRER — "+err.Error())
			fmt.Fprintln(os.Stderr)
		}
		slog.Error("key de chiffrement : refus de demarrer", "err", err)
		os.Exit(1)
	}

	// Rattrapage des sites created avant l'automatisme : chacun receives son
	// identifiant d'administration s'il n'en a pas. Apres l'initialisation de
	// la key, forcement — c'est elle qui scelle le secret.
	if err = api.BackfillSiteCredentials(ctx, st, secrets); err != nil {
		slog.Error("identifiants de site", "err", err)
	}

	// Local proxy is the controller itself: it never heartbeats over WSS, so mark it
	// present at startup with the product version (UI also special-cases internal).
	var touchErr = st.TouchProxy(ctx, store.LocalProxyID, version, 0, "", "", nil)
	if touchErr != nil {
		slog.Warn("touch proxy Local", "err", touchErr)
	}

	// Purge periodique des sessions expirees et des candidats disparus.
	go func() {
		var t = time.NewTicker(time.Hour)
		for range t.C {
			var bg = context.Background()
			_ = st.PurgeExpiredSessions(bg)
			// Un hote plus vu depuis 30 jours et jamais adopte n'a plus rien a
			// faire dans la file d'adoption.
			var purged, purgeErr = st.PurgeStaleDiscovered(bg, 30*24*time.Hour)
			if purgeErr == nil && purged > 0 {
				slog.Info("candidats stale purges", "count", purged)
			}
		}
	}()

	var srv = &api.Server{
		Cfg:      cfg,
		Version:  version,
		Build:    build,
		Store:    st,
		Auth:     &auth.Service{Store: st},
		Distrib:  distrib.New(cfg),
		Secrets:  secrets,
		Tunnels:  tunnel.NewHub(),
		Relocate: relocate.NewTracker(),
		Renumber: renumber.NewTracker(),
		// Pose automatique : configurer dans l'interface vaut approbation
		// (cf. internal/api/autoapply.go).
		Apply: api.NewAutoApplier(),
		Debug: debugRing,
		// Series temporelles. Absent = meshcraft fonctionne sans metriques et le
		// dit ; l'inventaire n'en a pas besoin.
		TSDB: tsdb.New(os.Getenv("MESHCRAFT_METRICS_URL")),
	}
	// LES RATES PAR PORT, derives des compteurs et reportes dans l'inventaire.
	//
	// Apres la construction du serveur parce que c'est lui qui porte le stockage de
	// series et la base ; en arriere-plan parce que rien de ce que fait cette boucle
	// n'est necessary pour repondre a une request. Sans stockage configure, elle le dit
	// une fois et stops (cf. RunPortRates).
	//
	// C'est la piece qui rendait la telemetry VISIBLE : trois ecrans lisaient already
	// `rx_mbps` / `tx_mbps` sur chaque port, et aucun code ne les ecrivait.
	go srv.RunPortRates(context.Background())

	slog.Info("meshcraft controller started", "listen", cfg.Listen,
		"version", version, "build", build)
	var httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err = httpSrv.ListenAndServe(); err != nil {
		slog.Error("serveur http", "err", err)
		os.Exit(1)
	}
}

// bootstrapAdmin creates le premier compte global-admin si la base est vide.
// Mot de passe : MESHCRAFT_ADMIN_PASSWORD, sinon genere et affiche UNE fois.
func bootstrapAdmin(ctx context.Context, cfg config.Config, st *store.Store) error {
	var n, err = st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	var password = cfg.AdminPassword
	var generated = false
	if password == "" {
		var raw = make([]byte, 12)
		if _, err = rand.Read(raw); err != nil {
			return err
		}
		password = hex.EncodeToString(raw)
		generated = true
	}
	var hash, hashErr = auth.HashPassword(password)
	if hashErr != nil {
		return hashErr
	}
	if _, err = st.CreateUser(ctx, cfg.AdminEmail, hash, "Administrateur", true); err != nil {
		return err
	}
	if generated {
		slog.Warn("compte admin initial created — NOTEZ CE MOT DE PASSE, il ne sera plus affiche",
			"email", cfg.AdminEmail, "password", password)
	} else {
		slog.Info("compte admin initial created", "email", cfg.AdminEmail)
	}
	return nil
}

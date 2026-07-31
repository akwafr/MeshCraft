package distrib

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version cible du proxy et descripteur de mise a jour.
//
// La source de verite est ce que le controleur SERT actually : le fichier
// `artifacts/VERSION` (horodatage de build, written par le Dockerfile) et les
// binaires eux-memes, haches a la demande. La note de specification parlait d'un
// `manifest.json` written au build ; hacher les fichiers servis est strictement
// plus honnete qu'un manifeste applied a cote d'eux, qui pourrait mentir.
//
// Cf. docs/01-Architecture/Versionnage et mise a jour du proxy.

// Upgrade describes la mise a jour proposee a un proxy.
type Upgrade struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type versionCache struct {
	mu      sync.Mutex
	value   string
	mtime   time.Time
	checked time.Time
}

var artifactsVersion versionCache

// TargetVersion renvoie la version des binaires servis, ou "" si le controleur
// n'en sert pas (build local, repertoire absent). Vide = aucune mise a jour ne
// sera proposee : mieux vaut ne rien proposer que proposer l'inconnu.
func (s *Service) TargetVersion() string {
	var path = filepath.Join(s.Cfg.ArtifactsDir, "VERSION")
	artifactsVersion.mu.Lock()
	defer artifactsVersion.mu.Unlock()
	// Relecture au plus une fois par seconde : le heartbeat de tout un parc ne
	// doit pas se transformer en rafale de stat().
	if time.Since(artifactsVersion.checked) < time.Second {
		return artifactsVersion.value
	}
	artifactsVersion.checked = time.Now()
	var info, err = os.Stat(path)
	if err != nil {
		artifactsVersion.value = ""
		return ""
	}
	if info.ModTime().Equal(artifactsVersion.mtime) && artifactsVersion.value != "" {
		return artifactsVersion.value
	}
	var data, readErr = os.ReadFile(path)
	if readErr != nil {
		artifactsVersion.value = ""
		return ""
	}
	artifactsVersion.mtime = info.ModTime()
	artifactsVersion.value = strings.TrimSpace(string(data))
	return artifactsVersion.value
}

// buildStamp reconnait un horodatage de build UTC AAAAMMJJ.HHMMSS, et RIEN d'autre.
//
// Reconnaitre la forme n'est pas une politesse de validation : c'est ce qui distingue
// « plus ancien » de « incomparable », et les deux menent a des decisions opposees.
var buildStamp = regexp.MustCompile(`^\d{8}\.\d{6}$`)

// IsNewer dit si `candidate` doit REMPLACER `current`.
//
// Le format AAAAMMJJ.HHMMSS est a largeur fixe : entre deux horodatages, la
// comparaison lexicographique EST la comparaison chronologique, sans parseur.
//
// # LE TRAP QUE CETTE FONCTION PORTAIT, ET CE QU'IL A COSTE
//
// Le corps was `candidate > current`, et son commentaire affirmait qu'une version
// d'un autre format « n'est jamais plus recente — elle est simplement differente, ce
// qui suffit a decider ». It was faux, et faux dans le sens le plus couteux :
//
//	« 20260729.213000 » > « dev »  →  FAUX
//
// Les chiffres se trient AVANT les lettres en ASCII. Un proxy dont la version est
// « dev » est donc plus « recent » que tout build passe, present et futur : le
// controleur ne lui propose RIEN, l'interface l'affiche « a jour », et il le reste
// pour toujours. C'est tres exactement ce qui a been observed sur `proxy-akwalan`,
// annonce `up_to_date` en version `dev` — un `docker compose pull` ne pouvait rien y
// changer non plus, puisque le registre integrated servait le same build.
//
// Et cela contredisait la rule de cette page : « aucune intervention sur la machine
// du proxy, jamais ». Un proxy bloque a `dev` ne se repare QUE sur sa machine.
//
// # LA RULE, DANS LES DEUX SENS
//
//   - une CANDIDATE qui n'est pas un horodatage n'est jamais proposee. C'est le
//     principe already written pour la version cible vide : mieux vaut ne rien proposer que
//     proposer l'inconnu ;
//   - une COURANTE qui n'est pas un horodatage est a REMPLACER, sans comparaison. Elle
//     ne se situe pas dans le temps, donc il n'y a rien a comparer — et un proxy qui
//     porte une version que la string de build ne fabrique plus (cf. le garde-fou du
//     Dockerfile) est precisely celui qu'il faut sortir de there.
//
// La consequence a connaitre : un proxy compile a la main, hors `make build`, se verra
// proposer le build empaquete du controleur. C'est voulu. Le refuser demande
// `auto_upgrade = false` ou un epinglage, i.e. une decision explicite, pas un
// effet de bord d'une comparaison de strings.
func IsNewer(candidate, current string) bool {
	if !buildStamp.MatchString(candidate) {
		return false
	}
	if !buildStamp.MatchString(current) {
		return true
	}
	return candidate > current
}

// UpgradeFor construit le descripteur pour l'architecture annoncee par le proxy.
// Renvoie nil si rien n'est servi pour cette plateforme : un proxy armv7 sur un
// controleur qui ne publie que amd64 ne doit pas recevoir d'offre.
func (s *Service) UpgradeFor(r *http.Request, version, osName, arch string) *Upgrade {
	if version == "" {
		return nil
	}
	if osName == "" {
		osName = "linux"
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	var arts, err = s.ListArtifacts()
	if err != nil {
		return nil
	}
	var a Artifact
	for _, a = range arts {
		if a.OS != osName || a.Arch != arch {
			continue
		}
		return &Upgrade{
			Version: version,
			URL:     s.externalBase(r).String() + a.URL,
			SHA256:  a.SHA256,
			Size:    a.Size,
		}
	}
	return nil
}

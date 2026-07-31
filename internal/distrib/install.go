package distrib

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Installation « sans Docker » : un script POSIX sh qui télécharge le binaire de
// la bonne architecture, en vérifie le SHA-256, crée un compte de service non
// privilégié et pose une unité systemd durcie.
//
// Les chemins sont ceux que le proxy utilise déjà (cf. cmd/proxy) : l'état vit
// dans /var/lib/meshcraft, et c'est aussi là que se rangera le binaire d'une
// mise à jour automatique (cf. docs/01-Architecture/Versionnage et mise à jour
// du proxy).
const (
	unitName  = "meshcraft-proxy.service"
	binPath   = "/usr/local/bin/meshcraft-proxy"
	envPath   = "/etc/meshcraft/proxy.env"
	stateDir  = "/var/lib/meshcraft"
	serviceID = "meshcraft"
)

// unameCases associe les réponses possibles de `uname -m` au suffixe d'artefact
// publié par le contrôleur.
var unameCases = []struct {
	patterns string // motifs de case sh, séparés par |
	arch     string // suffixe de l'artefact
}{
	{"x86_64|amd64", "amd64"},
	{"aarch64|arm64|armv8*", "arm64"},
	{"armv7*|armv6*|arm", "arm"},
}

// SystemdUnit renvoie l'unité systemd du proxy. Elle ne contient aucun secret :
// l'URL du contrôleur et le token d'enrollment vivent dans un fichier
// d'environnement en 0600, car un fichier d'unité est lisible par tous.
func (s *Service) SystemdUnit(r *http.Request) string {
	var base = s.externalBase(r).String()
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=meshcraft proxy — point de présence réseau\n")
	b.WriteString("Documentation=" + base + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("User=" + serviceID + "\n")
	b.WriteString("EnvironmentFile=" + envPath + "\n")
	b.WriteString("ExecStart=" + binPath + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5s\n")
	// systemd crée et donne au service son répertoire d'état.
	b.WriteString("StateDirectory=" + serviceID + "\n")
	b.WriteString("StateDirectoryMode=0750\n")
	b.WriteString("WorkingDirectory=" + stateDir + "\n\n")

	b.WriteString("# Moindre privilège : deux capabilities suffisent — le scan ARP/ND et\n")
	b.WriteString("# l'écoute de syslog (514/udp) et des traps (162/udp).\n")
	b.WriteString("AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE\n")
	b.WriteString("CapabilityBoundingSet=CAP_NET_RAW CAP_NET_BIND_SERVICE\n")
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("ProtectSystem=strict\n")
	b.WriteString("ProtectHome=yes\n")
	b.WriteString("PrivateTmp=yes\n")
	b.WriteString("ProtectKernelTunables=yes\n")
	b.WriteString("ProtectKernelModules=yes\n")
	b.WriteString("ProtectControlGroups=yes\n")
	b.WriteString("RestrictNamespaces=yes\n")
	b.WriteString("RestrictSUIDSGID=yes\n")
	b.WriteString("LockPersonality=yes\n")
	// AF_PACKET est nécessaire au scan ARP, AF_NETLINK à la lecture des
	// interfaces locales annoncées au contrôleur.
	b.WriteString("RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_PACKET\n")
	b.WriteString("SystemCallArchitectures=native\n")
	b.WriteString("SystemCallFilter=@system-service\n\n")

	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// InstallScript génère le script d'installation. Il embarque le token
// d'enrollment (usage unique, TTL court) et les SHA-256 des artefacts servis au
// moment de la génération : un binaire non conforme n'est jamais installé.
func (s *Service) InstallScript(r *http.Request, enrollToken string, expiresAt time.Time) (string, error) {
	var arts, err = s.ListArtifacts()
	if err != nil {
		return "", err
	}
	var sums = map[string]Artifact{}
	var a Artifact
	for _, a = range arts {
		if a.OS == "linux" {
			sums[a.Arch] = a
		}
	}

	var base = s.externalBase(r).String()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# meshcraft proxy — installation en service systemd (sans Docker).\n")
	b.WriteString("# Généré par le contrôleur " + base + "\n")
	b.WriteString("#\n")
	b.WriteString("# Le token d'enrollment embarqué ci-dessous est à USAGE UNIQUE et expire le\n")
	b.WriteString("# " + expiresAt.UTC().Format("2006-01-02 15:04:05") + " UTC. Passé ce délai, générez-en un autre.\n")
	b.WriteString("#\n")
	b.WriteString("# Le script est idempotent : relancé, il remplace le binaire et l'unité sans\n")
	b.WriteString("# toucher à l'identité déjà enrôlée (" + stateDir + "/state.json).\n")
	b.WriteString("set -eu\n\n")

	b.WriteString("CONTROLLER=\"" + base + "\"\n")
	b.WriteString("TOKEN=\"" + enrollToken + "\"\n")
	b.WriteString("BIN=\"" + binPath + "\"\n")
	b.WriteString("ENV_FILE=\"" + envPath + "\"\n")
	b.WriteString("STATE=\"" + stateDir + "\"\n")
	b.WriteString("UNIT=\"/etc/systemd/system/" + unitName + "\"\n\n")

	b.WriteString("if [ \"$(id -u)\" -ne 0 ]; then\n")
	b.WriteString("  echo \"This script installs a systemd service: re-run it as root (sudo sh $0).\" >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("command -v systemctl >/dev/null 2>&1 || {\n")
	b.WriteString("  echo \"systemd not found on this host: run the binary by hand with\" >&2\n")
	b.WriteString("  echo \"MESHCRAFT_CONTROLLER et MESHCRAFT_ENROLL_TOKEN, ou utilisez Docker.\" >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("}\n\n")

	b.WriteString("# Host architecture → artifact published by this controller, with the\n")
	b.WriteString("# expected SHA-256 (recorded when this script was generated).\n")
	b.WriteString("case \"$(uname -m)\" in\n")
	var c struct {
		patterns string
		arch     string
	}
	for _, c = range unameCases {
		var art, ok = sums[c.arch]
		if !ok {
			continue
		}
		b.WriteString(fmt.Sprintf("  %s) ART=%q; SUM=%q ;;\n", c.patterns, art.Name, art.SHA256))
	}
	b.WriteString("  *)\n")
	b.WriteString("    echo \"architecture $(uname -m) non distribuée par ce contrôleur\" >&2\n")
	b.WriteString("    exit 1 ;;\n")
	b.WriteString("esac\n\n")

	b.WriteString("TMP=\"$(mktemp)\"\n")
	b.WriteString("trap 'rm -f \"$TMP\"' EXIT\n")
	b.WriteString("echo \"→ téléchargement de $ART\"\n")
	b.WriteString("if command -v curl >/dev/null 2>&1; then\n")
	b.WriteString("  curl -fSL \"$CONTROLLER/downloads/proxy/$ART\" -o \"$TMP\"\n")
	b.WriteString("elif command -v wget >/dev/null 2>&1; then\n")
	b.WriteString("  wget -qO \"$TMP\" \"$CONTROLLER/downloads/proxy/$ART\"\n")
	b.WriteString("else\n")
	b.WriteString("  echo \"ni curl ni wget disponibles\" >&2; exit 1\n")
	b.WriteString("fi\n\n")

	b.WriteString("# Intégrité vérifiée AVANT installation : un binaire non conforme n'est\n")
	b.WriteString("# jamais posé sur le système.\n")
	b.WriteString("GOT=\"$(sha256sum \"$TMP\" | cut -d' ' -f1)\"\n")
	b.WriteString("if [ \"$GOT\" != \"$SUM\" ]; then\n")
	b.WriteString("  echo \"SHA-256 inattendu : $GOT (attendu $SUM)\" >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("echo \"→ SHA-256 conforme\"\n\n")

	b.WriteString("# Compte de service non privilégié : le proxy ne stocke aucun secret\n")
	b.WriteString("# d'équipement, il n'a rien à faire en root.\n")
	b.WriteString("if ! id " + serviceID + " >/dev/null 2>&1; then\n")
	b.WriteString("  useradd --system --home-dir \"$STATE\" --shell /usr/sbin/nologin " + serviceID + " 2>/dev/null \\\n")
	b.WriteString("    || adduser --system --home \"$STATE\" --shell /usr/sbin/nologin " + serviceID + " 2>/dev/null \\\n")
	b.WriteString("    || adduser -S -H -h \"$STATE\" -s /sbin/nologin " + serviceID + "\n")
	b.WriteString("fi\n\n")

	b.WriteString("install -m 0755 \"$TMP\" \"$BIN\"\n")
	b.WriteString("install -d -m 0750 \"$STATE\"\n")
	b.WriteString("chown " + serviceID + " \"$STATE\"\n\n")

	b.WriteString("# Le token est un secret : fichier d'environnement en 0600, jamais dans\n")
	b.WriteString("# l'unité systemd (lisible par tous).\n")
	b.WriteString("install -d -m 0755 /etc/meshcraft\n")
	b.WriteString("cat > \"$ENV_FILE\" <<EOF\n")
	b.WriteString("MESHCRAFT_CONTROLLER=$CONTROLLER\n")
	b.WriteString("MESHCRAFT_ENROLL_TOKEN=$TOKEN\n")
	b.WriteString("MESHCRAFT_PROXY_NAME=$(hostname -s 2>/dev/null || hostname)\n")
	b.WriteString("EOF\n")
	b.WriteString("chmod 0600 \"$ENV_FILE\"\n\n")

	b.WriteString("cat > \"$UNIT\" <<'UNIT_EOF'\n")
	b.WriteString(s.SystemdUnit(r))
	b.WriteString("UNIT_EOF\n\n")

	b.WriteString("systemctl daemon-reload\n")
	b.WriteString("systemctl enable --now " + unitName + "\n\n")
	b.WriteString("echo \"✓ proxy installé et démarré.\"\n")
	b.WriteString("echo \"  journal  : journalctl -u " + serviceID + "-proxy -f\"\n")
	b.WriteString("echo \"  état     : $STATE/state.json\"\n")
	b.WriteString("echo \"  Le token est consommé : il peut être retiré de $ENV_FILE.\"\n")
	return b.String(), nil
}

package distrib

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func digestOf(data []byte) string {
	var h = sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// externalBase renvoie l'URL publique du controleur : configuree, sinon
// derivee de la request (scheme + Host).
func (s *Service) externalBase(r *http.Request) *url.URL {
	if s.Cfg.ExternalURL != "" {
		var u, err = url.Parse(strings.TrimSuffix(s.Cfg.ExternalURL, "/"))
		if err == nil && u.Host != "" {
			return u
		}
	}
	var scheme = "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return &url.URL{Scheme: scheme, Host: r.Host}
}

// PublicURL renvoie l'URL publique du controleur telle qu'un proxy doit la
// contacter (configuree, sinon derivee de la request).
func (s *Service) PublicURL(r *http.Request) string {
	return s.externalBase(r).String()
}

// ImageRef renvoie la reference d'image du proxy dans le registre integrated,
// ex. ctrl.example.com:8080/meshcraft/proxy:latest
func (s *Service) ImageRef(r *http.Request) string {
	return s.externalBase(r).Host + "/" + imageName + ":latest"
}

// InsecureNote signale la configuration insecure-registries necessary en HTTP.
func (s *Service) InsecureNote(r *http.Request) string {
	var base = s.externalBase(r)
	if base.Scheme == "https" {
		return ""
	}
	return fmt.Sprintf(
		`Le controleur est servi en HTTP : ajouter {"insecure-registries":["%s"]} dans /etc/docker/daemon.json de l'hote du proxy (puis systemctl restart docker) pour autoriser le pull.`,
		base.Host)
}

// ComposeYAML genere le docker-compose du proxy : l'image est tiree du
// registre integrated du controleur, l'URL du controleur et le token
// d'enrollment sont pre-remplis.
func (s *Service) ComposeYAML(r *http.Request, enrollToken string) string {
	var base = s.externalBase(r)
	var b strings.Builder
	b.WriteString("# meshcraft proxy — genere par le controleur " + base.String() + "\n")
	b.WriteString("# L'image est tiree du registre integrated du controleur.\n")
	if s.InsecureNote(r) != "" {
		b.WriteString("# NOTE : " + s.InsecureNote(r) + "\n")
	}
	b.WriteString("services:\n")
	b.WriteString("  meshcraft-proxy:\n")
	b.WriteString("    image: " + s.ImageRef(r) + "\n")
	b.WriteString("    restart: unless-stopped\n")
	b.WriteString("    # host : indispensable pour les scans ARP/ND et la receive\n")
	b.WriteString("    # syslog/traps sur les reseaux locaux joignables par cet hote.\n")
	b.WriteString("    # En bridge, l'IP source des messages serait celle de la passerelle\n")
	b.WriteString("    # Docker, donc inutilisable pour attribuer un message a un device.\n")
	b.WriteString("    network_mode: host\n")
	b.WriteString("    cap_add:\n")
	b.WriteString("      - NET_RAW              # scan ARP / Neighbor Discovery\n")
	b.WriteString("      - NET_BIND_SERVICE     # syslog 514/udp, traps 162/udp\n")
	b.WriteString("    environment:\n")
	b.WriteString("      MESHCRAFT_CONTROLLER: \"" + base.String() + "\"\n")
	b.WriteString("      MESHCRAFT_ENROLL_TOKEN: \"" + enrollToken + "\"\n")
	b.WriteString("    volumes:\n")
	b.WriteString("      - meshcraft_proxy_state:/var/lib/meshcraft\n")
	b.WriteString("volumes:\n")
	b.WriteString("  meshcraft_proxy_state:\n")
	return b.String()
}

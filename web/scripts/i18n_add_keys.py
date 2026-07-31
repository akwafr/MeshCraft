#!/usr/bin/env python3
"""Add missing Phase 4 i18n keys and sync parity across all locale files."""
import json
import os

LOCALES_DIR = os.path.join(os.path.dirname(__file__), "..", "src", "i18n", "locales")
LANGS = ["en", "fr", "de", "es", "it", "pt", "nl", "ru", "ar", "zh", "ja"]

NEW = {
    "proxyPage": {
        "coverageHelp1": {
            "en": "Coverage defines who administers the proxy and for which sites it serves. You only see instance proxies and those covering one of your sites, and you can only add or remove your sites from their coverage. Acting on the proxy itself (revoke, delete, migrate) requires administering all sites it covers.",
            "fr": "La couverture dit qui administre le proxy et pour quels sites il sert. Vous ne voyez que les proxys d'instance et ceux couvrant un de vos sites, et vous ne pouvez ajouter ou retirer que vos sites de leur couverture. Agir sur le proxy lui-même (révoquer, supprimer, migrer) demande d'administrer tous les sites qu'il couvre.",
        },
        "coverageHelp2": {
            "en": "It does not define what the proxy can reach — that is observed. And a discovered device's site is chosen at adoption.",
            "fr": "Elle ne dit pas ce que le proxy peut joindre : ça, c'est observé. Et le site d'un équipement découvert reste choisi à son adoption.",
        },
        "coverageHelp3": {
            "en": "Cross-instance migration: first generate an enrollment token on the destination instance, then enter its URL and token here. On its next heartbeat, the proxy enrolls there, rewrites its local state, and switches — nothing to do on the proxy machine.",
            "fr": "Migration entre instances : générez d'abord un token d'enrollment sur l'instance d'arrivée, puis saisissez ici son URL et ce token. À son prochain heartbeat, le proxy s'enrôle là-bas, réécrit son état local et bascule — rien à faire sur la machine du proxy.",
        },
        "deployDockerHelpLabel": {
            "en": "Help — Docker proxy deployment",
            "fr": "Aide — déploiement proxy Docker",
        },
        "deployDockerHelp1": {
            "en": "The meshcraft/proxy image (amd64, arm64, armv7) is distributed by this controller's built-in registry: generated docker-compose pulls from here and embeds a 1-hour enrollment token.",
            "fr": "L'image meshcraft/proxy (amd64, arm64, armv7) est distribuée par le registre intégré de ce contrôleur : le docker-compose généré tire l'image d'ici et embarque un token d'enrollment valable 1 heure.",
        },
        "binaryInstallHelpLabel": {
            "en": "Help — binary installation",
            "fr": "Aide — installation par binaire",
        },
        "binaryInstallHelp1": {
            "en": "The script detects the host architecture, downloads the matching binary, verifies its SHA-256, creates an unprivileged service account, and installs a hardened systemd unit. It embeds a 1-hour enrollment token and is safe to re-run: an already enrolled identity is not touched.",
            "fr": "Le script détecte l'architecture de l'hôte, télécharge le binaire correspondant, vérifie son SHA-256, crée un compte de service non privilégié et installe une unité systemd durcie. Il embarque un token d'enrollment valable 1 heure et se relance sans risque : l'identité déjà enrôlée n'est pas touchée.",
        },
        "binaryInstallHelp2": {
            "en": "The token is a secret: the script stores it in /etc/meshcraft/proxy.env with mode 0600, never in the systemd unit which is world-readable.",
            "fr": "Le token est un secret : le script le range dans /etc/meshcraft/proxy.env en 0600, jamais dans l'unité systemd qui est lisible par tous.",
        },
        "binaryInstallHelp3": {
            "en": "Manual start: MESHCRAFT_CONTROLLER (this controller URL) and MESHCRAFT_ENROLL_TOKEN as environment variables. State is persisted in /var/lib/meshcraft/state.json.",
            "fr": "Lancement manuel : MESHCRAFT_CONTROLLER (URL de ce contrôleur) et MESHCRAFT_ENROLL_TOKEN en variables d'environnement. L'état est persisté dans /var/lib/meshcraft/state.json.",
        },
        "renameHelp1": {
            "en": "A tracking label, freely editable: the proxy identity is its UUID, not its name. Its enrollment, scan ranges, and access paths are not changed.",
            "fr": "Un libellé de suivi, librement modifiable : l'identité du proxy est son identifiant, pas son nom. Son enrollment, ses plages de scan et ses chemins d'accès ne sont pas touchés.",
        },
        "coverageModalHelp1": {
            "en": "Covered sites determine who sees this proxy, who administers it, and for which sites it can provide access paths.",
            "fr": "Les sites couverts déterminent qui voit ce proxy, qui l'administre, et pour quels sites il peut servir de chemin d'accès.",
        },
        "siteYouAdminTitle": {
            "en": "You administer this site: you can remove the proxy from its coverage",
            "fr": "Vous administrez ce site : vous pouvez retirer le proxy de sa couverture",
        },
        "siteNotAdminTitle": {
            "en": "Covered site you do not administer",
            "fr": "Site couvert que vous n'administrez pas",
        },
        "renameProxyTitle": {
            "en": "Rename this proxy",
            "fr": "Renommer ce proxy",
        },
        "protocolLabel": {
            "en": "protocol",
            "fr": "protocole",
        },
        "copy": {
            "en": "Copy",
            "fr": "Copier",
        },
        "upgradeHelp1Full": {
            "en": "The proxy downloads its architecture binary, verifies SHA-256, and re-executes. If it does not restart, it falls back to the image binary after two attempts and the failure appears in the table — the Docker image is never touched.",
            "fr": "Le proxy télécharge le binaire de son architecture, vérifie son SHA-256 et se réexécute. S'il ne redémarre pas, il revient de lui-même au binaire de son image après deux tentatives et l'échec s'affiche dans le tableau — l'image Docker n'est jamais touchée.",
        },
        "migrateApplyNoteFull": {
            "en": "The proxy applies the order on its next heartbeat (≤ 30 s). On failure (unreachable URL, expired token), it stays attached to this instance and the error appears in its logs — the order can be cancelled until applied.",
            "fr": "Le proxy applique l'ordre à son prochain heartbeat (≤ 30 s). En cas d'échec (URL injoignable, token expiré), il reste rattaché à cette instance et l'erreur apparaît dans ses journaux — l'ordre peut être annulé jusqu'à son application.",
        },
        "coverageRightsNote": {
            "en": " You can only act on sites you administer: others appear locked.",
            "fr": " Vous ne pouvez agir que sur les sites que vous administrez : les autres apparaissent verrouillés.",
        },
        "noDifferentVersion": {
            "en": "No different version to apply",
            "fr": "Aucune version différente à appliquer",
        },
    },
    "siteCredsPage": {
        "help3": {
            "en": "Password policies. Some ranges reject the site secret: MikroTik SwOS caps passwords at 15 characters, and its connector refuses the site secret before sending. meshcraft then generates a suitable password under a technical tag pwpolicy-len<N>, and tags affected devices. These credentials are held by meshcraft: their secret can be revealed and rotated, but scope cannot be changed or deleted.",
            "fr": "Politiques de mot de passe. Certaines gammes n'acceptent pas le secret du site : un MikroTik sous SwOS plafonne les mots de passe à 15 caractères, et son connecteur refuse le secret du site avant même de l'envoyer. meshcraft engendre alors un mot de passe adapté sous une étiquette technique pwpolicy-len<N>, et étiquette les équipements concernés. Ces identifiants sont tenus par meshcraft : leur secret se révèle et se fait tourner, mais leur portée ne se modifie pas et ils ne se suppriment pas.",
        },
        "help4": {
            "en": "SNMP. An v2c community applies site-wide — read-only: meshcraft never deploys write communities. A v3 identity targets tag snmpv3-<auth>-<priv>. Mixed fleets need as many v3 credentials as cryptographic families.",
            "fr": "SNMP. Une communauté v2c vaut pour tout le site — en lecture seule : meshcraft n'en déploie jamais en écriture. Une identité v3 vise une étiquette snmpv3-<auth>-<priv>. Un parc mixte aura autant d'identifiants v3 que de familles cryptographiques.",
        },
        "help5": {
            "en": "An v3 identity carries two distinct secret phrases: one authenticates, one encrypts. One compromised device in the family exposes monitoring for the whole family.",
            "fr": "Une identité v3 porte deux phrases secrètes distinctes : l'une authentifie, l'autre chiffre. Un seul équipement compromis dans la famille expose la supervision de toute la famille.",
        },
        "managedScopeTitle": {
            "en": "Reserved scope: this credential is generated and held by meshcraft based on what the platform connector declares. Its name and scope cannot be changed; deleting it would leave affected devices without an applicable credential.",
            "fr": "Portée réservée : cet identifiant est engendré et tenu par meshcraft d'après ce que le connecteur de la plateforme déclare. Son nom et sa portée ne se modifient pas ; le supprimer laisserait les équipements concernés sans identifiant applicable.",
        },
        "tagPrefix": {"en": "tag", "fr": "étiquette"},
        "groupPrefix": {"en": "group", "fr": "groupe"},
        "revealTitle": {
            "en": "Secret for « {{username}} »",
            "fr": "Secret de « {{username}} »",
        },
        "kindInherited": {
            "en": "{{kind}} (inherited kind)",
            "fr": "{{kind}} (genre hérité)",
        },
        "scopeTagHelp": {
            "en": "Prefer tag: a device can carry as many as needed, and tagging rules set them automatically. Group is unique per device and is only assigned via site configuration import — kept for existing fleets that use it.",
            "fr": "Préférer l'étiquette : un équipement en porte autant qu'il faut, et les règles d'étiquetage les posent toutes seules. Le groupe est unique par équipement et ne s'affecte plus que par import de configuration de site — il n'est conservé que pour les parcs qui l'utilisent déjà.",
        },
        "v3TargetTagHelp": {
            "en": "This identity only applies to devices with this tag: the v3 key derives from the algorithm pair; a device that does not accept them cannot carry it. The tag is technical — provisioning sets it from what the connector announces.",
            "fr": "Cette identité ne vaudra que pour les équipements portant cette étiquette : la clé v3 dérive du couple d'algorithmes, un équipement qui ne les accepte pas ne peut pas la porter. L'étiquette est technique — le provisionnement la pose d'après ce que le connecteur annonce.",
        },
        "v3RevealHelpFull": {
            "en": "These two phrases are deliberately different: SNMPv3 derives a key from each by the same calculation. The same phrase on both sides would make the encryption key only the start of the authentication key — whoever gets one gets the other.",
            "fr": "Ces deux phrases sont différentes à dessein : SNMPv3 dérive une clé de chacune, par le même calcul. La même phrase des deux côtés donnerait une clé de chiffrement qui n'est que le début de la clé d'authentification — qui obtient l'une obtient l'autre.",
        },
        "reservedScopeErrorDetail": {
            "en": "« {{tag}} » belongs to a reserved namespace: these credentials are generated and held by meshcraft based on what each platform connector declares. Choose an operational tag or site scope.",
            "fr": "« {{tag}} » appartient à un espace de noms réservé : ces identifiants sont engendrés et tenus par meshcraft d'après ce que chaque connecteur de plateforme déclare. Choisissez une étiquette opérationnelle ou la portée site.",
        },
        "secretNewUnchanged": {
            "en": " — new (empty = unchanged)",
            "fr": " — nouveau (vide = inchangé)",
        },
        "privNewUnchanged": {
            "en": "Encryption phrase — new (empty = unchanged)",
            "fr": "Phrase de chiffrement — nouvelle (vide = inchangée)",
        },
        "noteMaxLenPlaceholder": {
            "en": "e.g. 32 characters max on this range",
            "fr": "ex. 32 caractères max sur cette gamme",
        },
    },
}


def load(lang):
    path = os.path.join(LOCALES_DIR, f"{lang}.json")
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save(lang, tree):
    path = os.path.join(LOCALES_DIR, f"{lang}.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(tree, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main():
    trees = {lang: load(lang) for lang in LANGS}
    added = 0
    for section, keys in NEW.items():
        for key, texts in keys.items():
            en_val = texts["en"]
            fr_val = texts["fr"]
            for lang in LANGS:
                if section not in trees[lang]:
                    trees[lang][section] = {}
                if key not in trees[lang][section]:
                    added += 1
                if lang == "en":
                    trees[lang][section][key] = en_val
                elif lang == "fr":
                    trees[lang][section][key] = fr_val
                elif key not in trees[lang][section] or trees[lang][section][key] == trees["en"][section].get(key, en_val):
                    if lang != "en":
                        trees[lang][section][key] = en_val
    for lang in LANGS:
        save(lang, trees[lang])
    print(f"Synced keys; sections updated: {sum(len(v) for v in NEW.values())}")


if __name__ == "__main__":
    main()

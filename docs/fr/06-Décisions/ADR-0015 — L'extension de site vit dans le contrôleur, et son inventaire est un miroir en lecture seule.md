---
Statut: Précisé par ADR-0025
Date: 2026-10-24
---

> **Le point 5 a été rouvert.** La phrase « ni adoption, ni changement de
> configuration, ni redémarrage de borne » n'est plus exacte :
> [[ADR-0025 — meshcraft télécommande le contrôleur UniFi, il ne détient aucune
> intention]] autorise trois gestes passant par l'API du contrôleur. Le reste de
> ce document — dont « un équipement importé n'entre jamais dans `devices` » —
> reste en vigueur. Le texte ci-dessous n'est pas corrigé : c'est la trace de ce
> qui avait été décidé, et c'est ce qu'on vient y relire.

# ADR-0015 — L'extension de site vit dans le contrôleur, et son inventaire est un miroir en lecture seule

## Contexte

`CLAUDE.md` emploie déjà le mot **extension** : « un agent (un proxy ou une
extension) n'est jamais exécuté ici ». La phrase décrit le *produit d'un
pipeline de construction* — `internal/agenthost` fabrique une disposition OCI
que le registre interne sert, et quelque chose de distant va la tirer. C'est
tout ce que le mot désignait. **Aucune extension n'existe dans le code**, et
rien ne dit ce qu'une extension *fait*.

La demande qui force la question : un site meshcraft doit pouvoir être adossé
à un **site UniFi**, pour en importer les commutateurs et les bornes Wi-Fi.
Le site `ARENA` correspond au site UniFi nommé « lab Events #04 - ARENA ».

Faits relevés sur le terrain avant de décider :

- Le contrôleur UniFi est joignable **directement** depuis l'hôte du
  contrôleur meshcraft, en HTTPS, sur un nom public : `200` en ~1 s. Ce n'est
  pas une adresse RFC1918 au fond d'un site.
- Il expose une **API d'intégration v1** authentifiée par un en-tête
  `X-API-KEY` : `/info`, `/sites`, `/sites/{id}/devices`, le détail d'un
  équipement, `/statistics/latest`, `/clients`. Version applicative relevée :
  `10.6.109`.
- Les SSID n'y sont **pas** : `/sites/{id}/wlans` répond `404`. Il faut
  retomber sur l'API héritée `/proxy/network/api/s/{internalReference}/rest/wlanconf`,
  qui est indexée par la **référence courte** du site, pas par son UUID.
- Un équipement UniFi porte `adoptedAt`, `provisionedAt` et un
  `configurationId`. Le contrôleur UniFi **est** son autorité de
  configuration, et il le dit dans sa propre API.
- Le secret de test arrive par `UOS_URL` / `UOS_TOKEN` dans `.env.local`.

Un contrôleur UniFi n'est donc pas un équipement de plus : c'est un
**contrôleur pair**, qui détient lui-même un parc.

## Décision

**Une extension de site est une intégration qui s'exécute dans le contrôleur
meshcraft, configurée par site, et dont l'inventaire importé est un miroir en
lecture seule qui n'entre jamais dans `devices`.**

1. **Dans le contrôleur, pas dans un conteneur.** MDP v1 modélise *un pilote,
   un équipement* : `/reach`, `/state`, des gestes de port, un dialer SOCKS.
   Un contrôleur UniFi ne rentre pas dans ce moule — il faudrait inventer le
   pilote multi-équipement, que MDP v1 n'a pas, pour envelopper un client
   HTTP de trois appels. L'extension est donc du code du contrôleur.

2. **« Extension de site » et « extension-agent » sont deux mots différents,
   et on les distingue explicitement.** Celle-ci est une intégration locale.
   Le mot d'origine (`agent_repos`, `internal/agenthost`) reste réservé à ce
   qui doit **s'exécuter ailleurs**. Le jour où une extension aura besoin de
   tourner dans le réseau d'un site, elle deviendra une extension-agent et
   réutilisera `agenthost` — la présente décision ne ferme pas cette porte,
   elle refuse seulement de l'ouvrir pour un client HTTP.

3. **La joignabilité est une configuration, pas une architecture.** C'est le
   point qu'on allait rater en choisissant le conteneur « au cas où le
   contrôleur serait distant ». Une extension déclare un **transport** :
   `direct`, ou *via le proxy du site X*. meshcraft a déjà `internal/socks` et
   `internal/tunnel` ; un contrôleur UniFi enfermé dans un LAN se joint par le
   proxy déjà posé là, sans conteneur supplémentaire.

4. **Le secret appartient au coffre, pas à `.env.local`.** Une extension
   configurée *par site* n'a rien à faire dans le fichier d'environnement du
   contrôleur : un tel fichier est global, il est unique, il ne se révèle pas,
   il ne se journalise pas et il ne survit pas à deux sites adossés à deux
   contrôleurs UniFi distincts. Le jeton est donc chiffré par
   `internal/secretbox` et suit exactement le régime des identifiants de site
   existants : stocké scellé, révélé par un geste explicite, et **chaque
   révélation journalisée**. `UOS_URL` / `UOS_TOKEN` dans `.env.local` sont un
   **amorçage de développement** : de quoi tester contre le vrai UniFi, rien
   de plus, et aucun chemin de production ne les lit.

5. **L'inventaire importé est un miroir en lecture seule.** meshcraft ne
   configure rien à travers UniFi : ni adoption, ni changement de
   configuration, ni redémarrage de borne. Et, surtout, **un équipement
   importé n'est pas inséré dans `devices`** : cette table porte l'invariant
   « quelque chose que meshcraft configure, avec un connecteur et des
   identifiants ». Un commutateur UniFi a déjà un maître, qui le
   re-provisionne à son rythme ; deux autorités d'écriture sur le même
   équipement ne produisent pas une erreur, elles produisent une
   configuration qui revient en arrière toute seule, des minutes plus tard,
   sans rien dans aucun journal. On le **refuse explicitement** plutôt que de
   le découvrir.

6. **L'appariement se fait sur l'identifiant du site UniFi, le nom n'est
   qu'un affichage.** On stocke l'`id` (UUID stable), **et** la
   `internalReference` — parce que l'API héritée des SSID n'accepte que
   celle-là — et le `name` du dernier contact, comme libellé caché, jamais
   comme clé. C'est la même règle qu'[[ADR-0014 — Le libellé déclaré est une
   pièce à conviction, l'identité est dérivée]] : « lab Events #04 - ARENA »
   sera renommé un jour, et l'appariement doit y survivre.

7. **Un site UniFi disparu est orphelin, pas supprimé.** Si `/sites` répond
   et que l'`id` apparié n'y est plus, l'appariement passe `orphaned` avec
   l'horodatage du constat ; le miroir reste visible, estampillé périmé.
   On ne supprime rien. Et on distingue ce cas de « contrôleur injoignable » :
   seul le premier orpheline, parce qu'une panne réseau de dix minutes ne doit
   pas effacer le travail de corrélation de quelqu'un.

## Conséquences

- **Il y a désormais deux inventaires sur un site** : les `devices` que
  meshcraft pilote, et le miroir d'extension qu'il ne fait que lire. Toute
  question « qu'y a-t-il sur ce site ? » doit choisir sa réponse, et les
  compteurs du tableau de bord doivent dire laquelle ils comptent. C'est le
  prix principal, et il est permanent.
- **Un commutateur UniFi n'est pas configurable depuis meshcraft.** Quelqu'un
  arrivera en demandant pourquoi, puisque meshcraft sait parler à des
  commutateurs. La réponse est le point 5, et la porte de sortie est réelle :
  qu'on le désadopte d'UniFi, et il redevient un équipement ordinaire avec un
  connecteur. Il n'y a pas de demi-mesure offerte.
- L'interface doit **dire** « lecture seule », pas seulement omettre les
  boutons. Une absence de bouton se lit comme un droit manquant.
- Le contrôleur meshcraft acquiert une dépendance sortante vers un service
  tiers sur son propre chemin de requête. Tout appel UniFi porte donc un
  délai de garde dur, et l'échec d'une extension n'est jamais l'échec d'une
  page.
- L'API héritée `wlanconf` n'est pas sous contrat : elle peut casser à une
  mise à jour d'UniFi. On l'isole derrière une fonction nommée, et l'absence
  de SSID dégrade l'écran au lieu de le vider.
- Le registre de types d'extension est une donnée, pas un `switch` : UniFi OS
  est la première incarnation, pas la seule prévue.

## Alternatives écartées

**Un agent conteneurisé parlant MDP v1.** L'option conforme à la lettre de
`CLAUDE.md`, et celle qu'on allait prendre par cohérence. Elle achète
l'isolation — une extension qui plante ne plante pas le contrôleur — et une
cadence de livraison indépendante. Écartée sur un fait : le contrôleur UniFi
répond directement à l'hôte, donc le conteneur ne résout aucun problème
d'accès ici, et il faudrait tordre MDP v1 pour y faire tenir un contrôleur
pair qui détient 4 équipements. Le prix de ce refus est assumé au point 3
(un transport configurable remplace l'isolation réseau) et dans les
conséquences (délai de garde pour remplacer l'isolation de panne).

**Importer les équipements UniFi dans `devices`, avec un drapeau
`read_only`.** Très tentant : tout l'affichage existant devient gratuit —
façade, ports, topologie, recherche MAC. Écartée parce que le drapeau
mentirait par omission partout où il n'est pas lu, et il y a beaucoup
d'endroits : un geste de port, un déploiement d'identifiants, un plan de
renumérotation, une procédure de réinitialisation. Un invariant qui tient
dans le type est tenu ; un invariant qui tient dans un booléen à vérifier
dans trente appels est une fuite en attente.

**Rendre les commutateurs UniFi pilotables, puisque meshcraft sait le
faire.** Écartée au point 5. Ce n'est pas une limite technique, c'est un refus
de co-autorité : le symptôme d'une double autorité d'écriture n'est pas une
erreur, c'est une dérive silencieuse, et elle se diagnostique des semaines
plus tard.

**Garder le jeton dans `.env.local`.** Marche immédiatement, et c'est
d'ailleurs comme ça qu'on a testé. Écartée au point 4 : un réglage par site
dans un fichier global ne tient pas à deux sites, et un secret qui n'est ni
révélable ni journalisable n'est pas administrable.

**Apparier sur le nom du site UniFi**, qui est ce que l'opérateur lit et
reconnaît. Écartée au point 6 : un nom change, et l'appariement casserait
sans rien dire — ou, pire, se rattacherait au mauvais site après une
réutilisation de nom.

**Supprimer le miroir quand le site UniFi disparaît**, pour ne pas laisser de
données mortes. Écartée au point 7 : c'est indiscernable d'une panne au
moment où ça arrive, et la suppression est la seule des deux options qui ne
se répare pas.

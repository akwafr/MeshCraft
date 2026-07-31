---
tags: [securite]
---

# Credentials — le coffre des identifiants d'équipements

L'utilisateur fournit ce qui permet de se connecter aux équipements ; meshcraft les garde chiffrés et les utilise au bon moment ([[Discovery]], [[Réconciliation]], [[Monitoring]]).

## Où en est cette page

| Question | État |
|---|---|
| Secrets scellés en **AES-256-GCM**, nonce par valeur, jamais rendus par les listes | **livré** — `internal/secretbox` ; les listes n'exposent qu'un booléen « un secret existe » |
| Genres `admin`, `snmp_v2c`, `snmp_v3`, liste **fermée** côté API | **livré** — `checkCredScope`, `internal/api/sitecreds_handlers.go` |
| Portée par étiquette > groupe > site, avec priorité, et **signalement d'ambiguïté** | **livré** — `CredentialsForDevice`, événement `credential.scope_ambiguous` |
| Arrêt après 2 échecs par compte pour ne pas déclencher un lockout AAA | **livré** — `triesPerAccount`, avec un créneau réservé au compte d'usine |
| Révélation d'un secret : `site_admin`, appartenance au site vérifiée, tracée nominativement | **livré** — `POST …/reveal`, événement `credential.revealed` |
| Génération d'un identifiant de site à la création du site, et rattrapage au démarrage | **livré** — `EnsureSiteAdminCredential`, `BackfillSiteCredentials`, `internal/pwgen` |
| Chiffrement **enveloppe** KEK → DEK par site, rotation de DEK site par site | **décidé, non construit** — il y a une **seule** clé pour toute l'instance |
| Genres `ssh_password`, `ssh_key`, `http_login` | **décidé, non construit** — le seul transport de pose aujourd'hui est l'interface web de l'équipement |
| `device.credential_id` mémorisé, restriction par subnet | **décidé, non construit** — la portée remplace l'association, et il n'y a aucun filtrage par subnet |
| Clé maîtresse **hors base** par défaut | **partiel** — deux régimes, voir § *Stockage* |
| Accès de secours (break-glass) par équipement ou par site | **décidé, non construit** — la seule sortie de secret est `reveal`, un par un |

## Types

Le coffre porte **trois** genres, et la liste est fermée : toute autre valeur est refusée en 400. Les colonnes ci-dessous distinguent ce qui existe de ce qui reste à construire, parce que les deux étaient présentés côte à côte et se lisaient comme un inventaire.

| Type | Champs | Usage | État |
|---|---|---|---|
| `admin` | user, mot de passe | l'accès administrateur de l'équipement, quel que soit son transport | **livré** |
| `snmp_v2c` | communauté, en **lecture seule** | polling, discovery | **livré**, portée site imposée |
| `snmp_v3` | user, phrase d'authentification, phrase de chiffrement | idem, préféré | **livré**, portée étiquette imposée — voir [[Identité SNMP]] |
| `ssh_password` | user, password, enable/secret optionnel | CLI SSH (et Telnet) | **décidé, non construit** |
| `ssh_key` | user, clé privée (ed25519/rsa), passphrase opt. | CLI SSH — « certificat SSH » au sens large, y compris certs OpenSSH signés | **décidé, non construit** |
| `http_login` | user, password (+ hints driver : realm, chemin) | API REST / webform des équipements HTTP | **décidé, non construit** |

Pourquoi `admin` seul plutôt que trois genres par transport : la première gamme supportée n'a **ni SSH ni port série** ([[Connecteurs]]), et son unique porte est son interface web. Un genre par transport aurait donc demandé à l'exploitant de choisir un transport avant de savoir lequel l'équipement offre — alors que c'est précisément ce que le connecteur sait et lui pas. Le genre dira le transport le jour où un connecteur en offrira plusieurs ; d'ici là, `admin` veut dire « l'accès administrateur », et le connecteur décide comment on l'emploie.

> [!note] Ne pas confondre avec les identifiants standards de la découverte
> Une **autre** table existe, `known_credentials`, avec ses propres genres
> (`ssh`, `telnet`, `http`) et ses propres routes réservées au `global-admin` :
> ce sont les mots de passe d'usine essayés **en aveugle** avant qu'un équipement
> n'appartienne à un site ([[Discovery]]). Elle est scellée par la même clé, et
> c'est la seule chose qu'elle partage avec le coffre de site. Les mélanger
> ferait essayer `admin/admin` sur un parc en production.

## Portée et ordre d'essai

- Un credential appartient à un **site**, et peut être restreint à un **groupe d'équipements** ou à une **étiquette** ([[Étiquettes et portée des identifiants]]) — jamais aux deux à la fois, et le refus est explicite. **Livré.** La restriction à des **subnets** ou à des **devices précis** reste **décidée, non construite** : il n'y a aucun filtrage par subnet dans le code.
- La résolution est ordonnée par **spécificité** puis par **priorité** : étiquette (3) avant groupe (2) avant site (1), et à spécificité égale la priorité déclarée par l'exploitant. La liste **entière** est rendue à l'appelant, délibérément : deux étiquettes à la même priorité sont une **ambiguïté**, qui remonte à l'écran et dans le journal (`credential.scope_ambiguous`) au lieu d'être tranchée en silence par un `order by` — parce que le choix arbitraire aurait fonctionné, et se serait mis à choisir l'autre le jour d'un renommage.
- À l'adoption, essai **ordonné** sur le transport détecté ; **arrêt après 2 échecs par compte** pour ne pas déclencher de lockout AAA ([[Discovery]]). **Livré** (`triesPerAccount`), avec un raffinement que la conception n'avait pas prévu : un **créneau est réservé d'avance** au compte d'usine, sinon un parc déjà repris en main consommait le budget d'essais et un équipement neuf devenait inadoptable.
- Une fois l'association device↔credential établie, elle est mémorisée : **remplacé par autre chose**, et il faut le dire parce que la colonne n'existe pas. Il n'y a **pas** de `devices.credential_id`. Ce qui est enregistré est plus utile : l'état **observé** (`device_credential_state`) — quel identifiant, quelle génération et quel **nom de compte** ont réellement fonctionné sur cet équipement. L'association se **déduit** de la portée, l'état constate ce que l'équipement porte. Un pointeur figé aurait menti au premier changement d'étiquette ; la portée suit, et l'état corrige.
- Un changement de mot de passe sur le parc = éditer un seul credential — **vrai**, et c'est même le même code que « nouvelle génération » ([[Identité de service et rotation]] §13.1).
- Le modèle d'intent référence les credentials par URI opaque `cred://…`, jamais par valeur ([[Modèle de données standard]]).

## Stockage

```
clé de l'instance   ── env (MESHCRAFT_SECRET_KEY) ou base
   └── secrets      ── AES-256-GCM, nonce de 12 octets préfixé à chaque valeur
```

Ce que la conception décrivait était une **enveloppe** à deux étages :

```
master key (KEK)             ── env/KMS/fichier scellé au démarrage
   └── data keys (DEK) par site  ── chiffrées par la KEK, en base
          └── secrets            ── AES-256-GCM par la DEK du site
```

> [!important] L'enveloppe est décidée, non construite : il y a UNE clé
> Pas de KEK, pas de DEK par site. Une seule clé de 32 octets scelle tout ce que
> l'instance garde : `site_credentials` (les deux champs, la seconde phrase v3
> comprise), `credential_generations`, et `known_credentials`.
>
> Ce que l'enveloppe apporterait, et qu'il ne faut donc pas croire acquis :
> **rotation de la clé d'un site sans toucher aux autres**, et surtout **rayon de
> compromission borné au site**. Aujourd'hui, la clé unique donne tout le parc de
> toute l'instance.
>
> Ce que l'étage unique fait gagner, et qui explique l'ordre : il n'y a qu'un
> secret à savoir protéger, donc un seul endroit où se tromper. L'enveloppe ajoute
> une indirection dont la valeur est nulle tant que la clé du dessus n'est pas
> mieux gardée que celle du dessous — et c'est exactement le chantier en cours
> (voir ci-dessous). L'enveloppe vient **après**, pas avant.

- La clé maîtresse **devrait** être hors base : variable d'environnement, fichier, ou KMS externe. En pratique il y a **deux régimes**, et l'un d'eux n'est pas celui qu'on veut :
  - `MESHCRAFT_SECRET_KEY` définie (64 caractères hexadécimaux) → la clé vit hors base, comme prévu ;
  - variable absente → la clé est **rangée dans la base**, dans la même base que les secrets qu'elle protège. Le contrôleur **l'avertit au démarrage** en toutes lettres (« une fuite du dump exposerait les secrets »), et l'écran *Configuration → Identifiants* le dit aussi à l'exploitant, parce qu'un avertissement qui ne sort que dans les logs du conteneur n'est lu par personne.

  Le défaut est celui-là parce que l'alternative était de refuser de démarrer sans variable, ce qui aurait rendu une instance d'essai impossible à lancer — et parce qu'un coffre chiffré avec une clé mal rangée vaut toujours mieux qu'un coffre en clair. Mais ce n'est **pas** un état acceptable en production : dans ce régime, un dump PostgreSQL suffit.

> [!note] La bascule vers une clé d'environnement est un chantier en cours
> Passer une instance existante du régime « clé en base » au régime « clé
> d'environnement » ne se réduit pas à définir la variable : il faut rechiffrer, et
> savoir *avant de démarrer* si la clé fournie est bien celle qui ouvre le coffre.
> Ce mécanisme est en construction au moment où cette page est écrite ; elle ne le
> décrit pas, parce que documenter un chantier en mouvement produit une page fausse
> plus vite qu'une page absente. La seule chose à retenir ici : **définir la
> variable seule ne bascule rien**, et le contrôleur le dit dans son avertissement.

- Rotation de DEK par site possible sans re-chiffrer les autres sites — **décidée, non construite** (il n'y a pas de DEK).
- Les secrets ne sortent pas des listes : l'API renvoie les métadonnées (nom, type, user, dernière rotation, présence d'un secret) mais **jamais la valeur** — écriture seule, remplacement atomique. **Livré** : la requête de liste ne sélectionne même pas le champ scellé, seulement `octet_length(secret_enc) > 0`. Voir un identifiant est un droit d'`operator`, le lire un droit de `site_admin` : *voir* et *lire* ne sont pas la même permission.
- En mémoire : déchiffrés à l'usage, durée de vie minimale ; jamais écrits dans les logs ni les transcripts. **Livré, avec un trou nommé** :
  - aucun secret n'est journalisé, et le journal de mise au point **masque automatiquement** toute valeur dont la clé d'attribut contient `password`, `secret`, `token`, `cred`, `passwd` ou `key` — un filet volontairement large, parce qu'un masquage qui liste les champs à masquer en oublie un ;
  - le **transcript** rendu à l'opérateur ne porte que le nom de compte essayé et la **raison** de sa position dans l'ordre, jamais le secret. Le secret ayant fonctionné n'est pas rangé dans le transcript pour autant : c'est le **connecteur** qui mémorise le candidat accepté au moment où l'équipement répond oui, et le rend par `driver.Admin.Accepted()`. Cette formulation remplace celle qui décrivait un secret « retrouvé en rejouant l'ordre des candidats » : ce mécanisme existait bien, mais il **comparait la raison du candidat au libellé de l'étape de transcript** — deux chaînes que le code ne construit pas identiques — et rendait donc **toujours une valeur vide**. Sans conséquence sur l'Instant On, qui ignore l'ancien mot de passe ; fatal sur une plateforme qui le valide, comme SwOS et sa clé RC4. Un secret déduit d'une phrase écrite pour un humain est un secret qu'un reformulage casse en silence ;
  - **l'exception** : `POST /api/devices/{id}/web-probe` rend le **corps brut** d'une page de l'interface d'équipement (jusqu'à 8 MiB) sans aucun masquage. Si une page d'admin restitue un mot de passe dans son HTML, il ressort en clair. C'est une sonde de mise au point de connecteur, et elle est doublement restreinte (`site_admin` **et** `global-admin`) et journalisée — mais ce n'est pas un masquage, et une page qui n'aurait pas dû sortir en sort.
  - **une seconde exception, ouverte et non arbitrée** : la sauvegarde de configuration que le connecteur SwOS prend **juste avant d'écrire** est rendue telle quelle dans `ApplyResult.Backup`, donc dans la réponse HTTP de la pose. Elle porte le bloc de mot de passe de l'équipement (`.pwd.b`) et sa communauté SNMP — constaté sur un CSS326-24G-2S+. Ce n'est pas une sonde de mise au point réservée au `global-admin` : c'est le chemin de pose ordinaire, au niveau `operator`. Le journal, lui, est propre : `last_apply.backup_bytes` ne consigne que la **taille**. La difficulté n'est pas de cesser de la rendre, c'est de décider **où elle vit** : sur cette plateforme, `Commit` vaut `immediate` et cette copie est le **seul** retour arrière qui existe — elle ne se reconstitue pas, et la jeter en silence serait annoncer un filet qu'on a supprimé (voir [[Réconciliation]]). Le lot qui la conservera (`device_backups`) est décidé, non construit.
- Côté [[Proxy]] : aucun secret persisté — **livré**. Les communautés SNMP transitent bien dans les RPC via le tunnel au moment de l'usage et restent en mémoire ; les flux SSH/TLS le traversent chiffrés de bout en bout ([[Protocole contrôleur-proxy]]). Deux nuances dans [[Sécurité du proxy]] : le proxy persiste **son propre** secret d'identité, et un protocole relayé non chiffré (HTTP, telnet) traverse son processus en clair.

## Ce que meshcraft installe lui-même

Le coffre décrit ci-dessus contient ce que **l'utilisateur fournit**. Meshcraft pousse aussi ses **propres** accès sur les équipements — compte d'administration, user SNMP v3 (ou communauté dédiée en lecture), abonnement de streaming telemetry — dont il est le propriétaire et dont il assume la rotation. C'est une famille distincte, avec son propre cycle de vie : voir [[Identité de service et rotation]].

État à connaître avant de lire la suite : la **pose** de l'identité d'administration est livrée (`POST /api/devices/{id}/credentials/apply`, avec vérification par une vraie session), la pose SNMP aussi ([[Identité SNMP]]). Le **compte CLI de service**, le **streaming telemetry** et la **rotation automatique** ne sont pas construits.

Deux règles de cette note qui corrigent une simplification tentante :

- la **politique** (nom du compte, algorithmes, période de rotation, durcissement) est définie au niveau de l'instance et surchargeable par site — **décidé, non construit** : il n'existe pas de table de politique. Ce qui existe et qui en tient lieu : les algorithmes SNMP ont un **plancher en dur** (`sha256`/`aes128`) et le nom de compte souhaité est une constante (`mcadmin`), arbitrée par le connecteur ;
- les **secrets**, eux, sont générés **par équipement** — un secret de service commun à tout un parc fait d'un seul équipement compromis la clé de l'ensemble, alors qu'aucun humain n'a jamais à le taper. **Cet arbitrage a été renversé** : l'exploitation a tranché pour un secret **par site**, et la raison est solide — voir [[Identité de service et rotation]] §12. L'option par équipement existe en base (`per_device`) mais n'est **jamais exercée** : aucun code ne crée de génération par équipement.
- un secret de service n'est **jamais écrasé** : chaque changement crée une **génération**, parce qu'entre le changement dans l'admin et la fin du déploiement le parc porte plusieurs secrets à la fois. **Livré** — c'est cette historisation qui rend un changement de mot de passe survivable sur un équipement resté éteint, et [[Identité de service et rotation]] §13 en décrit le détail vérifié.

L'exception documentée à « un secret ne sort jamais de l'API » est l'**accès de secours** (break-glass), réservé au `global-admin` et tracé : sans lui, une panne de meshcraft rendrait le parc du client inadministrable.

> [!note] Le break-glass tel que décrit n'existe pas ; ce qui existe est plus étroit
> Il n'y a **aucun export** par équipement ou par site, et rien qui exige une
> ré-authentification. Ce qui est livré est la **révélation unitaire** :
> `POST …/reveal` pour l'identifiant courant, `POST …/generations/{n}/reveal` pour
> une génération précise — celle que porte encore un équipement resté en arrière.
> `site_admin`, appartenance au site de l'URL vérifiée (sans quoi il suffirait de
> deviner un UUID), et journalisation nominative en `credential.revealed`, en
> sévérité *avertissement*.
>
> C'est le break-glass ramené à l'échelle du site, et c'est un choix défendable :
> le geste dont on a besoin en panne est « donne-moi le mot de passe de CE switch »,
> pas « exporte-moi deux cents secrets ». Ce qui manque n'est donc pas la
> fonctionnalité, c'est ce qui entoure l'exception — la ré-authentification, le
> signalement aux autres administrateurs, et un événement dédié non supprimable.
> En attendant, un `site_admin` compromis lit les secrets de ses sites, et la seule
> chose qui le rattrape est le journal.

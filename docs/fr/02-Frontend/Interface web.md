---
tags: [frontend]
---

# Interface web

SPA **React + TypeScript**, servie par le binaire du [[Contrôleur]] (`go:embed all:webdist`), mobile-first — le choix face à Flutter est argumenté dans [[Stack technique]]. Temps réel par flux d'événements, données par API REST typée.

## Où en est cette page

| Question | État |
|---|---|
| SPA React + TS embarquée dans le binaire du contrôleur | **livré** — `internal/api/spa.go` |
| Site courant dans l'URL (`/s/{site}/…`), liens partageables, back/forward | **livré** — `web/src/App.tsx` |
| Sélecteur de site en permanence dans le header, limité aux sites autorisés | **livré** — `ListSites` filtre en SQL par `user_roles` |
| Page Équipements avec façade de ports colorée par VLAN | **livré** — [[Rendu des ports]] |
| Sélection multiple de ports (clic / ⇧ / Ctrl / ⇧+Ctrl), « mixte », plages compactées, PoE protégé | **livré**, et c'est la partie la plus aboutie de l'écran |
| Application d'une sélection en **une** requête transactionnelle, **une** ligne de journal | **livré** — `PUT /api/devices/{id}/ports` |
| Écrans Réglages : VLANs, management du site, proxys & plages, ajout d'équipement, identifiants | **livré** |
| Occupation de la plage de management affichée en clair | **livré** |
| **Client généré depuis l'OpenAPI** | **décidé, non construit** — il n'y a pas de spec OpenAPI ; le client est écrit à la main et typé à la main |
| **WebSocket `/ws/ui`** avec deltas appliqués au cache | **remplacé** — c'est un flux SSE qui annonce le changement, et le client recharge (§ *Temps réel*) |
| **PWA** installable : manifest, service worker, push | **décidé, non construit** — rien de tout cela n'existe |
| Onglet **Journal** dédié | **remplacé** — le journal vit dans **Monitoring** et sur le **Dashboard** |
| Sélection de ports **entre équipements** | **décidée, non construite** |
| Fiche équipement à onglets (Config / Ports / Statistiques / Événements / **Sauvegardes**) | **partiel** — une page unique, sans onglets ; il n'y a **aucune sauvegarde de configuration** |
| Bottom tabs sur mobile, pincement pour zoomer la façade | **décidés, non construits** — voir § *Mobile* |

## Navigation

La navigation réelle est en **onglets horizontaux**, et le menu latéral est réservé à deux sections :

```
┌────────────────────────────────────────────────────────────────────────┐
│ meshcraft  Dashboard Équipements Ports Topologie Monitoring Config     │
│                              [Site: Atelier Lyon ▾]  nicolas  [⇥]      │
├──────────┬─────────────────────────────────────────────────────────────┤
│ (latéral │                                                             │
│  seulem. │              contenu de la page                             │
│  en Conf.│                                                             │
│  et Ajout)│                                                            │
└──────────┴─────────────────────────────────────────────────────────────┘
```

- **Sélecteur de site** en permanence dans le header : liste limitée aux sites autorisés ([[RBAC]], filtrage en SQL). C'est une liste déroulante **avec champ de recherche** et navigation au clavier (saisie pour filtrer, ↑/↓, Entrée, Échap) plutôt qu'un `<select>` : une instance multi-sites en compte vite trop pour un menu natif. Changer de site **conserve l'écran courant**.

  Deux points de la conception qui ne sont pas là : l'entrée **« Tous les sites »** pour les rôles globaux (il faut toujours un site courant), et la **création de site depuis le header** — elle vit dans *Configuration → Sites*. La cloche de notifications non plus. Ce qui y est en plus : le nom de l'utilisateur, un badge **admin global** quand il en est un, et un bouton de déconnexion.
- Le site courant est dans l'URL (`/s/{site}/...`) : liens partageables, back/forward corrects. **Livré**, avec deux raffinements appris à l'usage : le dernier site est retenu en `localStorage` pour que les écrans d'instance gardent un contexte, et une **URL inconnue retombe sur le tableau de bord** du site courant — pas sur la liste des sites, qui donne l'impression d'avoir été déconnecté.
- Onglets réels : **Dashboard**, **Équipements** ([[Rendu des ports]]), **Ports**, **Topologie** ([[Weathermap]]), **Monitoring**, **Configuration**.

> [!note] Deux onglets de plus, un onglet de moins que la conception
> - **Dashboard** répond à « où en est mon parc ? », qui est la question qu'on se
>   pose en arrivant. C'est aussi pourquoi il est le repli des URL inconnues.
> - **Ports** est une vue **transversale au site** : tous les ports de tous les
>   équipements dans un tableau cherchable (port, description, VLAN, voisin). La
>   façade répond à « que fait ce switch ? », le tableau à « où est branché
>   ça ? » — et cette seconde question ne se répond pas en ouvrant quarante
>   fiches.
> - **Journal** n'existe pas comme onglet : le [[Journal d'événements]] filtrable
>   est **dans Monitoring**, à côté de l'état détaillé qu'il explique, et ses six
>   dernières lignes sont sur le Dashboard. Séparer le journal de l'état obligeait
>   à faire des allers-retours pour relier une alerte à sa cause.
> - **Profils** n'est pas un onglet non plus : profils de prise, d'équipements et
>   de switchs sont dans *Configuration* ([[Profils et groupes]]), là où vivent
>   VLANs, étiquettes et identifiants — ce sont des objets qu'on définit, pas des
>   écrans qu'on surveille.
>
> Le menu latéral n'apparaît donc que dans **Configuration** et dans **Ajouter un
> équipement**, cette dernière ayant son propre menu tout en gardant l'onglet
> Équipements actif : on y arrive depuis l'inventaire, et on y revient.

## Page Équipements (principale)

Chaque équipement = une **carte** : hostname, modèle, **place** dans le site, pastille d'état, et la **façade de ports** en petites cases colorées par VLAN — spécification complète dans [[Rendu des ports]]. Le pied de carte donne le nombre de ports actifs, le débit et le PoE consommé.

Deux écarts sur la carte : il n'y a **pas de logo constructeur**, et l'**IP de management** n'y figure pas (elle est sur la fiche). Et l'état affiché n'est pas `up`/`degraded`/`unreachable` mais quatre libellés en français — **EN LIGNE**, **ALERTE**, **INJOIGNABLE**, **INCONNU** — plus un cinquième qui n'était pas prévu et qui compte : **HORS PLAN**.

> [!important] Joignabilité et conformité au plan sont deux axes, pas un
> Un équipement qui répond parfaitement mais dont l'adressage de management ne
> suit pas le plan du site n'est pas en panne : il est **à migrer**
> ([[Adressage de management]]). Le rouge est donc réservé à ce qui ne répond
> plus, et l'écart au plan est un avertissement.
>
> La pastille porte en outre un **troisième** axe, la synchronisation : bleu
> clignotant pour « configuration en cours, pas encore posée », orange clignotant
> pour « l'équipement a été modifié en dehors de meshcraft ». L'arbitrage entre
> les axes est écrit une seule fois et partagé par tous les écrans : un
> équipement **injoignable garde sa pastille rouge** — annoncer « configuration en
> cours » sur un switch muet serait faux, rien n'est en cours ; sinon un écart de
> configuration prend le dessus sur le vert, parce qu'un équipement « en ligne »
> dont la configuration n'est pas celle qu'on croit n'est pas nominal. Recopier
> cette règle dans chaque page garantirait qu'un écran finisse par dire vert là où
> un autre dit orange, et l'exploitant croirait le plus rassurant.

- Tri/regroupement par emplacement, modèle ou statut ; recherche instantanée. **Non construit sur cette page** : la grille de cartes n'a ni tri ni recherche. La recherche transversale est sur l'onglet **Ports**, et le regroupement par **place** est sur la [[Weathermap]], sous forme de cadres.
- Clic sur un port → panneau latéral : détails et édition (profil de prise ou overrides) ; multi-sélection de ports. **Livré** sur la fiche du switch. Le panneau porte l'état, la vitesse, le PoE et le voisin ; les **MACs vues** et le **voisin LLDP** n'existent pas (aucune collecte BRIDGE-MIB ni LLDP — [[Monitoring]]), et le voisin affiché ailleurs vient de la topologie **saisie** ([[Weathermap]]). La multi-sélection **entre équipements** — le geste UniFi — n'est pas construite : la sélection est celle d'une façade.
- Clic sur l'équipement → fiche. **Livré, en page unique** plutôt qu'en onglets : résumé, façade, panneau de port ou de sélection, budget PoE, étiquettes, et les bandeaux d'état. Ce qui manque : une vue **Statistiques** (les courbes de trafic sont encore reconstituées, voir [[Weathermap]]), une vue **Événements** par équipement, et surtout les **Sauvegardes de conf** — il n'existe **aucune** sauvegarde de configuration dans le produit, ni table, ni route. C'est à savoir avant d'annoncer un durcissement à un client : la règle « une sauvegarde fraîche avant un changement `disruptive` » de [[Identité de service et rotation]] n'a rien sur quoi s'appuyer.
- Un bandeau « modifications non déployées » apparaît dès qu'un intent diffère de l'état appliqué → bouton **Déployer**.

> [!important] La pose est automatique — le bandeau n'apparaît que si elle a échoué
> Décision d'exploitation qui s'écarte de la conception, et qui est assumée dans
> le code (`internal/api/autoapply.go`) : **configurer quarante-huit ports dans
> l'interface EST l'approbation**. Demander un second geste sur un produit dont la
> promesse est d'administrer le parc revient à demander deux fois la même chose,
> et à laisser durablement un parc différent de ce que l'écran affiche.
>
> Les modifications sont donc regroupées (quelques secondes, sinon changer
> quarante-huit ports lancerait quarante-huit poses sur un équipement qui n'accepte
> que cinq sessions), sérialisées par équipement, et posées. Ce qui **n'est pas**
> autorisé pour autant : les garde-fous d'écriture restent entiers (le VLAN de
> management n'est jamais réécrit), et un échec **n'est jamais rejoué en boucle** —
> une tentative par changement, un événement, et l'équipement reste « en cours »,
> visible et reposable à la main.
>
> Le bandeau existe donc bien, mais il signale une pose **enlisée**, pas une
> intention en attente ; et il liste les ports concernés. Les bandeaux voisins
> suivent la même logique : dernière erreur de pose avec bouton de reprise, ports
> **ignorés** avec possibilité de forcer, réglages **non supportés** par la
> plateforme, équipement plus surveillé, équipement hors plan. Chaque refus porte
> son geste de réparation — c'est le motif du produit, et il vaut mieux que le
> bouton *Déployer* qu'il remplace. Le plan avant confirmation, lui, reste dans
> [[Réconciliation]] pour les gestes de masse.

## Le texte explicatif vit derrière une aide, pas dans la page

Décision d'interface prise après coup, sur un produit qui écrivait beaucoup et bien : les écrans étaient devenus **illisibles à force d'être explicites**. Sous chaque titre, sous chaque champ, un paragraphe gris (`.note`) disait pourquoi c'était ainsi. Le résultat n'est pas qu'on lit trop : c'est qu'**on ne lit plus rien**, et notamment plus les phrases qui, elles, devaient être lues.

Le partage se fait sur la **nature** du texte, pas sur sa longueur :

| Nature | Où elle vit | Pourquoi |
|---|---|---|
| **Constat** — ce que porte un équipement, une dérive, un port épargné, une pose incomplète, un manque | **à l'écran**, en clair | il change d'une fiche à l'autre, et il se lit chaque fois |
| **Ce que meshcraft ne fera pas** alors qu'on vient de le demander | **à l'écran**, en clair | masqué, l'exploitant croit son ordre parti et va chercher une panne sur un switch qui n'a jamais reçu la consigne |
| **Explication** — le pourquoi, la pédagogie, les raccourcis clavier, la nuance de vocabulaire | **derrière l'aide** | se lit une fois, encombre les mille suivantes |
| **Redite de ce que l'écran montre déjà** | **supprimée** | « regroupés par équipement » sous une liste manifestement groupée par équipement n'apprend rien |

Trois textes sont ainsi **partis pour de bon** : le sous-titre « regroupés par équipement » de l'onglet Ports, « débits relevés sur les ports de chaque liaison » du titre de la Topologie, et la légende de bas de page de la [[Weathermap]] (dix lignes qui décrivaient les flèches, les cadres, le zoom et la vue compacte). Les autres ont changé de place, pas disparu.

> [!important] Un constat long reste visible ; une explication courte passe derrière l'aide
> C'est le sens de la règle, et il se trompe facilement. La note issue de
> `mgmt_addressing.push_blocked` ([[Adressage de management]]) fait trois lignes et
> reste **en clair** : elle dit que l'adressage saisi n'est pas écrit sur
> l'équipement. Les notes du connecteur (`observed.notes`, `last_apply.notes`,
> `skipped`, `unsupported`) restent en clair aussi — ce sont des phrases écrites
> **sur du matériel réel** ([[Rendu des ports]] § *Notes du connecteur*), pas de la
> pédagogie. Et les bandeaux (`drift-banner`, `alert-banner`, `mgmt-warn-banner`)
> ne sont pas des sous-textes : on n'y touche pas.
>
> Toute la modale de bascule du management ([[Adressage de management]]) est
> laissée intacte pour la même raison : chacun de ses quatre paragraphes décrit ce
> qu'un geste irréversible va faire au matériel, au moment où on s'apprête à le
> faire.

### Le composant `HelpTip`

Un seul composant, dans `web/src/components.tsx`, posé **à côté du titre ou du libellé** qu'il explique — jamais au milieu d'un paragraphe. Un `?` cerclé de 15 px, discret au repos, qui s'accentue au survol et au focus.

- **Un clic, pas un survol.** Ce produit s'utilise sur tablette dans un local technique, et parfois au clavier seul : une aide qui n'existe qu'au `:hover` n'existe pas là-bas. Le clic ouvre et referme, Échap et le clic extérieur referment. Pas de survol **en plus** du clic — la bulle se refermerait au départ du pointeur alors qu'un clic vient de l'épingler, et le geste cesserait d'avoir un résultat prévisible.
- **Un vrai `<button>`**, atteignable au clavier, avec `aria-label`, `aria-expanded`, et la bulle liée par `aria-describedby` en `role="tooltip"`. La cible tactile est élargie par un pseudo-élément qui ne pousse rien — et dont le débord vers le bas est **limité à 4 px**, la marge que `label .lbl` laisse avant son champ : au-delà, demander l'aide volerait le clic destiné à la saisie.
- **Du contenu structuré**, pas une chaîne : le composant prend des `children`. La moitié de ces textes font trois phrases, certains une liste.
- **Position calculée, pas `absolute`.** Ces textes vivent surtout dans le panneau latéral de la fiche d'équipement, étroit et déjà en `overflow: auto` : une bulle positionnée par rapport à son parent y serait rognée. Elle est donc en `fixed`, placée d'après le rectangle du bouton, ramenée dans la fenêtre, et **retournée au-dessus** quand le bas manque de place. `max-height: 60vh` avec défilement propre pour les plus longues.

Deux composants existants ont reçu la même prise, pour que le geste soit le même partout : `Modal` accepte un `help` rendu à côté de son titre, et `Field` un `help` rendu à côté de son libellé. C'est le bon endroit pour « ce que ce champ décide » — un paragraphe posé **sous** le champ se lit après la saisie, quand la question ne se pose plus.

> [!note] Ce qui reste à faire
> Les infobulles `title` du navigateur cohabitent avec ce composant. Elles font
> déjà le travail sur les contrôles secondaires (le bouton de relecture, les
> `<select>` de collecteur et de domaine, les jetons d'étiquette) et n'ont pas été
> doublées d'une icône : deux mécanismes pour la même phrase seraient pires qu'un.
> Mais `title` est invisible au tactile, et le jour où le tactile sera traité pour
> de bon ([[Rendu des ports]], § *Accessibilité*), ces `title` devront devenir des
> `HelpTip`.

## Sélection de ports sur la façade

Configurer un switch port par port ne passe pas l'échelle : une salle de cours,
c'est douze prises identiques. La façade accepte donc une **sélection
multiple**, avec les conventions que tout le monde connaît déjà. **Livré
intégralement.**

| Geste | Effet |
| --- | --- |
| clic | sélectionne ce port seul, et pose l'**ancre** |
| ⇧ clic | prend la **plage** de l'ancre jusqu'ici, en remplaçant la sélection |
| Ctrl/⌘ clic | **ajoute ou retire** ce port |
| ⇧ + Ctrl/⌘ clic | **ajoute la plage** à la sélection existante |

Ces quatre gestes se lisent **derrière l'aide de la légende de façade**, et non plus dans un paragraphe du panneau latéral (§ *Le texte explicatif vit derrière une aide*) : un rappel de raccourcis est le cas d'école du texte qu'on lit une fois. L'icône est contre la façade qu'elle pilote — pas dans le panneau, qui affiche le port dès qu'on en a cliqué un.

Deux détails décident si c'est utilisable, et tous deux sont en place :

- la plage se mesure sur l'ordre **physique** des ports, pas sur les rangées
  affichées. Une façade de 16 ports ou plus est dessinée entrelacée (impairs
  dessus, pairs dessous) comme le matériel réel : « du 1 au 8 » doit prendre
  1…8, pas une rangée sur deux ;
- le panneau annonce **« mixte »** quand les ports sélectionnés diffèrent, au
  lieu de montrer la valeur du premier. Réduire une sélection hétérogène à son
  premier élément ferait mentir l'écran sur ce qu'on s'apprête à écraser.

Le panneau de sélection compacte les noms en plages (« 1/1–1/8, 1/12 ») : une
liste de huit noms obligerait à recompter pour vérifier qu'on a bien pris ce
qu'on croyait. Trois cas distincts, dont un qui se remarque à l'usage : deux
ports voisins s'écrivent « a, b » et non « a–b », parce qu'un tiret pour deux
éléments se lit plus lentement que la virgule qu'il remplace.

Le PoE n'est **jamais** forcé sur un port qui n'en a pas : une sélection peut
mélanger cuivre PoE et SFP, et écrire `poe: true` sur un SFP poserait une
intention que le matériel ne pourra jamais satisfaire — donc un écart permanent
en [[Réconciliation]]. **Livré**, et la capacité vient du groupe physique de la
façade, pas d'une saisie.

Détail livré que la conception n'avait pas prévu, et sans lequel la sélection
serait dangereuse : le patch est **fusionné** avec l'intention **propre à chaque
port**. Appliquer un VLAN à une plage n'efface donc pas les libellés ni les
réglages PoE des ports au passage — sinon « poser le VLAN 99 sur douze prises »
aurait aussi effacé douze descriptions.

Côté API, la sélection part en **une seule requête** (`PUT
/api/devices/{id}/ports`, corps `{ports: {nom: intention}}`), appliquée d'un
bloc **dans une transaction** et journalisée en **une ligne** :
`sw-labo-1930 · 8 ports (1/1, 1/2, 1/3 … 1/8) → accès VLAN 99`. **Livré, à la
lettre.** Un geste unique appliqué à moitié serait pire que rien — il faudrait
deviner quels ports ont pris — et quarante-huit lignes de
[[Journal d'événements]] pour un seul clic noieraient ce qu'elles sont censées
rendre lisible. Deux bornes en plus : un lot est plafonné (512 ports), et quand
les intentions du lot **diffèrent** le journal écrit « configurations mixtes »
plutôt que de résumer le lot par son premier élément — la même honnêteté que le
panneau.

## Temps réel

> [!important] Ce n'est pas un WebSocket, et le client ne reçoit aucune donnée
> La conception décrivait « un seul WebSocket `/ws/ui` » poussant des messages
> `port_state`, `device_state`, `job_progress`… appliqués en deltas au cache
> TanStack Query. Le livré est **un flux SSE** —
> `GET /api/sites/{siteID}/stream` — qui annonce **« ce site a changé »**, après
> quoi le client va rechercher la donnée par l'API REST habituelle.
>
> Trois messages seulement : `ready` (accusé applicatif), `ping` (battement,
> 20 s), et `change` — ce dernier portant la liste des catégories touchées
> (`port`, `device`, `sync`, `link`, `config`, `event`).
>
> **Pourquoi SSE plutôt que WebSocket** : un WebSocket sort du pipeline HTTP à
> l'upgrade, et oblige à refaire le contrôle de droits à la main — « exactement le
> genre d'endroit où un droit s'oublie » ([[RBAC]]). Le SSE est du HTTP ordinaire :
> même cookie, même middleware, et `requireSite(…, "viewer")` comme n'importe
> quelle route. Et le navigateur **reconnecte seul**, avec le délai que le serveur
> lui dicte.
>
> **Pourquoi ne rien pousser dans le flux** : pousser la donnée créerait un second
> chemin de sérialisation en parallèle du REST ; les deux divergeraient, et l'écran
> finirait par afficher ce que l'API ne renvoie plus. C'est le seul argument, et il
> suffit.
>
> **Pourquoi invalider large** : le message porte bien les catégories, mais le
> client ne les lit pas — il invalide **toute requête dont la clé contient
> l'identifiant du site**. Une table catégorie → clés serait une troisième copie
> du modèle, à tenir à jour à chaque écran ajouté, et un oubli y produirait un
> écran figé — la panne la plus difficile à imputer. Le coût est nul en pratique :
> react-query ne recharge que les requêtes réellement montées, et le serveur a
> déjà regroupé la salve (400 ms).

Cinq mécanismes rendent ce flux fiable, et chacun répond à une panne constatée :

- **repli par interrogation** à 10 s tant que le premier `ready` n'est pas arrivé. Ce n'est **pas** l'ouverture de la connexion qui fait foi : un proxy inverse qui tamponne laisse le flux s'ouvrir sans rien laisser passer, et l'écran se croirait en direct devant des données mortes. Un affichage qui mente est pire qu'un affichage lent ;
- **chien de garde à 50 s** — deux battements manqués. Un SSE peut mourir sans que le navigateur voie la moindre erreur (NAT d'entreprise qui oublie une connexion inactive) ; sans ce garde, l'écran resterait figé en se croyant vivant ;
- **suppression de l'écho** : chaque onglet s'identifie, et le serveur ne lui renvoie pas ses propres écritures. Sans cela, un glisser-déposer sur la [[Weathermap]] déclenchait un rechargement à chaque pixel ;
- **notification par middleware** plutôt que par un appel dans chaque handler : un handler qui oublie de notifier produirait un écran figé, et la faute serait invisible à la lecture ;
- **plafond d'abonnements** (256) : au-delà, le serveur répond 429 et le client retombe proprement en interrogation périodique. Refuser en clair vaut mieux qu'accepter une connexion qu'on ne saura pas alimenter.

La cadence de repli est alignée sur celle à laquelle la donnée arrive réellement — le proxy vide son tampon toutes les 10 s — parce qu'interroger plus vite ne montrerait rien de plus en faisant payer une requête par écran ouvert et par seconde.

Les **événements de port** (`port.up`, `port.down`, `port.flapping`), eux, sont bien réels : ce sont des événements du [[Journal d'événements]], produits par la corrélation de battement du contrôleur à partir d'`ifOperStatus` ([[Monitoring]]). Ils ne transitent simplement pas *dans* le flux UI : le flux dit « il s'est passé quelque chose sur ce site », et le journal en dit quoi.

## Mobile

- Layout responsive. **Partiel.** Sous 720 px, le menu latéral devient une barre **horizontale en haut** ; sous 820 px, les onglets défilent horizontalement ; sous 960 px, les panneaux latéraux passent sous le contenu. Il n'y a **pas de bottom tabs**, pas de menu hamburger, pas de tiroir — la navigation reste en haut à toutes les tailles.
- Les cases de ports restent tactiles (cible ≥ 32 px en zoom port, pincement pour zoomer la façade) : **non construit**. La taille des cases est une constante par écran ([[Rendu des ports]]), et surtout la **sélection multiple repose entièrement sur ⇧ et Ctrl**, donc sur un clavier. La fiche du switch — l'écran principal — n'est aujourd'hui **pas utilisable au doigt** pour ce qu'elle sert à faire.
- PWA : manifest + service worker (shell offline), notifications push web pour les alertes ([[Monitoring]]). **Décidé, non construit** : il n'y a ni manifest, ni service worker, ni icônes, ni push. Le seul geste dans cette direction est une couleur de thème dans le `<head>`.
- Les actions lourdes (déploiement massif) restent confirmables sur mobile mais l'édition fine cible d'abord le desktop. **C'est l'état de fait**, et il faut le lire comme tel plutôt que comme un arbitrage : le mobile n'a pas été traité, il n'a pas été restreint.

## Écrans Réglages notables

- **VLANs du site** : id, nom, **couleur** (déterminante pour le [[Rendu des ports]]), usage. **Livré** ; la palette **suggère** la première teinte non encore utilisée du site, et le choix reste libre par un sélecteur de couleur. Le **contraste n'est pas vérifié** : une couleur choisie à la main peut être illisible, et rien ne le dit ([[Rendu des ports]], § *Accessibilité*).
- **Management du site** : VLAN d'administration (tagué ou natif), sous-réseaux v4/v6, passerelle, et origine de l'adresse — **DHCP externe** ou **meshcraft**, auquel cas la plage qu'il s'autorise à allouer. **Livré**, y compris l'occupation de la plage affichée en clair (« 37 assignées, 2 réservées, 111 libres sur 150 ») : une plage qui se remplit doit se voir avant d'être pleine ([[Adressage de management]]). Une plage vide ou incohérente est dite comme telle plutôt que rendue en zéros.
- **Proxys & plages de scan** : liste filtrée à ce que l'utilisateur peut voir — les proxys d'instance et ceux couvrant un de ses sites ([[Proxy]]) — avec statut, version (badge de mise à jour), IP locales et la **couverture en pastilles**. **Livré**, avec la modale de couverture qui distingue les deux droits : un `site_admin` coche ou décoche **ses** sites un par un, un `global-admin` redéfinit la couverture entière et peut promouvoir le proxy en proxy d'instance ([[RBAC]] porte la matrice complète, qui est implémentée fidèlement). Déclaration des préfixes IPv4/IPv6 à scanner **sur un proxy** (le proxy « Local » = l'instance elle-même), et génération du déploiement en un bouton — `docker-compose` ou **script d'installation systemd** pour les hôtes sans Docker, chacun embarquant son token d'enrollment et la couverture choisie ([[Sécurité du proxy]], [[Déploiement Docker et IPv6]]).
- **Ajouter un équipement** : section dédiée avec son propre menu latéral — file des **découvertes automatiques**, **ajout manuel par IP**, **ajout par port série**, plus **modèle du catalogue**, l'outil de **réinitialisation** et les **plages de scan**. **Livré.** L'adoption pose deux questions **indépendantes** : le mode de configuration (poussée par la plateforme ou importée depuis l'équipement) et l'**adressage de management** — conserver celui de l'équipement, ou appliquer le plan du site avec l'adresse allouée montrée *avant* de valider ([[Adressage de management]], [[Discovery]]).
- **Identifiants du site** : coffre du site ([[Credentials]]), avec les générations et la révélation tracée ([[Identité de service et rotation]] §12–13).
- **Identifiants de découverte** : les couples utilisateur/mot de passe essayés sur les équipements rencontrés pendant la découverte, dans l'ordre de priorité. Écran d'**instance** et non de site, réservé au global-admin — la découverte précède le choix du site, et ces identifiants ne servent qu'à entrer sur ce que meshcraft ne gère pas encore. Ils vivaient auparavant dans la section « Ajouter un équipement » d'un site, ce qui les rendait introuvables à qui les cherchait dans la configuration ; l'ancienne URL y redirige. L'écran dit **quels genres sont réellement essayés et par quel moyen** — un genre stocké mais jamais tenté serait le pire des affichages — et signale les secrets devenus illisibles après un changement de clé de chiffrement, seul endroit où ce cas puisse se voir ([[Discovery]]).
- **Étiquettes**, **Import / Export** du site, **Pipelines**, et les écrans d'instance **Sites**, **Utilisateurs**, **Debug**, **Proxys** — ces trois derniers réservés au global-admin pour les deux premiers. Le pied du menu porte la **version et l'horodatage de build**, qui répond à « ma correction est-elle déployée ? ».

> [!note] Le client REST est écrit à la main, pas généré
> Il n'existe aucune spec OpenAPI dans le dépôt : `web/src/api.ts` porte les
> verbes et **les types** à la main, en regard des structures Go. C'est un coût
> réel — deux définitions à garder d'accord, et rien qui casse quand elles
> divergent — mais aussi la raison pour laquelle les types du front portent des
> commentaires que jamais un générateur n'écrirait, sur ce qu'un champ veut dire
> et sur les décisions qu'il ne faut pas confondre (voir `MetricUnavailable`, ou
> `MgmtConformity`). Le jour où la spec existera, ces commentaires devront vivre
> côté Go.

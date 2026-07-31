---
tags: [securite]
---

# RBAC — authentification et autorisations

## Où en est cette page

| Question | État |
|---|---|
| Rôles de site `viewer` / `operator` / `site_admin` + booléen d'instance | **livré** — table `user_roles` (migration `0001_init`), `model.RoleLevel`, `users.is_global_admin` |
| Mot de passe local en argon2id | **livré** — `internal/auth/password.go`, profil RFC 9106 (t=1, m=64 MiB, p=4) |
| Session par cookie opaque, seul le SHA-256 en base | **livré** — `internal/auth/auth.go`, table `sessions` |
| Portée par site vérifiée sur les routes du parc | **livré, mais handler par handler** — `requireSite`, `deviceForWrite`, `Auth.CanSite` ; ce n'est **pas** un middleware paramétré, et il y a des trous (§ *Le contrôle est écrit à la main*) |
| Matrice complète des droits sur un proxy | **livré** — `canSeeProxy`, `canAdminWholeProxy`, `requestedCoverage` |
| Adoption = `operator` sur le site choisi | **livré** — `internal/api/discovery_handlers.go` |
| 2FA TOTP | **décidé, non construit** — aucune colonne, aucun écran |
| OIDC / mapping de groupes IdP | **décidé, non construit** — aucune dépendance, aucun code |
| Tokens d'API (PAT) | **décidé, non construit** — les seuls jetons existants sont machine-à-machine (proxy) |
| Journal `user.*` de toute action mutante, avec IP | **presque rien** — seul `user.login`, sans IP (§ *Audit*) |
| Accès [[Accès web pass-through]] comme droit distinct | **décidé, non construit** — la fonctionnalité elle-même est une étude |
| Cookie `Secure` derrière un terminateur TLS, protection CSRF | **ouvert** — voir le callout de fin de § *Authentification* |

## Authentification

- **Locale** : email + mot de passe. **Livré**, en argon2id (`internal/auth/password.go`) : 64 MiB de mémoire et 4 voies de parallélisme, c'est-à-dire le profil qui rend une attaque par GPU coûteuse. Un simple bcrypt aurait été plus court à écrire et aurait laissé la base d'utilisateurs déchiffrable au premier vol de dump. Le hash porte ses propres paramètres (`$argon2id$v=19$m=…,t=…,p=…$…`), ce qui permettra de les durcir sans invalider les mots de passe existants. Une comparaison à temps constant, et un hash factice vérifié même quand l'email est inconnu, pour que la durée de la réponse ne dise pas si un compte existe.
- **2FA TOTP** optionnelle, fortement suggérée aux admins — **décidée, non construite**.
- **OIDC** : délégation à un IdP (Keycloak, Entra, Google) pour les déploiements d'entreprise, avec mapping des groupes IdP → rôles meshcraft — **décidé, non construit**. C'est la fonctionnalité qui décide si meshcraft entre dans une DSI ou reste un outil d'équipe : la garder au plan est le seul moyen de ne pas peindre le modèle d'autorisation dans un coin d'où l'IdP ne pourrait plus l'atteindre (d'où des rôles nommés et une table d'attributions, plutôt que des droits inférés d'un booléen).
- Sessions : **livré**. Cookie `meshcraft_session` `HttpOnly` + `SameSite=Lax`, 7 jours, 32 octets aléatoires dont **seul le SHA-256 est stocké** — un vol de la table `sessions` ne donne aucune session utilisable. L'API et la SPA sont same-origin puisque le frontend est servi par le [[Contrôleur]], ce qui évite d'avoir à choisir entre `SameSite=None` et un second mécanisme de jeton.
- Le flux temps réel de l'[[Interface web]] réutilise bien la session, mais ce n'est **pas** un WebSocket : c'est du **SSE** (`GET /api/sites/{id}/stream`), précisément pour cette raison. Un WebSocket sort du pipeline HTTP à l'upgrade et oblige à refaire le contrôle de droits à la main — « exactement le genre d'endroit où un droit s'oublie ». Le SSE traverse le middleware de session puis `requireSite(…, "viewer")` comme n'importe quelle route.
- Tokens d'API (PAT) au scoping identique, pour l'automatisation — **décidé, non construit**. Les seuls jetons non-session qui existent sont machine-à-machine et ne portent aucun droit d'utilisateur : le token d'enrollment d'un proxy et le couple `(proxy_id, secret)` du tunnel ([[Sécurité du proxy]]).

> [!warning] Deux trous connus dans le transport de la session
> Ils ne sont ni des choix ni des cibles : ce sont des **corrections à faire**.
>
> - Le cookie ne porte `Secure` que si le contrôleur termine lui-même le TLS
>   (`r.TLS != nil`). Derrière le reverse-proxy qui est le déploiement recommandé
>   ([[Déploiement Docker et IPv6]]), il sort **sans** `Secure`, alors que
>   `X-Forwarded-Proto` est là pour le dire. Le symptôme n'apparaît jamais en
>   développement, ce qui est précisément ce qui rend l'oubli durable.
> - Aucune protection **CSRF** : ni jeton, ni vérification d'`Origin`. La seule
>   barrière est `SameSite=Lax`. Les mutations étant toutes en JSON et en
>   POST/PUT/PATCH/DELETE, la surface est étroite en pratique — mais elle est
>   étroite par accident, pas par décision, et une future route en `POST`
>   acceptant un formulaire la rouvrirait sans que rien ne le signale.

Un premier compte global-admin est créé au démarrage si la table est vide (`cmd/controller/main.go`) : mot de passe pris dans `MESHCRAFT_ADMIN_PASSWORD` ou tiré au sort et **journalisé une seule fois**. Le défaut inverse — un mot de passe fixe documenté — fait des instances qu'on oublie de durcir.

## Modèle d'autorisation

Portée = **le site**. Un utilisateur possède des attributions `(rôle, site)` ou un rôle global. **Livré tel quel** : `user_roles(user_id, site_id, role)` avec `role in ('viewer','operator','site_admin')`, plus le booléen `users.is_global_admin`.

| Rôle | Droits |
|---|---|
| **viewer** | lecture : équipements, topologie, journal, profils |
| **operator** | viewer + éditer intents/profils, déployer, adopter des équipements |
| **site_admin** | operator + gérer VLANs, **plan d'adressage de management du site**, credentials du site, utilisateurs du site, **déployer des proxys couvrant ses sites** |
| **global-admin** | tout, tous sites + création de sites, **proxys d'instance**, connecteurs, mises à jour, gestion de l'instance |

Deux précisions que le code impose et qu'il vaut mieux lire ici :

- `global-admin` n'est **pas** une valeur de `role` : c'est un booléen sur l'utilisateur, et il court-circuite `CanSite`. Le chercher dans `user_roles` ne le trouvera jamais.
- La clé primaire `(user_id, site_id)` impose **un seul rôle par site et par utilisateur**. Cumuler `operator` et `site_admin` sur le même site est donc impossible par construction — et n'a pas de sens, les rôles étant ordonnés (`RoleLevel`).
- L'[[Interface web]] parle un autre vocabulaire — *ingénieur* / *technicien* / *lecture seule* (`useSiteRole` : `ing` / `tech` / `viewer`) — parce que « operator » et « site admin » ne veulent rien dire pour l'exploitant à qui on montre l'écran. La traduction est faite en un seul endroit ; c'est le même modèle.

- **Les proxys sont cadrés par leur couverture de sites**, qui est leur frontière de droits ([[Proxy]]). **Livré, et fidèlement** :

| Action sur un proxy | Droit requis | Implémentation |
|---|---|---|
| le voir dans l'inventaire | un rôle sur un site couvert (proxy d'instance : tout le monde) | `canSeeProxy` ; la liste est filtrée **en SQL** (`p.global or proxy_sites ∈ …`) |
| le déployer | `site_admin` d'au moins un site — la couverture du proxy créé est **bornée à ses sites** | `requestedCoverage` : 403 sans aucun site admin, `global:true` refusé au non-global-admin, chaque site demandé vérifié |
| ajouter/retirer un site de sa couverture | `site_admin` de **ce** site, et de celui-là seulement | `handleSetProxySiteMember`, plus trois refus en 409 : proxy d'instance, proxy interne, **dernier** site |
| le **renommer**, le révoquer, le supprimer, le migrer, y déclarer des plages de scan | `site_admin` de **tous** les sites couverts, ou `global-admin` | `canAdminWholeProxy`, via `proxyForAdmin` |
| en faire un proxy d'instance (ou l'inverse) | `global-admin` | `handleSetProxyCoverage`, derrière le middleware `RequireGlobalAdmin` |

  La logique est de cloisonnement : un site-admin ne doit pas pouvoir fabriquer ni détourner un proxy qui verrait le réseau d'un autre site. D'où la règle « tous les sites couverts » dès qu'on agit sur le proxy lui-même, et le refus de retirer le **dernier** site couvert — un proxy sans site n'est plus visible de personne, donc inadministrable sans un `global-admin`.

  Deux raffinements du code qui prolongent la même intention :

  - `proxyForAdmin` répond **404 quand le proxy n'est pas visible** et 403 seulement quand il l'est. Répondre 403 dans les deux cas transformerait la matrice en oracle d'existence : un site-admin apprendrait le nombre de proxys des autres sites en énumérant des identifiants.
  - un site-admin ne peut pas rattacher une de ses plages de scan au **domaine d'adressage** d'un proxy qu'il n'administre pas (`checkDomainChoice`). Sans ce garde-fou, la fusion des découvertes offrait un chemin d'escalade que la matrice ci-dessus ne fermait pas : on n'agit pas sur le proxy, on agit sur ce à quoi ses résultats se raccrochent.
- Le sélecteur de site de l'[[Interface web]] ne montre que les sites attribués — **livré** (`ListSites` joint `user_roles`). L'entrée « tous les sites » pour les rôles globaux reste **décidée, non construite** : le sélecteur exige aujourd'hui un site courant.
- **Adoption** ([[Discovery]]) : adopter exige le droit `operator` **sur le site choisi** — **livré**, et vérifié *avant* toute lecture du candidat, parce que c'est cette action, et elle seule, qui fait entrer un équipement dans un site. Ce que la doc annonçait en plus reste **ouvert** : la liste des sites proposés n'est pas filtrée à ceux où l'utilisateur est `operator`, seulement à ceux où il a un rôle. Un `viewer` voit donc le site dans le choix et se prend un 403 à la validation — un refus juste, au plus mauvais moment.

> [!warning] Le contrôle est écrit à la main, pas déclaré par route
> C'est l'écart le plus important de cette page, et il porte sur du code qui
> existe : il n'y a **pas** de middleware `(permission, site)`. Il y a deux
> middlewares — « authentifié » et « global-admin » — et **tout le reste** est un
> appel explicite dans le handler : `requireSite(w, r, minRole)`,
> `deviceForWrite(w, r, minRole)`, ou `Auth.CanSite` en direct. Environ soixante-dix
> appels, tous corrects là où ils sont.
>
> Le défaut n'est pas dans ce qui est écrit, c'est que **rien ne casse quand on
> oublie de l'écrire**. Une route ajoutée sans son appel est ouverte à tout
> utilisateur authentifié, et le test qui l'aurait attrapée n'existe pas. Des trous
> connus à ce jour, tous sur des routes d'instance, donc non scopées par une URL de
> site : `GET /api/proxies/tunnels`, `GET /api/discovery/prefixes`, et surtout la
> file de découverte — `GET /api/discovery/candidates`, la **configuration brute**
> d'un candidat, et `DELETE` d'un candidat, qui n'est pas une lecture.
>
> La doc a longtemps affirmé « chaque route API porte une exigence `(permission,
> site)` vérifiée en middleware ». C'est la cible, et elle reste la bonne : un
> contrôle déclaré à la déclaration de route se relit d'un coup d'œil et se teste
> exhaustivement. Mais l'affirmer au présent faisait chercher un middleware
> inexistant à qui voulait comprendre pourquoi une route était ouverte.
>
> Nuance à ne pas perdre : pour les routes en `/api/sites/{siteID}/…`, le scoping
> par l'**URL** plus `requireSite` est aussi solide qu'un filtrage SQL — la
> requête ne peut pas déborder du site qu'elle nomme. Le filtrage en SQL par les
> sites autorisés n'est indispensable que pour les listes **globales**, et là il est
> bien en place : `ListSites` et `ListProxies` filtrent en base. C'est le reste des
> listes d'instance qui ne filtre rien.

- Les [[Credentials]] sont **utilisables** par operator (déploiement). Leurs secrets ne sortent pas des écrans de liste, mais **ils sont relisibles** : `POST /api/sites/{id}/credentials/{cred}/reveal` rend la valeur en clair à un `site_admin`, appartenance du credential au site de l'URL vérifiée, et l'appel est journalisé nominativement en `credential.revealed` (sévérité *avertissement*). Ce n'est pas une entorse au modèle, c'est le break-glass ramené à l'échelle du site — voir [[Identité de service et rotation]] §12 pour pourquoi il vaut mieux qu'il existe et soit tracé.
- **[[Identité de service et rotation]]** : la politique d'instance (comptes de service, algorithmes SNMP, télémétrie, périodes de rotation) est au `global-admin`, qui peut **verrouiller** un champ pour en faire un plancher que les sites ne peuvent pas abaisser ; la surcharge de site est au `site_admin`. Déclencher une rotation est une action d'`operator` sur le périmètre concerné, puisque c'est un déploiement comme un autre ([[Réconciliation]]). **Décidé, non construit** : il n'existe pas de table de politique, donc aucun de ces droits n'a encore d'objet sur lequel s'appliquer. Ce qui est livré à la place, et qui suit bien la règle ci-dessus : les identifiants de site et leurs générations sont en `site_admin`, la révélation aussi.
- L'accès [[Accès web pass-through]] est un droit distinct (il équivaut à un accès admin direct à l'équipement) : `operator` minimum + activable par site. **Décidé, non construit** — la fonctionnalité elle-même est encore une étude. Ce qui existe déjà et qui touche l'équipement par son interface web est **plus** restreint que la cible, pas moins : la sonde `POST /api/devices/{id}/web-probe` exige `site_admin`, et `POST /api/discovery/device-fetch` exige `global-admin`. C'est le bon sens de l'erreur tant que le droit dédié n'existe pas.

## Audit

Toute action mutante (login, adoption, apply, édition de profil, création de token, accès pass-through) émet un événement `user.*` dans le [[Journal d'événements]] avec l'identité, l'IP et l'objet touché. Pas de table d'audit séparée : un seul fil, filtrable `type=user.*`.

Le choix du fil unique est **livré et employé partout** — une centaine de types d'événements, dont `credential.revealed`, `proxy.coverage_changed`, `device.collector` — mais la famille `user.*` est, elle, en friche.

> [!warning] La famille `user.*` ne contient qu'un seul événement
> `user.login`, et rien d'autre. Ni `user.logout`, ni `user.login_failed`, ni
> aucun des gestes de la gestion des comptes : **créer un utilisateur, lui donner
> `site_admin`, le promouvoir global-admin ou le supprimer ne laisse aucune
> trace**. Et l'**IP** promise ci-dessus n'est nulle part : `InsertEvent` ne porte
> pas de colonne pour elle, seulement `actor` (l'email de session).
>
> Ce n'est pas un manque cosmétique. Le fil unique n'a de valeur d'audit que s'il
> est exhaustif sur les gestes de privilège ; incomplet, il est pire qu'absent,
> parce qu'on l'interroge en croyant qu'il répond. Un `type=user.*` qui rend une
> seule ligne se lit « personne n'a rien fait », pas « nous n'écrivons pas ».
>
> Ce qui manque est étroit et connu : émettre dans les cinq handlers de
> `users_handlers.go` et dans `handleLogout`, et ajouter l'IP (`middleware.RealIP`
> est déjà en place, l'adresse est dans la requête). L'ordre de priorité est
> `user.roles_changed` d'abord : c'est le seul geste qui change ce que quelqu'un
> peut faire.

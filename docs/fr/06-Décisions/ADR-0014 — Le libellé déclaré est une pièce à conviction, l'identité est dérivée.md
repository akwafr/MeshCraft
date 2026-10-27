---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0014 — Le libellé déclaré est une pièce à conviction, l'identité est dérivée

## Contexte

Un utilisateur signale « deux modèles strictement identiques dans le soft ».
Il y en a **trois**, et ils désignent les **12 mêmes commutateurs physiques**,
tous en `vendor = "Aruba (HPE)"` :

| occurrences | `devices.model` stocké |
|---|---|
| 8 | `Aruba Instant On 1930 48G 4SFP/SFP+ Switch` |
| 3 | `HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930` |
| 1 | `HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A` |

La cause n'est pas un bug : **ce constructeur publie deux grammaires de
libellé**, et elles entrent par deux chemins de découverte différents.
`internal/driver/instanton/inspect.go` le sait et le gère déjà — la forme
sysDescr (page de login, SNMP, parfois la référence catalogue `JL685A`) et la
forme de l'API de provisioning, qui compte sans le « p » (« 48G 4SFP/SFP+ »).
Les deux **doivent** rester reconnues au parse : sans la seconde, aucune regex
ne matche, les 4 cages SFP+ sont adoptées en cuivre et la façade retombe à
plat en 52 ports (c'est écrit dans le commentaire de `parsePorts`, et
`inspect_test.go` l'épingle).

Ce qui manque n'est donc pas une lecture, c'est une **identité**. Rien, dans le
produit, ne dit que ces trois chaînes nomment le même matériel.

### L'étendue réelle, mesurée

Le problème est **structurellement général et aujourd'hui local**. Quatre
producteurs indépendants écrivent `devices.model` :

1. le `Identify` d'un connecteur (grammaire propre au constructeur) ;
2. `discovery.ExtractFacts` (`internal/discovery/fingerprint.go`), générique,
   pour tous les constructeurs ;
3. le catalogue et l'adoption (`internal/catalog`, `cand.Model`) ;
4. un opérateur, à la main (`PATCH /api/devices/{id}`, import de configuration
   de site) — chemin sans aucune grammaire.

Mais sur l'inventaire réel — 37 équipements, 6 sites — **seule la famille
Instant On 1930 présente la collision**. Vérifié : MikroTik, Cisco et FS ont
une grammaire et une seule par famille, et les paires qui s'en approchent
(`CRS326-24G-2S+` / `CSS326-24G-2S+`, `S3400-48T4SP` / `S3900-24T4S`) sont du
matériel réellement différent. La raison tient à une borne : le `modelRe`
générique ne capture qu'un **seul jeton sans espace de 32 caractères au plus**,
donc il ne peut pas produire une phrase façon sysDescr. Il n'a mordu nulle part
ailleurs parce qu'il n'en est pas capable.

Risques latents repérés dans le code, sans témoin dans l'inventaire : MikroTik
RouterOS lit le modèle à deux endroits (`/system/resource board-name`, puis
`/system/routerboard model` qui l'écrase s'il existe — la référence
routerboard peut porter un suffixe de boîtier), et SwOS à deux autres (realm
Digest, `/sys.b`). Même forme de piège, une grammaire seulement pour l'instant.

Enfin, `matchSNMPIdentity` (`internal/api/reachability.go`) a **déjà** rencontré
le problème et l'a résolu localement, pour lui seul : il ne compare jamais deux
libellés, il cherche un jeton distinctif (quatre caractères, un chiffre). Le
commentaire cite textuellement la forme `JL685A`. La décision ci-dessous
généralise ce raisonnement au lieu de l'inventer.

## Décision

**Le libellé que l'équipement a déclaré n'est jamais réécrit. L'identité du
matériel est dérivée à la lecture, et c'est elle que l'interface montre.**

1. **`devices.model` reste brut, et aucune migration ne le touche.** C'est la
   pièce à conviction : le jour où une façade est fausse, la première question
   est « quelle grammaire est arrivée ? », et elle n'a de réponse que si la
   chaîne est intacte.

2. **L'identité est dérivée, jamais stockée.** `internal/modelid` répond
   `Of(vendor, model) → {Key, Label, SKU}`. Elle rejoint `Sync`, `Drift`,
   `CollectKind`, `WatchExcluded` sur l'étagère des champs de `model.Device`
   calculés à la lecture, et pour la même raison : une copie en base figerait
   la dérivation du jour où elle a été écrite, alors qu'une dérivation calculée
   s'améliore pour **toutes** les lignes existantes le jour où elle apprend une
   grammaire de plus.

3. **L'identité canonique est : marque + famille + géométrie de façade**, et la
   géométrie est lue **dans le libellé lui-même**, pas dans un catalogue. Pour
   les trois formes ci-dessus : Aruba, Instant On 1930, 48 cuivre + 4 SFP+ —
   les trois le disent toutes les trois, chacune dans sa grammaire. Le libellé
   canonique est recomposé à partir de ça
   (`Aruba Instant On 1930 48G 4SFP/SFP+ Switch`), ce qui n'invente aucun nom :
   c'est la forme que l'API de provisioning du constructeur émet, et celle que
   8 des 12 lignes portent déjà.

4. **La référence catalogue (`JL685A`) n'est PAS l'identité.** Elle est plus
   précise, et c'est exactement pourquoi elle ne peut pas servir : elle est
   **absente de deux des trois formes**. Une identité fondée sur elle
   re-séparerait le groupe au moment où on en a besoin — c'est-à-dire le bug,
   réécrit autrement. Règle générale : **l'identité canonique est la chose la
   plus précise que TOUS les chemins peuvent dire, pas la plus précise qu'UN
   chemin dit.** Le SKU est conservé à côté (`ID.SKU`), optionnel, affiché quand
   il est là, jamais requis.

5. **Quand l'identité n'est pas atteignable, le libellé brut EST son identité.**
   Si aucune règle de famille ne reconnaît la chaîne (cas de MikroTik, Cisco,
   FS, Arista, ProCurve aujourd'hui), `Label` vaut le brut inchangé et `Key` est
   le brut normalisé. Deux interdits, qui sont la raison d'être de cette clause :
   ne jamais rendre une clé **vide** (tous les inconnus se regrouperaient en un
   seul modèle fantôme), et ne jamais **fusionner** deux inconnus (du matériel
   différent serait déclaré identique). Un inconnu reste seul.

6. **On ne renomme que là où on a la preuve de deux grammaires.** Une règle de
   famille s'ajoute sur témoignage — deux libellés constatés pour un même
   matériel — pas par anticipation. Partout ailleurs, le brut est déjà canonique.

## Conséquences

- **Un champ de plus sur le fil** (`model_key`, `model_label`), et **deux points
  de dérivation à ne pas oublier** : `withSyncVLANs` (toutes les listes
  d'équipements et la fiche) et `publicDevicesOf` (tableau de bord partagé). Un
  troisième payload de device ajouté demain sans l'appel fera réapparaître le
  doublon, silencieusement. C'est le prix, il est assumé, et il est écrit ici
  pour que le prochain le voie.
- **`model.Device.MarshalJSON` serait la garantie, et elle nous est interdite** :
  `deviceView` embarque `model.Device`, donc la méthode serait promue et
  `json.Marshal(deviceView)` ne sérialiserait plus que la moitié du payload.
  Piège classique de Go, vérifié avant d'écrire : la dérivation reste explicite.
- **La fiche d'équipement montre désormais le libellé canonique** ; le brut passe
  en infobulle, là où il était déjà dans les listes. Il n'est plus lisible d'un
  coup d'œil — c'est une perte réelle, compensée par le fait qu'il reste intact
  en base et sur l'API.
- **`modelShort` (`web/src/modelLabel.ts`) n'est pas touché et ne devient pas
  l'identité.** Il traite un autre problème — faire tenir un libellé en 28
  caractères dans une tuile — et il ne pourrait pas servir : il ne se déclenche
  qu'au-delà d'une longueur, et il réduit deux des trois formes à « Aruba 1930 »
  mais la troisième à « Aruba JL685A ». Un raccourcisseur d'affichage n'est pas
  une identité ; les surcharger l'un de l'autre casserait les deux.
- **Le parsing des connecteurs n'est pas touché.** `internal/modelid` relit la
  géométrie avec les mêmes deux grammaires que `instanton/parsePorts`, sans
  réutiliser son code : les deux lectures ont des métiers différents (façade
  contre étiquette), et le connecteur in-tree part du dépôt (Phase 4). Deux
  lecteurs, assumés, chacun testé.
- **Aucune règle de regroupement n'est posée** pour les familles où la clé
  fusionne mais les libellés diffèrent (MikroTik `…-2S+` contre `…-2S+RM`, si le
  cas arrive) : la clé sert à dire « c'est le même modèle », pas à élire un nom.

## Alternatives écartées

**Canonicaliser à l'écriture, plus une migration qui converge les lignes
existantes.** Une seule chaîne en base, tout le reste devient trivial — et c'est
l'option qu'on propose naturellement. Écartée pour deux raisons, dont une
seule suffirait. D'abord elle **écrase ce que l'équipement a réellement
déclaré**, c'est-à-dire la seule trace qui permette de diagnostiquer une façade
fausse. Ensuite une migration qui réécrit `devices.model` sur des installations
en production est précisément ce que `CLAUDE.md` appelle coûteux à défaire :
l'information détruite ne revient pas, et il faudrait re-découvrir 12
équipements de production pour la retrouver. « C'est plus simple » n'achète pas
ça.

**Prendre le SKU (`JL685A`) comme identité.** La plus précise des références,
donc la candidate évidente. Absente de deux des trois formes : l'identité
vaudrait `JL685A` pour 1 équipement et rien pour 11. On aurait remplacé trois
groupes par deux.

**Stocker l'identité dérivée dans une colonne, avec un backfill.** Évite de
l'oublier quelque part — mais fige la dérivation d'aujourd'hui, et impose une
migration de plus à chaque grammaire apprise. Une identité est un **point de
vue sur la donnée**, pas une donnée ; elle appartient à la lecture.

**Corriger uniquement côté frontend, dans `modelLabel.ts`.** Le moins de code,
et le symptôme disparaît de l'écran. Mais l'API continue de servir trois
modèles — y compris à la surface d'outils MCP ([[ADR-0012 — La surface d'outils
n'est pas la surface d'API]]) et au tableau de bord public — et le prochain
client réimplémenterait la dérivation, différemment.

**Un catalogue de modèles, par référence constructeur.** La réponse propre, et
beaucoup plus de travail : il faut le tenir à jour pour sept constructeurs, et
un modèle absent du catalogue n'a plus d'identité du tout. La géométrie lue
dans le libellé donne 90 % du résultat sans rien à maintenir, et dégrade vers le
brut au lieu de dégrader vers rien.

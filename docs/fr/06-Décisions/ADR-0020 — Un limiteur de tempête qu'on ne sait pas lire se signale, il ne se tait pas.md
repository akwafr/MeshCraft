---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0020 — Un limiteur de tempête qu'on ne sait pas lire se signale, il ne se tait pas

## Contexte

Le storm control **existe déjà par interface** dans meshcraft, et il porte son
unité : `wire/mdpv1/stormcontrol.go` arbitre depuis le 2026-08 que `StormLimit`
transporte toujours un `StormRateUnit` explicite, et qu'**aucun connecteur ne
convertit** — parce que `percent → pps` demande une taille de trame et
`percent → bps` un débit de ligne qui change sous la configuration. Trois
classes (`broadcast`, `multicast`, `unknown_unicast`), trois unités
(`percent`, `pps`, `bps`), neuf connecteurs qui en expriment ce qu'ils peuvent.

Le 2026-10-25, un relevé **en lecture seule** sur `core01-arena` (DCS-7050SX-64-R,
EOS 4.28.13M, cœur de production du site ARENA) a demandé à l'équipement sa
propre grammaire plutôt qu'à une documentation. `show cli commands all-modes`
l'énonce en dix-neuf lignes ; voici celles qui comptent, verbatim :

```
IntfConfigMode: [no|default] storm-control all             level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control broadcast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control multicast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] storm-control unknown-unicast level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
IntfConfigMode: [no|default] logging event stormcontrol discards [ use-global ]
GlobalConfigMode: [no|default] storm-control bum aggregate traffic-class TRAFFIC_CLASS level pps PPS
GlobalConfigMode: [no|default] storm-control burst time TIME UNITS
```

Quatre faits en sortent, et ils ne pèsent pas le même poids.

1. **Il y a une quatrième classe par interface, `all`.** `show running-config all`
   la rend sur chacune des soixante interfaces du châssis (`no storm-control all`),
   à côté des trois autres. Ce n'est pas la réunion des trois : elle police
   **toute** la trame, unicast établi compris. Aucune autre plateforme de la
   flotte ne l'a — ni Cisco IOS, ni FASTPATH, ni FS, ni ProCurve, ni RouterOS,
   ni SwOS.

2. **L'unité n'est pas seulement un pourcentage.** Le connecteur `arista/eos`
   déclare `percent` et **seulement** `percent`, et son en-tête explique que les
   variantes `pps` et `kbps` « existent sur certains ASIC de la gamme et pas sur
   d'autres ». La grammaire de **cet** équipement liste les trois formes sans
   condition : `level LEVEL`, `level pps PPS`, `level rate RATE RATE_UNIT`. La
   prudence du connecteur sur l'**écriture** reste justifiée — la grammaire
   énonce ce que le parseur accepte, pas ce que le forwarding agent programme,
   et `show platform trident storm-control` montre que la puce est programmée en
   **kbps** quoi qu'on ait tapé. Mais cette prudence a été appliquée à la
   **lecture**, et là elle ne protège de rien.

3. **La lecture ignore silencieusement ce qu'elle ne comprend pas, et c'est un
   défaut.** `reStormLine` est ancrée :

   ```
   ^\s*(no\s+)?storm-control\s+(broadcast|multicast|unknown-unicast)(?:\s+level\s+([0-9.]+))?\s*$
   ```

   `storm-control broadcast level pps 100` ne correspond pas — `[0-9.]+` ne
   mange pas `pps`, et `\s*$` ne mange pas le reste. La ligne est donc sautée.
   Or `parseStormControl` applique ensuite la règle « le silence dans un bloc
   qu'on a lu **est** l'absence de limiteur » et rend la classe
   `Enabled: false`. **Un port limité à 100 paquets/s est rapporté comme non
   protégé**, et `storm-control all level 5` l'est aussi. C'est exactement le
   mensonge que ce fichier a été écrit pour empêcher, à l'endroit où personne ne
   l'avait cherché : la règle du silence est juste, son domaine d'application
   était trop large.

   Le même trou existe chez `cisco/ios`, qui le contourne déjà à la main : son
   parseur collecte les ports portant `storm-control unicast` dans un
   `[]string` local et les rend en `StormControlState.Notes`, en prose. La
   contrainte est donc partagée par au moins deux connecteurs, et chacun l'a
   résolue dans son coin.

4. **L'état est beaucoup plus riche que la configuration, et il dit son unité.**
   `show storm-control` est une commande d'**état**, et elle existe :

   ```
   Port       Type             Level    Units Rate(Mbps) Status        Drops Reason
   Et22       unknown-unicast  0.01     %     1          active       235275
              broadcast        0.01     %     1          active            0
              multicast        0.01     %     1          active            0
   ```

   L'équipement nomme l'**unité** lui-même (colonne `Units`), résout le
   pourcentage en débit absolu contre la vitesse nominale du port
   (`Rate(Mbps)` — 0,01 % de 10 G = 1 Mbit/s), compte les trames jetées
   (`Drops`) et dit si le limiteur configuré est réellement **en vigueur**
   (`Status`, et `reason` quand il ne l'est pas ; en JSON, `active` par port et
   `dormant` par classe). Rien de tout cela n'a d'équivalent dans
   `StormControlPortState`, qui ne porte que `{Class, Enabled, Rate, Unit}`.

   Un piège s'y cache : la sortie `| json` de cette même commande rend
   `"level": 1` là où le texte rend `0.01` et où la configuration dit `0.01`.
   Les dix ports policés du châssis portent tous la même valeur, donc l'échelle
   de ce champ — centièmes de pourcent, ou recopie du `rate` en Mbit/s, qui
   valent 1 tous les deux ici — **n'est pas départageable sans écrire**. Le
   texte et la configuration, eux, concordent.

## Décision

**Une instruction `storm-control` observée et non comprise devient une donnée du
contrat ; la règle « le silence vaut absence » ne s'applique plus qu'à un bloc
dont toutes les instructions ont été comprises.**

1. **`StormControlPortState` gagne `Foreign []StormForeignLimit`.** Chaque entrée
   porte la classe visée quand il y en a une (`Class` vide quand la plateforme
   nomme quelque chose qu'aucune classe ne couvre), la **ligne verbatim** de
   l'équipement, et une phrase disant pourquoi elle n'est pas modélisée. C'est
   un troisième état, et il était manquant : jusqu'ici « classe absente de
   `Limits` » voulait dire « non lue » et « présente avec `Enabled: false` »
   voulait dire « aucun limiteur ». Il n'y avait aucune façon de dire « il y a
   un limiteur ici et je ne prétendrai pas le comprendre ».

2. **Un parseur ne saute jamais une ligne `storm-control`.** Ce qu'il ne sait pas
   réduire à un `StormLimit` part dans `Foreign`. La propriété ne dépend donc
   pas de la liste des formes connues aujourd'hui : une variante EOS que ce
   relevé n'a pas vue se signalera au lieu de disparaître.

3. **La classe `all` n'entre pas dans le contrat.** Elle est propre à EOS, et
   surtout **elle n'est pas la réunion des trois** : un opérateur qui lirait
   « all » comme « les trois ensemble » se tromperait sur l'unicast établi.
   Elle est rapportée en `Foreign` avec `Class` vide.

4. **L'option `logging event stormcontrol discards` n'entre pas dans le contrat.**
   Propre à EOS, et c'est une option de **journalisation**, pas un limiteur.

5. **Lire une unité n'oblige pas à l'écrire.** `level pps PPS` est lu comme
   `StormRatePPS` — l'unité existe déjà dans le contrat, et rapporter « 100
   paquets/s » est la vérité. `StormControlCaps.Units` reste `percent` seul,
   parce qu'il répond à une autre question : ce que le connecteur accepte
   d'**écrire** sans convertir. Les deux surfaces sont séparées et peuvent
   légitimement différer.

6. **`level rate RATE RATE_UNIT` est `Foreign`.** Le jeton `RATE_UNIT` n'est pas
   développé par `show cli commands`, et le seul moyen de connaître son
   vocabulaire est d'en écrire un. Rapporter un débit sans son unité serait la
   faute fondatrice de ce fichier ; rapporter l'unité devinée serait la même
   faute avec de l'aplomb. La ligne part donc verbatim, à l'écran, pour un
   humain.

7. **Une écriture ne remplace pas un limiteur étranger en silence.**
   `ApplyStormControl` consultait l'état relu pour savoir s'il avait quelque
   chose à faire ; une classe `Foreign` n'est pas une classe absente, donc le
   geste s'exécute quand le plan nomme cette classe — l'opérateur l'a demandé —
   mais `ApplyResult.Notes` dit quelle ligne a été remplacée.

## Conséquences

- **`Foreign` est un champ que huit connecteurs sur neuf laisseront vide**, et
  c'est voulu : il ne décrit pas une capacité de plateforme, il décrit une
  rencontre. Le remplir est le travail du parseur, pas de la table de
  capacités — il n'y a donc rien à ajouter à `StormControlCaps`.
- **L'écran doit afficher `Foreign`, sinon le champ ne sert à rien.** Un port
  qui porte `storm-control all level 5` cesse d'être présenté comme non
  protégé ; en échange, la section storm control d'un équipement EOS peut
  désormais montrer une ligne que meshcraft ne sait pas éditer. C'est le prix,
  et il est payé dans le bon sens : un contrôle grisé avec la ligne de
  l'équipement à côté vaut mieux qu'un écran qui affirme « aucun limiteur ».
- **Le relevé n'a pas établi la forme exacte que `show running-config` donne aux
  variantes `pps` et `rate`.** L'obtenir demanderait de les configurer, donc
  d'écrire sur un cœur de production. Le parseur est écrit contre la grammaire
  que l'équipement a énoncée, et la règle 2 fait que se tromper sur le rendu
  coûte une ligne en `Foreign` au lieu d'un port faussement déclaré libre.
- **`Drops`, `Status`/`dormant`, `Rate(Mbps)` restent hors contrat.** Les
  compteurs sont de la télémétrie et l'en-tête de `stormcontrol.go` les y
  renvoie déjà. « Configuré mais dormant » mériterait d'exister — c'est la
  différence entre un port que l'écran dit protégé et un port qui l'est — mais
  aucune autre plateforme de la flotte n'a été relevée là-dessus, et un champ
  dont on ne sait pas si un seul autre connecteur peut le remplir est un champ
  qui attendra d'avoir deux porteurs. Noté, pas modélisé.
- **La sortie `| json` de `show storm-control` ne doit pas être lue** tant que
  l'échelle de son champ `level` n'est pas départagée. Le connecteur continue
  de lire `show running-config`, ce qui ne coûte aucun aller-retour
  supplémentaire puisque l'apply la relit déjà.

## Alternatives écartées

**Ajouter `StormAll` comme quatrième classe.** La lecture naïve de la demande, et
la plus tentante puisque EOS la nomme à côté des trois autres. Écartée deux
fois. D'abord parce qu'un seul connecteur sur neuf pourrait l'exprimer : la
case apparaîtrait sur tous les écrans et serait refusée partout sauf sur
l'Arista, ce qui est précisément le défaut que `StormControlCaps` a été créé
pour éviter. Ensuite, et c'est la raison dirimante, parce que `all` ne veut pas
dire ce que son nom suggère : elle police l'unicast qui a atteint sa
destination. Un opérateur qui coche « all » en croyant cocher les trois obtient
une limitation de débit que personne n'a demandée — le même défaut, mot pour
mot, que celui qui fait refuser `storm-control unicast` à `cisco/ios`.

**Lire `level pps 100` comme 100 % pour ne pas perdre la ligne.** Jamais
formulée ainsi, mais c'est ce que ferait une regex élargie à
`level\s+\S+\s+([0-9.]+)`. Cinq ordres de grandeur, acceptés sans un mot.

**Laisser la lecture ignorer ces lignes et se contenter d'une phrase dans
`Notes`.** L'état actuel, et ce que fait déjà `cisco/ios`. Écartée parce que
`Notes` est un `[]string` au niveau de l'**équipement** : rien n'y relie une
phrase à un port, donc rien ne permet à un écran de griser la bonne ligne ni à
un apply de savoir qu'il va écraser quelque chose. La prose convient pour
expliquer une absence, pas pour porter une observation par port — et la preuve
est que `cisco/ios` a dû inventer un `[]string` parallèle pour s'en sortir.

**Convertir `level rate 10 mbps` en bits par seconde.** L'arithmétique serait
exacte, et le contrat autorise déjà un changement de multiple exact dans une
même unité (`StormRateInKbps`). Écartée parce que le vocabulaire de `RATE_UNIT`
n'a pas été lu : convertir suppose de savoir que `mbps` en est un membre et
qu'il vaut 10⁶ bit/s plutôt que 2²⁰, et ces deux suppositions ne sont pas des
lectures. Le jour où une écriture de contrôle établit le vocabulaire, cette
alternative redevient la bonne — et le changement sera une ligne de parseur,
pas une forme de données, parce que `StormRateBPS` existe déjà.

**Écrire sur `core01-arena` pour départager `RATE_UNIT` et l'échelle du champ
`level` en JSON.** Deux questions ouvertes, une seule commande de configuration
pour les fermer. Écartée sans hésiter : c'est un cœur de production, le geste
qui renseigne est le geste qui coupe, et `Foreign` existe précisément pour que
l'ignorance ait une représentation honnête plutôt qu'un prix en interruption de
service.

---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0026 — Un lien de topologie a deux moitiés, chacune colorée par l'egress de son extrémité

## Contexte

Sur le plan de site, un lien était **un trait uniforme de 2,2 px**, coloré par
`loadColor` — une rampe verte→rouge, propre à ce fichier, lue contre
`max(toA, toB)` divisé par la capacité la plus haute des deux bouts.

Trois faits en découlaient :

- **l'asymétrie était invisible.** Un uplink qui déverse 4 Gb/s vers le cœur et
  reçoit 30 Mb/s se peignait exactement comme son inverse. Or le sens est la
  première question qu'on pose à un lien chargé : « qui pousse ? » ;
- **la rampe du lien n'était pas celle du produit.** `loadColor` montait au
  rouge, ce qu'ADR-0003 s'interdit explicitement (« la rampe s'arrête au lime
  […] un uplink saturé n'est pas une panne »). Une colonne Trafic bleue et un
  lien rouge décrivaient la même charge ;
- **l'absence de mesure se peignait comme du repos.** `linkRatesBetween`
  renvoie `{toA: 0, toB: 0}` quand aucun bout ne porte de compteur, et
  `internal/api/portrates.go` écrit déjà `0` plutôt que `null` dans
  l'inventaire en le disant dans son commentaire : « Zero is not "no
  measurement" — inventory cannot express ignorance on this field ». Un lien
  dont on ne savait rien était donc un lien au repos à l'écran.

La demande était de voir l'egress de chaque côté, en couleur, avec les valeurs.

## Décision

Un lien en service est **deux moitiés**. La moitié adjacente au commutateur X
est colorée par **l'egress de X sur ce lien** — le `tx` de son port — lu sur la
**rampe ADR-0003 contre la vitesse négociée de ce port-là**, et se termine par
une **pointe dont le sommet est exactement le milieu du segment**.

Quatre points, dans l'ordre où ils se contestent :

1. **La métrique est le `tx` du port proche, jamais une somme ni une moyenne.**
   Additionner ou moyenner les deux sens masque l'asymétrie, qui est la seule
   chose que ce rendu ajoute. À défaut du `tx` proche, le `rx` du port d'en
   face : c'est la même grandeur, aux pertes près.
2. **Chaque moitié lit contre SON port.** Deux bouts peuvent avoir négocié des
   vitesses différentes (une cage 10G sur un module 1G, une paire abîmée tombée
   à 100M) ; si c'est le cas, les deux moitiés d'un même lien n'ont pas la même
   couleur pour le même débit, et c'est une information, pas un défaut.
3. **Une moitié sans mesure est GRISE (`--text-dim`) et porte un `?`.** Jamais
   le bleu du bas de rampe, qui voudrait dire « au repos » sur un lien dont on
   ne sait rien.
4. **Les pointes n'existent que sur un lien qui transmet.** Un lien coupé ou
   bloqué par le spanning-tree reste un trait uniforme sans pointes.

## Conséquences

- **La rampe colore désormais un lien, pas seulement une courbe.** ADR-0003
  disait « elle ne colore qu'un tracé », en opposition aux pastilles, bordures
  et textes de statut. Un lien de plan **est** un tracé, donc la frontière
  tient — mais elle est désormais tendue, et le prochain qui voudra colorer une
  pastille avec `trafficColor` citera ce précédent. Les étiquettes de débit
  gardent leur encre neutre pour cette raison exacte.
- **`--traffic-0` (#2b5bd7) et `--accent` (#4f7cff) sont deux bleus voisins**, et
  le second est l'encre du blocage STP. Un lien au repos et un lien bloqué
  seraient confondables à la teinte seule. C'est la **présence des pointes** qui
  les sépare : un lien qui transmet a toujours deux sommets qui se touchent en
  son milieu, un lien bloqué n'en a jamais. On accepte donc qu'un lecteur qui
  ne connaît pas cette règle hésite une fois.
- **Le jaune `--indeterminate` n'est pas utilisé pour l'inconnu sur ce
  tracé**, contrairement à la règle générale de 2026-08-08 (UI-GUIDE §2). Il
  est à 14° de `--traffic-5` (#c6e018), le haut de la rampe : une moitié
  aveugle se lirait comme une moitié saturée, l'exact contresens que ce rendu
  existe pour empêcher. Le jaune n'est pas perdu — la **pastille d'état du
  nœud** à ce bout le porte déjà (`deviceDot`, `blindNow`), et c'est son travail
  de dire « on a perdu le point de vue sur ce commutateur ». La moitié de lien
  n'a qu'à dire « ce nombre n'est pas une lecture », ce que le gris et le `?`
  disent sans ambiguïté. Le prix payé : sur ce seul tracé, « jamais observé » et
  « plus observable » se peignent pareil.
- **Les étiquettes de débit changent de sens.** La pastille proche de A
  affichait ce qui arrivait VERS A ; elle affiche maintenant ce qui SORT de A,
  pour que le nombre et la couleur sous lui parlent de la même grandeur. Un
  opérateur habitué à l'ancienne lecture lira l'inverse pendant un moment. La
  flèche du glyphe (`▸`/`◂`) pointe dans le sens de la moitié, et c'est le seul
  garde-fou.
- **Les pointes occupent le milieu**, là où l'étiquette de vitesse vivait. Son
  déport passe de 7 à 15 px et celui de l'étiquette STP de 14 à 16 px : sur un
  lien court, les pastilles s'écartent donc un peu plus du trait qu'avant, et
  tombent un cran plus tôt dans l'ordre d'abandon de `topologyLinkLabels.ts`.
- **Sous 20 px de longueur, les pointes disparaissent.** Elles rétrécissent
  d'abord — plafonnées à 45 % de leur moitié, ce qui rend le non-débordement
  structurel et non réglé — puis s'effacent quand il ne reste plus la place
  d'en dessiner une lisible. Deux équipements posés l'un contre l'autre ont donc
  un lien à deux couleurs sans flèche.

## Alternatives écartées

**Un seul trait, coloré par le maximum des deux sens.** C'est l'état d'avant.
Répond à « ce lien est-il chargé » et jamais à « dans quel sens », et c'est la
seconde qui mène à une action.

**Un trait, coloré par la somme des deux sens.** Plus juste pour « charge du
câble », mais l'asymétrie disparaît une seconde fois, et un lien à 500/500 se
lirait comme un lien à 1000/0 sur un gigabit — deux situations qui n'appellent
pas la même chose.

**Garder `loadColor` vert→rouge et ne faire que couper le lien en deux.** Le
moins de travail, et ça laisse deux rampes contradictoires dans le produit pour
la même grandeur. Le rouge sur un uplink qui fait son travail est précisément ce
qu'ADR-0003 refuse.

**Faire de l'inconnu un trait pointillé.** Le pointillé est déjà pris deux
fois sur ce tracé : lien hors service, et voisin LLDP vu d'un seul côté. Un
troisième sens sur le même canal n'en est plus un.

**Peindre l'inconnu en `--indeterminate` comme partout ailleurs.** Refusé pour
la collision de teinte avec le haut de rampe, détaillée en conséquence. C'est
la seule entorse de cet ADR à une règle générale du produit, et elle est locale
à ce tracé.

**Des pointes de taille fixe.** Deux pointes de 11 px sur un lien de 15 px se
chevauchent et dépassent chacune chez l'autre — l'exact « débordement » que la
demande nomme. La taille est donc relative à la moitié, et le sommet est
construit *sur* le point milieu plutôt que calculé pour y arriver.

**Afficher les deux valeurs seulement au survol.** La topologie est l'écran où
l'on cherche quel lien pousse ; obliger à survoler quatorze liens pour le
trouver annule le gain. Les deux valeurs sont donc permanentes, et
`topologyLinkLabels.ts` les retire déjà quand le lien devient trop court pour
les porter. Le survol garde ce qu'il sait faire de mieux : les 24 h.

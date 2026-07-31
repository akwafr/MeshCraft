---
tags: [frontend, topologie]
---

# Weathermap — topologie LLDP

Deuxième page majeure : les équipements du site placés librement sur un plan, reliés par leurs liens réels découverts via **LLDP**, avec l'état (et à terme la charge) des liens — une weathermap au sens classique.

## Où en est cette page

L'écran existe (onglet **Topologie**, `web/src/pages/Plan.tsx`), il est utilisable, et il n'est **pas** une weathermap : les liens qu'il montre sont **saisis à la main**, pas découverts.

| Question | État |
|---|---|
| Écran de plan : nœuds placés, liens dessinés, zoom, niveau de détail | **livré** — `web/src/pages/Plan.tsx` |
| Positions persistées par site | **livré** — colonnes `devices.pos_x` / `pos_y` (migration `0002`), pas de table dédiée |
| Liens : création, suppression, état, note, vitesse | **livré, en SAISIE** — `POST /api/sites/{id}/links`, `DELETE /api/links/{id}` |
| Cadres de regroupement sur le plan | **livré, mais dérivés du champ *place*** de chaque équipement, pas d'un découpage du plan |
| Panneau « diagnostic des liaisons » | **livré** — liste les liens qui ne sont pas `up` ou qui portent une note |
| Façade de ports dans le nœud au zoom élevé | **livré** — le nœud passe en vue compacte sous 60 % |
| **Découverte LLDP** des liens, corrélation des deux extrémités | **LIVRÉE** depuis le 29/07/2026 — collecte, stockage et tracé, distincts des liens saisis ([[Voisinage LLDP]]) |
| Charge des liens (la vraie fonction weathermap) | **livrée** depuis le 29/07/2026 — les débits viennent des compteurs (`internal/api/portrates.go`), et `simulated: false` le dit. Reste simulé le seul cas où le stockage de séries est absent de l'instance |
| Voisins fantômes (**adoption** d'un clic), agrégats LACP, proxy en nœud spécial | **décidés, non construits** — le voisin fantôme est désormais *listé* avec son châssis, mais il n'y a aucun geste d'adoption |
| Image de fond par site, layout automatique, export PNG/SVG, mode mural | **décidés, non construits** |
| Mise à jour temps réel | **livré, autrement** — pas de WebSocket : un flux SSE annonce « ce site a changé » et l'écran recharge ([[Interface web]]) |

> [!warning] Deux natures de liens cohabitent sur cette page, et il faut les lire différemment
> C'est l'écart à connaître avant tout autre, parce qu'il change ce que chaque trait
> **veut dire** :
>
> - un lien **SAISI** (`device_links`, trait épais, couleur d'état) est une
>   **déclaration d'un exploitant**. Il survit au débranchement, et c'est tout son
>   intérêt : c'est contre lui qu'on compare le réel ;
> - un lien **OBSERVÉ** (dérivé du voisinage LLDP, trait léger décalé de 5 px) est un
>   **constat daté**, vieux d'une minute au plus. Il disparaît quand le câble part.
>
> Les mêler ferait disparaître un lien saisi parce qu'un switch a été débranché dix
> secondes. Rien de la collecte n'écrit donc dans `device_links`, et il n'y a **aucune
> route d'écriture** pour un constat : il est poussé par le collecteur ou il n'existe pas.
>
> **Ce qui reste vrai des liens saisis**, et ne change pas avec ce lot : il n'y a **pas de
> route de mise à jour** d'un lien, donc l'état et la note sont **figés à la création**. Un
> lien saisi `up` restera vert même si le port tombe — la couleur ne dit pas ce que fait le
> réseau, elle dit ce qu'on a tapé. Modifier une liaison se fait en la supprimant et en la
> recréant. Ce sont les transitions de port (`port.up` / `port.down`) qui portent la vérité
> de l'état, et elles sont réelles ([[Monitoring]]).
>
> **Ce qui a changé** : un lien manquant sur la carte n'est plus forcément une saisie qui
> manque — le voisinage peut le montrer, en trait léger, sans qu'on l'ait tapé.

> [!important] Un lien vu d'UN SEUL CÔTÉ est le cas NORMAL, et il se trace
> La conception d'origine formulait la corrélation ainsi : *A voit B et B voit A → confirmé
> (trait plein) ; vu d'un seul côté → présumé (pointillé)*, et ajoutait qu'un lien vu d'un
> seul côté « est le symptôme le plus fréquent d'un vrai problème ». **Cette dernière phrase
> est fausse sur le parc de référence**, et le relevé le démontre : une plateforme entière
> n'implémente pas 802.1AB (MikroTik en SwOS — walk de `1.0.8802` **vide**, mesuré). Tout
> lien qui touche un CSS326 ne sera **jamais** vu que d'en face, quoi qu'on fasse. S'y
> ajoutent les voisins hors inventaire, qui ne racontent rien par construction.
>
> L'écran présente donc l'unilatéralité comme un **degré de certitude**, pas comme un
> défaut : le trait est atténué, l'infobulle nomme les deux causes ordinaires, et rien ne
> suggère d'aller chercher une panne. Voir [[Voisinage LLDP]] §1.1 et §5.

> [!note] Pourquoi ni la couleur ni le pointillé long ne pouvaient porter la distinction
> Les deux axes évidents étaient **déjà pris** par les liens saisis : la **couleur** porte
> l'état (`--danger`, `--warn`, `--border`) et le **pointillé « 6 5 »** porte l'état `down`.
> Réemployer l'un ou l'autre aurait rendu un lien observé indiscernable d'un lien saisi en
> panne — la confusion exacte que toute cette séparation existe pour empêcher.
>
> Le troisième axe retenu est la **teinte d'accent**, un pointillé **court** (« 2 3 ») et une
> épaisseur moindre. Plus un **décalage perpendiculaire de 5 px**, sans lequel un constat qui
> confirme une déclaration serait superposé au pixel près — les deux relient les centres des
> mêmes nœuds — et l'un des deux serait invisible. Les liens observés sont dessinés **avant**
> les liens saisis, donc en dessous : le plan déclaré reste au premier plan.

## Données

- **Nœuds** : les devices du site. Position `x,y` persistée — non pas dans une table `device_positions` mais dans les colonnes `devices.pos_x` / `pos_y`, écrites par le `PATCH` d'équipement ordinaire. Le [[Proxy]] du site comme nœud spécial et l'**image de fond** (plan du bâtiment, uploadée par site) restent **décidés, non construits** : `sites.plan` porte bien un jsonb, mais il ne contient que des dimensions, et l'écran n'en lit que la largeur et la hauteur.
- **Liens** : **deux sources**, jamais mêlées. Les **saisis** viennent de `device_links` (modale « Ajouter une liaison »). Les **observés** viennent de `GET /api/sites/{id}/lldp`, dérivés du voisinage LLDP collecté au polling — `lldpRemTable`, décodée par `internal/snmppoll/lldp.go`. La **corrélation des deux extrémités** est livrée et sa réciprocité est **exacte** : A/pa doit clamer B/pb *et* B/pb clamer A/pa. La règle plus large « A et B se voient » aurait attrapé quelques cas de plus et se serait trompée sur celui qui compte — deux équipements reliés par **deux câbles**, où elle aurait confirmé mutuellement les deux mauvaises moitiés. La nuance ne vit **pas** dans `device_links`, dont le schéma reste inchangé : elle vit dans `lldp_neighbors` et `lldp_self` (migration 0028), et le caractère bilatéral est **calculé à la lecture** plutôt que stocké — stocké, il se périmerait dès qu'un côté cesse de voir l'autre. Voir [[Voisinage LLDP]] §3.
- **Voisins fantômes** : un voisin LLDP non adopté (châssis inconnu de l'inventaire) apparaît en nœud grisé → clic = proposition d'adoption ([[Discovery]]). **Toujours non construit**, mais plus impossible : le constat existe désormais en base et remonte dans `links` avec `b_device` vide, son châssis et son `sysDescr`. Il est **listé** dans le panneau « Voisinage LLDP observé », ce qui donne de quoi créer l'équipement à la main. Ce qui manque est le **tracé** (un fantôme n'a pas de position sur le plan, et lui en inventer une reviendrait à dessiner un équipement au hasard) et le **geste d'adoption**. Sur le parc de référence, trois voisins sont dans ce cas — un RB5009UG, un CRS305 et un S3900 — donc ce n'est pas un cas de bord.
- Agrégats LACP : liens parallèles entre deux mêmes devices regroupés en un trait épais avec badge `×2`. **Décidé, non construit** : deux liens A↔B **saisis** sont toujours dessinés comme deux traits **superposés au pixel près**, puisqu'ils partent du même centre de nœud. Un agrégat est donc indiscernable d'un lien unique — et c'est le cas où il faudrait le plus le voir. Le décalage de 5 px introduit pour les liens observés ne règle **pas** ce cas : il sépare l'observé du saisi, pas deux liens de même nature. En revanche la dérivation LLDP, elle, distingue bien deux câbles entre les mêmes équipements (réciprocité exacte, cf. ci-dessus) : l'information existe, c'est le rendu qui manque.

## Rendu

- Canvas pan/zoom. **Livré, sans bibliothèque** : il n'y a *aucune* dépendance graphique dans `web/package.json` — ni `@xyflow/react`, ni `elkjs`, ni `d3`. Le plan est fait main : les nœuds sont des `<div>` positionnés en absolu, les liens un **unique `<svg>`** superposé, et le zoom un `transform: scale()` sur le calque, persisté en `localStorage`. Le déplacement se fait par le défilement natif du conteneur ; le zoom au Ctrl/⌘ + molette **garde fixe le point sous le curseur**, et un bouton « Ajuster » cadre le contenu.

  Pourquoi pas React Flow : la page avait besoin de trois choses — placer, relier, zoomer — et d'une quatrième qui n'est pas dans les bibliothèques de graphes, à savoir **dessiner la façade de ports** dans le nœud. Une bibliothèque de 200 Ko pour trois primitives, dans un binaire que l'on veut petit et servi hors ligne par le [[Contrôleur]], se paie sans rien rendre. Ce qui **manque** de ce choix est réel et connu : pas de **minimap**, pas de glisser du fond pour se déplacer, et aucun **layout automatique** — un équipement neuf naît à `(0,0)`, donc tous empilés dans le coin haut-gauche jusqu'à placement manuel. Sur un site de cinquante switchs, la première ouverture du plan est un tas.
- Un nœud = version compacte de la carte équipement : logo, hostname, statut ; **zoom sémantique** — **livré** : sous 60 % de zoom le nœud n'affiche que son nom, au-dessus il montre sa façade complète ([[Rendu des ports]], cases de 9 px). Le seuil n'est pas cosmétique : en dessous, les cases ne sont plus lisibles, et les dessiner ferait payer le rendu de plusieurs milliers d'éléments pour rien. Ce qui reste **non construit** : les liens **ne s'ancrent pas sur le port réel**, ils partent du centre du nœud. C'est ce qui rendra la façade dans le nœud vraiment utile — savoir *par où* passe un lien — et c'est aussi ce qui séparera visuellement deux liens parallèles.
- Liens colorés par état : vert (up), rouge (une extrémité down), gris (device unreachable). **Livré**, mais l'état vient de la **saisie** (voir le callout), pas de l'observation. Le dégradé de couleur par **charge** (ratio débit/vitesse depuis les compteurs SNMP — la vraie fonction weathermap), avec libellé `1G · 34 %`, reste **décidé, non construit** — mais ses deux ingrédients existent enfin : le débit mesuré par port (`rx_mbps` / `tx_mbps`, dérivés des compteurs) et la vitesse négociée du lien (`link`). Il ne manque que le rapport et l'échelle de couleur.

> [!success] Les débits affichés sur les liens sont MESURÉS — depuis le 29 juillet 2026
> Ce callout portait l'inverse, et il avait raison : les libellés de débit venaient d'un
> jitter déterministe (`jitAt`) autour des `rx_mbps` / `tx_mbps` que seul un **import de
> configuration** avait posés. Le collecteur lisait bien `if_counters`, et **rien** ne
> convertissait ces compteurs en débit par port — donc zéro sur tout équipement découvert,
> et un chiffre qui bouge sans venir du parc sur les autres.
>
> La dérivation existe maintenant : `internal/api/portrates.go` interroge la base de
> séries toutes les 30 s (`rate()` sur une fenêtre de 2 min) et reporte le résultat dans
> `devices.ports[].rx_mbps` / `tx_mbps`. Les trois écrans qui lisaient déjà ces champs — les
> liens de cette page, le top des ports du [[Monitoring]], les étincelles de la fiche
> équipement — affichent donc du mesuré, **sans qu'une ligne de leur code change**. Et
> `GET /api/sites/{id}/traffic` rend `simulated: false`.
>
> **`jitAt` a été RETIRÉ**, ainsi que l'horloge `useTick` qui l'entretenait. Il faisait
> osciller les libellés de ±18 % toutes les 2,2 secondes : un exploitant lisait 43 Mb/s là
> où le port en portait 37, et voyait le chiffre changer sans que rien ne bouge sur le
> réseau. C'était acceptable tant que la valeur sous-jacente était fictive ; sur une
> mesure, c'est du bruit ajouté à une donnée juste — et c'est sur ce genre de chiffre qu'on
> décide d'une capacité. Les valeurs ne bougent donc plus qu'au rythme du rafraîchissement
> réel de la fiche. Une interface plus calme, qui dit la vérité.
>
> La courbe de REPLI a perdu sa variation pour la même raison : quand la mesure n'est pas
> disponible, elle est désormais PLATE, à la valeur d'inventaire, avec son drapeau. Une
> ligne droite ne prétend rien ; une courbe inventée prétend tout.
>
> Le drapeau `simulated: true` subsiste pour trois cas légitimes, et l'écran doit continuer
> de le lire : aucun stockage de métriques configuré, une instance sans historique, ou un
> stockage momentanément injoignable. La courbe redevient alors dérivée de l'inventaire.

- Premier placement : layout automatique (arbre depuis la racine STP/gateway, sinon force-directed elkjs), puis l'utilisateur ajuste et les positions sont sauvées. **Décidé, non construit.** Ce qui existe à la place, et qui rend le tas initial supportable : des **cadres en pointillés dérivés du champ *place*** de chaque équipement. Une zone par valeur de *place*, dont le cadre est **mesuré** sur la hauteur réelle des nœuds (un switch chargé en ports dépasse largement une hauteur devinée, et un cadre trop court laisse l'équipement déborder de son propre lieu — or le cadre est la seule chose qui dise « ces switchs sont au même endroit »). Déplacer un équipement dans un lieu le fait changer de cadre. Ce n'est pas un layout, mais c'est la **structure** qu'un layout devrait respecter, et elle vient d'une donnée que l'exploitant tient déjà à jour.
- Mise à jour temps réel : **livré, mais pas par le mécanisme décrit**. Il n'y a pas de WebSocket poussant des deltas : un flux **SSE** annonce « ce site a changé » et l'écran recharge par l'API ([[Interface web]], § *Temps réel*). Un raffinement propre à cet écran : le flux **supprime l'écho de ses propres écritures** (en-tête de tabulation), sans quoi un glisser-déposer de nœud aurait déclenché un rechargement à chaque pixel.

> [!note] La légende de bas de page a été retirée
> L'écran portait sous le plan un paragraphe de dix lignes qui décrivait tout :
> le sens des flèches et sur quel port les compteurs sont lus, ce que les cadres
> en pointillés entourent, le survol d'une liaison, le glissement d'un switch, le
> zoom au Ctrl/⌘ + molette, le bouton « Ajuster », et le passage en vue compacte
> sous 60 %. Il a été **supprimé**, pas déplacé ([[Interface web]], § *Le texte
> explicatif vit derrière une aide*).
>
> Ce n'est pas une perte d'information mais un constat sur ce que la page montre
> déjà : les flèches sont dessinées, les cadres portent le nom du lieu, l'infobulle
> de liaison donne le détail au survol, les boutons de zoom sont là avec leur
> `title`, et la vue compacte se voit en zoomant. Ce que ce paragraphe apportait
> vraiment — **sur quel port les compteurs sont lus** — est de toute façon un
> détail que la légende de bas de page portait mal. Maintenant que les débits
> viennent des compteurs, cette phrase-là devra revenir — derrière une aide, et
> non en bas de page.

## Interactions

- Drag pour placer : **livré** (pointer events, position temporaire pendant le glissement, enregistrement au relâcher, et un clic sans mouvement navigue vers la fiche du switch). Le **verrouillage** d'une position reste non construit.
- Clic lien → détail (ports des deux côtés, vitesse, erreurs, historique d'événements du lien) : **partiel** — les deux extrémités, la vitesse et la note sont dans l'infobulle du lien et dans le panneau de diagnostic ; il n'y a pas d'historique par lien, faute d'événements de lien autres que la création et la suppression.
- Clic nœud → même panneau que la page équipements : **livré**, sous la forme d'une navigation vers la fiche du switch plutôt que d'un panneau latéral.
- Export PNG/SVG de la carte (documentation d'exploitation) : **décidé, non construit**. Il n'y a aucun export dans l'écran.
- Mode « mural » plein écran sans chrome pour l'affichage NOC : **décidé, non construit**. Les deux raisons de le repousser sont levées depuis le 29/07/2026 — la découverte LLDP et la charge mesurée — et c'est ce qui le rend désormais envisageable : c'est le seul usage où personne n'est là pour douter de ce qui s'affiche, donc le seul qui exigeait que rien n'y soit inventé.

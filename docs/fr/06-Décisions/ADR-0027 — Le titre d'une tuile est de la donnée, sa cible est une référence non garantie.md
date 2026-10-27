---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0027 — Le titre d'une tuile est de la donnée, sa cible est une référence non garantie

## Contexte

Le catalogue de tuiles (`store.TileCatalog` côté serveur,
`web/src/dashboardTiles.tsx` côté navigateur) est **entièrement fait de clés
i18n**. Chaque tuile déclare un `labelKey`, et `make test-web` vérifie que
cette clé existe en anglais et en français. C'est vrai des dix-huit tuiles
existantes sans exception, et c'est devenu une habitude de lecture : « un
libellé de tuile, ça se traduit ».

Deux demandes arrivent en même temps et attaquent cette habitude par les deux
bouts :

1. **Renommer une tuile.** Deux tuiles « Trafic du site » sur un même tableau,
   réglées sur deux fenêtres différentes, portent le même titre ; l'exploitant
   veut lire « Trafic — 7 jours ». Ce texte vient de lui.
2. **Une tuile de trafic par port.** Elle ne veut rien dire tant qu'on ne l'a
   pas pointée sur un équipement et un port. Elle porte donc une **référence**
   vers une ligne de `devices` — et sur un site de 724 ports, cette référence
   est le cœur de la tuile.

La colonne `dashboard_tiles.options` existe déjà (migration 0041) et porte des
réglages par tuile : la fenêtre du graphique de trafic, la sévérité minimale du
journal. Jusqu'ici ce sont des **énumérations fermées** — quatre fenêtres, cinq
sévérités —, validées côté serveur parce que la route publique s'en sert pour
borner ce qu'elle demande au magasin de séries. Aucune n'est du texte libre, et
aucune ne désigne une autre ligne de la base.

La question n'est donc pas « où ranger deux champs », c'est : **est-ce que
`options` a le droit de porter de la donnée utilisateur et une référence que
la base ne garantit pas ?**

## Décision

**Oui, et les deux sont des données ordinaires rangées dans `options`.**

**Le titre est de la donnée, pas une chaîne i18n.** `options.title` porte le
texte tel que l'exploitant l'a tapé. Il ne part dans aucune locale, ne passe
par aucun `t()`, et survit intact à un changement de langue. Trois règles le
cadrent, et elles sont la décision autant que la phrase ci-dessus :

- **Une tuile sans `title` garde son libellé traduit.** Le `labelKey` du
  catalogue reste le défaut, résolu à l'affichage. On ne stocke **jamais** le
  libellé traduit au moment du renommage : écrire « Trafic du site » dans
  `options.title` parce que l'interface était en français figerait la langue
  d'une tuile par le seul fait de l'avoir renommée une fois.
- **Un titre vide efface la clé, il ne la stocke pas vide.** Renommer avec une
  chaîne blanche **revient au libellé par défaut**. La clé est *supprimée* de
  `options`, pas mise à `""` : « pas de titre personnalisé » doit être un seul
  état, sinon `sameLayout` voit une modification là où l'écran n'a pas bougé,
  et le bouton « Enregistrer » apparaît pour un geste annulé.
- **Un titre est borné** — 60 caractères, refusés côté serveur, pas seulement
  côté navigateur. Un en-tête de tuile fait deux colonnes de grille au
  minimum ; au-delà, le CSS tronque, et la borne existe pour que le stockage ne
  porte pas un roman.

**La cible est une référence que rien ne garantit.** `options.device_id` et
`options.port` désignent un équipement et un port, et **aucune contrainte de
clé étrangère ne les protège** : ni la base, ni le `on delete cascade` des
équipements. Supprimer un équipement laisse derrière lui des tuiles qui le
pointent encore, et c'est **accepté plutôt que subi** :

> Une tuile dont la cible a disparu **le dit sobrement et reste en place**.

Trois disparitions, trois phrases distinctes — l'équipement supprimé, le port
absent de la façade, le port sans mesure — et dans les trois cas la tuile garde
ses sélecteurs pour être re-pointée. Elle ne se supprime pas d'elle-même, et
elle ne trace **jamais** une courbe plate : zéro est une mesure, l'absence de
mesure n'en est pas une.

## Conséquences

- **Aucune migration**, et c'est le gain direct : `options` est déjà du `jsonb`
  opaque au magasin. Le titre et la cible voyagent avec la disposition, par la
  route qui existe (`PUT /api/dashboards/{id}/tiles`), et sont enregistrés par
  le bouton « Enregistrer » que l'exploitant utilise déjà pour avoir déplacé
  une tuile. Pas de nouvelle route, pas de nouveau geste.
- **Le serveur valide désormais un contenu d'`options`, ce qu'il ne faisait
  pas.** `options` était vérifié « JSON valide » et rien d'autre, donc sans
  borne de taille. À partir du moment où une de ses clés est **affichée**, ne
  pas la borner serait un trou : la borne de 60 caractères et un plafond global
  sur la taille du bloc sont posés dans `tileInput.valid`.
- **Le commentaire de `store.DashboardTile` devient à moitié faux et doit être
  corrigé** : `options` n'est plus « des réglages, le reste est l'affaire de
  l'interface ». Il porte une référence dont la durée de vie n'est pas celle de
  la ligne qu'elle désigne.
- **Une tuile ciblée ne peut pas être semée.** Les deux tableaux par défaut
  placent toutes les tuiles du catalogue — c'est une exigence testée
  (`TestDefaultDashboardsCoverTheWholeCatalogue`), et elle existe pour qu'aucune
  tuile ne soit invisible à qui n'ouvre jamais le sélecteur. Une tuile sans
  cible n'a rien à montrer sur un site neuf : le catalogue gagne donc un
  `Targeted bool`, et l'exigence de semis ne s'applique qu'aux tuiles qui ne le
  sont pas. Le prix est réel et assumé : **la tuile « Trafic d'un port » ne se
  découvre qu'en ouvrant le sélecteur de tuiles.**
- **Un titre personnalisé n'est pas traduit, par construction.** Un exploitant
  francophone qui renomme une tuile impose son texte au collègue qui lit
  l'interface en anglais. C'est le comportement voulu — un tableau de bord
  appartient au site (migration 0041), et un nom donné par quelqu'un est un nom,
  pas un libellé d'interface. Le même arbitrage que pour le nom d'un tableau de
  bord, d'un VLAN ou d'un profil de port, qui ne sont pas traduits non plus.
- **Un lien public affiche un titre personnalisé tel quel.** Il faisait déjà
  voyager le nom du tableau et du site ; un titre de tuile est de la même
  nature, et la projection publique le laisse donc passer. À savoir en
  renommant : ce qu'on écrit dans un titre de tuile est lisible par qui détient
  le lien.

## Alternatives écartées

**Une colonne `title text` sur `dashboard_tiles`.** La base aurait borné la
longueur elle-même, ce qui est son métier, et le champ aurait été visible dans
le schéma au lieu d'être caché dans un sac. Écartée parce qu'elle fait payer
une migration pour un champ dont la validation tient en trois lignes, et
surtout parce qu'elle n'aurait rien réglé pour la **cible** : il aurait fallu
une deuxième colonne, puis une troisième à la prochaine tuile paramétrable, et
`options` existe précisément pour que le nombre de colonnes ne suive pas le
nombre de tuiles. Une table majoritairement vide est le motif que la migration
0041 a déjà refusé.

**Un `device_id uuid references devices(id) on delete cascade` dans une
colonne.** Techniquement la bonne façon de ne jamais avoir de référence
pendante — et le mauvais produit : supprimer un équipement ferait **disparaître
la tuile** du tableau de bord de tout le monde, en laissant un trou dans la
disposition sans un mot d'explication. C'est exactement ce que
`dashboardGrid` refuse déjà de faire pour un type de tuile inconnu, et pour la
même raison : un trou dans une disposition se lit comme une perte de données.
Avec `on delete set null`, on obtient ce que cette décision retient — une tuile
qui survit et le dit — au prix d'une colonne et d'une migration.

**Traiter le titre comme une clé i18n et l'ajouter aux locales.** La pente
naturelle vu le catalogue actuel, et c'est elle qu'il faut nommer pour
l'écarter : une clé ajoutée au moment d'un renommage casse la parité en+fr à
l'exécution (le gel des neuf autres locales la rend absente partout ailleurs),
et un texte d'exploitant n'a de toute façon aucune traduction à aller chercher.
Variante plus insidieuse, écartée par la même règle : stocker le libellé
**traduit** du catalogue comme titre de départ dans le champ du formulaire. Le
champ s'ouvre donc sur le titre personnalisé s'il y en a un, et **vide** sinon,
avec le libellé par défaut en `placeholder`.

**Un titre vide stocké tel quel, affiché tel quel.** Littéral et défendable :
l'exploitant a demandé un titre vide. Écarté parce qu'un en-tête vide ne se
distingue pas d'un bug d'affichage, et que « effacer le nom » veut dire
« reprendre celui d'origine » dans tous les logiciels où ce geste existe. Le
revenir-au-défaut est de surcroît le seul moyen d'annuler un renommage sans
retaper la traduction exacte à la main.

**Supprimer automatiquement une tuile dont la cible a disparu.** Évite l'état
dégradé en supprimant l'information. Un équipement remplacé revient souvent
sous le même nom, et une tuile effacée pendant un remplacement de matériel est
un réglage perdu que personne ne sait avoir perdu.

---
Statut: Accepté
Date: 2026-10-24
Remplace: ADR-0006
---

# ADR-0008 — Le référentiel de temps est l'horloge de celui qui regarde

## Contexte

[[ADR-0006 — La fraîcheur se mesure sur l'horloge du serveur]] a réglé le bon
problème avec le mauvais référentiel.

Il a correctement établi que calculer `Date.now() - last_seen` compare deux
horloges sans rapport, et que c'est ce qui affichait un proxy sain comme
« silencieux depuis 6 min ». Il a ensuite décidé de tout mesurer **sur
l'horloge du contrôleur**.

Deux défauts y sont apparus à l'usage :

1. **Les âges sont redevenus justes, les heures affichées non.** Quinze
   endroits rendent un horodatage serveur en heure absolue — « 10:48 »,
   la date d'une rotation d'identifiant, l'heure d'un événement. Ils sont
   restés dans le référentiel du contrôleur. Un opérateur dont le poste a six
   minutes d'avance lit « dernier événement à 10:48 » alors que sa propre
   montre dit 10:54 : l'âge est juste et l'heure ne l'est pas, ce qui est
   peut-être pire que les deux faux ensemble.
2. **Le contrôleur n'est pas plus fiable que le poste.** ADR-0006 a déplacé la
   dépendance sans la supprimer. L'hôte du contrôleur dérive aussi — c'est
   exactement ce que son propre journal signalait sur le proxy.

Le produit ne doit dépendre de l'exactitude d'**aucune** des deux horloges.

## Décision

**L'horloge de celui qui regarde est le référentiel.** Tout horodatage venu du
serveur est **traduit dans ce référentiel** avant d'être utilisé, pour un âge
comme pour un affichage.

Le décalage reste appris de l'en-tête `Date` de chaque réponse. Il ne sert plus
à dire quelle horloge a raison — question à laquelle rien ne permet de
répondre — mais uniquement à **annuler l'écart** entre les deux.

```
t_vu = t_serveur − décalage        (décalage = t_serveur_now − t_client_now)
âge  = Date.now() − t_vu
```

L'âge devient identique à celui d'ADR-0006 — c'est la même soustraction — mais
l'heure affichée tombe désormais sur la montre de l'opérateur. Les deux
lectures redeviennent cohérentes entre elles, **sans qu'aucune des deux
horloges ait besoin d'être juste**.

**La bannière de désaccord n'est plus sur toutes les pages.** C'est un message
système : elle vit dans la **configuration**. Un écart d'horloge ne rend plus
rien faux à l'écran — il n'y a donc plus de raison d'interrompre quelqu'un qui
regarde une façade de ports.

## Conséquences

- Toute lecture de temps est désormais cohérente **avec la montre de
  l'opérateur**, ce qui est le seul repère qu'il peut recouper sans rien ouvrir.
- Les horodatages affichés ne correspondront pas à ceux des **journaux côté
  serveur** quand les horloges divergent. C'est le prix, et c'est le bon sens
  de l'échange : l'écran sert l'opérateur, le journal sert l'enquête — et la
  bannière, en configuration, est l'endroit où l'on apprend que les deux
  diffèrent.
- Il reste **une** fonction d'entrée (`noteServerDate`) et **deux** de sortie
  (`viewerTime`, `ageMs`). `serverNow()` disparaît : il invitait à raisonner
  dans le référentiel du serveur, qui n'est plus le référentiel.
- Un poste dont l'horloge saute en cours de session voit ses heures bouger
  d'un coup. Correct : elles suivent sa référence, et c'est elle qui a bougé.
- `Date.now()` reste juste — et obligatoire — pour tout ce qui est local :
  minuteurs, animations, deadlines des procédures série.

## Alternatives écartées

**Rester sur ADR-0006** et traduire aussi les affichages absolus vers le
référentiel du contrôleur. Cohérent, et ça laisse l'écran afficher des heures
qui ne concordent avec aucune horloge que l'opérateur peut consulter.

**Tout afficher en UTC.** Supprime l'ambiguïté et la lisibilité avec : personne
ne lit une façade de ports en UTC.

**Afficher les deux heures.** Doublerait chaque horodatage de l'interface pour
une situation anormale.

**Ne rien corriger et réparer NTP.** Déjà écarté dans ADR-0006, pour la même
raison : meshcraft est consulté depuis des postes qu'il n'administre pas.

## Addendum du 2026-10-24 — la mesure, après coup

Le point 2 du contexte supposait que l'hôte du contrôleur dérivait. Il a été
mesuré depuis, et c'est pire que supposé :

```
timedatectl    System clock synchronized: no
               NTP service: n/a          (aucun démon installé)
écart mesuré   −6 min 06 s sur le temps réel
RTC            6 min DEVANT l'horloge système
```

Le sens de l'écart RTC/système mérite d'être lu dans le bon ordre : la RTC
était à **~2 s du temps vrai**. Ce n'est donc pas elle qui avait dérivé, c'est
l'horloge système qui s'en était éloignée — 46,77 ppm de retard, soit 4,04 s
par jour sur 90 jours d'uptime, ce qui rend compte des 366 s à la seconde près.
Un redémarrage aurait *corrigé* l'heure, pas l'inverse.

L'hôte **retardait de six minutes et n'avait aucune synchronisation**. Les six
minutes de l'anecdote fondatrice — « silencieux depuis 6 min » — ne venaient
donc ni du poste de l'opérateur ni du proxy : **elles venaient d'ici**. Le
journal du contrôleur qui annonçait le proxy « en avance de 4 m 15 » décrivait
en réalité son propre retard, vu à l'envers. Le poste de l'opérateur avait
raison depuis le début.

Ce que ça ne change pas : **la décision**. Une horloge serveur juste n'aurait
rendu correct aucun des quinze affichages absolus, puisqu'ils seraient restés
dans un référentiel que l'opérateur ne peut pas recouper.

Ce que ça confirme, c'est le rejet ci-dessus : NTP a été déployé sur cet hôte
le jour même, et ce déploiement **ne remplace pas** la traduction de
référentiel. Il supprime une cause de dérive, sur la seule machine qu'on
administre — les postes qui consultent restent hors de portée, et c'est
toujours eux que l'opérateur regarde.

La leçon de méthode : trois horloges qui **concordent entre elles** ne sont pas
trois horloges justes. Le contrôleur, la base et les conteneurs s'accordaient
parfaitement — ils lisent tous la même horloge noyau. Il manquait une référence
*extérieure*, et elle ne coûtait qu'une requête :

```bash
docker run --rm --network host curlimages/curl:latest -sI https://www.cloudflare.com | grep -i '^date:'
```

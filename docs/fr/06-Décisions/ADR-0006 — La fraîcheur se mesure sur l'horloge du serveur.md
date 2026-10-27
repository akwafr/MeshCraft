---
Statut: Remplacé par ADR-0008
Date: 2026-10-24
---

# ADR-0006 — La fraîcheur se mesure sur l'horloge du serveur, pas sur celle du navigateur

## Contexte

Un opérateur a signalé des proxies affichés « silencieux depuis 6 min ». La
mesure, faite au moment du signalement :

- `last_seen` des deux proxies : **6 secondes**.
- Horloges hôte, contrôleur et base : **concordantes**.
- Heartbeat proxy : 30 s, keepalive yamux du tunnel : 20 s. Rien d'anormal.
- Et dans le journal du contrôleur, sur chaque trap reçue de `proxy-lab` :

  > `the proxy timestamp is AHEAD by 4m15.71s compared to the controller — no
  > transport delay can explain it: this proxy's clock and the controller's
  > diverge`

Le proxy se portait bien. Ce qui était faux, c'était **l'horloge de la machine
qui regardait**.

L'interface calcule partout la fraîcheur ainsi :

```ts
const age = Date.now() - new Date(p.last_seen).getTime();
```

`last_seen` est écrit par le **contrôleur**. `Date.now()` est l'horloge du
**navigateur**. Les deux n'ont aucune raison d'être d'accord, et sur le réseau
de ce site elles ne le sont pas. Un poste en avance de six minutes affiche tout
le parc comme périmé de six minutes — proxies, équipements, relevés, journaux.

Le dépôt compte **131 appels à `Date.now()`** dans le frontend. Tous ceux qui
soustraient un horodatage venu du serveur portent ce défaut.

Ce qui rend le bug coûteux n'est pas l'affichage : c'est que le symptôme
accuse **le mauvais composant**. On part vérifier un proxy qui va bien.

## Décision

La fraîcheur d'un horodatage serveur se mesure contre l'**horloge du
serveur**, jamais contre celle du navigateur.

Le décalage est appris de l'en-tête `Date` que **chaque réponse HTTP porte
déjà** — aucun aller-retour supplémentaire, aucun point d'entrée nouveau. Le
client le met à jour à chaque réponse et expose `serverNow()`.

Quand le décalage dépasse un seuil franc, l'interface le **dit** : une bannière
nommant l'horloge locale comme fautive, pas le parc.

## Conséquences

- Le délai de transport entre dans la mesure : la lecture de `Date` est juste à
  un aller-retour près, typiquement quelques dizaines de millisecondes. Sans
  objet pour des seuils qui vivent en dizaines de secondes.
- Un `Date` HTTP a une résolution d'**une seconde**. Suffisant ici, et à ne pas
  réutiliser pour chronométrer quoi que ce soit de fin.
- `Date.now()` reste correct — et obligatoire — pour tout ce qui est **local**
  : un minuteur, une durée d'animation, un anti-rebond. La règle ne vaut que
  pour la différence avec un horodatage **venu du serveur**.
- Deux horloges existent désormais côté client. C'est une complexité réelle, et
  le nom (`serverNow`) est ce qui doit la rendre évidente au point d'appel.
- La bannière peut apparaître chez quelqu'un dont l'horloge est juste et dont
  le **serveur** dérive. Elle nomme donc un désaccord, pas un coupable.

## Alternatives écartées

**Ne rien faire et corriger le NTP du site.** Nécessaire, et insuffisant :
meshcraft est utilisé depuis des postes qu'il ne contrôle pas, et la prochaine
machine mal réglée reproduira exactement ce signalement. Un produit de
supervision ne peut pas faire dépendre son diagnostic de l'horloge de celui qui
le regarde.

**Allonger le keepalive / raccourcir le heartbeat**, ce qui était la demande
initiale. N'aurait rien changé : le heartbeat arrivait toutes les 30 s et
`last_seen` avait 6 secondes. Aurait ajouté du trafic et laissé le bug intact.

**Renvoyer l'âge déjà calculé par le serveur** (`age_seconds` à côté de
`last_seen`). Juste, et ça fige la valeur à l'instant de la réponse : un
tableau laissé ouvert deux minutes afficherait un âge qui ne vieillit pas.
L'offset vieillit correctement.

**Un point d'entrée `/api/time`.** Un aller-retour pour une information que
chaque réponse porte déjà dans son en-tête.

**Faire confiance au `Date` d'une seule réponse, au démarrage.** Un poste qui
se resynchronise en cours de session, ou qui sort d'une veille, garderait un
offset périmé. Chaque réponse le rafraîchit pour un coût nul.

# Index des décisions (ADR)

Un **ADR** — *Architecture Decision Record* — consigne un choix que quelqu'un
voudra comprendre, contester ou refaire plus tard. Il n'explique pas comment le
code marche : le code le dit déjà, et les commentaires de ce dépôt le disent
longuement. Il explique **pourquoi ce choix-là plutôt qu'un autre**, et ce qu'on
a accepté de perdre en le faisant.

## Ce qui mérite un ADR

Trois critères, un seul suffit :

- **Coûteux à défaire.** Une forme de données, un contrat sur le fil, un
  défaut qui s'installe chez des utilisateurs existants.
- **Contre-intuitif.** Quelqu'un de compétent arrivera et proposera l'inverse,
  de bonne foi, parce que la raison n'est pas visible depuis le code.
- **Arbitré entre deux maux.** Il n'y avait pas de bonne réponse, juste une
  moins mauvaise, et le prix payé doit rester écrit.

Ce qui n'en mérite pas : un nom de variable, une correction de bug évidente, un
choix que le prochain referait identiquement sans y penser.

## La règle

**L'ADR s'écrit AVANT le travail, pas après.** C'est ce qui le rend utile : il
force à nommer les alternatives pendant qu'elles sont encore ouvertes, au lieu
de justifier après coup ce qui a été tapé en premier. Un ADR écrit à la fin est
un compte-rendu, pas une décision.

Le commit qui met en œuvre un ADR le cite par son numéro.

**Un ADR accepté ne se modifie pas.** On le remplace : un nouvel ADR, un
nouveau numéro, et l'ancien passe en `Remplacé par ADR-XXXX`. Réécrire une
décision efface la trace du moment où on pensait autrement, qui est exactement
ce qu'on vient relire.

## Format

Un fichier par décision : `ADR-NNNNN — titre court.md`, où `NNNNN` est un
identifiant de **cinq chiffres tirés au sort**, jamais réutilisé
([[ADR-74818 — Un numéro d'ADR se tire au sort, il ne se prend pas au suivant]]).
Les ADR-0001 à ADR-0032, antérieurs à cette règle, gardent leur numéro
séquentiel à quatre chiffres. Le tirage écarte tout identifiant déjà présent
sur n'importe quelle branche :

```bash
used=$(git log --all --name-only --format= | grep -o 'ADR-[0-9]\+' | sort -u)
while :; do n=$(shuf -i 10000-99999 -n 1); echo "$used" | grep -qx "ADR-$n" || break; done
echo "ADR-$n"
```

Quatre sections, dans cet ordre :

| Section | Ce qu'elle contient |
|---|---|
| **Contexte** | Ce qui était vrai avant, et ce qui a forcé la question. Des faits, pas des intentions. |
| **Décision** | Ce qu'on fait, à l'indicatif présent. Une phrase doit suffire à la dire. |
| **Conséquences** | Ce que ça coûte, y compris ce qu'on s'interdit désormais. Un ADR sans conséquence négative est un ADR qui ment. |
| **Alternatives écartées** | Les options réelles, et pourquoi chacune a perdu. C'est la section qu'on vient lire. |

En-tête : `Statut` (Proposé · Accepté · Remplacé par ADR-XXXX · Précisé par
ADR-XXXX · Abandonné) et `Date`.

`Précisé par` existe pour la supersession **partielle** : un ADR dont un point
sur sept a été rouvert, les six autres restant en vigueur. `Remplacé par`
enverrait le lecteur ailleurs alors que le document fait toujours autorité ;
réécrire le point effacerait la trace du moment où on pensait autrement. Le
nouvel ADR dit lequel il rouvre, et ne répète pas le reste.

## Les décisions

Rangées par date d'en-tête : jusqu'à ADR-0032 le numéro suit la date, après
il n'en dit plus rien.

| N° | Titre | Statut |
|---|---|---|
| [[ADR-0001 — Deux thèmes, et la palette de statut qui doit y survivre]] | Deux thèmes | Accepté |
| [[ADR-0002 — Le verre est du chrome, jamais du contenu]] | Matière | Accepté |
| [[ADR-0003 — La rampe de trafic est logarithmique et relative à la capacité]] | Rampe de trafic | Accepté |
| [[ADR-0004 — Provisioning Junos sans commit confirmed]] | Junos | Accepté |
| [[ADR-0005 — Sélecteur marque puis modèle pour les procédures série]] | Sélecteur série | Accepté |
| [[ADR-0006 — La fraîcheur se mesure sur l'horloge du serveur]] | Horloge | Remplacé par ADR-0008 |
| [[ADR-0007 — L'agrégat est un port sur la façade]] | Agrégats | Accepté |
| [[ADR-0008 — Le référentiel de temps est l'horloge de celui qui regarde]] | Horloge | Accepté |
| [[ADR-0009 — Le balayage ne vole jamais un geste de défilement]] | Navigation au balayage | Accepté |
| [[ADR-0010 — L'écran de réinitialisation liste tout, et la marque est un champ]] | Liste de réinitialisation | Accepté |
| [[ADR-0011 — Un jeton authentifie, il n'autorise pas]] | Jetons d'API | Accepté |
| [[ADR-0012 — La surface d'outils n'est pas la surface d'API]] | Serveur MCP | Accepté |
| [[ADR-0013 — Commutation et routage sont deux modes de navigation, pas deux produits]] | Modes de navigation | Remplacé par ADR-0019 |
| [[ADR-0014 — Le libellé déclaré est une pièce à conviction, l'identité est dérivée]] | Identité de modèle | Accepté |
| [[ADR-0015 — L'extension de site vit dans le contrôleur, et son inventaire est un miroir en lecture seule]] | Extensions de site | Précisé par ADR-0025 |
| [[ADR-0016 — Une borne Wi-Fi est un équipement, mais elle ne se montre qu'au Wi-Fi et sur la topologie]] | Modèle Wi-Fi | Proposé |
| [[ADR-0017 — Le refus d'une lecture courte appartient à l'absorption]] | Lecture courte | Accepté |
| [[ADR-0018 — Le nom d'un port à l'écriture est une observation, pas une constante]] | Nommage des ports | Accepté |
| [[ADR-0019 — Le sélecteur ne porte que la marque, le mode se lit dans la barre]] | Sélecteur de mode | Précisé par ADR-0031 |
| [[ADR-0020 — Un limiteur de tempête qu'on ne sait pas lire se signale, il ne se tait pas]] | Storm control | Accepté |
| [[ADR-0021 — Le nom d'une région MST est une identité, pas un libellé]] | Région MSTP | Accepté |
| [[ADR-0022 — Rejoindre une région MST est une capacité déclarée, pas une conséquence de « sait écrire STP »]] | Région MSTP | Accepté |
| [[ADR-0023 — La configuration spanning-tree ne part que sur un geste, et la dérive se lit dans un bandeau]] | Écriture STP | Accepté |
| [[ADR-0024 — Une alerte se mérite : le journal décide, la description et l'interco qualifient]] | Alerting | Accepté |
| [[ADR-0025 — meshcraft télécommande le contrôleur UniFi, il ne détient aucune intention]] | Gestes UniFi | Précisé par ADR-0029 |
| [[ADR-0026 — Un lien de topologie a deux moitiés, chacune colorée par l'egress de son extrémité]] | Rendu des liens | Accepté |
| [[ADR-0027 — Le titre d'une tuile est de la donnée, sa cible est une référence non garantie]] | Tuiles de tableau de bord | Accepté |
| [[ADR-0028 — La décomposition du trafic par classe est une capacité déclarée, et une classe non déclarée se dit à l'écran]] | Supervision par port | Accepté |
| [[ADR-0029 — Le SSID s'édite, la borne ne s'édite pas, et seul ce qui se relit se propose]] | Édition Wi-Fi | Accepté |
| [[ADR-0030 — La rampe de trafic a un seul moteur de couleur, et le dégradé SVG l'échantillonne]] | Rampe de trafic | Accepté |
| [[ADR-0031 — Le sélecteur de mode est une bascule, et la marque n'est plus une porte]] | Sélecteur de mode | Accepté |
| [[ADR-0032 — Une tuile peut déclarer une zone vivante en mode édition]] | Grille de tableau de bord | Accepté |
| [[ADR-80600 — Ctrl+F cherche dans le site, la seconde pression rend la main au navigateur]] | Recherche globale | Accepté |
| [[ADR-74818 — Un numéro d'ADR se tire au sort, il ne se prend pas au suivant]] | Numérotation des ADR | Accepté |
| [[ADR-46584 — L'identité est une icône, et la déconnexion vit derrière elle]] | Menu de compte | Accepté |
| [[ADR-73885 — Le fond de la page prend la teinte du mode]] | Fond par mode | Accepté |

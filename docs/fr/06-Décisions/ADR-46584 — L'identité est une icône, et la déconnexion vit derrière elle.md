---
Statut: Accepté
Date: 2026-10-26
---

# ADR-46584 — L'identité est une icône, et la déconnexion vit derrière elle

## Contexte

La barre haute portait, à droite, un **groupe d'identité** de cinq
contrôles : le nom de l'utilisateur (lien vers le profil depuis
[[ADR-0011 — Un jeton authentifie, il n'autorise pas]]), le badge
« admin global », le drapeau de langue, les trois segments d'apparence, et
une icône de déconnexion.

Mesuré sur la barre de commutation, en français : ce groupe pèse **398 px**
sur une barre qui en veut 1180 et en a 960. C'était l'objet le plus large de
la barre, devant les cinq onglets. Il a fallu deux replis pour le faire
tenir : le nom disparaît entre 960 et ~1140 px (seul le glyphe reste), et
sous 960 px tout le groupe passe derrière un bouton `…`. Résultat, trois
présentations de la même chose selon la largeur, et une barre qui débordait
quand même à 25 des largeurs testées entre 721 et 1440 px
([[ADR-80600 — Ctrl+F cherche dans le site, la seconde pression rend la main au navigateur]]
consigne la discontinuité à 960 px que ce repli provoque).

Ces cinq contrôles répondent à des questions qu'on se pose une fois par
session. Les onglets et le sélecteur de site, à côté, servent à chaque clic.

La demande : **remplacer l'icône de déconnexion par une icône d'utilisateur**,
qui ouvre un panneau donnant le nom, la permission, le lien vers le profil
et la déconnexion, et **retirer le nom de la barre**.

## Décision

**Une seule icône de personne, à toutes les largeurs. Derrière elle :
l'identité, la permission en vigueur, le profil, l'apparence, la langue et
la déconnexion.**

1. **La barre ne porte plus ni le nom ni la déconnexion.** Une icône de
   personne prend la place de l'icône de déconnexion, au même endroit, à
   toutes les largeurs. Le repli `…` sous 960 px et le repli du nom entre 960
   et 1140 px disparaissent, puisqu'il n'y a plus rien à replier.

2. **La première ligne du panneau est l'identité, et c'est le lien vers le
   profil.** Le nom, la permission, et la destination dite en clair (« Mon
   profil »). ADR-0011 faisait du nom la porte du profil ; il le reste, il a
   simplement quitté la barre.

3. **La permission affichée est celle qui s'applique à l'écran ouvert.** Un
   admin global voit son badge. Les autres voient leur rôle **sur le site
   courant** (lecteur, opérateur, administrateur du site), et rien quand
   aucun site n'est sélectionné. Montrer la liste de tous les rôles
   répondrait à « que puis-je faire en général », alors que la question
   qu'on se pose devant un geste est « que puis-je faire ici ». La liste
   complète reste sur la page de profil.

4. **Le panneau s'ouvre au survol, au clic et au clavier, et c'est le clic
   qui le fige.** Le survol seul rendrait la déconnexion impossible au doigt
   et aléatoire au clavier. Un panneau ouvert au survol se referme quand le
   pointeur s'en va ; ouvert ou cliqué, il reste jusqu'à `Échap` ou un clic
   au large (`usePicker`, UI-GUIDE §8.1).

5. **C'est une divulgation, pas un `role="menu"`.** Le panneau contient un
   lien, un groupe radio, un sélecteur et un bouton. En mode menu, un lecteur
   d'écran ne lit que les `menuitem` : les segments d'apparence et la liste
   de langues deviendraient muets. `aria-expanded` + `aria-controls`
   décrivent ce que c'est réellement.

## Conséquences

- **Se déconnecter coûte un geste de plus** : ouvrir le panneau, puis
  cliquer. C'est le prix payé, et il est assumé : on se déconnecte une fois
  par session, on navigue cent fois.
- **Le nom de l'utilisateur n'est plus visible en permanence.** Sur un poste
  partagé, savoir sous quel compte on agit demande d'ouvrir le panneau.
  L'icône porte le nom dans son `aria-label` (« Compte — Ada Byron »), mais
  un lecteur voyant ne le lit pas d'un coup d'œil.
- **La barre gagne environ 380 px**, et il n'y a plus qu'une seule
  présentation de l'identité à maintenir. La règle « le menu de compte ne lit
  aucun point de rupture » entre dans UI-GUIDE §9.4.
- **Un rôle affiché est un rôle lu dans `/api/auth/me`**, au chargement de la
  session. Un rôle changé par un admin pendant la session n'y apparaît qu'au
  rechargement, comme partout ailleurs dans l'interface.
- La langue et l'apparence, réglages de la personne, la suivent dans le
  panneau. Les changer coûte aussi un geste de plus ; on les change une fois
  par mois.

## Alternatives écartées

**Garder le nom dans la barre et ne déplacer que la déconnexion.** C'est la
moitié de la demande, et elle laisse en place le plus gros des 398 px, donc
les deux replis et les trois présentations. Le nom était justement l'élément
que la barre ne savait pas garder entre 960 et 1140 px.

**Un vrai `role="menu"` avec des `menuitem`.** Le motif « menu de compte »
habituel, et il rendrait muets, pour un lecteur d'écran, deux des quatre
contrôles du panneau. Écarté au point 5.

**Ouverture au survol seul**, comme sur beaucoup de sites. Écartée au
point 4 : la déconnexion vit désormais dans ce panneau, et un doigt ne
survole jamais.

**Afficher tous les rôles de l'utilisateur dans le panneau.** Juste, mais
une liste de douze sites répond à la mauvaise question à cet endroit. Elle
est sur la page de profil, à un clic, depuis la première ligne du panneau.

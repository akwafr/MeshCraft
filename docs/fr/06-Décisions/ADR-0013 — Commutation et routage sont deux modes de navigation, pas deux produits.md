---
Statut: Remplacé par ADR-0019
Date: 2026-10-24
---

# ADR-0013 — Commutation et routage sont deux modes de navigation, pas deux produits

## Contexte

Le produit s'ouvre au L3 : routage, pare-feu, DNS, DHCP. La navigation actuelle
tient sur une ligne — Équipements, Ports, Topologie, Clients — et quatre
entrées de plus la ferait déborder, sur un écran de portable comme sur un
téléphone, où la barre d'onglets basse est déjà pleine.

La demande est un **sélecteur porté par le logo** : à gauche « Commutation »,
à droite « Routage », le logo devenant rouge en routage.

Le piège est de prendre ça pour une demande cosmétique. Un mode qui masque la
moitié de la navigation est une **décision d'architecture** : chaque écran
futur devra déclarer son camp, et quelqu'un arrivera de bonne foi en
demandant pourquoi il ne voit pas le pare-feu depuis la page des ports.

## Décision

**Le mode filtre la NAVIGATION. Il ne filtre rien d'autre.**

1. **Même données, même API, mêmes droits.** Le mode n'est ni une permission,
   ni une partition de données, ni un réglage de site. C'est une préférence de
   celui qui regarde, au même titre que le thème. Un `viewer` en mode routage
   n'acquiert rien ; un administrateur en mode commutation ne perd rien.
   L'autorisation reste entièrement `CanSite` / `RequireGlobalAdmin`, comme le
   pose [[ADR-0011 — Un jeton authentifie, il n'autorise pas]].

2. **L'URL fait autorité, le sélecteur n'est qu'un raccourci.** Un lien
   profond vers `/routing/firewall` ouvre le pare-feu **et bascule le mode**.
   Il ne renvoie pas un 404, et il n'affiche pas une page dont l'onglet est
   invisible. C'est la règle qui empêche le mode de devenir un piège : un lien
   collé dans un ticket doit marcher pour quelqu'un dont le mode est l'autre.

3. **Le mode est persistant par visiteur**, comme le thème : on le retrouve au
   retour. Mais la règle 2 prime toujours sur la préférence enregistrée.

4. **Le rouge du mode routage est un jeton DÉDIÉ, pas `--danger`.**
   C'est le point qu'on allait rater. [[ADR-0001 — Deux thèmes, et la palette
   de statut qui doit y survivre]] fait du rouge la couleur de l'échec, sur
   toute la surface du produit. Réutiliser `--danger` pour dire « vous êtes en
   routage » ferait dire deux choses incompatibles à la même couleur, et le
   jour où un pare-feu tombe en panne, l'écran est rouge pour deux raisons
   sans rapport. Le mode reçoit donc son propre jeton, décliné dans les deux
   thèmes, choisi pour être **distinct** du rouge de statut.

5. **La couleur ne porte jamais le mode à elle seule.** Le sélecteur est un
   vrai contrôle : il a un libellé textuel (« Commutation » / « Routage »), il
   est atteignable au clavier, il annonce son état. Un logo qui change de
   teinte n'est pas une information pour qui ne distingue pas cette teinte.

## Conséquences

- **Chaque écran futur déclare son mode.** C'est une donnée de la table de
  routage de l'application, pas une convention tacite — un écran sans mode est
  un écran que personne ne trouve.
- Un écran peut légitimement appartenir **aux deux** (les équipements portent
  des interfaces L3 autant que des ports). Le modèle doit l'autoriser dès le
  départ ; forcer un camp unique nous obligerait à dupliquer des pages.
- **Le basculement n'est pas une navigation.** Passer en routage depuis la page
  des clients doit mener quelque part de sensé, pas afficher une page vide : le
  mode a une destination par défaut.
- Sur mobile, la barre d'onglets basse change de contenu d'un coup. C'est le
  bénéfice recherché — quatre entrées au lieu de huit — et c'est aussi ce qui
  surprend le plus : d'où le libellé visible, pas seulement la couleur.
- Le sélecteur occupe le logo, qui est **aussi** le retour au tableau de bord.
  Deux gestes sur un même objet : il faut que glisser et cliquer restent
  distinguables, au doigt comme à la souris.

## Alternatives écartées

**Une seule barre à huit entrées.** L'option honnête, et sans doute la bonne
si le produit devait s'arrêter là. Écartée parce que la liste ne s'arrête pas à
huit : le L3 amène routage, pare-feu, DNS, DHCP, et la suite amènera autre
chose. Une barre qui déborde ne se répare pas, elle se tronque.

**Deux applications, ou un sous-domaine par mode.** Résout la navigation en
créant deux produits, deux sessions, deux façons de se tromper de fenêtre. Et
ça contredit la réalité : c'est le même équipement, souvent le même port.

**Un réglage de site, ou un réglage d'instance.** Ferait du mode une propriété
des données au lieu d'une préférence de lecture. Deux personnes regardant le
même site doivent pouvoir le regarder différemment.

**Masquer selon les droits** plutôt que selon un mode choisi. Séduisant — on
ne montre que ce qui est permis — et c'est exactement la confusion
qu'ADR-0011 existe pour éviter : l'autorisation est un modèle, la navigation
en est un autre, et les faire coïncider les fait diverger. On masquerait aussi
à un lecteur légitime la preuve que la fonction existe.

**Réutiliser `--danger` pour le mode routage**, puisque c'est « le rouge du
produit ». Écarté au point 4 : une couleur, une signification.

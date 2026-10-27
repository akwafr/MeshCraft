---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0007 — L'agrégat est un port sur la façade, et le fallback LACP est une capability

## Contexte

L'agrégation de liens occupe aujourd'hui une **tuile séparée** sous la façade
(`LagPanel.tsx`) : un encart « Agrégation de liens » avec un bouton « Nouvel
agrégat » et, le reste du temps, une phrase disant qu'il n'y en a pas. Sur un
équipement sans agrégat — la majorité — c'est un bloc permanent qui n'apprend
rien.

Or le contrat dit déjà le contraire de ce que l'écran montre. `wire/mdpv1/lag.go` :

> « A NAMED AGGREGATE of faceplate ports that behaves like one port : it has
> members, a mode, and a VLAN mapping of exactly a port's shape. […] in the
> bridge tables the bond appears AS an interface — indistinguishable from a
> physical port. Every platform surveyed treats the aggregate the same way
> (EOS's Port-ChannelN, IOS's PoN are interfaces), which is why `LAGState.VLAN`
> reuses `PortState` verbatim. »

Le modèle de données traite l'agrégat comme un port. L'interface le traite
comme un encart. C'est l'écran qui a tort.

Deuxième manque, plus concret : le **fallback LACP** — laisser des ports
commuter même quand LACP n'est pas monté, dans le même VLAN ou dans un autre.
C'est ce qui évite qu'un serveur dont le bond n'est pas encore configuré se
retrouve sans réseau du tout. `LAGState` ne le porte pas, et toutes les
plateformes ne savent pas le faire.

## Décision

**1. L'agrégat devient un type d'interface sur la façade.** Les agrégats
s'affichent comme des ports, après les interfaces physiques, dans leur propre
bloc (`FacadeBlock`, comme « SFP+ »). Sélectionner un agrégat **met en valeur
ses ports membres** sur la façade.

**2. Un port « ajouter » en pointillé.** Après les agrégats existants, une
case en pointillé avec une icône `+`. L'activer **assombrit toute la page** et
n'y laisse que la liste des ports physiques, pour en sélectionner les membres,
avec les options de l'agrégat sur le côté.

**3. Le fallback est une capability, pas une case à cocher.** `LAGState` gagne
un champ `Fallback`, et le contrat gagne un geste `lag-fallback`. Une
plateforme qui ne le déclare pas **ne montre pas l'option** — elle ne la
montre pas grisée non plus : un contrôle désactivé sans explication est pire
qu'absent (UI-GUIDE §2, « ne peignez pas une limite de plateforme en orange »).

Changement de contrat **additif**, donc v1, dans l'ordre imposé par
`CLAUDE.md` : types Go d'abord (`wire/mdpv1/lag.go`), `schemas.json` régénéré
par `go run ./tools/mdpschema`, `openapi.yaml` à la main, puis la copie
vendorisée du dépôt drivers et un examen de conformité.

## Conséquences

- **La façade cesse d'être une image du matériel**, et c'est le prix réel de
  cette décision. Un agrégat n'est pas une prise ; il n'existe nulle part sur
  le capot. Accepté parce que le bloc est **étiqueté séparément** et vient
  après les prises physiques, comme « SFP+ » l'est déjà — on ajoute un registre,
  on ne mélange pas les deux.
- **La sélection par plage ne doit pas traverser les blocs.** Elle se mesure
  sur l'ordre physique (cf. ADR-0005 et le commentaire de `FacadeBlockRows`) ;
  un ⇧-clic d'un port physique vers un agrégat n'a aucun sens et doit être
  refusé, pas silencieusement interprété.
- **L'assombrissement est une modale**, donc il relève de `Modal` et de la pile
  de congédiement (`useDismissable`) : Échap, clic extérieur, piège de focus.
  Une surcouche écrite à la main rouvrirait les sept défauts que §8.1 a fermés.
- **Trois états au lieu de deux** pour le fallback : supporté et actif,
  supporté et inactif, **non supporté**. Le troisième n'est pas « inactif » —
  le confondre ferait croire qu'un geste est possible là où il ne l'est pas.
- `LagPanel.tsx` disparaît. Son contenu de diagnostic (quels membres, quel
  mode, monté ou non) doit réapparaître dans le panneau latéral quand un
  agrégat est sélectionné, sinon la refonte perd de l'information.
- Un connecteur qui déclare `lags` mais pas `lag-fallback` reste valide. Le
  champ `Fallback` dans un intent envoyé à un tel connecteur doit être
  **refusé explicitement**, pas ignoré : un refus silencieux écrirait un
  agrégat différent de celui que l'écran montre.

## Alternatives écartées

**Garder la tuile et y ajouter le fallback.** Le moins cher, et ça laisse
l'écran en désaccord avec son propre modèle de données. La tuile restera un
bloc vide sur la majorité des équipements.

**Afficher les agrégats mêlés aux ports physiques**, à leur place dans la
numérotation. Détruirait la lecture de la façade comme image du matériel, qui
est la chose que ce produit protège le plus (cf. le commentaire de
`FacadeBlockRows` sur l'enroulement).

**Un panneau latéral au lieu d'un assombrissement.** Le panneau latéral fait
300 px et sert déjà à autre chose ; choisir des membres demande de voir **toute**
la façade, ce qui est précisément ce que l'assombrissement libère.

**Déduire le support du fallback d'une liste de plateformes codée en dur dans
le frontend.** C'est ce que `SOFT_RESET_DRIVERS` faisait avant ADR-0005, et la
raison pour laquelle il a été supprimé : le navigateur n'a pas à tenir un
catalogue de ce que les connecteurs savent faire. Le contrat a un mécanisme
pour ça, `Supports()`.

**Un booléen `fallback` sans VLAN.** La demande précise « dans les mêmes ou
d'autres VLAN ». Un booléen obligerait à y revenir, et un champ de contrat est
très coûteux à élargir après coup.

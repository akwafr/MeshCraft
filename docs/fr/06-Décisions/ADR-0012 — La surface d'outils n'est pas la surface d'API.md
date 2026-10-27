---
Statut: Accepté
Date: 2026-10-24
Dépend de: ADR-0011
---

# ADR-0012 — La surface d'outils n'est pas la surface d'API

> **Entorse à la convention, écrite ici plutôt que tue.** Cet ADR arrive
> *après* le code (`cmd/mcp`), ce que `CLAUDE.md` proscrit explicitement. Il
> n'est donc pas une justification : les choix ci-dessous ont été réexaminés
> contre leurs alternatives avant fusion, et le point 4 a été **vérifié dans le
> code et dans un test** plutôt que cru sur parole, parce que c'est lui qui
> rend les trois autres réels ou cosmétiques. Ce que cette entorse a coûté est
> dit dans « Conséquences ».

## Contexte

[[ADR-0011 — Un jeton authentifie, il n'autorise pas]] règle *qui* est un
client MCP : personne de particulier. Il porte le jeton d'un utilisateur et ne
peut rien que cet utilisateur ne puisse. L'autorisation est donc réglée.

Reste la question qu'ADR-0011 ne traite pas, et qui est entièrement différente :
**quels gestes un agent doit-il pouvoir seulement *formuler***.

Le contrôleur expose 198 handlers. Deux faits les rendent non-transposables
tels quels :

1. **Chaque schéma d'outil est dans le contexte de l'agent à *chaque*
   requête.** Une surface d'outils n'est pas un catalogue où l'on pioche, c'est
   un coût permanent. 198 schémas noieraient la question posée.
2. **Un agent qui se trompe ne se trompe pas comme un humain qui se trompe.**
   Un opérateur qui clique « migrer l'adresse de management » a traversé un
   écran, un avertissement et une confirmation, et il sait dans quel bâtiment
   est l'équipement. Un agent produit le même appel depuis une inférence sur un
   nom de route.

## Décision

**La surface d'outils est dessinée à partir des questions du produit, pas des
routes du contrôleur.** 198 handlers deviennent 17, 29 ou 35 outils selon le
mode.

1. **Un outil se mérite.** Il existe s'il *compose* plusieurs routes en une
   question réelle, s'il *garde* un geste, ou s'il *enseigne* du vocabulaire.
   Sinon il n'existe pas et le reste passe par l'échappatoire (point 4).
   Exemple non négociable : `get_device` projette depuis la **liste**, parce
   que le verdict de conformité se calcule sur la liste — une lecture par
   équipement répondrait `absent` là où la vérité est `unknown`.

2. **Les descriptions portent les pièges, pas la syntaxe.** Qu'un collecteur
   ne couvrant pas son site **arrête la collecte en laissant le voyant au
   vert** ; que le drapeau DHCP-snooping par port soit de la *confiance*, donc
   qu'une valeur fausse **retire** la protection ; qu'un débit de storm-control
   sans son unité soit à cinq ordres de grandeur. C'est ce qu'un auteur non-Go
   ne peut pas lire dans un compilateur.

3. **Les gestes dangereux reçoivent trois réponses différentes, choisies sur
   le MODE DE DÉFAILLANCE et non sur le niveau de danger.**
   - **Absents** quand l'échec signifie que plus personne n'atteint
     l'équipement, ou qu'on touche à ce qui borne le client lui-même :
     migration de management et saut d'adresse, liens de partage de tableau de
     bord, administration des utilisateurs et des rôles, réinitialisation
     usine, révélation d'identifiant.
   - **Une porte unique, gardée par le nom** pour toute suppression :
     `delete_object` exige le nom exact de l'objet, **relu à l'appel**. Quinze
     outils de suppression seraient quinze noms à mal lire.
   - **Inoffensifs par défaut**, en copiant l'idiome du produit : un
     déploiement d'identifiant **répète** tant qu'on ne demande pas d'écrire,
     un profil **prévisualise**.

   Le principe qui range les trois : **une confirmation qu'un agent tape prouve
   qu'il a lu l'objet, elle ne vaut pas consentement.** C'est le bon garde-fou
   quand le dégât est une ligne perdue, qu'une ressaisie annule. C'est le
   mauvais quand le dégât est qu'il faut quelqu'un avec un câble console dans
   un bâtiment.

4. **L'échappatoire est tenue par la même liste.** `api_call` couvre la longue
   traîne, mais refuse `DELETE` à tous les modes et passe par les mêmes
   exclusions que les outils dédiés. **Sans cette clause, le point 3 serait
   décoratif** : exclure la migration de la liste d'outils ne vaut rien si une
   route arbitraire la rejoint. Un test vérifie qu'un appel refusé **n'atteint
   jamais le contrôleur**, pas seulement qu'il renvoie une erreur.

5. **Le défaut est la lecture seule, et `tools/list` est FILTRÉE.** Un outil
   d'écriture n'est pas refusé, il est **invisible** — un agent ne planifie pas
   autour d'un outil qu'il ne verra jamais réussir. Même raison qu'ADR-0011 :
   le défaut doit être le moins puissant, pas le plus pratique.

## Conséquences

- **Une surface à maintenir qui ne se génère pas.** Une route nouvelle
  n'apparaît pas toute seule : elle tombe dans l'échappatoire jusqu'à ce que
  quelqu'un juge qu'elle mérite un outil. C'est le prix assumé du point 1.
- **La liste d'exclusions est désormais un objet de sécurité.** Elle doit
  bouger dans le même commit que la route qu'elle nomme, sinon un geste
  dangereux devient atteignable par `api_call` sans que personne l'ait décidé.
- **Ce que l'entorse a coûté, concrètement** : l'alternative « génération 1:1
  depuis l'OpenAPI » n'a jamais été évaluée à armes égales, puisqu'au moment
  de l'écrire le travail manuel existait déjà. Elle est écartée ci-dessous sur
  argument, pas sur mesure. Si quelqu'un revient avec un prototype généré dont
  le coût en contexte est acceptable, **cet ADR doit être remplacé, pas
  défendu**.
- Le serveur n'a **aucun privilège propre** et ne devient pas un composant de
  confiance : tout ce qui précède borne ce qu'un agent peut *formuler*, jamais
  ce qu'il a le *droit* de faire, qui reste entièrement ADR-0011.

## Alternatives écartées

**Générer un outil par route depuis l'OpenAPI.** L'option évidente, et celle
qu'on proposera de bonne foi. Écartée sur deux motifs : le coût en contexte est
permanent et payé à chaque requête ; et surtout une surface 1:1 enseigne à
l'agent **la structure des routes du contrôleur** au lieu du vocabulaire du
produit — il apprendrait qu'il existe une liste d'équipements, pas qu'un
verdict de conformité n'a de sens que sur cette liste.

**Confirmer au lieu d'exclure.** Séduisant parce qu'uniforme : tout est
possible, tout demande confirmation. Écarté parce que ça traite une ligne
supprimée et un équipement injoignable comme le même risque. Une confirmation
n'est pas un consentement, et pour la migration de management la réponse du
commit est *de toute façon perdue* — même « est-ce que ça a marché » n'est pas
connaissable depuis ici.

**Pas d'échappatoire du tout.** Le plus sûr sur le papier. Écarté parce que la
longue traîne rendrait le serveur inutilisable pour le travail réel, et que
l'utilisateur bloqué ne renonce pas : il passe à `curl` avec son jeton, où il
n'y a **aucun** garde-fou. Une échappatoire bornée est plus sûre que pas
d'échappatoire contournée.

**Un compte de service privilégié pour le MCP.** Contredit frontalement
ADR-0011. Le journal dirait « le MCP a supprimé ce VLAN ».

**Un SDK MCP en Node/TypeScript.** Ajouterait une seconde chaîne d'outils à un
dépôt qui ne tolère que Docker, et un arbre de modules dans le `go.sum` de
**chaque** build du contrôleur et du proxy. Le protocole utile tient en
`initialize` / `tools/list` / `tools/call` / `ping`.

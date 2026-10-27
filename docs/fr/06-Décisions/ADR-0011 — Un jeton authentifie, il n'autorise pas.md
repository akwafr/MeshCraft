---
Statut: Accepté
Date: 2026-10-24
---

# ADR-0011 — Un jeton d'API authentifie, il n'autorise pas

## Contexte

Il faut des jetons d'API par utilisateur, donnant accès à ce que le frontend
sait faire, et un serveur MCP pour qu'un agent pilote le produit.

Ce qui existe déjà et qui décide presque tout :

- **L'authentification** est un cookie de session (`meshcraft_session`), dont
  le jeton est **haché en base** (`auth.HashToken`). Le middleware charge
  l'utilisateur dans le contexte, ou renvoie 401.
- **L'autorisation** est entièrement ailleurs : `RequireGlobalAdmin` pour les
  sous-arbres d'instance, et surtout `CanSite(ctx, siteID, minRole)` avec
  `model.RoleLevel` pour tout le reste. Chaque handler l'appelle lui-même.

Le risque principal de cette demande n'est donc pas cryptographique. C'est
qu'un jeton **réimplémente un second modèle de permissions** à côté du premier.
Deux modèles de permissions finissent toujours par diverger, et le jour où ils
divergent, c'est la porte la plus permissive qui gagne.

## Décision

**Un jeton d'API est un second moyen de prouver *qui* on est. Il ne change
jamais *ce qu'on a le droit de faire*.**

1. **Même identité, même autorisation.** Le middleware accepte
   `Authorization: Bearer <jeton>` et place dans le contexte **exactement le
   même `model.User`** qu'un cookie. Tous les `CanSite` et `RequireGlobalAdmin`
   existants s'appliquent sans être touchés. Aucun handler n'apprend qu'il
   existe des jetons.
2. **Un jeton ne peut que RÉDUIRE.** Il porte une portée facultative — lecture
   seule, liste de sites, date d'expiration — qui s'applique **en plus** des
   droits de l'utilisateur, jamais à leur place. Un jeton d'un `viewer` ne
   devient pas administrateur parce qu'il demande le droit.
3. **Haché au repos, montré une fois.** Même traitement que les sessions. La
   valeur en clair n'existe qu'à la création, dans la réponse. Elle n'est ni
   stockée, ni journalisée, ni récupérable.
4. **Jamais dans un cookie.** Le jeton ne voyage que dans l'en-tête
   `Authorization`. C'est ce qui le rend **immunisé au CSRF** : un navigateur
   n'envoie pas un en-tête tout seul.
5. **Le frontend ne change pas de porte.** Il reste sur le cookie. (Voir les
   alternatives : l'idée « le frontend devrait tourner sur les jetons » est
   séduisante et elle est écartée.)

## Conséquences

- Un jeton vaut exactement les droits de son porteur **au moment de l'appel** :
  retirer un rôle à quelqu'un ferme ses jetons dans la seconde, sans avoir à
  les révoquer un par un. C'est le bénéfice direct de n'avoir qu'un modèle.
- **Une surface d'authentification de plus à auditer.** Le middleware doit être
  le seul endroit qui connaisse les deux formes, et un test doit vérifier qu'un
  jeton expiré, révoqué, ou en lecture seule sur une écriture, échoue — pas
  seulement qu'un jeton valide passe.
- Un jeton perdu est aussi grave que le mot de passe de son porteur, **moins**
  la portée qu'il s'est donnée. D'où l'expiration et la lecture seule par
  défaut : le défaut doit être le moins puissant, pas le plus pratique.
- La **journalisation** devient obligatoire : chaque usage met à jour un
  `last_used_at`, et la liste du profil le montre. Un jeton dont personne ne
  sait s'il sert est un jeton que personne n'ose révoquer.
- Le MCP n'a **aucun privilège propre**. Il porte le jeton de l'utilisateur qui
  l'a configuré et ne peut rien faire que cet utilisateur ne puisse faire. Il
  n'est pas un composant de confiance, c'est un client.

## Alternatives écartées

**Faire tourner le frontend sur les jetons**, comme le suggère la demande.
Tentant — une seule porte, donc une seule chose à sécuriser. Écarté parce que
stocker un jeton de longue vie dans un navigateur est **strictement pire** que
le cookie actuel : un cookie `HttpOnly` est hors de portée du JavaScript, un
jeton en `localStorage` est lisible par n'importe quelle XSS, et il ne s'expire
pas tout seul. Le bon sens de l'échange est l'inverse de celui qui est proposé.

Ce que la demande veut vraiment reste tenu, et c'est le point 1 : **la même
surface d'API sert les deux**. Il n'y a pas d'API « pour les agents » à côté
d'une API « pour le frontend ». Si une page sait faire quelque chose, un jeton
le peut aussi, parce que c'est le même handler.

**Des permissions propres au jeton** (choisir des gestes un par un). C'est le
second modèle de permissions, et le défaut que tout cet ADR existe pour éviter.

**Un JWT.** Autoporteur, donc non révocable sans liste de révocation — soit on
accepte qu'un jeton volé reste valide jusqu'à expiration, soit on consulte la
base à chaque appel et on a payé la complexité pour rien. Une valeur aléatoire
hachée en base fait le même travail, se révoque, et se compare en temps
constant.

**Un jeton par instance plutôt que par utilisateur.** Un secret partagé
n'identifie personne : le journal dirait « quelqu'un a supprimé ce VLAN ».

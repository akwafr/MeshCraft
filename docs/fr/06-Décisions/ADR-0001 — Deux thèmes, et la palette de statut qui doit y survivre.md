---
Statut: Accepté
Date: 2026-10-24
Note: Consigné après coup — la décision a été prise pendant la refonte UI du
  2026-10-24, avant que la convention ADR n'existe. Les suivants sont écrits
  avant le travail.
---

# ADR-0001 — Deux thèmes, et la palette de statut qui doit y survivre

## Contexte

meshcraft était **sombre uniquement** depuis son origine : treize propriétés
déclarées une fois sur `:root`, aucune redéfinition claire nulle part, et le
guide UI disait en toutes lettres « ne conditionnez rien au schéma de
couleur ».

La palette porte une décision **produit**, pas esthétique, documentée au §2 du
guide : `--warn` orange dit « on voit l'équipement et quelque chose ne va
pas » ; `--indeterminate` jaune dit « meshcraft a perdu le point de vue, et
l'équipement va peut-être très bien ». Les confondre envoie quelqu'un dans la
mauvaise baie.

Or `--indeterminate` vaut `#e8d44d`. Sur fond blanc, il est **invisible**.

## Décision

Deux thèmes, le sombre restant le **défaut**. Chaque couleur est déclarée deux
fois : une valeur de repli, puis la vraie en `light-dark()`.

```css
--bg: #080a11;                        /* repli */
--bg: light-dark(#eef1f6, #080a11);   /* la vraie */
```

Les valeurs claires des encres de statut ne sont **pas** un assombrissement
mécanique : `--warn` part vers le rouge (28°), `--indeterminate` vers l'olive
(52°). L'écart passe à 24° là où le sombre n'en tient que 13.

## Conséquences

- Un navigateur qui ignore `light-dark()` jette la seconde déclaration et
  garde la première : il atterrit sur le **thème sombre**, celui que le
  produit a toujours servi. Le mode dégradé d'une vieille tablette en salle
  technique est « ça ressemble à l'an dernier », pas « ça n'a plus de
  couleurs ».
- Ce qui voyage entre les thèmes est la **distance perceptuelle**, pas les
  coordonnées. Quiconque retouche une de ces deux encres doit re-mesurer
  l'écart, pas la teinte.
- Toute couleur écrite en dur dans un `style={{}}` de `.tsx` est désormais un
  défaut potentiel : elles ont toutes été écrites contre un fond sombre.
- Le sombre reste le défaut **contre** l'usage moderne (suivre le système).
  Basculer les installations existantes en clair au premier OS clair aurait
  été un changement visible que personne n'a demandé. « Auto » est à un geste.

## Alternatives écartées

**Rester sombre uniquement.** Tenable, et c'est ce que le guide prescrivait.
Écarté sur demande explicite.

**Assombrir mécaniquement les encres pour le clair.** C'est le piège : ça
effondre l'orange et le jaune en un seul brun et supprime silencieusement la
distinction que tout le §2 existe à protéger.

**Dupliquer la palette dans deux blocs `@media` / `[data-theme]`.** Deux
déclarations par couleur au lieu d'une, et surtout deux endroits à maintenir —
la garantie qu'ils divergent. `light-dark()` tient les deux dans une ligne.

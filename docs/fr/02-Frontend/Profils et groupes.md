---
tags: [frontend, modele]
---

# Profils et groupes

Le mécanisme « à la UniFi » : définir une configuration **une fois**, l'appliquer à des ensembles de ports et d'équipements, et ne gérer que les exceptions.

## Où en est cette page

Le mécanisme existe, mais **pas sous la forme d'un héritage** : c'est un **jeu de règles** qui produit une configuration, et l'exploitant la relit avant de la poser. La différence est développée dans [[Profils de switchs]] ; elle change ce que cette page peut promettre.

| Question | État |
|---|---|
| **VLAN** de site : id, nom, couleur, usage | **livré** — plus un VLAN 1 structurel, indélébile (migration `0018`, [[VLAN par défaut]]) |
| **Profil de prise** : mode, PVID, tagged, PoE | **livré** — écran *Configuration → Profils de prise*, appliqué depuis la fiche du switch |
| **Profil de switch** : règles → configuration de la façade, avec **aperçu** | **livré** — `internal/portrules`, [[Profils de switchs]] |
| Aperçu qui distingue *désigné* et *obtenu*, prises non couvertes, refus en bloc | **livré** — et c'est le garde-fou le plus abouti de la page |
| Incompatibilité d'une prise avec ce qu'une règle demande, signalée **avant** la pose | **livré pour les profils de switch** ; pour l'application directe d'un profil de prise, le PoE est corrigé en silence |
| **Profil d'équipement** : services communs (STP, SNMP, syslog, NTP…) et affectations par défaut | **écran de saisie livré, rien ne le compile** |
| **Groupe d'équipements** | **hérité** — plus d'écran, modèle conservé pour la portée des identifiants |
| Chaîne de **résolution** par héritage (défauts site → profil → overrides) | **décidée, non construite** |
| Badge « hérité de : profil *bureau* » / « override local », bouton « revenir à l'héritage » | **décidé, non construit** — un profil appliqué **recopie** ses valeurs |
| Recompilation des devices concernés quand un profil change, bandeau « n équipements à déployer » | **décidé, non construit** |
| Refus de supprimer un VLAN ou un profil **référencé** | **décidé, non construit** — la suppression aboutit (callout ci-dessous) |
| Profils **versionnés** comme l'intent, historique de qui a changé quoi | **décidé, non construit** |

## Objets

| Objet | Portée | Contenu | État |
|---|---|---|---|
| **VLAN** | site | id 802.1Q, nom, couleur ([[Rendu des ports]]), usage (mgmt/users/voice/iot…) | **livré** |
| **Profil de prise** | site | mode access/trunk, PVID, tagged, PoE — le sous-arbre `ports[].*` du [[Modèle de données standard]] | **livré** ; voice VLAN, STP edge/guards et filtres (dhcp snooping trust, RA guard, storm control) restent **décidés, non construits** |
| **Profil d'équipement** | site | services communs : STP mode/priorité, SNMP, syslog, NTP, DNS, mgmt (ssh/http/telnet on-off), igmp snooping + **affectations par défaut** de profils de prises | **saisie livrée, compilation non construite** |
| **Groupe d'équipements** | site | *hérité* — ensemble nommé de devices ; un groupe référence un profil d'équipement. Voir l'encadré ci-dessous | **hérité** |
| **Profil de switch** | site | règles qui produisent la configuration des prises d'un équipement à partir de sa façade — [[Profils de switchs]] | **livré** |

> [!warning] Le groupe n'a plus d'écran
> Le profil d'équipement qu'il porte n'est compilé nulle part, et aucune interface
> n'a jamais affecté un équipement à un groupe. Sa seule fonction vivante — décider
> quel identifiant s'applique — est mieux remplie par une **étiquette**
> ([[Étiquettes et portée des identifiants]]), qui n'est pas unique par équipement.
> Le modèle reste en place tant que des identifiants visent un groupe ; le §1 de
> [[Profils de switchs]] décrit le chemin de sortie, par étapes. L'ancienne URL
> `/config/groupes` redirige vers les profils de switchs.
>
> Ce que le groupe promettait pour la configuration — « ajouter un switch au groupe
> suffit à le rendre conforme » — est repris par le **profil de switch**, qui
> l'obtient sans avoir à ranger le parc au préalable.

> [!warning] Le profil d'équipement a un écran, et rien derrière
> Il se crée, se modifie et se supprime (*Configuration → Profils d'équipements*),
> et **aucune de ses valeurs n'atteint jamais un équipement** : rien ne le compile,
> et aucun écran n'y rattache un device. C'est aujourd'hui un formulaire dont le
> contenu ne sort pas de la base.
>
> Il n'est pas retiré, parce que la liste des services communs qu'il porte est
> exactement ce qu'il faudra pousser (§ *services* du
> [[Modèle de données standard]]) et que la moitié de ces services est déjà posée
> par d'autres chemins, mieux ciblés : **SNMP** par l'identité de service
> ([[Identité SNMP]]), la **destination de traps** par le collecteur déclaré
> ([[Monitoring]]). Le profil d'équipement est donc en attente de ce qui reste —
> STP, NTP, DNS, interrupteurs de management — et non d'une réécriture.
>
> Ce qu'il faut éviter d'ici là : promettre à un client qu'y renseigner un serveur
> NTP configure son parc. L'écran devrait le dire lui-même.

## Résolution

Ordre de priorité croissant **visé** :

```
défauts site  →  profil d'équipement (via groupe)  →  overrides device
      →  profil de prise (affecté par défaut ou à la main)  →  overrides port
```

> [!important] Il n'y a pas d'héritage : un profil appliqué RECOPIE ses valeurs
> C'est l'écart le plus important de la page, parce qu'il change la nature de
> l'objet. Le modèle d'un port (`PortIntent`) ne porte **aucune référence** au
> profil dont il vient : appliquer le profil *bureau* à douze prises y écrit mode,
> PVID, tagged et PoE, et le lien est perdu à l'instant même.
>
> Ce qui en découle, et qu'il faut savoir avant de s'appuyer sur la page :
>
> - il n'y a **pas de badge d'origine** (« hérité de : profil *bureau* »), pas de
>   distinction visible entre une valeur héritée et un override local, et pas de
>   bouton « revenir à l'héritage ». Rien ne dit d'où vient ce qu'on lit ;
> - **modifier un profil ne change rien sur le parc.** Aucune recompilation, aucun
>   bandeau « n équipements ont des changements à déployer », aucun diff global.
>   Les prises déjà configurées gardent les anciennes valeurs, et l'écran ne le
>   signale pas ;
> - **supprimer un profil ne casse rien** — précisément parce que rien ne le
>   référence.
>
> Ce n'est pas seulement une étape manquante. La copie a un avantage réel, et c'est
> celui que [[Profils de switchs]] exploite : ce qui est posé sur l'équipement est
> **exactement ce que l'exploitant a relu dans l'aperçu**, sans qu'un changement
> ailleurs puisse le modifier après coup. Un héritage vivant est plus puissant et
> plus surprenant : il fait bouger la configuration d'un switch qu'on n'a pas
> touché.
>
> La cible reste l'héritage, parce que « ne gérer que les exceptions » est la
> promesse du produit et qu'elle ne tient pas sans lui. Mais l'ordre à suivre n'est
> pas d'ajouter une référence dans `PortIntent` : c'est de **rendre la
> recompilation visible avant de la rendre automatique** — le bandeau et le diff
> global d'abord, la propagation ensuite. L'inverse ferait bouger des parcs sans
> que personne ne voie pourquoi.

- Chaque niveau ne définit que ce qu'il veut imposer (merge par champ, « absent = hérite ») : **cible**. Ce qui existe et qui en est le germe : l'application d'une sélection **fusionne** le patch avec l'intention propre de chaque port, donc poser un VLAN sur douze prises n'efface pas leurs libellés ([[Interface web]]).
- Ce que le **profil de switch** fait à la place, et qui répond au même besoin par l'autre bout : une règle nomme un **ensemble** (« les quatre dernières prises RJ45 »), jamais un numéro de port absolu. Elle vaut donc pour un 48 ports comme pour un 8 ports — « c'est toute la différence entre un profil et un copier-coller ». Et le calcul est **pur** : une façade et un jeu de règles entrent, une attribution prise par prise sort, sans base, sans équipement, sans horloge — parce qu'une décision qu'on ne peut pas rejouer à l'identique n'est pas relisible.

## Exemples

- Profil de prise `bureau-standard` : access PVID 20, PoE on. *(Voice VLAN, STP edge et BPDU guard sont dans le modèle visé, pas dans le formulaire livré.)*
- Profil de prise `uplink` : trunk natif 10, tagged all.
- Profil de switch `etage-standard` : les prises RJ45 sauf les quatre dernières → `bureau-standard`, les quatre dernières RJ45 et les cages SFP → `uplink`. Appliqué à un 48 comme à un 8 ports, sans réécriture.
- Profil d'équipement `etage-standard` : RSTP prio 32768, snmp v3, syslog vers proxy — **saisissable, non posé** (voir le callout ci-dessus).
- Groupe `Bâtiment B` : 12 switchs → `etage-standard`. **Hérité** : le geste équivalent est d'appliquer le profil de switch aux douze équipements, ou de poser une étiquette pour la portée d'un identifiant.

## Garde-fous

- Suppression d'un VLAN/profil référencé : refusée tant que des ports l'utilisent (l'UI liste les usages).

> [!warning] Cette protection n'existe pas : la suppression aboutit
> Supprimer un VLAN ou un profil ne vérifie **aucun usage**. Le seul refus est
> structurel : le **VLAN 1** ne peut pas être supprimé, parce que les équipements
> l'ont de toute façon et qu'il est le natif implicite des trunks — le retirer du
> site ne l'enlèverait de nulle part, ça rendrait seulement illisible ce que
> portent les prises ([[VLAN par défaut]]).
>
> Pour tous les autres, la suppression passe. Les ports qui portaient le VLAN
> gardent son numéro dans leur intention, mais le site ne sait plus le nommer ni le
> colorer : la case perd sa couleur et redevient neutre ([[Rendu des ports]]), donc
> **la configuration devient invisible sans changer**. La conséquence est plus
> pernicieuse qu'une erreur : rien ne casse, l'équipement reste configuré comme
> avant, et l'écran cesse de le montrer.
>
> Deux atténuations : la suppression émet un événement en **avertissement** dans le
> [[Journal d'événements]] (`vlan.deleted`, `profile.deleted`) avec l'auteur, et
> elle exige `site_admin`. Ce qui manque est le décompte des usages — que le code
> sait déjà faire, puisque la légende VLAN affiche « n ports » par VLAN. C'est ce
> décompte qu'il faut opposer à la suppression.

- Les profils sont versionnés comme l'intent ([[Modèle de données standard]]) : l'historique montre qui a changé quoi. **Décidé, non construit** — un profil est une ligne mise à jour en place, sans révision. Ce qui existe : l'événement de modification dans le [[Journal d'événements]], avec l'auteur et la date. C'est *qui et quand*, pas *quoi* : on ne peut pas revenir à l'état d'avant.
- Un profil incompatible avec un équipement (capability manquante, ex. pas de PoE) est signalé **à l'affectation**, pas au déploiement ([[Connecteurs]]).

> [!important] Livré pour les profils de switch, et mieux que la formulation ne le dit
> L'aperçu d'un profil de switch (`POST /api/devices/{id}/switch-profile/preview`)
> est le garde-fou le plus abouti de cette page, et il porte **quatre** natures
> d'avertissement là où la conception n'en prévoyait qu'une :
>
> - **par prise** : ce que la règle demandait et que la prise ne sait pas faire —
>   le PoE sur une cage SFP, typiquement ;
> - **par règle** : le nombre de prises **désignées** et le nombre **obtenues**.
>   Les deux diffèrent dès qu'une règle placée plus haut a pris les mêmes prises,
>   et une règle qui en désigne trente sans en gagner aucune n'est pas fausse, elle
>   est **masquée** — ce qui est indiscernable si l'on n'affiche qu'un seul des
>   deux chiffres ;
> - les prises **qu'aucune règle ne désigne**, dites explicitement, et qui restent
>   **inchangées** — un profil qui ne couvre pas tout ne doit pas laisser croire
>   qu'il a tout traité ;
> - un refus **en bloc** quand le plan porte un problème rédhibitoire : mieux vaut
>   ne rien poser que poser à moitié.
>
> Pour l'application **directe** d'un profil de prise à une sélection, en revanche,
> le seul garde-fou est le PoE, et il est **corrigé en silence** : `poe: true` sur
> une prise sans PoE devient `poe: false`, sans avertissement à l'écran. Le journal
> le dit (« configurations mixtes »), l'écran non. C'est le bon comportement — poser
> une intention insatisfiable créerait un écart permanent en [[Réconciliation]] —
> mais il devrait se voir, comme l'aperçu sait le faire.

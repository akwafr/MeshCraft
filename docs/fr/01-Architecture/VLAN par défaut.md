---
tags: [reseau, orchestration, donnees]
---

# VLAN par défaut

Deux règles, une seule cause : **le VLAN 1 n'est pas une décision d'exploitant,
c'est un fait matériel**. Tout switch en sort d'usine avec, et aucun ne permet de
s'en défaire. meshcraft ne peut donc pas le traiter comme un VLAN ordinaire sans
décrire un parc qui n'existe pas.

## 1. Tout site possède le VLAN 1, et ne peut pas le perdre

`store.CreateSite` insère le site **et** son VLAN 1 dans la même transaction. Pas
de création en deux temps : il ne doit exister aucun instant où un site est là
sans son VLAN par défaut, sinon la première relecture d'équipement remonte un
VLAN « inconnu » qui n'a rien d'inconnu.

La migration `0018_default_vlan.sql` rattrape l'existant. Elle est écrite en
`where not exists` : un site qui décrivait déjà son VLAN 1 garde **son** nom et
**sa** couleur — la règle impose la présence, pas l'étiquetage.

Côté API, `handleDeleteVlan` et `handleUpdateVlan` refusent en `409` :

| Action sur le VLAN 1 | Résultat |
|---|---|
| Supprimer | refusé — l'effacer du site ne l'enlèverait de nulle part |
| Changer son `vlan_id` | refusé — ce serait le supprimer par un autre chemin |
| Renommer, recolorer, décrire | **autorisé** — c'est de la présentation |

L'écran *Configuration → VLANs* remplace le bouton de suppression par la mention
« par défaut » et verrouille le champ ID. Le garde-fou reste néanmoins côté
serveur : l'interface n'est qu'une politesse, l'API est la règle.

> [!warning] Une porte reste ouverte : l'import de configuration de site
> `store.ImportSiteConfig` **purge** la table `vlans` du site puis réinsère celles
> du fichier ([[Modèle de données standard]], § *Format d'échange*). Un fichier
> `meshcraft_site_config` écrit à la main sans VLAN 1 laisse donc le site sans son
> VLAN par défaut — la règle est tenue à la **création** et par la **migration**,
> pas à l'import.
>
> Le cas ne s'atteint pas par un aller-retour meshcraft : l'export lit la base, où
> le VLAN 1 est toujours là. Il s'atteint par un fichier fabriqué, ce qui est
> précisément l'usage que ce format existe pour permettre. La réparation est celle
> de la migration `0018`, au même endroit : un `where not exists` après la
> réinsertion. Trois lignes, contre un site dont la première relecture d'équipement
> remonterait un VLAN « inconnu » qui n'a rien d'inconnu.

## 2. Un trunk sans natif explicite a le VLAN 1 pour natif

> [!important] Sauf natif explicite
> Un PVID demandé gagne toujours. Le VLAN 1 n'est le natif que lorsque **aucun**
> autre n'est désigné — et que le port est un **trunk** : côté serveur
> `recon.ParsePorts` ne substitue le défaut que sur `mode == "trunk"`, côté écran
> `nativeVlan()` rend `null` pour un port d'accès sans PVID.

Ce n'est pas une convention de confort, c'est la correction d'une **fausse
dérive**. L'équipement rapporte toujours un natif ; s'il n'a rien reçu, il
rapporte 1. Une intention stockée à `pvid: 0` ne correspondrait donc jamais à
l'observation, et [[Réconciliation]] afficherait `port.GEx.pvid: 0 → 1` sur tous
les trunks du parc, en permanence, sans que personne n'ait touché à quoi que ce
soit. Pire : l'application chercherait à « corriger » l'écart en retirant le
natif à chaque passage.

La normalisation vit dans `recon.ParsePorts`, c'est-à-dire **à la frontière de
lecture de l'intention**. Tout ce qui consomme une configuration de port —
comparaison de dérive, application automatique, traduction en appartenance par
VLAN — passe par là et hérite de la règle sans la redire. Un port `libre` n'y
gagne rien : l'absence d'intention n'est pas une intention.

Le rendu suit la même logique via `nativeVlan()` dans `web/src/network.ts` : un
trunk sans natif désigné se colore du VLAN 1 et répond « oui » quand on demande
s'il porte le VLAN 1.

## 3. Le VLAN 1 n'est pas une ligne de table — et ce que ça change à l'écriture

La règle « tout switch a le VLAN 1 » se vérifie sur le matériel, mais **pas sous la
forme qu'on attendrait** : sur les deux plateformes MikroTik livrées, le VLAN 1
existe *sans* que la table des VLAN le déclare, et c'est une différence de nature,
pas de présentation.

| Plateforme | Comment le VLAN 1 existe | Relevé |
|---|---|---|
| **SwOS** (CSS326) | **implicitement**. Les deux cages SFP de l'équipement de référence ont `dvid = 1` alors que sa table `/vlan.b` ne contient QUE les VLAN 99 et 100 | `dvid[24] = dvid[25] = 0x01`, table à deux lignes |
| **RouterOS** (CRS326) | par une entrée **DYNAMIQUE** que l'équipement se fabrique tout seul | `{"vlan-ids":"1","dynamic":"true","comment":"added by pvid","untagged":"bridge,sfp-sfpplus2"}` |

Trois conséquences, toutes dans le code d'écriture :

1. **Ne jamais créer la ligne du VLAN 1 sur SwOS.** La créer transformerait le
   VLAN 1 en VLAN *déclaré*, dont seuls les ports listés seraient membres —
   c'est-à-dire changerait le comportement de tout le trafic non tagué de
   l'équipement, alors que personne ne l'a demandé. `swos.implicitVLAN` porte cette
   règle, et la première version du connecteur échouait à l'inverse : elle cherchait
   une ligne pour le VLAN 1 et se plaignait de son absence sur un équipement
   parfaitement configuré.
2. **Ne jamais rien retirer du VLAN 1 sur SwOS.** Il n'a pas de ligne : il n'y a
   rien à en retirer, et le mentionner dans un refus ferait chercher un retrait
   impossible sur tous les ports d'un équipement sorti d'usine.
3. **Ne jamais écrire dans une entrée dynamique sur RouterOS.** Elle n'a pas de
   `.id` stable, elle ne se modifie pas, et elle disparaît quand la raison qui l'a
   fait naître disparaît. La pose crée une entrée **statique à côté**, et le dit au
   transcript parce que c'est contre-intuitif : on « ajoute » un VLAN qui
   apparaissait déjà dans la lecture.

> [!important] Le VLAN natif implicite du §2 est ce qui rend `admit-only-vlan-tagged` inécrivable
> La règle du §2 — un trunk sans natif explicite a le VLAN 1 pour natif — a une
> conséquence que personne n'avait vue venir côté RouterOS : **meshcraft ne peut pas
> exprimer un trunk qui refuse le trafic non tagué**.
>
> Un trunk meshcraft a *toujours* un VLAN natif, puisque `recon.ParsePorts` y met le
> VLAN 1 quand l'intention n'en désigne pas. Or un VLAN natif veut dire « ce trafic
> entre et sort de ce port sans étiquette ». Écrire
> `frame-types=admit-only-vlan-tagged` — la valeur qu'un trunk « propre » devrait
> porter sur un CRS3xx — jetterait donc précisément le trafic du VLAN natif que le
> plan demande.
>
> Le connecteur laisse les trunks en `admit-all`, ce que RouterOS appelle un port
> **hybride**, et c'est exactement ce que le modèle décrit. La limite est réelle et
> elle est *en amont* des connecteurs : c'est le modèle standard qui n'a pas
> l'intention « trunk sans natif ». La lever demanderait un `pvid: null` distinct de
> `pvid: 0` dans `devices.ports`, et donc de rouvrir la fausse dérive que le §2
> existe pour éviter. Ce n'est pas un arbitrage à faire à la légère.

## Voir aussi

- [[Réconciliation]] — pourquoi une intention doit être comparable à une mesure
- [[Modèle de données standard]] — la forme stockée de `devices.ports`
- [[Adressage de management]] — l'autre VLAN qu'on ne traite pas comme les autres
- [[Connecteurs]] — §*Le modèle VLAN des deux plateformes MikroTik* : ce que chaque
  plateforme porte, ce que la pose y écrit, et ce qu'elle refuse
- [[Profils de switchs]] — ce qu'une règle peut demander, et ce qu'aucun connecteur
  ne saura poser

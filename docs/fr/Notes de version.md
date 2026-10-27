---
tags: [meshcraft, livraison]
---

# Notes de version

La **version produit** (fichier `VERSION` à la racine) nomme les releases et la
documentation. Elle n'intervient **pas** dans la décision de mise à jour d'un
proxy, qui se prend sur l'horodatage de build — voir
[[Versionnage et mise à jour du proxy]].

## 0.2.0 — 2026-10-25

Le cycle qui ouvre meshcraft au **L3**, lui donne une **API utilisable par des
tiers**, et transforme une série de silences en informations. Dix-huit
décisions l'accompagnent ([[Index des décisions]], ADR-0011 à 0028).

### Ce qui est nouveau

- **Jetons d'API par utilisateur** et **serveur MCP** — même identité, mêmes
  droits, aucun second modèle de permissions (ADR-0011, ADR-0012).
- **Deux modes de navigation**, commutation et routage, avec les écrans
  Routage, Pare-feu, DNS et DHCP (ADR-0013, ADR-0019).
- **Extensions de site** et première incarnation **UniFi** : inventaire des
  bornes et des SSID, puis les gestes adopter / oublier / reprovisionner
  (ADR-0015, ADR-0016, ADR-0025).
- **Alerting** avec canaux **Telegram** et **Slack** — abonné du journal, pas
  second modèle d'événement (ADR-0024).
- **Tuiles de tableau de bord** renommables, et une tuile de trafic par port
  (ADR-0027).

### Ce que le produit cessait de dire

C'est la part la moins visible de cette version, et probablement la plus
utile : plusieurs écrans affirmaient des choses fausses **sans le signaler**.

- Un **limiteur de tempête** exprimé en paquets par seconde n'était pas lu, et
  la règle « le silence vaut absence de limiteur » concluait alors *« port non
  protégé »* sur un port protégé (ADR-0020).
- Le **SVI de management** était le dernier bloc lu du running-config, pas
  celui que la session emprunte. Sur Cisco IOS, le même défaut **dirigeait une
  écriture** vers un lien de transit. Corrigé sur EOS, IOS et fs-s3900.
- Une **lecture courte** devenait l'intention en silence : 48 ports sur 52
  importés, avec un `in_sync` par-dessus, parce que l'intention et
  l'observation sortaient de la même lecture tronquée (ADR-0017).
- Le **nom d'une région MSTP** n'était affiché nulle part — assez pour qu'un
  renommage de site scinde une fabric en deux régions, sans symptôme, pendant
  des semaines (ADR-0021, ADR-0022, ADR-0023).
- Les **cages SFP+** d'un FS S3400 disparaissaient du modèle sur une
  convention de numérotation, emportant les deux uplinks actifs (ADR-0018).
- Trois **libellés constructeur** du même commutateur produisaient trois
  modèles distincts à l'inventaire (ADR-0014).

### La règle qui revient

Une plateforme **déclare** ce qu'elle sait faire, et ce qu'elle ne sait pas
faire **se dit à l'écran** au lieu de manquer. Rejoindre une région MST
(ADR-0022), décomposer le trafic par classe (ADR-0028) : dans les deux cas,
une série vide ne distingue pas « la collecte démarre » de « cette plateforme
ne saura jamais », et c'est la capacité déclarée qui tranche.

Son corollaire : **un invariant tenu par un type est tenu ; tenu par un
commentaire, il a déjà échoué**. `absorbObserved` portait sa règle de sûreté
en capitales dans son en-tête, et le premier nouvel appelant l'a ignorée.

### Horloges

Le référentiel de temps est celui de **celui qui regarde** (ADR-0008, cycle
précédent), mais deux soustractions mélangeaient encore les référentiels —
dont celle qui armait une suppression. NTP a par ailleurs été posé sur l'hôte
du contrôleur, qui retardait de six minutes sans synchronisation.

### Connu et non résolu

- Les ports collectés en **gNMI n'émettent aucun événement de port** : les
  transitions partent sans `IfIndex` et sont rejetées en aval. Aucune alerte
  de port sur un Arista tant que ce n'est pas corrigé.
- `fs-s3400` garde deux chemins de lecture du management aux départages
  opposés ; quand ils divergent, **le masque est silencieusement perdu**.
- `fs-s3900` n'a **aucun contrôle d'extinction** sur ses SVI : un SVI éteint
  peut revendiquer le management. Combler ce trou demande une capture
  matérielle, pas une expression régulière devinée.
- L'**unknown unicast** n'est collectable nulle part : la MIB standard ne
  l'expose pas par port, et le seul chiffre du parc compte des trames *jetées*.
  Deux classes vraies plutôt que trois dont une inventée.
- La table `events` n'a **aucune rétention**, et elle est désormais sur un
  chemin chaud.

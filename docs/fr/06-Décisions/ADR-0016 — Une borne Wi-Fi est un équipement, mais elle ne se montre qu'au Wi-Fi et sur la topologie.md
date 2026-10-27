---
Statut: Proposé
Date: 2026-10-24
---

# ADR-0016 — Une borne Wi-Fi est un équipement, mais elle ne se montre qu'au Wi-Fi et sur la topologie

## Contexte

Le Wi-Fi n'est modélisé **nulle part** dans meshcraft. Le modèle de données
part d'un équipement commutant : une façade, des ports, des VLAN par port,
du STP, du PoE, du storm control, un connecteur qui sait écrire tout ça.

L'import UniFi amène des bornes. Les quatre relevées sur le site `ARENA`
sont des AC Pro (`U7PG2`), et voici ce que le contrôleur UniFi en dit
réellement :

- une identité complète : nom, modèle, firmware, adresse IP, MAC, état. Et
  une **configuration**, que le contrôleur UniFi détient et dont meshcraft
  lit les relevés ;
- deux ports RJ45 (`idx` 1 et 2, dont un seul `UP`) : des ports, donc, mais
  deux et pas quarante-huit, et sans VLAN ni profil ;
- deux **radios** : `802.11n` à 2,4 GHz, canal 1, largeur 20 MHz ; `802.11ac`
  à 5 GHz, canal 36, largeur 40 MHz ;
- des statistiques qui n'existent sur aucun commutateur : `txRetriesPct` par
  radio, et une charge CPU/mémoire de borne ;
- un SSID de site (`ARENA`, WPA2-PSK, bandes `2g` + `5g`) qui n'appartient à
  **aucune** borne en particulier : toutes le diffusent ;
- onze **clients** associés, rattachés à une borne par `uplinkDeviceId`.

Une borne est donc, techniquement, un **équipement** : une boîte avec une
identité, une adresse, une configuration et un port d'uplink branché sur le
réseau. Ce qui la distingue d'un commutateur, c'est **ce qu'elle porte** et
**qui la pilote**. Elle a des radios, des bandes, des canaux, des SSID et des
clients associés, attributs qu'un port d'accès n'a pas. Et sa configuration
appartient au contrôleur UniFi, pas à meshcraft
([[ADR-0015 — L'extension de site vit dans le contrôleur, et son inventaire est un miroir en lecture seule]]).

Aujourd'hui, une borne qui parle LLDP apparaît déjà sur la topologie, mais
comme un **pair fantôme** : un voisin inconnu, tracé en lien fin, sans nom de
modèle ni lien vers quoi que ce soit. meshcraft sait pourtant exactement ce
que c'est.

## Décision

**La borne Wi-Fi est un équipement, et le produit le dit. Elle ne se montre
qu'à deux endroits : l'écran Wi-Fi, qui est sa page, et la topologie, qui est
sa place dans le réseau. Le SSID appartient au site, pas à la borne.**

1. **Un équipement, pas un équipement piloté.** Dans le vocabulaire du
   produit, une borne est un équipement : elle a un nom, un modèle, un
   firmware, une adresse, un état, une configuration. Mais elle n'entre pas
   dans `devices`, dont l'invariant est « quelque chose que meshcraft
   configure par un connecteur » (ADR-0015 §5). Elle vit dans l'inventaire
   miroir de l'extension, sous la classe `access_point`. La frontière tient
   à qui détient la configuration, pas à la nature de l'objet.

2. **Trois niveaux, pas un.** Le modèle importé distingue :
   - la **borne** : identité, modèle, firmware, état, adresse, les quelques
     ports cuivre ;
   - la **radio** : bande, standard, canal, largeur. Elle est enfant de la
     borne, et jamais un port ;
   - le **SSID** : un objet du site, avec ses bandes et son mode de
     sécurité, et pas un enfant de borne.

   Le client associé n'est pas un quatrième niveau : il se rattache par MAC
   aux clients que meshcraft modélise déjà.

3. **Elle apparaît sur l'écran Wi-Fi et sur la topologie, et nulle part
   ailleurs.**
   - **L'écran Wi-Fi est sa page.** Identité, radios, configuration lue, SSID
     diffusés, clients associés. C'est là qu'on clique pour la voir.
   - **La topologie la montre comme une borne, plus comme un fantôme.** Le
     pair LLDP inconnu dont la MAC correspond à une borne du miroir devient
     un nœud nommé, avec son type et son état, branché sur le port où il est
     vu. Le cliquer mène à l'écran Wi-Fi, pas à une page d'équipement.
   - **Elle reste absente de la liste Équipements**, des pages de
     commutateur, des façades, des gestes de port, du PoE, du STP et des
     sélecteurs d'équipement. Ces écrans sont ceux de ce que meshcraft
     pilote, et une borne y répondrait « néant » à chaque question.

4. **La corrélation avec le monde commuté se fait par MAC, pas par fusion de
   modèle.** Une borne est branchée sur un port d'un commutateur que meshcraft
   pilote peut-être déjà, donc LLDP et la table FDB connaissent son adresse.
   On relie les deux inventaires par cette adresse, au moment de la lecture,
   d'abord par LLDP, puis par la FDB à défaut. C'est ce qui place la borne sur
   la topologie et répond à « sur quel port est cette borne » sans en faire
   un équipement piloté.

5. **Le Wi-Fi est un écran du mode Commutation.** L'écran **déclare** son
   mode
   ([[ADR-0019 — Le sélecteur ne porte que la marque, le mode se lit dans la barre]]),
   et ce mode est Commutation : une borne ponte des trames sur un VLAN, elle
   ne route pas. Le mode ne filtre que la navigation, donc cette déclaration
   ne restreint ni les données ni les droits.

6. **Les unités sont celles de la radio, pas celles du cuivre.** La bande se
   dit en GHz, la largeur en MHz, le canal est un entier sans unité, le taux
   de réémission en pourcentage. On ne réutilise **pas** la rampe de trafic
   d'[[ADR-0003 — La rampe de trafic est logarithmique et relative à la
   capacité]] pour une qualité radio : une capacité radio n'est pas une
   capacité de port, et la même rampe dirait une chose fausse.

## Conséquences

- **Il faut écrire un écran Wi-Fi au lieu d'en réutiliser un.** Pas de
  façade, pas de table de ports. C'est le coût direct, et il se paie une fois.
- **La topologie gagne un deuxième type de nœud**, qui ne vient pas de
  `devices`. Elle doit lire le miroir en plus de l'inventaire piloté et
  savoir dessiner un nœud qu'elle ne peut ni configurer ni ouvrir sur place.
  Tout code de topologie qui suppose « un nœud = une ligne de `devices` »
  devient faux.
- **Le lien vers une borne n'a qu'une moitié mesurée par meshcraft.**
  [[ADR-0026 — Un lien de topologie a deux moitiés, chacune colorée par l'egress de son extrémité]]
  colore chaque moitié par l'egress de son extrémité. Pour la moitié côté
  borne, l'egress de la borne est l'ingress du port de commutateur en face :
  c'est le même câble. On le lit là, et on ne demande pas de compteurs à
  UniFi pour la topologie. Si la borne est branchée sur un commutateur que
  meshcraft ne pilote pas, le lien n'a aucune mesure, et il est dessiné comme
  tel.
- **Une borne invisible en LLDP et absente de la FDB n'est pas sur la
  topologie.** Elle reste sur l'écran Wi-Fi. On ne l'accroche pas « quelque
  part » pour remplir le dessin.
- **Compter « les équipements d'un site » exige désormais de dire si les
  bornes sont dedans.** La liste Équipements compte ce que meshcraft pilote.
  La topologie montre tout ce qui est branché. Les deux chiffres diffèrent, et
  c'est voulu.
- Le jour où un deuxième fournisseur de Wi-Fi arrive, c'est ce modèle-ci
  qu'il devra remplir. Il est donc public et doit rester neutre : les champs
  sont ceux du 802.11, pas ceux d'UniFi. `wlanStandard`, `frequencyGHz`,
  `channelWidthMHz` se traduisent ; `configurationId` ne se traduit pas et
  reste une donnée opaque du fournisseur.
- Un SSID porté par le site, et non par la borne, interdit d'emblée
  d'afficher « les SSID de cette borne » comme une vérité locale. C'est le bon
  modèle, et il va contre l'intuition : l'opérateur, lui, voit bien un SSID
  dans l'air à côté d'une borne. L'écran devra donc faire le rattachement
  explicitement, et ne pas le laisser deviner.
- Les radios sont un relevé instantané, pas une configuration. On ne promet
  pas de les voir changer en direct, et un canal affiché peut être périmé. La
  fraîcheur se lit selon
  [[ADR-0008 — Le référentiel de temps est l'horloge de celui qui regarde]].

## Alternatives écartées

**Mettre les bornes dans `devices` avec `brand = "UniFi"`.** L'option qui
coûte zéro ligne de modèle et rend tout l'affichage existant gratuit,
topologie comprise. Écartée parce qu'elle confond « est un équipement » et
« est piloté par meshcraft ». La première chose que fait quelqu'un en
cliquant un équipement de la liste est d'ouvrir ses ports. Deux ports RJ45
sur une page de façade conçue pour quarante-huit, sans VLAN, sans PoE et sans
voisin, ce n'est pas une dégradation : ça ressemble à un bug. Et chaque geste
de port devrait apprendre une exception.

**Garder la borne hors de la topologie**, ce que disait la première rédaction
de cet ADR (« pas de topologie gratuite »). Écartée : la topologie répond à
« qu'est-ce qui est branché où », et une borne est branchée quelque part. La
laisser en pair fantôme montre un voisin inconnu là où meshcraft sait qu'il
y a une AC Pro nommée, ce qui est une information juste rendue fausse par
omission. C'est aussi sur la topologie qu'on cherche une borne quand des
clients Wi-Fi se plaignent d'un VLAN.

**La lister dans Équipements avec un filtre « bornes »**, pour qu'elle soit
partout où sont les équipements. Écartée : chaque écran qui consomme la liste
(sélecteurs, gestes en masse, conformité de management) devrait savoir
exclure la classe, et le premier oubli propose un geste de port sur une
borne. Deux endroits précis valent mieux qu'une exclusion à répéter partout.

**Modéliser la radio comme un port**, avec la bande en guise de connecteur.
Séduisant, parce qu'[[ADR-0007 — L'agrégat est un port sur la façade]] a déjà
élargi une fois la notion de port. Écartée : l'agrégat se *comporte* comme
un port, avec une vitesse, des membres de VLAN, des compteurs, un voisin. Une
radio n'a rien de tout ça. La faire passer pour un port contaminerait toutes
les règles de port (profils, storm control, root bridge) avec un cas « sauf
si c'est une radio ».

**Un unique modèle « nœud de réseau » générique**, dont commutateur et borne
seraient deux spécialisations. L'architecture propre, en théorie. Écartée
parce qu'elle impose de refondre `devices`, la table la plus centrale et la
plus écrite du produit, pour accueillir quatre bornes en lecture seule. Le
rapport entre le risque et le gain n'y est pas aujourd'hui. Si un second
fournisseur de Wi-Fi arrive et qu'un troisième modèle d'objet se présente,
c'est l'ADR à écrire à ce moment-là.

**Rattacher le SSID à la borne**, puisque c'est la borne qui l'émet.
Écartée : le SSID est configuré au niveau du site UniFi et diffusé par toutes
les bornes. Le dupliquer par borne créerait N copies à réconcilier, et la
première incohérence entre deux copies ne se distinguerait pas d'un vrai
écart de configuration.

**Mettre l'écran Wi-Fi en mode Routage**, au motif que le Wi-Fi « n'est pas
de la commutation ». Écartée au point 5 : une borne ponte des trames sur un
VLAN, elle ne route pas. La mettre en routage l'éloignerait précisément des
ports sur lesquels elle est branchée.

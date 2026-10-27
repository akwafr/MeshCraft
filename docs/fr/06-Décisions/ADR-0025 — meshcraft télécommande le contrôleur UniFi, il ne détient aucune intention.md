---
Statut: Précisé par ADR-0029
Date: 2026-10-25
---

> **Une phrase du point 2 a été rouverte.** « Aucun champ de réglage importé ne
> devient modifiable » n'est plus exacte, et l'alternative écartée « un
> entre-deux explicite, champ par champ » a été reprise par la porte qu'elle
> nommait elle-même : [[ADR-0029 — Le SSID s'édite, la borne ne s'édite pas, et
> seul ce qui se relit se propose]] rend les **réglages de SSID** éditables.
> Tout le reste de ce document fait autorité et est le socle d'ADR-0029 — en
> particulier §3 (aucune intention stockée, donc aucun convergeur), §4 (une
> seule porte d'écriture, close à la compilation) et §6 (`rc:ok` n'est pas une
> preuve). Les réglages **par borne** restent non modifiables, et ADR-0029 dit
> pourquoi c'est désormais une mesure et non un choix.

# ADR-0025 — meshcraft télécommande le contrôleur UniFi, il ne détient aucune intention

## Contexte

[[ADR-0015 — L'extension de site vit dans le contrôleur, et son inventaire est
un miroir en lecture seule]] §5 interdit trois choses nommément : « ni
adoption, ni changement de configuration, ni redémarrage de borne ». Le paquet
`internal/unifi` n'a aucune méthode d'écriture, et son en-tête dit pourquoi :
c'est là que la promesse est tenue, pas dans un commentaire.

L'écran Wi-Fi affiche cette promesse en toutes lettres. Un opérateur l'a lue
et a répondu : « j'aimerais que meshcraft puisse faire les actions de base ;
adopter, supprimer, reconfigurer ».

**L'objection d'ADR-0015 ne couvre pas cette demande.** Elle visait *deux
autorités d'écriture sur un même équipement* — le symptôme n'étant pas une
erreur mais une configuration qui revient toute seule, des minutes plus tard,
sans trace. Cet argument vaut pour une écriture **directe vers la borne**, en
SSH ou en SNMP, dans le dos du contrôleur. Il ne vaut pas si meshcraft passe
par l'**API du contrôleur UniFi** : il est alors un **client** d'UniFi, au même
titre que l'interface web d'UniFi elle-même, et il n'y a toujours qu'un seul
maître.

Reste la question qu'ADR-0015 n'a pas eu à poser : **qui détient
l'intention ?** « Reconfigurer » suppose de savoir ce que la configuration
*devrait* être. Or ADR-0015 a posé que l'inventaire importé est un **miroir** :
meshcraft reflète, il ne décrit pas.

### Ce que le contrôleur offre réellement en écriture

Relevé sur le contrôleur de production, application `10.6.109`, avant de
décider. C'est le fait qui tranche, et il n'était pas devinable :

- **L'API d'intégration v1 n'a qu'une seule action**, et elle l'énumère
  elle-même quand on lui en demande une autre :
  `POST /sites/{id}/devices/{id}/actions` répond
  `Invalid $.action value (valid values: 'RESTART')`. Le seul geste sous
  contrat est donc le redémarrage — précisément celui que personne n'a
  demandé.
- **`PATCH` et `PUT` sur la ressource équipement répondent `405`.** L'API
  d'intégration n'offre **aucune** écriture de configuration : ni renommage,
  ni canal, ni rien.
- **`DELETE /sites/{id}/devices/{id}` répond `200` et sort la borne du
  site.** Ce verbe n'est documenté nulle part, n'apparaît dans aucune
  énumération, ne demande aucune confirmation et n'a **aucune forme inerte** :
  l'identifiant *est* la totalité de la charge utile, donc la requête qui
  servirait à vérifier que la route existe est déjà la requête qui détruit.
  Constaté en production sur une borne en service, qu'il a fallu ré-adopter et
  renommer à la main — sept minutes d'indisponibilité. Le fait est consigné
  ici parce qu'il est la raison d'être du point 5.
- **Adopter et reprovisionner n'existent que sur l'API héritée**,
  `POST /proxy/network/api/s/{ref}/cmd/devmgr`, celle qu'ADR-0015 avait déjà
  isolée pour les SSID en la déclarant « sous aucun contrat ». Elle accepte le
  même en-tête `X-API-KEY`, sans jeton anti-CSRF. Commandes reconnues, établies
  en les visant sur une MAC qui n'existe pas : `adopt`, `force-provision`,
  `restart`, `set-locate`, `unset-locate`, `upgrade`.
- **Cette API échoue en s'ouvrant.** `{"cmd":"une-commande-inventée"}` répond
  `{"meta":{"rc":"ok"}}`, et `delete-device` sur une MAC inexistante répond
  `rc:ok` aussi. Donc `rc:ok` **n'est pas une preuve** qu'un geste a eu lieu :
  une faute de frappe dans un nom de commande réussit silencieusement et ne
  fait rien.
- **Les droits du jeton se lisent.** `GET /proxy/network/api/self` renvoie la
  fiche de l'administrateur derrière la clé, avec `is_super`, `is_owner` et
  `super_site_permissions`. Un jeton en lecture seule est donc discernable
  **avant** de s'en servir, et non au pire moment.

## Décision

**meshcraft télécommande le contrôleur UniFi : il déclenche des gestes et ne
détient aucune intention. Le miroir reste un miroir.**

1. **Tout geste part vers l'API du contrôleur, jamais vers l'équipement.**
   C'est la règle structurante, et elle est ce qui rend la chose compatible
   avec ADR-0015 : meshcraft devient un client d'UniFi, pas un second maître.
   Une session ouverte sur une borne — SSH, SNMP, HTTP — est hors de cette
   décision et reste interdite.

2. **Aucune intention n'est stockée, et « reconfigurer » se réduit à
   `force-provision`.** C'est le cœur de l'affaire. meshcraft ne dit jamais ce
   que la configuration d'une borne *devrait* être ; il demande au contrôleur
   de **réaffirmer la sienne**. Le geste est donc exactement une télécommande :
   l'intention reste chez UniFi, qui en est l'autorité, et meshcraft n'a aucune
   colonne qui pourrait la contredire. Aucun champ de réglage importé ne
   devient modifiable.

3. **Être un client ne sauve que les gestes, pas l'intention.** C'est le point
   contre-intuitif, et c'est pour lui que cet ADR existe. Passer par l'API
   d'UniFi supprime la seconde *autorité d'écriture* ; ça ne supprimerait pas
   la seconde *autorité de décision*. Si meshcraft mémorisait « le canal doit
   être 36 » et le poussait, et que quelqu'un met 44 dans l'interface UniFi, il
   y aurait deux décideurs même avec un seul écrivain — et la configuration
   reviendrait toute seule, par meshcraft au lieu de par le contrôleur. Le
   symptôme qu'ADR-0015 refusait serait rigoureusement le même. La distinction
   client/maître n'est donc pas un laissez-passer vers la convergence : elle
   autorise des impératifs, pas un état désiré.

4. **Une seule porte d'écriture, et son argument est infalsifiable.** La
   garantie d'ADR-0015 — « ce paquet n'a aucune méthode d'écriture, un relecteur
   le vérifie en lisant la liste des méthodes » — est remplacée par une garantie
   de même nature, tenue par le type et non par un commentaire : `Client` n'a
   qu'une seule méthode qui écrit, et elle prend un `Gesture` dont le champ est
   non exporté et sans forme littérale. Aucun appelant hors du paquet ne peut
   en fabriquer un ; l'ensemble des écritures que meshcraft sait demander est
   donc **clos à la compilation**, et se compte en comptant les trois `var` qui
   les déclarent. Le `Gesture` nul est inutilisable, donc la porte refuse par
   défaut.

5. **Oublier se garde par le nom exact, relu chez UniFi.** C'est l'idiome
   d'[[ADR-0012 — La surface d'outils n'est pas la surface d'API]] et de
   `delete_object` : une valeur que l'appelant n'a pu obtenir qu'**en lisant
   l'objet qu'il va détruire**, comparée à ce que l'autorité détient à
   l'instant de l'appel. Et le nom est relu **chez UniFi, pas dans le miroir** :
   confirmer contre une copie qui peut avoir des heures est une porte qui
   s'ouvre sur un objet qui n'est plus celui qu'on a lu. Vu ce que `DELETE` fait
   sans prévenir, c'est la seule confirmation qui tienne.

6. **`rc:ok` n'est pas une preuve : chaque geste se vérifie par une
   relecture.** Puisque l'API héritée répond `ok` à une commande qu'elle ne
   connaît pas, un geste n'est réputé fait que lorsqu'une lecture le confirme —
   la borne est présente et adoptée après `adopt`, absente après l'oubli. Un
   geste non confirmé est rapporté comme non confirmé, jamais comme réussi.

7. **Les droits du jeton se lisent avant de s'exercer, et le produit le dit.**
   L'extension expose ce que le jeton permet réellement, lu sur `self`. Un
   jeton en lecture seule donne un écran qui explique qu'il ne peut pas
   écrire ; il ne donne pas trois boutons qui échoueront au moment où on en a
   besoin.

8. **Oublier passe par l'API d'intégration, adopter et reprovisionner par
   l'API héritée** — faute de mieux, et c'est écrit pour que personne ne croie
   que les trois gestes ont la même solidité. Le `DELETE` d'intégration répond
   par un vrai code de statut, là où `delete-device` échoue en s'ouvrant : à
   geste égal, on prend la route qui sait dire non.

## Conséquences

- **L'écran ne peut plus dire « meshcraft ne modifie rien ». Cette phrase
  devient fausse** et change dans le même commit, en+fr. Elle est remplacée par
  la distinction réelle, qui est plus difficile à formuler et doit l'être
  quand même : meshcraft déclenche des gestes chez le contrôleur, et ne décide
  d'aucun réglage.
- **Changer un réglage reste une visite dans l'interface UniFi.** C'est le prix
  principal et il est permanent. Quelqu'un arrivera en demandant pourquoi
  « reconfigurer » ne propose pas de choisir un canal ou un SSID : la réponse
  est le point 2, et la porte de sortie est celle d'ADR-0015 — qu'on désadopte
  la borne d'UniFi, et elle redevient un équipement ordinaire avec un
  connecteur.
- **Deux gestes sur trois reposent sur une API que UniFi ne promet pas.** Une
  mise à jour du contrôleur peut les casser sans préavis. Ils sont isolés
  derrière des fonctions nommées, comme les SSID le sont déjà, et leur échec
  est un échec de geste — jamais un échec de page.
- **La garantie « ce paquet n'écrit pas » est définitivement perdue**, et
  remplacée par « ce paquet écrit trois choses, nommées ici ». C'est un
  affaiblissement réel : on ne peut plus conclure à l'innocuité en lisant une
  liste de méthodes vide. La contrepartie est que l'ensemble reste énumérable
  et que son extension est un geste visible en revue.
- **Un geste rend le miroir périmé à l'instant où il réussit.** Chaque geste
  déclenche donc une réimportation, et l'écran doit supporter de voir une borne
  disparaître de la liste — c'est le cas normal après un oubli, pas une erreur
  de lecture.
- **On s'interdit d'ajouter un quatrième geste sans le dire.** `restart`,
  `upgrade` et `set-locate` sont reconnus par le contrôleur et délibérément
  absents : redémarrer est ce qu'ADR-0015 nommait, et personne ne l'a demandé.
- Le journal du site reçoit une entrée par geste, avec l'auteur. Un oubli est
  `warning` : c'est la seule des trois à ne pas se réparer sans une
  ré-adoption, et la ré-adoption ne rend ni le nom ni les réglages propres à la
  borne.

## Alternatives écartées

**meshcraft détient l'intention pour un sous-ensemble nommé de réglages, et
converge comme pour les autres équipements** (option 2 de la demande). C'est
l'option puissante, et celle qui rendrait l'inventaire UniFi homogène avec le
reste du produit. Écartée pour deux raisons, dont une est factuelle et close
le débat : l'API d'intégration **n'offre aucune écriture de configuration**
(`405` sur `PATCH` et `PUT`), donc il n'existe nulle part sous contrat où
pousser une intention — il faudrait bâtir la convergence sur l'API héritée,
celle qui répond `rc:ok` à une commande inventée. On aurait construit la
fonction la plus exigeante du produit sur son fondement le moins fiable. La
seconde raison est le point 3 : la double décision ramène exactement le
symptôme qu'ADR-0015 refusait, et il faudrait alors répondre à « qui gagne ? »,
ce qu'aucune des deux réponses possibles ne permet de faire honnêtement — « UniFi
gagne » rend l'intention décorative, « meshcraft gagne » fait de l'interface
UniFi un piège pour celui qui l'utilise.

**Un entre-deux explicite, champ par champ** (option 3). Séduisant parce qu'il
permettrait de commencer petit — le nom de la borne, par exemple, qui est
inoffensif. Écarté parce que la frontière ne se lirait nulle part : un écran où
deux champs sur quinze sont modifiables n'enseigne pas la règle, il enseigne
qu'il faut essayer pour savoir. Et le premier champ ajouté ouvre la discussion
sur le deuxième, sans qu'aucun critère ne dise où s'arrêter. Si un besoin
précis arrive — un seul réglage, nommé, avec sa raison — c'est un ADR à écrire
à ce moment-là, contre ce besoin-là.

**Écrire directement vers la borne, en SSH ou en SNMP**, puisque meshcraft sait
déjà parler à du matériel Ubiquiti et qu'un connecteur EdgeSwitch existe.
Écartée au point 1, et c'est le seul cas où l'objection d'ADR-0015 s'applique
telle quelle : la borne serait reconfigurée dans le dos de son contrôleur, qui
la re-provisionnerait à son rythme en effaçant le geste. Le symptôme ne serait
pas une erreur mais une dérive, diagnostiquée des semaines plus tard.

**Oublier avec `delete-device` sur l'API héritée**, par cohérence avec les deux
autres gestes qui n'ont pas le choix. Écartée au point 8 : cette commande
répond `rc:ok` sur une borne qui n'existe pas, donc elle ne sait pas distinguer
« oubliée » de « jamais vue », et c'est exactement la distinction qui compte
pour un geste irréversible.

**Garder la confirmation à une case à cocher ou à un second clic**, comme
`power_cycle_ports` le fait avec `confirm=true`. Écartée au point 5 : un
booléen est une valeur que l'appelant produit sans avoir rien lu, donc il ne
protège pas de l'erreur qui compte — se tromper d'objet. `power_cycle_ports`
peut s'en contenter parce que son geste se répare en attendant un redémarrage ;
un oubli ne se répare pas.

**Un booléen `can_write` posé à la configuration de l'extension**, déclaré par
l'opérateur au moment où il colle le jeton. Écartée : c'est une déclaration, pas
une mesure, et elle serait fausse dès que le jeton est remplacé par un autre.
Les droits se lisent sur `self`, donc ils se lisent — un invariant tenu par une
lecture vaut mieux qu'un invariant tenu par une case cochée il y a six mois.

**Un drapeau de confirmation global « j'autorise meshcraft à écrire sur
UniFi »**, armé une fois dans les réglages du site. Écartée parce qu'un
interrupteur armé une fois est armé pour toujours : il protège le premier geste
et aucun des suivants, alors que c'est le centième qui se fait distraitement.

---
Statut: Accepté
Date: 2026-10-25
---

# ADR-0029 — Le SSID s'édite, la borne ne s'édite pas, et seul ce qui se relit se propose

## Contexte

La demande est de **voir et modifier les réglages des bornes Wi-Fi et des
SSID** depuis meshcraft, via le contrôleur UniFi.

[[ADR-0025 — meshcraft télécommande le contrôleur UniFi, il ne détient aucune
intention]] §2 dit le contraire en une phrase : « Aucun champ de réglage
importé ne devient modifiable ». Et son alternative écartée n°2, « un
entre-deux explicite, champ par champ », refuse précisément ce qui est demandé
ici — en laissant toutefois une porte, nommée : « si un besoin précis arrive —
un seul réglage, nommé, avec sa raison — c'est un ADR à écrire à ce moment-là,
contre ce besoin-là ». Le présent document emprunte cette porte. Il ne rouvre
rien d'autre d'ADR-0025 : les huit points sur les gestes, la porte d'écriture
unique, la relecture obligatoire et l'absence d'intention stockée sont le socle
sur lequel il repose.

### Ce qui a été mesuré, et ce qui ne pouvait pas l'être

Relevé **en lecture seule** sur le contrôleur de production, application
`10.6.109`, un jour où quatre bornes AC Pro servaient des joueurs. Aucune
écriture n'a été émise : le matin même, une borne est sortie du réseau pendant
onze minutes parce qu'un relevé cherchait quels verbes HTTP l'API acceptait, et
ses réglages par borne ont été perdus. L'interdiction d'écrire est donc une
contrainte de ce relevé, pas une pudeur — et elle laisse des inconnues, listées
plus bas, qui sont le prix de ne pas avoir recommencé.

- **`GET /proxy/network/api/s/{ref}/rest/wlanconf` rend 67 champs par SSID**, et
  **`GET …/rest/wlanconf/{id}` rend le même objet à l'unité**. C'est le fait qui
  rend ce travail possible : un réglage de SSID posé peut être **relu
  individuellement** et comparé champ par champ.
- **`GET …/rest/wlanconf/{id-inexistant}` répond `rc:ok` avec `data: []`.**
  L'échec-vers-le-succès de l'API héritée n'épargne donc pas non plus les
  lectures. Une relecture qui ne vérifie pas que `data` est non vide confirme
  l'existence d'un objet qui n'existe pas.
- **`GET …/rest/device` et `GET …/rest/device/{id}` répondent `404
  api.err.NotFound`.** La ressource REST par équipement — celle par laquelle
  les contrôleurs UniFi historiques acceptaient un `PUT` de réglages — **n'existe
  pas sur cette application**. Il n'y a donc, sur ce contrôleur, aucune route
  connue vers un réglage par borne.
- **La configuration d'une borne n'est lisible que par des relevés** :
  `stat/device`, `stat/device/{mac}`, `v2/api/site/{ref}/device`. Ce sont des
  statistiques, pas des ressources, et surtout **elles omettent les champs
  restés à leur défaut** — `led_override` est simplement absent d'une borne qui
  n'y a jamais touché. Donc, même si une route d'écriture apparaissait, l'absence
  d'un champ à la relecture ne se distinguerait pas de « pas appliqué ».
- **`x_passphrase` revient en clair**, à la fois dans `rest/wlanconf` et dans
  `v2/api/site/{ref}/wlan/enriched-configuration`. La phrase de passe d'un
  réseau de production est donc lisible par quiconque détient le jeton.
- **Les choix d'un champ de référence sont lisibles** : `GET …/rest/networkconf`
  donne les réseaux (et leur VLAN) que `networkconf_id` désigne, `GET
  /proxy/network/v2/api/site/{ref}/apgroups` donne les groupes de bornes.
- **`PUT` sur `rest/wlanconf/{id}` n'a pas été vérifié**, et ne pouvait pas
  l'être : le vérifier est une écriture. Son existence est inférée de la forme
  de la ressource — collection et élément en `GET`, identifiant inconnu rendu
  comme un ensemble vide — qui est celle du gestionnaire REST générique d'UniFi.
  C'est une inférence, elle est écrite comme telle, et la conception ci-dessous
  est faite pour qu'elle soit fausse sans dommage.

## Décision

**meshcraft est un éditeur de SSID. Il n'édite pas les bornes. Un réglage qui
ne se relit pas ne se propose pas.**

1. **Éditeur, pas convergeur, et c'est ce qui le rend compatible avec
   ADR-0025 §3.** meshcraft affiche ce que le contrôleur détient, laisse
   modifier, pousse, relit — et **ne garde rien entre deux éditions**. Pas de
   colonne d'état désiré, pas de détection de dérive, pas de boucle de
   convergence. L'« intention » d'une édition vit le temps d'une requête HTTP et
   meurt avec elle. Il n'y a donc jamais deux décideurs : si quelqu'un change le
   même SSID dans l'interface UniFi, meshcraft n'a rien à réaffirmer, et le
   symptôme qu'ADR-0015 refusait — une configuration qui revient toute seule —
   ne peut pas se produire.

2. **La frontière est une classe d'objet, pas une liste de champs.** C'est la
   réponse à l'objection d'ADR-0025 contre l'« entre-deux champ par champ », et
   c'est le point qui décide. Ce qui était reproché à l'entre-deux, c'est que
   « deux champs sur quinze modifiables n'enseigne pas la règle, il enseigne
   qu'il faut essayer pour savoir ». Ici la règle tient en une phrase lisible à
   l'écran : **le SSID s'édite, la borne ne s'édite pas**. Elle n'est pas un
   arbitrage de goût — elle est la mesure : le SSID a une ressource REST qui se
   relit à l'unité, la borne n'en a aucune.

3. **Ce qui se relit est livrable ; ce qui ne se relit pas n'est pas proposé.**
   C'est le découpage, et il remplace « tous les réglages possibles » par « tous
   ceux qu'on peut poser **et** vérifier ». Un champ n'entre dans l'écran que si
   `GET …/rest/wlanconf/{id}` le rend, donc qu'une relecture peut le comparer à
   ce qui a été envoyé. Les champs écartés le sont nommément, avec leur raison,
   dans les Conséquences — pas par omission.

4. **`rc:ok` n'est jamais le juge : la relecture l'est.** ADR-0025 §6 posait la
   règle pour les gestes ; elle est ici le mécanisme entier. Une édition est
   « confirmée » quand, et seulement quand, une relecture de l'objet montre
   **`data` non vide** et chaque champ modifié égal à ce qui a été demandé.
   Sinon elle est rapportée comme acceptée-non-confirmée, exactement comme un
   geste.

5. **Lire-fusionner-écrire l'objet entier, jamais un corps partiel.** C'est
   l'arbitrage entre deux mauvaises options et le prix doit rester écrit. Le
   gestionnaire REST hérité d'UniFi a historiquement voulu l'objet complet, et un
   corps partiel y a effacé des champs. Effacer un champ qu'on ne voulait pas
   toucher est précisément le sinistre que ce dossier a déjà subi — les réglages
   par borne perdus un matin — et précisément le danger de la phrase de passe.
   Donc : on relit l'objet complet, on y applique les seules clés changées, on
   repousse le tout. Tout ce qu'on n'a pas touché repart **bit pour bit** tel que
   le contrôleur l'a donné. Le prix : un champ que le contrôleur calcule et
   refuse en entrée ferait échouer **toutes** les éditions. Cet échec-là est
   bruyant et immédiat, donc acceptable ; l'inverse — un `PUT` partiel qui passe
   et blanchit trois champs — est silencieux et se découvre à la LAN suivante.

6. **La garde est une précondition sur les valeurs remplacées, pas un nom
   tapé.** Le client envoie, pour chaque champ qu'il change, **ce qu'il a vu**
   (`was`) et **ce qu'il veut** (`want`). Le serveur relit chez UniFi, à
   l'instant de l'écriture, et **refuse tout l'ensemble** si un seul des champs
   visés ne porte plus la valeur vue. Rien n'est envoyé dans ce cas. C'est
   l'idiome d'ADR-0025 §5 déplacé du nom exact vers les valeurs remplacées, et
   c'est strictement plus fort pour une édition : le nom tapé ne protège que
   contre l'erreur d'objet, la précondition protège aussi contre l'éditeur
   concurrent — celui qui a changé ce même SSID dans l'interface UniFi pendant
   qu'on remplissait le formulaire. Elle ne coûte aucun frottement : l'écran a
   déjà lu les valeurs, il n'a pas à les faire retaper.

7. **La phrase de passe s'écrit, ne se lit pas, et ne s'efface pas par
   distraction.** Trois propriétés la décident, et aucune ne peut être omise :
   - le contrôleur la rend **en clair**, donc elle est *relisible*, donc une
     écriture de phrase de passe est **confirmable** ;
   - [[ADR-0015 — L'extension de site vit dans le contrôleur, et son inventaire
     est un miroir en lecture seule]] interdit de l'importer, donc elle n'est
     **jamais stockée** et **jamais envoyée au navigateur** ;
   - la fusion du point 5 la renvoie telle quelle, donc une édition qui ne la
     touche pas **ne peut pas la blanchir**.

   Le champ est donc **en écriture seule** : vide signifie « inchangée », et
   l'écran le dit **en mots**, jamais par des points de suspension qui
   ressembleraient à une valeur présente. Non vide signifie « remplacer ». La
   confirmation compare la relecture à ce qui a été envoyé **côté serveur**, et
   ne rapporte que « concordant » ou « non concordant » : la valeur ne retraverse
   jamais vers le navigateur et n'entre dans aucune ligne de journal. **Il n'y a
   pas de bouton « révéler la phrase de passe actuelle »** — ce serait un import
   sous un autre nom, par la seule surface du produit qui n'a pas de coffre.

8. **La porte d'écriture reste celle d'ADR-0025 §4, élargie d'un geste et pas
   d'un millimètre de plus.** `Client.Do` demeure la seule méthode écrivante, son
   argument demeure un `Gesture` inconstructible hors du paquet, et l'ensemble
   des écritures demeure **clos à la compilation** — il se compte maintenant en
   comptant **quatre** `var`. Le test réflexif qui énumère la surface exportée
   est mis à jour dans le même diff, donc une cinquième écriture reste un acte
   visible en revue. L'édition n'a **pas** sa propre route vers le contrôleur :
   elle passe par la même porte, avec la même relecture obligatoire.

9. **Le mode de sécurité n'est pas proposé, et ce n'est pas un oubli.** Il est
   pourtant relisible, donc le point 3 ne l'exclut pas. Il est exclu parce que
   la combinaison cohérente de clés que WPA3 exige — `security`, `wpa_mode`,
   `wpa_enc`, `wpa3_support`, `wpa3_transition`, `pmf_mode`, `sae_psk` — ne peut
   pas être **énumérée** en lisant un contrôleur qui n'en utilise qu'une seule
   (`wpapsk` / `wpa2` / `ccmp`). Poser une combinaison devinée sur un SSID en
   service est exactement le geste qui met un lieu hors de l'air. Le besoin réel
   derrière « changer la sécurité » — faire tourner la phrase de passe — est
   servi par le point 7, à mode de sécurité constant.

10. **Le bandeau d'autorité disparaît ; les deux faits utiles qu'il portait
    descendent là où ils servent.** Le bandeau « Le contrôleur est l'autorité de
    configuration » expliquait une architecture à quelqu'un qui vient
    administrer son Wi-Fi, et sa deuxième phrase — « meshcraft n'en stocke rien
    et n'en modifie rien » — devient **fausse** au point 1 : meshcraft modifiera
    bel et bien, à travers le contrôleur. Il est retiré, clés i18n comprises,
    dans toutes les locales où elles existent. Mais il portait deux faits qui
    comptent encore pour l'opérateur, et chacun **survit à l'endroit où la
    question se pose** plutôt qu'en tête de page :
    - **meshcraft ne détient rien, donc ne surveille rien.** Un réglage changé
      ailleurs ne « dérive » pas ici : il est ce qu'il est au dernier relevé. Ce
      fait se dit **dans le formulaire d'édition**, au moment où quelqu'un
      s'apprête à poser une valeur — c'est là, et pas en haut d'une liste, qu'on
      pourrait croire que meshcraft retient ce qu'on vient de taper.
    - **la fraîcheur du miroir.** Ce qui est listé date du dernier import. Ce
      fait se dit **sur la carte des SSID**, à côté de la liste qu'il qualifie.
      Il devient plus nécessaire avec l'édition, pas moins — et il a une
      contrepartie exacte : le **formulaire**, lui, ne lit pas le miroir. Il lit
      le contrôleur à l'ouverture (point 6 l'exige, sinon la précondition
      porterait sur une vérité vieille d'une heure). Donc la liste est datée et
      le formulaire est frais, et c'est cette asymétrie que l'écran doit rendre
      lisible au lieu d'une phrase générale sur l'autorité.

    La règle générale derrière ce retrait : **une page d'administration n'est pas
    l'endroit où on enseigne le modèle de données.** Un fait sur l'autorité ne
    mérite l'écran que là où il change le geste suivant de celui qui le lit.

11. **L'import devient une boucle de fond à 30 s, et l'absence de chevauchement
    est structurelle, pas gardée.** Le bouton « Importer maintenant » de l'écran
    Wi-Fi disparaît : rafraîchir une liste n'est pas une décision d'opérateur.
    La cadence demandée est de 30 s et le plafond d'une passe complète est de
    90 s, donc trois passes pourraient se chevaucher — et c'est le jour où le
    contrôleur est lent, c'est-à-dire le jour où ça compte, que ça se verrait.
    La garde n'est **pas** un verrou :
    - **une seule goroutine**, qui parcourt *toutes* les extensions appariées de
      *tous* les sites **séquentiellement**. Il n'y a donc pas de second coureur
      à exclure. Une boucle par extension aurait demandé un verrou par
      extension, et surtout aurait fait partir N requêtes au même instant vers N
      contrôleurs au premier démarrage ;
    - **le délai court depuis la FIN de la passe précédente.** Donc une
      `sleep` après la passe, jamais un `Ticker` : un ticker se déclenche sur
      l'horloge murale et ses tops s'accumulent derrière une passe lente, ce qui
      est exactement le défaut à éviter. Le contrôleur voit au plus une passe à
      la fois, quelle que soit sa lenteur.

    Reste un lecteur concurrent, nommé plutôt que caché : la réimportation qui
    suit un geste (ADR-0025, Conséquences). Elle est déclenchée par un humain,
    donc bornée par lui, et son écriture du miroir est atomique — au pire une
    passe perd contre l'autre et la suivante corrige trente secondes plus tard.

12. **Un contrôleur qui ne répond pas n'est pas martelé, et le produit le
    dit.** Recul progressif par extension — une minute, doublée à chaque échec,
    plafonnée — et l'extension en recul est simplement **sautée** par la passe.
    Deux conséquences voulues :
    - **corriger l'extension annule le recul.** C'est ce qui rend le bouton
      inutile plutôt que regretté : après avoir collé un jeton neuf, la passe
      suivante est à moins de trente secondes, pas à un quart d'heure. Un geste
      manuel « au cas où » à côté d'une boucle serait deux mécaniques pour une
      question.
    - **le bouton de la page de configuration des extensions reste**, et ce
      n'est pas une incohérence : il répond à « mon jeton est-il bon ? » au
      moment où on le tape, ce qu'une boucle ne peut pas faire *là, tout de
      suite*, à côté du champ. Une mécanique par question : la boucle entretient
      une liste, ce bouton valide une saisie.

13. **La boucle se tait, et c'est la règle la plus importante de ce point.**
    Une passe toutes les 30 s qui écrirait une ligne de journal ferait
    2 880 lignes par jour et par site, et noierait le journal que des humains
    lisent. Donc le journal ne reçoit que des **transitions** :
    - `extension.imported` **seulement si la passe a changé quelque chose** (des
      lignes retirées, ou des effectifs qui ont bougé depuis la dernière passe
      réussie). Une passe qui confirme l'état n'écrit rien ;
    - `extension.unreachable` (avertissement) **une fois**, au passage de
      « ça marchait » à « ça ne marche plus », en disant le recul ;
    - `extension.recovered` une fois, au retour ;
    - `extension.orphaned` comme aujourd'hui : c'est déjà une transition.

    **Et le journal est l'entrée de l'alerting** ([[ADR-0024 — Une alerte se
    mérite : le journal décide, la description et l'interco qualifient]]), donc
    il fallait vérifier qu'un produit ne s'alarme pas de ce qu'il vient de lire.
    Vérifié : `alertableTypes` est une **liste blanche exhaustive de six types**,
    et aucun `extension.*` n'y figure. L'alerting est donc structurellement
    sourd à la synchronisation, par la conception d'ADR-0024 et non par chance.
    Un test l'affirme désormais, pour qu'ajouter un `extension.*` à cette liste
    soit un acte qui doit répondre à la question au lieu de la découvrir.

14. **La fraîcheur, devenue implicite, doit devenir visible.** Avec un bouton,
    l'opérateur savait quand il avait importé ; avec une boucle il ne sait plus
    rien, sauf si l'écran le dit. C'est le même besoin que le point 10 et il
    s'aggrave ici. L'écran affiche donc **l'heure du dernier relevé réussi**
    (`last_sync_at`, à l'horloge de celui qui regarde — [[ADR-0008 — Le
    référentiel de temps est l'horloge de celui qui regarde]]) et, quand il y a
    lieu, **le fait qu'un échec dure** : un relevé qui ne passe plus n'est pas un
    écran vide, c'est un écran daté avec une raison à côté.

## Conséquences

- **L'écran Wi-Fi cesse de dire qu'il n'y a de réglage nulle part.** La phrase
  d'ADR-0025 — « il n'y a pas de réglage sur cet écran parce qu'il n'y a de
  réglage nulle part » — devient fausse et change dans le même commit, en+fr.
  La distinction qui la remplace est celle du point 2, et elle doit être dite en
  toutes lettres : les SSID de ce site s'éditent ici, les bornes non, et la
  raison est que le contrôleur n'offre pas de ressource par borne.
- **Le bandeau d'autorité est supprimé, et avec lui deux clés i18n dans les
  onze locales.** Le point 10 dit où vont les deux faits qu'il portait. Ce qui
  est perdu pour de bon, et assumé : l'explication en tête de page de *pourquoi*
  le contrôleur décide. Quelqu'un arrivera en se demandant pourquoi les bornes
  ne s'éditent pas — la réponse est à côté de la liste des bornes, en une
  phrase, et son développement est ici.
- **Les réglages de SSID construits** sont ceux que la relecture sait vérifier :
  diffusion active, nom, SSID masqué, bandes, réseau (donc VLAN), réseau
  invité, isolation des clients, itinérance rapide, transition BSS, économie
  d'énergie UAPSD, optimisation du multicast, proxy ARP — et la phrase de passe
  sous le régime du point 7.
- **Les réglages écartés, et pourquoi** :
  - *tous les réglages par borne* (nom, canal, largeur, puissance, LED) :
    `rest/device` répond 404, aucune route d'écriture atteignable, et les relevés
    omettent les défauts donc l'absence ne vaut pas preuve ;
  - *le mode de sécurité et la famille WPA3* : point 9 ;
  - *`mac_filter_*`, `private_preshared_keys`, `schedule*`, `radius_*`,
    `hotspot2conf`* : des listes et des objets structurés. La comparaison de
    relecture y est définissable, mais leurs invariants ne se lisent pas sur un
    contrôleur qui ne s'en sert pas — et les clés pré-partagées privées portent
    des phrases de passe, que ADR-0015 interdit d'importer ;
  - *`minrate_*`, `dtim_*`, `group_rekey`, `pmf_mode`* : relisibles, donc
    recevables au titre du point 3, mais leurs plages sûres dépendent du matériel
    et personne ne les a demandés. Ce sont les prochains candidats naturels, et
    les ajouter ne demandera pas d'ADR : le point 3 les couvre déjà ;
  - *créer et supprimer un SSID* : `POST` et `DELETE` sur `wlanconf` ne sont pas
    vérifiés, et une suppression n'a pas de forme inerte — c'est la leçon
    d'ADR-0025 sur le `DELETE` d'intégration, et elle vaut ici sans l'avoir
    payée deux fois.
- **Ce qui part non validé sur matériel, et c'est assumé.** `PUT` sur
  `rest/wlanconf/{id}` n'a jamais été émis vers un contrôleur. Les deux issues
  d'une inférence fausse sont tenues par la conception : un verbe refusé devient
  un refus bruyant (`405` → geste refusé), un verbe accepté sans effet devient
  une édition **non confirmée**. Dans les deux cas rien n'est annoncé comme posé
  qui ne le soit. Ce qui reste inconnu, c'est si le contrôleur **accepte l'objet
  entier** en entrée : s'il refuse un champ qu'il calcule, toute édition échoue
  au premier essai, visiblement, et la liste des champs à retirer du corps se lira
  dans son message.
- **Un `PUT` périme le miroir à l'instant où il réussit**, comme un geste. Il
  déclenche donc la même réimportation, et l'écran doit supporter de voir un SSID
  changer de nom sous lui.
- **La phrase de passe d'un réseau de production est lisible par quiconque
  détient le jeton**, et ce fait ne dépend pas de nous : il dépend d'UniFi. Le
  noter ici est la seule chose que ce document puisse en faire, et c'est un
  argument pour qu'un jeton d'extension soit traité comme un secret de premier
  rang — ce que `site_extensions.token_enc` et `SealedColumns()` font déjà.
- **On s'interdit d'ajouter un réglage par borne sans un nouvel ADR**, même le
  jour où une mise à jour d'UniFi fait réapparaître `rest/device`. Le point 2
  serait alors rouvert, et la frontière « le SSID s'édite, la borne non » cesse
  d'être une phrase enseignable : c'est exactement le genre de décision qui doit
  se reprendre explicitement plutôt que se laisser éroder par la bonne nouvelle
  d'une route qui remarche.

## Alternatives écartées

**Tenir une intention en base pour les réglages de SSID, et converger** —
l'option que « réglages » suggère naturellement, et celle qui rendrait
l'inventaire UniFi homogène avec le reste du produit. Écartée pour la raison du
point 1, qui est celle d'ADR-0025 §3 et n'a pas bougé : le problème n'est pas
qui écrit, c'est qui décide. Deux décideurs sur un même SSID obligent à
répondre « qui gagne ? », et les deux réponses sont mauvaises — « UniFi gagne »
rend l'intention décorative, « meshcraft gagne » fait de l'interface UniFi un
piège pour celui qui l'utilise. Et la convergence serait bâtie sur l'API qui
répond `rc:ok` à une commande inventée : la fonction la plus exigeante du
produit sur son fondement le moins fiable.

**Éditer les bornes quand même, en acceptant de ne pas pouvoir confirmer.**
C'est la lecture littérale de la demande, et elle a un défenseur honnête : mieux
vaut un canal qu'on pose peut-être que pas de canal du tout. Écartée parce que
sur cette API, « peut-être » est indiscernable de « non » : `rc:ok` ne prouve
rien, les relevés omettent les défauts, et aucune route d'écriture par borne
n'est même atteignable. On livrerait un écran dont chaque enregistrement
afficherait « accepté, non confirmé » — ce qui n'enseigne pas la prudence mais
apprend à ignorer l'avertissement, et prépare la fois où il comptait.

**Un `PUT` partiel, limité aux clés changées.** Plus simple, plus petit sur le
réseau, et ce qu'on écrirait spontanément. Écarté au point 5 : si le
gestionnaire hérité interprète un corps partiel comme l'objet entier, les
soixante champs absents sont remis à leur défaut, phrase de passe comprise. Le
coût de se tromper n'est pas symétrique, et le relevé ne pouvait pas trancher
puisque trancher était une écriture.

**Afficher la phrase de passe actuelle, puisque le contrôleur la donne en
clair.** Séduisant : ce serait utile, et l'argument « elle est déjà lisible avec
le jeton » n'est pas faux. Écarté au point 7. ADR-0015 n'interdit pas seulement
de stocker la phrase de passe, il interdit de l'importer ; la faire traverser
vers un navigateur, c'est la poser dans un cache HTTP, un historique d'outils de
développement et une capture d'écran de support, c'est-à-dire dans trois
endroits qui n'ont pas de coffre. Et le besoin derrière la demande est de **la
changer**, pas de la lire.

**Un champ de mot de passe prérempli d'une valeur masquée**, comme le font
beaucoup de formulaires. Écarté, et c'est le piège nommé dans la demande : un
champ qui affiche des points ressemble à une valeur présente, donc un
enregistrement distrait le renvoie — ou renvoie les points eux-mêmes. Vide plus
une phrase qui dit « laisser vide pour ne pas changer » est moins élégant et
sans ambiguïté.

**Garder la garde d'ADR-0025 §5 telle quelle : faire retaper le nom du SSID
avant chaque enregistrement.** Cohérent avec l'oubli d'une borne, et déjà
implémenté. Écarté au point 6 : une édition est réversible là où un oubli ne
l'est pas, donc le frottement n'est pas au même prix ; et surtout le nom tapé ne
voit pas l'éditeur concurrent, alors que la précondition sur les valeurs
remplacées le voit. Le frottement aurait coûté plus et protégé moins.

**Un verrou « une passe à la fois » par extension, avec un `Ticker` à 30 s.**
La forme qu'on écrit spontanément, et elle marche. Écartée au point 11 parce
qu'elle répond à la mauvaise question : avec un ticker, les tops continuent
d'arriver pendant une passe lente et le verrou les fait *tomber* — donc le
comportement sous charge dépend d'un détail de rejet silencieux. Une seule
goroutine qui dort après avoir fini n'a pas de tops à rejeter, et son
comportement sous charge est le même qu'à vide : plus lent, et c'est tout.

**Une boucle par extension**, ce qui permettrait des cadences différentes par
contrôleur. Écartée au point 11 : au démarrage, N boucles partent au même
instant vers N contrôleurs, et le coût qui compte n'est pas celui de meshcraft
mais celui du contrôleur distant et de son jeton. Le jour où une extension
mérite sa propre cadence, c'est une cadence par extension dans la passe unique,
pas une goroutine de plus.

**Garder le bouton « Importer maintenant » à côté de la boucle**, « au cas où ».
Écartée au point 12 : deux mécaniques pour une question, et la seule raison
légitime de forcer une passe — un jeton qu'on vient de réparer alors que le
recul est à son plafond — est supprimée à sa source en annulant le recul quand
l'extension est modifiée. Un bouton dont la raison d'être a disparu finit par
être cliqué pour autre chose.

**Émettre `extension.imported` à chaque passe et laisser le journal filtrer.**
Écartée au point 13 : le journal n'est pas qu'un fichier qu'on grep, c'est la
source de l'alerting (ADR-0024) et la page que quelqu'un ouvre pour comprendre
un incident. 2 880 lignes par jour et par site n'y rendent pas la
synchronisation plus traçable, elles rendent tout le reste illisible — et la
traçabilité qu'on croit gagner est celle d'un événement qui, par construction,
ne dit rien.

**Une deuxième méthode écrivante sur `Client`, à côté de `Do`**, puisque éditer
un SSID ne ressemble pas à un geste sur une borne. Écarté au point 8 : la
garantie d'ADR-0025 §4 est que l'ensemble des écritures se compte en lisant une
liste. Une deuxième porte, même bien commentée, la remplace par « il faut
chercher », et c'est exactement ce qu'ADR-0025 a refusé de laisser à un
commentaire.

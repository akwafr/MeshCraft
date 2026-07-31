---
tags: [etude, risques]
---

# Points durs et risques

Par gravité × probabilité, avec la parade retenue.

## 1. La fiabilité des connecteurs est LE produit
Parser du CLI et des UI web hétérogènes est un travail sans fin ; un parse faux peut produire un diff faux, donc un apply faux. **Parades** : tests par transcripts réels rejoués en CI ([[Connecteurs]]) ; le diff est montré à l'humain avant apply ([[Réconciliation]]) ; mode « lecture seule » par connecteur tant qu'il n'est pas certifié écriture ; transcripts complets conservés pour diagnostic.

## 2. Se couper la main (lockout)
Changer VLAN mgmt/IP/uplink à distance peut isoler l'équipement — et parfois tout le site si c'est le switch cœur. **Parades** : hiérarchie commit-confirm → archive-rollback → rollback applicatif + watchdog de joignabilité ([[Réconciliation]]) ; flag `disruptive` avec confirmation renforcée ; jamais de rejeu automatique d'un apply interrompu ; le proxy sur site survit à la coupure et peut restaurer.

## 3. Les équipements web-only (webform)
Firmwares qui changent le HTML d'une version à l'autre, chiffrement JS maison, mono-session admin. **Parades** : cibler par *famille* d'UI plutôt que par modèle ; capabilities dégradées honnêtes (le driver déclare le sous-ensemble qu'il sait faire) ; [[Accès web pass-through]] comme échappatoire permanente pour l'utilisateur ; prioriser les familles à endpoints JSON stables ([[État de l'art]]).

## 4. Volumétrie monitoring
500 switchs × 48 ports × polling 60 s = ~400k points/min en pic naïf. **Parades** : getbulk agrégé côté [[Proxy]] (une remontée par cycle, pas une par OID) ; deltas seulement (un port qui ne change pas ne produit pas d'événement) ; partitions + agrégats ([[Schéma base de données]]) ; interface metrics prête pour bascule VictoriaMetrics.

## 5. Sécurité de la chaîne contrôleur→proxy
Le contrôleur peut dialer dans les réseaux clients : cible de choix. **Parades** : [[Sécurité du proxy]] (enrollment, révocation, allowlist CIDR locale, quotas d'ingestion), [[Credentials]] (enveloppe, secrets jamais relisibles), audit systématique ([[RBAC]]).

## 6. Telnet et SNMP v2c
Protocoles en clair, parfois inévitables sur le vieux parc. **Parades** : v3/SSH préférés automatiquement quand la capability existe ; warning permanent et visible dans l'UI sur tout device en telnet/v2c ; le trafic en clair reste confiné au LAN local du proxy (jamais sur le WAN, le tunnel WSS chiffre tout).

## 7. Hétérogénéité LLDP
Champs remplis inégalement (chassis-id MAC vs nom, port-id ifName vs ifIndex). **Parade** : corrélation multi-critères (MAC, sysname, mgmt-ip) avec niveau de confiance affiché ([[Weathermap]] : plein vs pointillé) — accepter l'incertitude plutôt que d'afficher du faux.

## 8. Tentation d'architecture distribuée trop tôt
Microservices, bus de messages, multi-région… **Parade** : monolithe Go + Postgres jusqu'à preuve du contraire ; les deux vraies frontières sont déjà posées (interface `ProxyHandle`, interface `Driver`) et suffisent pour scaler plus tard ([[Contrôleur]]).

## 9. Périmètre produit
« Tout ce qu'on peut configurer sur un switch » est infini (ACL L3, routage, QoS, stacking…). **Parade** : le [[Modèle de données standard]] versionné borne le périmètre ; la règle « absent = non géré » permet la cohabitation avec la conf existante ; roadmap par familles de features ([[Roadmap]]), pas par constructeur.

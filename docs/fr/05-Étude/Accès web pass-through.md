---
tags: [etude, proxy]
---

# Accès web pass-through — piloter les équipements « web only »

Deux besoins distincts pour les équipements sans SSH :

## 1. Configuration automatisée (le connecteur webform)

Le [[Connecteurs|driver]] `transport/webform` pilote l'UI web de l'équipement **par HTTP directement** (pas par navigateur) : reproduire les requêtes que fait le navigateur (login → cookie/token → POST des formulaires / endpoints JSON internes).

- Méthode d'ingénierie : capturer les échanges de l'UI d'origine (devtools/HAR) une fois par plateforme, puis coder ces séquences dans le driver. Beaucoup de « smart switches » (D-Link DGS-1210, TP-Link Easy Smart, Netgear GS3xx, Zyxel GS1900) ont en réalité des endpoints CGI/JSON assez stables par famille.
- Les pièges connus, à encapsuler dans la brique webform : mono-session admin (le driver doit se déconnecter proprement), tokens CSRF, chiffrements maison du mot de passe en JS (à réimplémenter en Go), et « sauvegarder » ≠ « appliquer » (deux étapes sur beaucoup de modèles).
- Dernier recours si une UI est inautomatisable en HTTP pur (login canvas, JS obfusqué) : headless browser **côté contrôleur** (chromedp) — à éviter, coûteux et fragile ([[Points durs et risques]]). Cas spécial hors HTTP : TP-Link Easy Smart ne se configure que par un protocole UDP propriétaire → RPC dédié côté proxy, prévu par le [[Protocole contrôleur-proxy]].

## 2. Accès manuel à l'UI de l'équipement (le vrai pass-through)

L'utilisateur veut parfois ouvrir la page web du switch lui-même (feature non couverte, diagnostic) alors que l'équipement n'est joignable que par le [[Proxy]].

**Mécanisme** : reverse proxy HTTP par session, au travers du tunnel.

```
navigateur ── https://ctrl.example/pt/{session}/... ── contrôleur
                                      │ dial TCP via tunnel (ProxyHandle.DialTCP)
                                      ▼
                             équipement 10.1.2.3:80
```

- L'utilisateur clique « Ouvrir l'interface web » sur la fiche équipement → le contrôleur crée une **session pass-through** (TTL 15 min glissant, droit dédié [[RBAC]], événement d'audit dans le [[Journal d'événements]]).
- Le contrôleur fait reverse proxy : réécrit `Host`, strippe le préfixe `/pt/{session}`, réécrit les `Location` et cookies (`Path`). HTTPS interne à cert auto-signé : accepté par le contrôleur (fingerprint épinglé au premier accès), l'utilisateur, lui, reste sur le TLS propre du contrôleur.
- **Limites assumées** : les UI aux chemins absolus codés en dur ou aux WebSocket internes peuvent casser en préfixe de chemin. Plan B intégré : servir la session sur un **sous-domaine dédié** (`pt-{session}.ctrl.example`, wildcard DNS+cert) — zéro réécriture de chemin, compatibilité maximale. Le préfixe est le défaut (aucun prérequis DNS), le sous-domaine est l'option propre.
- Sécurité : jamais d'authentification automatique à l'équipement dans ce mode (meshcraft n'injecte pas le mot de passe admin dans une session utilisateur) ; c'est un accès *transporté*, pas délégué. Fermeture de session = fermeture des connexions tunnelisées.

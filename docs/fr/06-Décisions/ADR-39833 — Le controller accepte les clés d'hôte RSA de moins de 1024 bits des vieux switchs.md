---
Statut: Accepté
Date: 2026-11-06
---

# ADR-39833 — Le controller accepte les clés d'hôte RSA de moins de 1024 bits des vieux switchs

## Contexte

Un ProCurve 2650 (J4899A, H.10.x) du banc LES répond en SSH avec une **clé
d'hôte RSA de 896 bits**. Depuis Go 1.24, `crypto/rsa` refuse toute clé de moins
de 1024 bits, et le refus a lieu pendant l'échange de clés — avant même la
présentation d'un identifiant. La découverte le rapportait tel quel :

    crypto/rsa: 896-bit keys are insecure

Le switch ne pouvait donc être ni identifié, ni adopté, ni lu, ni joint depuis
la console, alors que telnet et l'interface web étaient ouverts et que les
listes d'algorithmes SSH de tous les connecteurs portent déjà
`diffie-hellman-group1-sha1`, `3des-cbc` et `ssh-dss` pour ce millésime.

## Décision

Le binaire controller est compilé avec `//go:debug rsa1024min=0`
(`cmd/controller/main.go`). Rien d'autre ne change : le proxy, le serveur MCP et
l'outillage gardent le comportement par défaut de Go.

## Conséquences

- Le SSH vers les switchs dont la clé d'hôte fait moins de 1024 bits remarche :
  découverte, connecteurs et console.
- Le réglage vaut pour tout le processus controller : il assouplit aussi la
  vérification RSA ailleurs dans ce processus (TLS vers l'extérieur compris).
  meshcraft n'accepte ni n'émet de telles clés lui-même ; l'exposition est un
  pair qui en présente une, soit exactement le cas ancien qu'on rétablit.
- Le repli sur telnet a été écarté : il ferait passer le mot de passe
  d'administration en clair sur le VLAN de management, ce qui est pire qu'une
  clé d'hôte faible sur ce même VLAN.
- Quand le dernier switch concerné sortira du parc, retirer la directive tient
  en une ligne.

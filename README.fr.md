<!-- i18n-source: README.md -->
<!-- i18n-span: intro sha256:0b3afd4c9bcca86fbeec4c2152c985e53748fc1a38706288b97fb0634a82921d -->
<!-- i18n-span: non-goals sha256:e100e9decc99337fb657e9e70709a723716108a104f3b03018a23724c597071d -->

# Probavi

[English](README.md) · [Magyar](README.hu.md) · [Deutsch](README.de.md) · **Français** · [Español](README.es.md)

> **English is authoritative.** Ceci est une traduction de l'introduction de [README.md](README.md), à jour au 2026-08-04. En cas de divergence, le texte anglais fait foi : l'installation, les exemples et l'inventaire des capacités ne sont à jour qu'en anglais.

*Probavi* — du latin **« j'ai prouvé ».** Le parfait est tout l'enjeu : non pas « nous testons les restaurations », mais « cette restauration a été effectuée et prouvée, voici l'enregistrement signé ».

**Vous avez des sauvegardes. Mais quand avez-vous prouvé pour la dernière fois qu'elles se restaurent ?**

Probavi est une plateforme auto-hébergée et indépendante des moteurs, dédiée à la **vérification continue des restaurations**. Elle ne réalise pas les sauvegardes — vos outils existants (pg_dump, pgBackRest, wal-g, mysqldump, …) le font déjà très bien. Le rôle de Probavi est de *prouver* en continu que ces sauvegardes sont réellement restaurables.

1. Selon un calendrier, elle prend une véritable sauvegarde et effectue une **véritable restauration** dans une sandbox jetable et isolée (par ex. un conteneur Docker).
2. Elle exécute des **vérifications** sur la base restaurée — de « a-t-elle démarré ? » aux comptages de lignes et à la fraîcheur des données, jusqu'aux assertions SQL personnalisées.
3. Elle consigne le résultat dans un **enregistrement de preuve signé, où toute altération ultérieure devient visible** : ce qui a été restauré, quand, en combien de temps, ce qui a été vérifié et quel en a été le résultat.

Le résultat n'est pas une coche verte. C'est un historique auditable et cryptographiquement vérifiable de la restaurabilité de votre organisation — y compris les temps de restauration mesurés (RTO) et leur évolution.

## Pourquoi

- La ligne de log « backup completed successfully » ne prouve presque rien. Les sauvegardes échouent en silence : corruption, segments WAL manquants, incompatibilités de version, clés de chiffrement perdues, mauvaises bases sauvegardées pendant des mois.
- La réglementation exige de plus en plus une capacité de reprise *testée et documentée*, et pas seulement des sauvegardes (voir DORA, NIS2 et les recommandations du NIST en matière de plan de continuité).
- Les fournisseurs cloud proposent des tests de restauration pour leurs propres services managés. Si vous exploitez des bases sur vos propres VM, sur du bare metal ou dans un parc hétérogène, aucun outil neutre et ouvert ne le fait pour vous. Probavi est cet outil.

## Non-objectifs

Probavi ne réalisera **pas** de sauvegardes, n'implémentera **pas** son propre planificateur, ne gérera **pas** d'identifiants de base de données au-delà de ce dont un exercice a besoin, et ne tentera **pas** d'être une plateforme de supervision. Un noyau réduit, un objectif précis.

## La CLI parle aussi français

`PROBAVI_LANG=fr probavi run --config drill.yaml` — l'aide et les diagnostics s'affichent en français. Les sorties machine ne changent jamais de langue : les enregistrements de preuve, les résumés JSON, le protocole d'adaptateur et les logs sont des contrats et restent en anglais partout ([docs/i18n.md](docs/i18n.md)).

## Pour aller plus loin (en anglais)

- [README.md](README.md) — état, installation, démarrage rapide, providers de sandbox, planification, notifications, game-day PRA
- [docs/](docs/) — spécifications normatives : protocole d'adaptateur, schéma de preuve, i18n, notifications
- [docs/capabilities.json](docs/capabilities.json) — inventaire généré et lisible par machine de ce que Probavi sait faire aujourd'hui
- [ROADMAP.md](ROADMAP.md) · [CHANGELOG.md](CHANGELOG.md) · [AGENTS.md](AGENTS.md) · [LICENSE](LICENSE) (Apache-2.0)

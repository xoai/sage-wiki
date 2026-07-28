[English](README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | **Français** | [Русский](README_ru.md)

<!-- translations: may-lag -->
> ⚠️ Cette traduction peut être en retard sur README.md — la version anglaise fait foi.

# sage-wiki

**sage-wiki** est une mémoire graphe et une base de connaissances que les agents IA et les humains construisent et interrogent ensemble. Déposez des documents ; un compilateur LLM les transforme en un wiki interconnecté doté d'un graphe de connaissances — les agents l'interrogent via MCP, les humains le parcourent en markdown brut. Activez les passes de graphe opt-in et il devient un graphe *avec preuves* : entités typées, relations porteuses de provenance, alias résolus et citations par fait dans les réponses. Un seul binaire Go le fait passer du vault personnel au hub d'équipe, jusqu'au graphe de connaissances d'entreprise.

**→ Pour commencer : [Installation](#installation) · [Démarrage rapide](#démarrage-rapide)**

Née de [l'idée d'Andrej Karpathy](https://x.com/karpathy/status/2039805659525644595) d'une base de connaissances personnelle compilée par LLM, construite avec le [Sage Framework](https://github.com/xoai/sage). Quelques leçons tirées en chemin [ici](https://x.com/xoai/status/2040936964799795503).

- **Mémoire graphe avec citations.** Posez des questions relationnelles via `wiki_graph_query` — les réponses ne s'ancrent que dans des arêtes de graphe sérialisées ; avec le graphe avec preuves activé, chaque citation porte son document source et sa confiance.
- **Conçu pour les agents et les humains.** 18 outils MCP plus des fichiers de compétences générés enseignent aux agents quand chercher, capturer et compiler ; les humains disposent de markdown natif Obsidian, d'une TUI et d'une interface web sur les mêmes données.
- **Confiance et provenance.** Les sorties de requêtes restent en quarantaine jusqu'à vérification ; chaque relation avec preuves enregistre quel document l'a affirmée.
- **Vos sources en entrée, un wiki en sortie.** Le pipeline de compilation lit articles scientifiques, notes, code et e-mails ; résume ; extrait les concepts ; et rédige des articles interconnectés — la couche d'ingestion de tout ce qui précède. Chaque nouvelle source enrichit les articles existants ; le wiki se bonifie à mesure qu'il grandit.
- **Interrogez votre wiki.** La recherche hybride au niveau des fragments, avec expansion de requêtes par LLM, re-classement et assemblage de contexte conscient du graphe, renvoie des réponses avec citations.
- **Passe à l'échelle pour 100K+ documents.** La compilation par paliers indexe tout rapidement et ne dépense le budget LLM que là où cela compte.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Les points sur la bordure extérieure représentent les résumés de tous les documents de la base de connaissances, tandis que les points du cercle intérieur représentent les concepts extraits de la base de connaissances, avec des liens montrant comment ces concepts se connectent entre eux._

## Du vault personnel au graphe de connaissances d'entreprise

- **Personnel** — superposez un vault Obsidian existant (`init --vault`), tournez sur des [modèles locaux](docs/guides/local-models.md) pour un coût nul, et activez les passes de graphe (`ontology.triples` + `ontology.resolve`) quand vous voulez le graphe avec preuves.
- **Équipe** — partagez un même wiki via git ou un [serveur auto-hébergé](docs/guides/self-hosted-server.md), passez en revue ensemble les propositions de résolution d'entités et la [confiance des sorties](docs/guides/output-trust.md), et fédérez plusieurs wikis avec le hub. Voir [Configuration d'équipe](docs/guides/team-setup.md).
- **Entreprise** — déplacez le stockage vers [PostgreSQL/pgvector](docs/guides/storage-backends.md), activez les [métriques](docs/guides/metrics.md), placez une authentification devant le serveur, et faites passer l'ingestion à l'échelle avec la [compilation par paliers](docs/guides/large-vault-performance.md).

## Guides

| Guide | Description |
|-------|-------------|
| [Couche mémoire agent](docs/guides/agent-memory-layer.md) | Configuration MCP, fichiers de compétences, workflows de capture, boucle lire-capturer-évoluer |
| [Mémoire graphe](docs/guides/graph-memory.md) | Relations avec preuves, extraction de triplets, résolution d'entités, Q&R sur graphe |
| [Configuration](docs/guides/configuration.md) | Le config.yaml complet annoté, configuration multi-fournisseurs, worker de serve |
| [Configuration d'équipe](docs/guides/team-setup.md) | Modèles de déploiement git synchronisé, serveur partagé et fédération hub |
| [Qualité de recherche](docs/guides/search-quality.md) | Indexation par fragments, expansion de requêtes, re-classement, expansion par graphe, ANN |
| [Performance des grands vaults](docs/guides/large-vault-performance.md) | Compilation par paliers, contre-pression, analyseurs de code, passage à l'échelle 100K+ |
| [Confiance des sorties](docs/guides/output-trust.md) | Vérification d'ancrage, consensus, cycle de vie promotion/rétrogradation |
| [Authentification par abonnement](docs/guides/subscription-auth.md) | Connexion OAuth, import de tokens, gestion des identifiants |
| [Serveur auto-hébergé](docs/guides/self-hosted-server.md) | Docker Compose, Syncthing, reverse proxy, déploiement VPS |
| [Backends de stockage](docs/guides/storage-backends.md) | Installation SQLite vs PostgreSQL/pgvector, bascule, dimensionnement du pool |
| [Relations configurables](docs/guides/configurable-relations.md) | Types d'ontologie personnalisés, synonymes multilingues, restrictions de types |
| [Personnalisation des prompts](docs/guides/customizing-prompts.md) | Échafaudage de prompts, remplacements par type, champs frontmatter personnalisés |
| [Modèles locaux](docs/guides/local-models.md) | Configuration Ollama, routage GPU/CPU, config de modèle par passe |
| [Métriques](docs/guides/metrics.md) | Instantanés de logs, endpoint /metrics, contrôles de cardinalité |
| [Packs de contribution](CONTRIBUTING.md) | Création de packs, écriture de parseurs, soumission au registre |

## Installation

```bash
# CLI uniquement (sans interface web)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Avec interface web (nécessite Node.js pour compiler les ressources frontend)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Démarrage rapide

![Pipeline du compilateur](sage-wiki-compiler-pipeline.png)

### Nouveau projet (greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# Ajoutez des sources dans raw/
cp ~/papers/*.pdf raw/
# Modifiez config.yaml pour ajouter la clé API et choisir les LLMs
sage-wiki compile                                  # première compilation
sage-wiki search "attention mechanism"             # recherche hybride
sage-wiki query "How does flash attention work?"   # Q&R avec citations
sage-wiki tui                                      # tableau de bord terminal
sage-wiki serve --ui                               # navigateur (build webui)
sage-wiki compile --watch                          # surveillance du dossier
```

Chaque clé de `config.yaml`, annotée ligne par ligne : [Configuration](docs/guides/configuration.md).

### Surcouche Vault (vault Obsidian existant)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Modifiez config.yaml pour définir les dossiers source/à ignorer, ajouter la clé API, choisir les LLMs
sage-wiki compile --watch
```

Vous préférez les conteneurs ? Les images Docker multi-arch précompilées et les
fichiers compose sont couverts dans le [guide du serveur auto-hébergé](docs/guides/self-hosted-server.md).

## Formats sources supportés

| Format      | Extensions                              | Ce qui est extrait                                          |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | Corps du texte avec frontmatter analysé séparément          |
| PDF         | `.pdf`                                  | Texte intégral via extraction Go pure                       |
| Word        | `.docx`                                 | Texte du document depuis le XML                             |
| Excel       | `.xlsx`                                 | Valeurs des cellules et données des feuilles                |
| PowerPoint  | `.pptx`                                 | Contenu textuel des diapositives                            |
| CSV         | `.csv`                                  | En-têtes + lignes (jusqu'à 1000 lignes)                     |
| EPUB        | `.epub`                                 | Texte des chapitres depuis le XHTML                         |
| E-mail      | `.eml`                                  | En-têtes (de/à/objet/date) + corps                          |
| Texte brut  | `.txt`, `.log`                          | Contenu brut                                                |
| Transcriptions | `.vtt`, `.srt`                       | Contenu brut                                                |
| Images      | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | Description via LLM de vision (légende, contenu, texte visible) |
| Code        | `.go`, `.py`, `.js`, `.ts`, `.rs`, etc. | Code source                                                 |

Il suffit de déposer les fichiers dans votre dossier source — sage-wiki détecte le format automatiquement. Les images nécessitent un LLM capable de vision (Gemini, Claude, GPT-4o). Besoin d'un format non listé ? sage-wiki prend en charge les [parseurs externes](#parseurs-externes) — des scripts dans n'importe quel langage qui lisent stdin et écrivent du texte sur stdout.

## Mémoire graphe

D'emblée, le wiki construit un graphe de connaissances par proximité de
mots-clés — des concepts liés là où des mots-clés de relation coexistent avec
un `[[wikilink]]` dans le même bloc. Activez les
**passes de graphe opt-in** pour en faire un graphe avec preuves :

- **Extraction de triplets** (`ontology.triples.enabled`) — un appel LLM
  supplémentaire par document entièrement compilé extrait des entités et des
  relations typées, chacune portant un extrait de preuve, une confiance et un
  document source.
- **Résolution d'entités** (`ontology.resolve.enabled`) — les variantes de
  forme de surface (« NASA » / « National Aeronautics and Space Administration »)
  sont liées à une entité canonique. Les propositions à haute confiance
  s'appliquent automatiquement (seuil 0.85 ; mettez exactement `1.0` pour une
  revue seule), et chaque lien est exactement réversible avec
  `ontology resolve --unlink`.
- **Q&R sur graphe** — l'outil MCP `wiki_graph_query` répond aux questions
  relationnelles multi-sauts ancrées *uniquement* dans un ensemble borné et
  sérialisé d'arêtes ; les citations portent `source_doc` et `confidence`
  quand l'arête est avec preuves (les arêtes par proximité de mots-clés n'en
  portent aucune). Le contexte des Q&R classiques nomme aussi l'arête de
  liaison sous chaque article lié.

Profondeur, coûts, workflow de revue et sémantique d'annulation : [Mémoire graphe](docs/guides/graph-memory.md).

## Commandes

La surface principale ; exécutez `sage-wiki <command> --help` pour les flags.

| Commande | Description |
| ------- | ----------- |
| `sage-wiki init [--vault] [--skill <agent>] [--pack <name>] [--prompts]` | Initialiser le projet (greenfield ou surcouche vault) |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | Compiler les sources en articles wiki |
| `sage-wiki serve [--transport stdio\|sse] [--ui] [--port 3333]` | Serveur MCP / interface web |
| `sage-wiki reindex [--drop-chunk-vectors]` | Reconstruit l'index de chunks à partir des documents sur disque avec les `chunk_size` / `chunk_overlap_tokens` actuels |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | Recherche hybride (BM25 + vecteur + graphe ontologique) |
| `sage-wiki query "question"` | Q&R sur le wiki avec citations |
| `sage-wiki tui` | Tableau de bord terminal interactif |
| `sage-wiki ontology <query\|list\|add\|resolve>` | Interroger, gérer et résoudre le graphe d'ontologie |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | Ajouter des sources |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | Inspecter les sources et la couverture de compilation |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | Santé, validation de la config, modifications en attente |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | Maintenance et écritures manuelles |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | Hub multi-projets |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | Capture de connaissances |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | Générer ou actualiser les fichiers de compétences agent |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | Correspondances de provenance, version |

Les familles de commandes thématiques vivent avec leurs guides : `pack *` dans
[CONTRIBUTING](CONTRIBUTING.md), `auth *` (login, import, status, logout,
migrate) dans [Authentification par abonnement](docs/guides/subscription-auth.md), et
`verify` / `outputs *` dans [Confiance des sorties](docs/guides/output-trust.md).

## TUI

```bash
sage-wiki tui
```

Un tableau de bord terminal complet avec 4 onglets :

- **[F1] Parcourir** — Naviguer dans les articles par section (concepts, résumés, sorties). Flèches pour sélectionner, Entrée pour lire avec rendu markdown glamour, Échap pour revenir en arrière.
- **[F2] Rechercher** — Recherche floue avec aperçu en panneau divisé. Tapez pour filtrer, résultats classés par score hybride, Entrée pour ouvrir dans `$EDITOR`.
- **[F3] Q&R** — Questions-réponses conversationnelles en streaming. Posez des questions, obtenez des réponses synthétisées par LLM avec citations des sources. Ctrl+S sauvegarde la réponse dans outputs/.
- **[F4] Compiler** — Tableau de bord de compilation en direct. Surveille les répertoires sources pour détecter les modifications et recompile automatiquement. Parcourez les fichiers compilés avec aperçu.

Changement d'onglet : `F1`-`F4` depuis n'importe quel onglet, `1`-`4` sur Parcourir/Compiler, `Esc` retourne à Parcourir. Quitter avec `Ctrl+C`.

## Interface web

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333, nécessite un build -tags webui
```

- **Navigateur d'articles** avec rendu markdown, coloration syntaxique et `[[wikilinks]]` cliquables
- **Recherche hybride** avec résultats classés et extraits
- **Graphe de connaissances** — visualisation interactive à forces dirigées des concepts et de leurs connexions
- **Q&R en streaming** — posez des questions et obtenez des réponses synthétisées par LLM avec citations des sources
- **Table des matières** avec suivi du défilement ; mode sombre/clair avec détection des préférences système ; les liens d'articles brisés apparaissent en gris

Construite avec Preact + Tailwind, intégrée via `go:embed` (~1.2 MB, ~420 KB compressée gzip) ; omettez `-tags webui` pour un binaire CLI/MCP uniquement. Tokens d'authentification, hôtes autorisés et durcissement du déploiement : [Serveur auto-hébergé](docs/guides/self-hosted-server.md).

## Intégration MCP

![Intégration MCP](sage-wiki-interfaces.png)

Ajoutez à `.mcp.json` (Claude Code ; les autres agents dans le [guide de la couche mémoire agent](docs/guides/agent-memory-layer.md)) :

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/wiki"]
    }
  }
}
```

Clients réseau : `sage-wiki serve --transport sse --port 3333`. Le serveur
expose 18 outils — recherche, lecture, requête de graphe, capture, compilation
à la demande et plus ; la configuration par agent et les workflows de capture
vivent dans le [guide de la couche mémoire agent](docs/guides/agent-memory-layer.md).

**Fichiers de compétences agent** — `sage-wiki skill refresh --target <agent>`
écrit une section comportementale dans le fichier d'instructions de l'agent
(CLAUDE.md, .cursorrules, …) lui enseignant quand chercher, quoi capturer et
comment interroger, dérivée de votre config. Cibles : `claude-code`, `cursor`,
`windsurf`, `agents-md` (Antigravity), `codex`, `gemini`, `generic`.

**Capture de connaissances** — les agents stockent leurs découvertes en retour
via `wiki_capture` / `wiki_learn`, fermant la boucle lire-capturer-évoluer.
Workflows et astuces : [Couche mémoire agent](docs/guides/agent-memory-layer.md).

## Opérations

- **Stockage** — SQLite par défaut (fichier unique, zéro config) ; PostgreSQL +
  pgvector pour les déploiements serveur. Bascule et dimensionnement du pool : [Backends de stockage](docs/guides/storage-backends.md).
- **Observabilité** — instantanés de logs structurés et un endpoint `/metrics`
  opt-in : [Métriques](docs/guides/metrics.md).
- **Sorties structurées** — les passes d'extraction LLM utilisent le mécanisme
  natif de chaque fournisseur (tool-use Anthropic, `response_format` OpenAI,
  `responseSchema` Gemini) avec un repli validant par extraction de blocs de code.
- **Identifiants** — les tokens d'abonnement vivent dans le trousseau de l'OS
  quand il est disponible ; exécutez `sage-wiki auth migrate` une fois pour y
  déplacer les identifiants stockés en fichier. [Authentification par abonnement](docs/guides/subscription-auth.md).
- **Configuration** — chaque clé, annotée, avec des recettes multi-fournisseurs
  et le worker de compilation du mode serve : [Configuration](docs/guides/configuration.md).
- **Résolution d'entités** — application automatique à 0.85, exactement réversible avec `--unlink` ; voir [Mémoire graphe](#mémoire-graphe) ci-dessus.
- **Types de relations/entités personnalisés** — étendez les types intégrés ou
  ajoutez les vôtres (`ontology.relation_types`), avec synonymes multilingues
  et restrictions de types : [Relations configurables](docs/guides/configurable-relations.md).
- **Confiance des sorties** — les sorties de requêtes restent en quarantaine
  jusqu'à être ancrées, confirmées par consensus ou promues manuellement : [Confiance des sorties](docs/guides/output-trust.md).
- **Réglage de la recherche** — découpage en fragments, expansion, re-classement,
  expansion par graphe et ANN opt-in : [Qualité de recherche](docs/guides/search-quality.md).

### Coût

sage-wiki suit l'utilisation des tokens et estime le coût de chaque compilation.
Le **cache de prompts** (activé par défaut) réutilise les prompts système entre
les appels au sein d'une passe de compilation — Anthropic et Gemini mettent en
cache explicitement, OpenAI met en cache automatiquement — économisant 50-90%
sur les tokens d'entrée. L'**API Batch**
(Anthropic, OpenAI et Gemini) divise par deux le coût des grandes compilations :

```bash
sage-wiki compile --batch       # soumettre le lot, point de contrôle, quitter
sage-wiki compile               # vérifier le statut, récupérer à la fin
```

`compile --estimate` prévisualise le coût ; `compiler.mode: auto` bascule
automatiquement en batch au-delà d'un seuil. Détails : [Configuration](docs/guides/configuration.md).

### Passage à l'échelle pour les grands vaults

La compilation par paliers achemine chaque source selon son type et son
utilisation au lieu de tout compiler par LLM :

| Palier | Ce qui se passe | Coût | Temps par doc |
|------|-------------|------|-------------|
| **0** — Indexation seule | Recherche plein texte FTS5 | Gratuit | ~5ms |
| **1** — Indexation + embedding | FTS5 + embedding vectoriel | ~$0.00002 | ~200ms |
| **2** — Analyse de code | Résumé structurel via analyseur regex (sans LLM) | Gratuit | ~10ms |
| **3** — Compilation complète | Résumer + extraire les concepts + rédiger les articles | ~$0.05-0.15 | ~5-8 min |

Pour les grands vaults : indexez tout au palier 1 (un vault de 100K documents
en ~5.5 heures), puis compilez à la demande — la promotion automatique, la
contre-pression et les analyseurs de code sont couverts dans
[Performance des grands vaults](docs/guides/large-vault-performance.md).

## Écosystème

### Packs de contribution

Les packs regroupent types d'ontologie, prompts et déclencheurs de compétences
pour un domaine. Huit packs intégrés fonctionnent hors ligne :

| Pack | Public | Ontologie clé |
|------|----------|-------------|
| `academic-research` | Chercheurs | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | Équipes dev | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | Preneurs de notes | relates_to, inspired_by, fleeting_note |
| `study-group` | Étudiants | explains, prerequisite_of, definition |
| `meeting-organizer` | Managers | decided, assigned_to, action_item |
| `content-creation` | Rédacteurs | references, revises, draft, published |
| `legal-compliance` | Équipes juridiques | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research` en applique un à l'initialisation ;
`pack install <name|url>` en ajoute d'autres. Créer et publier des packs :
[CONTRIBUTING](CONTRIBUTING.md).

### Parseurs externes

Gérez n'importe quel format de fichier avec un script dans n'importe quel
langage (stdin → texte sur stdout), déclaré dans `parsers/parser.yaml` derrière
un double opt-in — ils s'exécutent comme des sous-processus non isolés avec
application d'un timeout et suppression des variables d'environnement. Détails
d'écriture et de durcissement : [CONTRIBUTING](CONTRIBUTING.md) ;
la discussion sur la frontière de confiance : [Configuration d'équipe](docs/guides/team-setup.md).

### Équipes

Trois modèles de partage — synchronisation git, serveur partagé, fédération
hub — plus la revue de confiance en équipe et la gestion des coûts : [Configuration d'équipe](docs/guides/team-setup.md).

## Benchmarks

Évaluation actuelle ([eval/REPORT.md](eval/REPORT.md), avril 2026) : score de
qualité global **85.9–86.7%** (un composite de métriques de recherche,
d'extraction, de citation et d'intégrité du graphe), recall@1 en recherche
**97.5–99.7%**, recall@10 100% sur la suite de benchmarks synthétiques. La
surcharge de compilation hors LLM (hachage + analyse de dépendances) reste
sous la seconde — le temps réel est dominé par les appels API LLM.
Reproduisez avec le harnais dans
[eval/](eval/README.md) :

```bash
python3 eval/eval.py .               # évaluation complète sur votre wiki
python3 -m unittest discover eval    # auto-tests du harnais
```

## Architecture

![Architecture Sage-Wiki](sage-wiki-architecture.png)

- **Stockage :** SQLite avec FTS5 (recherche BM25) + vecteurs BLOB (similarité cosinus) + table compile_items pour le suivi palier/état par source
- **Ontologie :** Graphe entité-relation typé avec parcours BFS et détection de cycles
- **Recherche :** Pipeline unifié — FTS5 et vecteurs, au niveau des documents comme des fragments, fusionnés par RRF pondéré avec le graphe ontologique comme troisième canal ; avec suppression des termes trop fréquents adaptée au corpus, pondération des colonnes servant de proxy de titre, et départage par fraîcheur pour les documents dont la date d'origine est connue. L'expansion de requête et le re-classement par LLM (soumis à un seuil de couverture) sont activables appel par appel sur les surfaces de recherche et activés par défaut pour les questions-réponses, qui utilisent aussi l'expansion de contexte par graphe à 4 signaux. Les réponses de recherche signalent les sources non compilées pour la compilation à la demande.
- **Compilateur :** Pipeline par paliers (Palier 0 : indexation, Palier 1 : embedding, Palier 2 : analyse de code, Palier 3 : compilation LLM complète) avec contre-pression adaptative, extraction Pass 2 concurrente, cache de prompts, API batch (Anthropic + OpenAI + Gemini), suivi des coûts, compilation à la demande via MCP, scoring de qualité et conscience des cascades. L'embedding inclut une reprise avec backoff exponentiel, une limitation de débit optionnelle et le mean-pooling pour les entrées longues. 10 analyseurs de code intégrés (Go via go/ast, 8 langages via regex, extraction de clés de données structurées).
- **MCP :** 18 outils (7 lecture, 9 écriture, 2 composés) via stdio ou SSE, dont `wiki_graph_query` pour les Q&R de graphe multi-sauts avec citations de provenance, `wiki_compile_topic` pour la compilation à la demande et `wiki_capture` pour l'extraction de connaissances
- **TUI :** tableau de bord terminal bubbletea + glamour à 4 onglets (parcourir, rechercher, Q&R, compiler) avec affichage de la distribution par paliers
- **Interface web :** Preact + Tailwind CSS intégrée via `go:embed` avec build tag (`-tags webui`)
- **Scribe :** Interface extensible pour l'ingestion de connaissances depuis les conversations. Le scribe de session traite les transcriptions JSONL de Claude Code.
- **Packs :** Système de packs de contribution avec 8 packs intégrés, registre basé sur Git, cycle de vie installation/application/suppression/mise à jour, application transactionnelle avec restauration par snapshot, fusion en remplissage seul et sécurité par liste blanche de config.
- **Parseurs externes :** Parseurs de formats de fichiers enfichables à l'exécution via protocole subprocess stdin/stdout. Exécution en bac à sable avec timeout, suppression d'environnement et isolation réseau (Linux).

Zéro CGO. Go pur. Multi-plateforme.

## Licence

MIT

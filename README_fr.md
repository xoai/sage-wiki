[English](README.md) | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | **Français** | [Русский](README_ru.md)

# sage-wiki

Une implémentation de [l'idée d'Andrej Karpathy](https://x.com/karpathy/status/2039805659525644595) pour une base de connaissances personnelle compilée par LLM. Développé avec le [Sage Framework](https://github.com/xoai/sage).

Quelques leçons tirées de la construction de sage-wiki [ici](https://x.com/xoai/status/2040936964799795503).

Déposez vos articles scientifiques, articles et notes. sage-wiki les compile en un wiki structuré et interconnecté — avec extraction de concepts, découverte de références croisées, et recherche intégrale.

- **Vos sources en entrée, un wiki en sortie.** Ajoutez des documents dans un dossier. Le LLM lit, résume, extrait les concepts et rédige des articles interconnectés.
- **Passe à l'échelle pour 100K+ documents.** La compilation par paliers indexe tout rapidement, ne compile que ce qui compte. Un vault de 100K documents est consultable en quelques heures, pas en mois.
- **Connaissances cumulatives.** Chaque nouvelle source enrichit les articles existants. Le wiki devient plus intelligent au fil de sa croissance.
- **Compatible avec vos outils.** S'ouvre nativement dans Obsidian. Se connecte à tout agent LLM via MCP. Fonctionne comme un binaire unique — avec des clés API ou votre abonnement LLM existant.
- **Interrogez votre wiki.** Recherche améliorée avec indexation par fragments, expansion de requêtes par LLM et re-classement. Posez des questions en langage naturel et obtenez des réponses avec citations.
- **Compilation à la demande.** Les agents peuvent déclencher la compilation pour des sujets spécifiques via MCP. Les résultats de recherche signalent les sources non compilées disponibles.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Les points sur la bordure extérieure représentent les résumés de tous les documents de la base de connaissances, tandis que les points du cercle intérieur représentent les concepts extraits de la base de connaissances, avec des liens montrant comment ces concepts se connectent entre eux._

## Installation

```bash
# CLI uniquement (sans interface web)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# Avec interface web (nécessite Node.js pour compiler les ressources frontend)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Formats sources supportés

| Format       | Extensions                              | Ce qui est extrait                                                  |
| ------------ | --------------------------------------- | ------------------------------------------------------------------- |
| Markdown     | `.md`                                   | Corps du texte avec frontmatter analysé séparément                  |
| PDF          | `.pdf`                                  | Texte intégral via extraction Go pure                               |
| Word         | `.docx`                                 | Texte du document depuis le XML                                     |
| Excel        | `.xlsx`                                 | Valeurs des cellules et données des feuilles                        |
| PowerPoint   | `.pptx`                                 | Contenu textuel des diapositives                                    |
| CSV          | `.csv`                                  | En-têtes + lignes (jusqu'à 1000 lignes)                             |
| EPUB         | `.epub`                                 | Texte des chapitres depuis le XHTML                                 |
| E-mail       | `.eml`                                  | En-têtes (de/à/objet/date) + corps                                  |
| Texte brut   | `.txt`, `.log`                          | Contenu brut                                                        |
| Transcriptions | `.vtt`, `.srt`                        | Contenu brut                                                        |
| Images       | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | Description via LLM de vision (légende, contenu, texte visible)     |
| Code         | `.go`, `.py`, `.js`, `.ts`, `.rs`, etc. | Code source                                                         |

Il suffit de déposer les fichiers dans votre dossier source — sage-wiki détecte le format automatiquement. Les images nécessitent un LLM capable de vision (Gemini, Claude, GPT-4o).

Besoin d'un format non listé ? sage-wiki prend en charge les **parseurs externes** — des scripts dans n'importe quel langage qui lisent stdin et écrivent du texte brut sur stdout. Voir [Parseurs externes](#parseurs-externes) ci-dessous.

## Démarrage rapide

![Pipeline du compilateur](sage-wiki-compiler-pipeline.png)

### Nouveau projet (greenfield)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# Ajoutez des sources dans raw/
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# Modifiez config.yaml pour ajouter la clé API et choisir les LLMs
# Première compilation
sage-wiki compile
# Recherche
sage-wiki search "attention mechanism"
# Poser des questions
sage-wiki query "How does flash attention optimize memory?"
# Tableau de bord interactif dans le terminal
sage-wiki tui
# Parcourir dans le navigateur (nécessite la compilation avec -tags webui)
sage-wiki serve --ui
# Surveillance du dossier
sage-wiki compile --watch
```

### Surcouche Vault (vault Obsidian existant)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Modifiez config.yaml pour définir les dossiers source/à ignorer, ajouter la clé API, choisir les LLMs
# Première compilation
sage-wiki compile
# Surveiller le vault
sage-wiki compile --watch
```

### Docker

```bash
# Télécharger depuis GitHub Container Registry
docker pull ghcr.io/xoai/sage-wiki:latest

# Ou depuis Docker Hub
docker pull xoai/sage-wiki:latest

# Exécuter avec votre répertoire wiki monté
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# Ou compiler depuis les sources
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

Tags disponibles : `:latest` (branche main), `:v1.0.0` (versions), `:sha-abc1234` (commits spécifiques). Multi-architecture : `linux/amd64` et `linux/arm64`.

Consultez le [guide d'auto-hébergement](docs/guides/self-hosted-server.md) pour Docker Compose, la synchronisation Syncthing, le reverse proxy et la configuration des fournisseurs LLM.

## Commandes

| Commande                                                                                | Description                                         |
| --------------------------------------------------------------------------------------- | --------------------------------------------------- |
| `sage-wiki init [--vault] [--skill <agent>]`                                            | Initialiser le projet (greenfield ou surcouche vault) |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | Compiler les sources en articles wiki               |
| `sage-wiki serve [--transport stdio\|sse]`                                              | Démarrer le serveur MCP pour les agents LLM         |
| `sage-wiki serve --ui [--port 3333]`                                                    | Démarrer l'interface web (nécessite `-tags webui`)   |
| `sage-wiki lint [--fix] [--pass name]`                                                  | Exécuter les passes de linting                      |
| `sage-wiki search "query" [--tags ...]`                                                 | Recherche hybride (BM25 + vecteur)                  |
| `sage-wiki query "question"`                                                            | Questions-réponses sur le wiki                      |
| `sage-wiki tui`                                                                         | Lancer le tableau de bord interactif dans le terminal |
| `sage-wiki ingest <url\|path>`                                                          | Ajouter une source                                  |
| `sage-wiki status`                                                                      | Statistiques et état de santé du wiki               |
| `sage-wiki provenance <source-or-concept>`                                              | Afficher les correspondances de provenance source↔article |
| `sage-wiki doctor`                                                                      | Valider la configuration et la connectivité         |
| `sage-wiki diff`                                                                        | Afficher les modifications sources en attente par rapport au manifeste |
| `sage-wiki list`                                                                        | Lister les entités, concepts ou sources du wiki     |
| `sage-wiki write <summary\|article>`                                                    | Rédiger un résumé ou un article                     |
| `sage-wiki ontology <query\|list\|add>`                                                 | Interroger, lister et gérer le graphe d'ontologie   |
| `sage-wiki hub <add\|remove\|search\|status\|list>`                                    | Commandes du hub multi-projets                      |
| `sage-wiki learn "text"`                                                                | Enregistrer une entrée d'apprentissage              |
| `sage-wiki capture "text"`                                                              | Capturer des connaissances à partir de texte        |
| `sage-wiki add-source <path>`                                                           | Enregistrer un fichier source dans le manifeste     |
| `sage-wiki skill <refresh\|preview> [--target <agent>]`                                 | Générer ou actualiser les fichiers de compétences agent |
| `sage-wiki pack install <name\|url>`                                                    | Installer un pack de contribution |
| `sage-wiki pack apply <name> [--mode merge\|replace]`                                   | Appliquer un pack installé au projet |
| `sage-wiki pack remove <name>`                                                          | Supprimer un pack du projet |
| `sage-wiki pack list`                                                                   | Lister les packs appliqués, en cache et intégrés |
| `sage-wiki pack search <query>`                                                         | Rechercher dans le registre de packs |
| `sage-wiki pack update [name]`                                                          | Mettre à jour les packs installés |
| `sage-wiki pack info <name>`                                                            | Afficher les détails d'un pack |
| `sage-wiki pack create <name>`                                                          | Créer un nouveau répertoire de pack |
| `sage-wiki pack validate [path]`                                                        | Valider le schéma et les fichiers d'un pack |
| `sage-wiki auth login --provider <name>`                                                | Connexion OAuth pour l'authentification par abonnement |
| `sage-wiki auth import --provider <name>`                                               | Importer les identifiants depuis des outils CLI existants |
| `sage-wiki auth status`                                                                 | Afficher les identifiants d'abonnement stockés      |
| `sage-wiki auth logout --provider <name>`                                               | Supprimer les identifiants stockés                  |
| `sage-wiki verify [--all] [--since 7d] [--limit 20]`                                   | Vérification d'ancrage sur les sorties en attente   |
| `sage-wiki outputs list [--state pending\|confirmed\|conflict\|stale]`                  | Lister les sorties par état de confiance            |
| `sage-wiki outputs promote <id>`                                                        | Promouvoir manuellement une sortie en confirmée     |
| `sage-wiki outputs reject <id>`                                                         | Rejeter et supprimer une sortie en attente          |
| `sage-wiki outputs resolve <id>`                                                        | Promouvoir la réponse, rejeter les conflits concurrents |
| `sage-wiki outputs clean [--older-than 90d]`                                            | Supprimer les sorties obsolètes/anciennes en attente |
| `sage-wiki outputs migrate`                                                             | Migrer les sorties existantes vers le système de confiance |
| `sage-wiki scribe <session-file>`                                                       | Extraire des entités d'une transcription de session |

## TUI

```bash
sage-wiki tui
```

Un tableau de bord terminal complet avec 4 onglets :

- **[F1] Parcourir** — Naviguer dans les articles par section (concepts, résumés, sorties). Flèches pour sélectionner, Entrée pour lire avec rendu markdown glamour, Échap pour revenir en arrière.
- **[F2] Rechercher** — Recherche floue avec aperçu en panneau divisé. Tapez pour filtrer, résultats classés par score hybride, Entrée pour ouvrir dans `$EDITOR`.
- **[F3] Q&R** — Questions-réponses conversationnelles en streaming. Posez des questions, obtenez des réponses synthétisées par LLM avec citations des sources. Ctrl+S sauvegarde la réponse dans outputs/.
- **[F4] Compiler** — Tableau de bord de compilation en direct. Surveille les répertoires sources pour les modifications et recompile automatiquement. Parcourez les fichiers compilés avec aperçu.

Changement d'onglet : `F1`-`F4` depuis n'importe quel onglet, `1`-`4` sur Parcourir/Compiler, `Échap` retourne à Parcourir. Quitter avec `Ctrl+C`.

## Interface web

![Architecture Sage-Wiki](sage-wiki-webui.png)

sage-wiki inclut un visualiseur optionnel dans le navigateur pour lire et explorer votre wiki.

```bash
sage-wiki serve --ui
# S'ouvre à http://127.0.0.1:3333
```

Fonctionnalités :

- **Navigateur d'articles** avec rendu markdown, coloration syntaxique et `[[wikilinks]]` cliquables
- **Recherche hybride** avec résultats classés et extraits
- **Graphe de connaissances** — visualisation interactive à forces dirigées des concepts et de leurs connexions
- **Q&R en streaming** — posez des questions et obtenez des réponses synthétisées par LLM avec citations des sources
- **Table des matières** avec suivi du défilement, ou basculer vers la vue graphe
- **Mode sombre/clair** avec détection des préférences système
- **Détection de liens brisés** — les liens vers des articles manquants apparaissent en gris

L'interface web est construite avec Preact + Tailwind CSS et intégrée dans le binaire Go via `go:embed`. Elle ajoute ~1,2 Mo (compressé gzip) à la taille du binaire. Pour compiler sans l'interface web, omettez le flag `-tags webui` — le binaire fonctionnera toujours pour toutes les opérations CLI et MCP.

Options :

- `--port 3333` — changer le port (par défaut 3333)
- `--bind 0.0.0.0` — exposer sur le réseau (par défaut localhost uniquement, sans authentification)

## Configuration

`config.yaml` est créé par `sage-wiki init`. Exemple complet :

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# Dossiers sources à surveiller et compiler
sources:
  - path: raw # ou dossiers vault comme Clippings/, Papers/
    type: auto # détection automatique depuis l'extension du fichier
    watch: true

output: wiki # répertoire de sortie compilé (_wiki pour la surcouche vault)

# Dossiers à ne jamais lire ni envoyer aux APIs (mode surcouche vault)
# ignore:
#   - Daily Notes
#   - Personal

# Fournisseur LLM
# Supportés : anthropic, openai, gemini, ollama, openai-compatible, qwen
# Pour OpenRouter ou d'autres fournisseurs compatibles OpenAI :
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# Pour Alibaba Cloud DashScope Qwen :
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY} # expansion des variables d'environnement supportée
  # auth: subscription          # utiliser les identifiants d'abonnement au lieu de api_key
                                # nécessite : sage-wiki auth login --provider <name>
                                # fournisseurs supportés : openai, anthropic, gemini
  # base_url:                   # point de terminaison personnalisé (OpenRouter, Azure, etc.)
  # rate_limit: 60              # requêtes par minute
  # extra_params:               # paramètres spécifiques au fournisseur fusionnés dans le corps de la requête
  #   enable_thinking: false    # ex. : désactiver le mode réflexion Qwen
  #   reasoning_effort: low     # ex. : contrôle du raisonnement DeepSeek

# Modèle par tâche — utiliser des modèles moins chers pour le volume, de qualité pour la rédaction
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# Fournisseur d'embeddings (optionnel — détecté automatiquement depuis le fournisseur API)
# Remplacer pour utiliser un fournisseur différent pour les embeddings
embed:
  provider: auto # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # clé séparée pour les embeddings
  # base_url:                   # point de terminaison séparé
  # rate_limit: 0              # limite RPM pour les embeddings (0 = sans limite ; mettre à 1200 pour Gemini Tier 1)

# Note multi-fournisseurs :
# La section api configure le fournisseur LLM principal utilisé pour toutes les tâches
# du compilateur et des requêtes (résumer, extraire, rédiger, linter, interroger).
# La section embed peut utiliser un fournisseur DIFFÉRENT pour les embeddings — avec
# ses propres identifiants et limites de débit. Cela permet de mixer les fournisseurs
# pour le coût ou la qualité :
#
#   api:
#     provider: anthropic                    # Claude pour la génération
#     api_key: ${ANTHROPIC_API_KEY}
#   models:
#     summarize: claude-haiku-4-5-20251001   # modèle économique pour le travail en masse
#     write: claude-sonnet-4-20250514        # modèle de qualité pour les articles
#     query: claude-sonnet-4-20250514
#   embed:
#     provider: openai                       # OpenAI pour les embeddings
#     model: text-embedding-3-small
#     api_key: ${OPENAI_API_KEY}
#
# Avec l'authentification par abonnement, vous pouvez vous authentifier auprès de
# plusieurs fournisseurs :
#   sage-wiki auth login --provider anthropic
#   sage-wiki auth import --provider gemini
# Puis utilisez Anthropic pour la génération et Gemini pour les embeddings.

compiler:
  max_parallel: 20 # appels LLM simultanés (avec contre-pression adaptative)
  debounce_seconds: 2 # temporisation en mode surveillance
  summary_max_tokens: 2000
  article_max_tokens: 4000
  # extract_batch_size: 20     # résumés par appel d'extraction de concepts (réduire pour éviter la troncature JSON sur les grands corpus)
  # extract_max_tokens: 8192   # tokens max en sortie pour l'extraction de concepts (augmenter à 16384 si l'extraction est tronquée)
  auto_commit: true # commit git après compilation
  auto_lint: true # exécuter le lint après compilation
  mode: auto # standard, batch, ou auto (auto = batch quand 10+ sources)
  # estimate_before: false    # afficher l'estimation de coût avant de compiler
  # prompt_cache: true        # activer le cache de prompts (par défaut : true)
  # batch_threshold: 10       # sources min pour le mode auto-batch
  # token_price_per_million: 0  # remplacer la tarification (0 = utiliser la tarification intégrée)
  # timezone: Asia/Shanghai   # fuseau horaire IANA pour les horodatages utilisateur (par défaut : UTC)
  # article_fields:           # champs frontmatter personnalisés extraits de la réponse LLM
  #   - language
  #   - domain

  # Compilation par paliers — indexer rapidement, compiler ce qui compte
  default_tier: 3 # 0=indexer, 1=indexer+embedder, 3=compilation complète
  # tier_defaults:             # remplacements de palier par extension
  #   json: 0                  # données structurées — indexation uniquement
  #   yaml: 0
  #   lock: 0
  #   md: 1                    # prose — indexation + embedding
  #   go: 1                    # code — indexation + embedding + analyse
  # auto_promote: true         # promouvoir au palier 3 selon les hits de recherche
  # auto_demote: true          # rétrograder les articles obsolètes
  # split_threshold: 15000     # caractères — découper les grands docs pour une rédaction plus rapide
  # dedup_threshold: 0.85      # similarité cosinus pour la déduplication de concepts
  # backpressure: true         # concurrence adaptative sur les limites de débit

search:
  hybrid_weight_bm25: 0.7 # poids BM25 vs vecteur
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # expansion de requête par LLM pour les Q&R (par défaut : true)
  # rerank: true              # re-classement par LLM pour les Q&R (par défaut : true)
  # chunk_size: 800           # tokens par fragment pour l'indexation (100-5000)
  # graph_expansion: true     # expansion de contexte par graphe pour les Q&R (par défaut : true)
  # graph_max_expand: 10      # articles max ajoutés via l'expansion par graphe
  # graph_depth: 2            # profondeur de parcours de l'ontologie (1-5)
  # context_max_tokens: 8000  # budget de tokens pour le contexte de requête
  # weight_direct_link: 3.0   # signal graphe : relation ontologique entre concepts
  # weight_source_overlap: 4.0 # signal graphe : documents sources partagés
  # weight_common_neighbor: 1.5 # signal graphe : voisins communs Adamic-Adar
  # weight_type_affinity: 1.0  # signal graphe : bonus de paire de types d'entités

serve:
  transport: stdio # stdio ou sse
  port: 3333 # mode SSE uniquement

# Confiance des sorties — mettre en quarantaine les sorties de requêtes jusqu'à vérification
# trust:
#   include_outputs: false       # "false" (par défaut), "verified", "true" (historique)
#   consensus_threshold: 3       # confirmations pour la promotion automatique
#   grounding_threshold: 0.8     # score d'ancrage minimum (0.0-1.0)
#   similarity_threshold: 0.85   # seuil de correspondance des questions
#   auto_promote: true           # promotion automatique quand tous les seuils sont atteints

# Types d'ontologie (optionnel)
# Étendre les types intégrés avec des synonymes supplémentaires ou ajouter des types personnalisés.
# ontology:
#   relation_types:
#     - name: implements           # étendre un type intégré avec plus de synonymes
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # ajouter un type de relation personnalisé
#       synonyms: ["regulates", "regulated by", "调控"]
#   entity_types:
#     - name: decision
#       description: "A recorded decision with rationale"
```

### Configuration multi-fournisseurs

sage-wiki vous permet d'utiliser différents fournisseurs LLM pour différentes tâches. La section `api` définit le fournisseur principal pour la génération (résumer, extraire, rédiger, linter, interroger), tandis que `embed` peut utiliser un fournisseur complètement séparé pour les embeddings — chacun avec ses propres identifiants et limites de débit.

**Cas d'utilisation :**
- **Optimisation des coûts** — modèle économique pour la résumation en masse, modèle de qualité pour la rédaction d'articles
- **Le meilleur de chaque monde** — Claude pour la génération, OpenAI pour les embeddings, Ollama pour la recherche locale
- **Mixage d'abonnements** — utilisez votre abonnement ChatGPT pour la génération et votre abonnement Gemini pour les embeddings

**Exemple : Claude pour la génération + embeddings OpenAI**

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}

models:
  summarize: claude-haiku-4-5-20251001    # économique pour le travail en masse
  extract: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514         # qualité pour les articles
  lint: claude-haiku-4-5-20251001
  query: claude-sonnet-4-20250514

embed:
  provider: openai
  model: text-embedding-3-small
  api_key: ${OPENAI_API_KEY}
```

**Exemple : Authentification par abonnement avec deux fournisseurs**

```bash
sage-wiki auth login --provider anthropic
sage-wiki auth import --provider gemini
```

```yaml
api:
  provider: anthropic
  auth: subscription

embed:
  provider: gemini
  # pas besoin de api_key — utilise les identifiants d'abonnement Gemini importés
```

La section `models` contrôle quel modèle est utilisé par tâche, le tout au sein du fournisseur principal. Différents modèles peuvent avoir des compromis coût/qualité très différents — utilisez des modèles plus petits (haiku, flash, mini) pour les passes à haut volume comme la résumation, et des modèles plus grands (sonnet, pro) pour la rédaction d'articles et les Q&R.

### Relations configurables

L'ontologie dispose de 8 types de relations intégrés : `implements`, `extends`, `optimizes`, `contradicts`, `cites`, `prerequisite_of`, `trades_off`, `derived_from`. Chacun possède des synonymes de mots-clés par défaut utilisés pour l'extraction automatique.

Vous pouvez personnaliser les relations via `ontology.relations` dans `config.yaml` :

- **Étendre un type intégré** — ajouter des synonymes (ex. : mots-clés multilingues) à un type existant. Les synonymes par défaut sont conservés ; les vôtres sont ajoutés.
- **Ajouter un type personnalisé** — définir un nouveau nom de relation avec ses synonymes de mots-clés. Les noms de relations doivent être en minuscules `[a-z][a-z0-9_]*`.

Les relations sont extraites par proximité de mots-clés au niveau des blocs — un mot-clé doit coexister avec un `[[wikilink]]` dans le même paragraphe ou bloc de titre. Cela évite les arêtes parasites provenant de correspondances inter-paragraphes.

Vous pouvez également restreindre les types d'entités qu'une relation connecte :

```yaml
ontology:
  relation_types:
    - name: curated_by
      synonyms: ["curated by", "organized by"]
      valid_sources: [exhibition, program]
      valid_targets: [artist]
```

Quand `valid_sources`/`valid_targets` sont définis, les arêtes ne sont créées que si le type d'entité source/cible correspond. Vide = tous les types autorisés (par défaut).

Zéro configuration = comportement identique au comportement actuel. Les bases de données existantes sont migrées automatiquement à la première ouverture. Consultez le [guide complet](docs/guides/configurable-relations.md) pour des exemples spécifiques au domaine, les relations à types restreints et le fonctionnement de l'extraction.

## Optimisation des coûts

sage-wiki suit l'utilisation des tokens et estime le coût pour chaque compilation. Trois stratégies pour réduire les coûts :

**Cache de prompts** (par défaut : activé) — Réutilise les prompts système entre les appels LLM au sein d'une passe de compilation. Anthropic et Gemini mettent en cache explicitement ; OpenAI met en cache automatiquement. Économise 50 à 90 % sur les tokens d'entrée.

**API Batch** — Soumettez toutes les sources en un seul lot asynchrone pour une réduction de coût de 50 %. Disponible pour Anthropic et OpenAI.

```bash
sage-wiki compile --batch       # soumettre le lot, sauvegarder le point de contrôle, quitter
sage-wiki compile               # vérifier le statut, récupérer quand c'est terminé
```

**Estimation des coûts** — Prévisualisez le coût avant de vous engager :

```bash
sage-wiki compile --estimate    # afficher la ventilation des coûts, quitter
```

Ou définissez `compiler.estimate_before: true` dans la configuration pour demander une confirmation à chaque fois.

**Mode auto** — Définissez `compiler.mode: auto` et `compiler.batch_threshold: 10` pour utiliser automatiquement le mode batch lors de la compilation de 10+ sources.

## Authentification par abonnement

Utilisez votre abonnement LLM existant au lieu de clés API. Supporte ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot et Google Gemini.

```bash
# Connexion via le navigateur (OpenAI ou Anthropic)
sage-wiki auth login --provider openai

# Ou importer depuis un outil CLI existant
sage-wiki auth import --provider claude
sage-wiki auth import --provider copilot
sage-wiki auth import --provider gemini
```

Puis définissez `api.auth: subscription` dans votre `config.yaml` :

```yaml
api:
  provider: openai
  auth: subscription
```

Toutes les commandes utiliseront vos identifiants d'abonnement. Les tokens se rafraîchissent automatiquement. Si un token expire et ne peut pas être rafraîchi, sage-wiki revient à `api_key` avec un avertissement.

**Limitations :** Le mode batch n'est pas disponible avec l'authentification par abonnement (désactivé automatiquement). Certains modèles peuvent ne pas être accessibles via les tokens d'abonnement. Consultez le [guide d'authentification par abonnement](docs/guides/subscription-auth.md) pour plus de détails.

## Confiance des sorties

Lorsque sage-wiki répond à une question, la réponse est une affirmation générée par LLM, pas un fait vérifié. Sans garde-fous, les mauvaises réponses sont indexées dans le wiki et polluent les requêtes futures. Le système de confiance des sorties met en quarantaine les nouvelles sorties et exige une vérification avant qu'elles n'entrent dans le corpus consultable.

```yaml
# config.yaml
trust:
  include_outputs: verified  # "false" (exclure tout), "verified" (confirmées uniquement), "true" (historique)
  consensus_threshold: 3     # confirmations nécessaires pour la promotion automatique
  grounding_threshold: 0.8   # score d'ancrage minimum
  similarity_threshold: 0.85 # similarité cosinus pour la correspondance des questions
  auto_promote: true          # promotion automatique quand les seuils sont atteints
```

**Fonctionnement :**

1. **Requête** — sage-wiki répond à votre question. La sortie est écrite dans `wiki/under_review/` comme en attente.
2. **Consensus** — Si la même question est posée à nouveau et produit la même réponse à partir de fragments sources différents, les confirmations s'accumulent. L'indépendance est évaluée via la distance de Jaccard entre les ensembles de fragments.
3. **Ancrage** — Exécutez `sage-wiki verify` pour vérifier les affirmations contre les passages sources via l'implication LLM.
4. **Promotion** — Lorsque les seuils de consensus et d'ancrage sont atteints, la sortie est promue vers `wiki/outputs/` et indexée dans la recherche.

```bash
# Vérifier les sorties en attente
sage-wiki outputs list

# Exécuter la vérification d'ancrage
sage-wiki verify --all

# Promouvoir manuellement une sortie de confiance
sage-wiki outputs promote 2026-05-09-what-is-attention.md

# Résoudre un conflit (promouvoir une, rejeter les autres)
sage-wiki outputs resolve 2026-05-09-what-is-attention.md

# Nettoyer les anciennes sorties en attente
sage-wiki outputs clean --older-than 90d

# Migrer les sorties existantes vers le système de confiance
sage-wiki outputs migrate
```

Les modifications de sources lors de `sage-wiki compile` rétrogradent automatiquement les sorties confirmées lorsque leurs sources citées sont modifiées. Consultez le [guide de confiance des sorties](docs/guides/output-trust.md) pour l'architecture complète, la référence de configuration et le dépannage.

## Passage à l'échelle pour les grands vaults

sage-wiki utilise la **compilation par paliers** pour gérer les vaults de 10K à 100K+ documents. Au lieu de tout compiler via le pipeline LLM complet, les sources sont acheminées à travers des paliers selon le type de fichier et l'utilisation :

| Palier | Ce qui se passe | Coût | Temps par doc |
|--------|----------------|------|--------------|
| **0** — Indexation seule | Recherche plein texte FTS5 | Gratuit | ~5ms |
| **1** — Indexation + embedding | FTS5 + embedding vectoriel | ~0,00002 $ | ~200ms |
| **2** — Analyse de code | Résumé structurel via analyseur regex (sans LLM) | Gratuit | ~10ms |
| **3** — Compilation complète | Résumer + extraire les concepts + rédiger les articles | ~0,05-0,15 $ | ~5-8 min |

Par défaut (`default_tier: 3`), toutes les sources passent par le pipeline LLM complet — le même comportement qu'avant la compilation par paliers. Pour les grands vaults (10K+), définissez `default_tier: 1` pour tout indexer en ~5,5 heures, puis compilez à la demande — quand un agent interroge un sujet, la recherche signale les sources non compilées, et `wiki_compile_topic` compile uniquement ce cluster (~2 min pour 20 sources).

**Fonctionnalités clés :**
- **Valeurs par défaut par type de fichier** — Les fichiers JSON, YAML et lock passent automatiquement au palier 0. Configuration par extension via `tier_defaults`.
- **Promotion automatique** — Les sources sont promues au palier 3 après 3+ résultats de recherche ou lorsqu'un cluster de sujets atteint 5+ sources.
- **Rétrogradation automatique** — Les articles obsolètes (90 jours sans requêtes) sont rétrogradés au palier 1 pour recompilation lors du prochain accès.
- **Contre-pression adaptative** — La concurrence s'auto-ajuste aux limites de débit de votre fournisseur. Démarre à 20 parallèles, réduit de moitié sur les 429, récupère automatiquement.
- **10 analyseurs de code** — Go (via go/ast), TypeScript, JavaScript, Python, Rust, Java, C, C++, Ruby, plus extraction de clés JSON/YAML/TOML. Le code obtient des résumés structurels sans appels LLM.
- **Compilation à la demande** — `wiki_compile_topic("flash attention")` via MCP compile les sources pertinentes en temps réel.
- **Score de qualité** — Couverture des sources par article, complétude de l'extraction et densité des références croisées suivies automatiquement.

Consultez le [guide complet de passage à l'échelle](docs/guides/large-vault-performance.md) pour la configuration, les exemples de remplacement de palier et les objectifs de performance.

## Qualité de recherche

sage-wiki utilise un pipeline de recherche amélioré pour les requêtes Q&R, inspiré de l'analyse de l'approche de récupération de [qmd](https://github.com/dmayboroda/qmd) :

- **Indexation au niveau des fragments** — Les articles sont découpés en fragments de ~800 tokens, chacun avec sa propre entrée FTS5 et son embedding vectoriel. Une recherche de « flash attention » trouve le paragraphe pertinent dans un article Transformer de 3000 tokens.
- **Expansion de requête par LLM** — Un seul appel LLM génère des réécritures par mots-clés (pour BM25), des réécritures sémantiques (pour la recherche vectorielle) et une réponse hypothétique (pour la similarité d'embedding). Un contrôle de signal fort saute l'expansion quand le meilleur résultat BM25 est déjà confiant.
- **Re-classement par LLM** — Les 15 meilleurs candidats sont évalués par le LLM pour leur pertinence. Un mélange tenant compte de la position protège les résultats de récupération à haute confiance (rangs 1-3 obtiennent 75 % de poids de récupération, rangs 11+ obtiennent 60 % de poids du re-classeur).
- **Recherche vectorielle multilingue** — Recherche cosinus exhaustive sur tous les vecteurs de fragments, combinée avec BM25 via fusion RRF. Cela garantit que les requêtes multilingues (ex. : requête en polonais contre du contenu en anglais) trouvent des résultats sémantiquement pertinents même sans aucun chevauchement lexical.
- **Expansion de contexte améliorée par graphe** — Après la récupération, un évaluateur de graphe à 4 signaux trouve les articles liés via l'ontologie : relations directes (×3,0), documents sources partagés (×4,0), voisins communs via Adamic-Adar (×1,5) et affinité de types d'entités (×1,0). Cela fait remonter des articles structurellement liés mais manqués par la recherche par mots-clés/vecteurs.
- **Contrôle du budget de tokens** — Le contexte de requête est plafonné à une limite de tokens configurable (par défaut 8000), avec les articles tronqués à 4000 tokens chacun. Le remplissage glouton priorise les articles les mieux évalués.

|                     | sage-wiki                                  | qmd               |
| ------------------- | ------------------------------------------ | ----------------- |
| Recherche par fragments | FTS5 + vecteur (double canal)          | Vecteur uniquement |
| Expansion de requête | Basée sur LLM (lex/vec/hyde)              | Basée sur LLM     |
| Re-classement       | LLM + mélange tenant compte de la position | Cross-encoder     |
| Contexte par graphe | Expansion par graphe à 4 signaux + parcours 1-saut | Pas de graphe |
| Coût par requête    | Gratuit (Ollama) / ~0,0006 $ (cloud)      | Gratuit (GGUF local) |

Zéro configuration = toutes les fonctionnalités activées. Avec Ollama ou d'autres modèles locaux, la recherche améliorée est entièrement gratuite — le re-classement est auto-désactivé (les modèles locaux peinent avec l'évaluation JSON structurée) mais la recherche au niveau des fragments et l'expansion de requête fonctionnent toujours. Avec les LLM cloud, le coût supplémentaire est négligeable (~0,0006 $/requête). L'expansion et le re-classement peuvent être activés/désactivés via la configuration. Consultez le [guide complet de qualité de recherche](docs/guides/search-quality.md) pour la configuration, la ventilation des coûts et la comparaison détaillée.

## Personnalisation des prompts

sage-wiki utilise des prompts intégrés pour la résumation et la rédaction d'articles. Pour personnaliser :

```bash
sage-wiki init --prompts    # crée le répertoire prompts/ avec les valeurs par défaut
```

Cela crée des fichiers markdown modifiables :

```
prompts/
├── summarize-article.md    # comment les articles sont résumés
├── summarize-paper.md      # comment les articles scientifiques sont résumés
├── write-article.md        # comment les articles de concepts sont rédigés
├── extract-concepts.md     # comment les concepts sont identifiés
└── caption-image.md        # comment les images sont décrites
```

Modifiez n'importe quel fichier pour changer la façon dont sage-wiki traite ce type. Ajoutez de nouveaux types de sources en créant `summarize-{type}.md` (ex. : `summarize-dataset.md`). Supprimez un fichier pour revenir au comportement par défaut intégré.

### Champs frontmatter personnalisés

Le frontmatter des articles est construit à partir de deux sources : les **données de vérité terrain** (nom du concept, alias, sources, horodatage) sont toujours générées par le code, tandis que les **champs sémantiques** sont évalués par le LLM.

Par défaut, `confidence` est le seul champ évalué par le LLM. Pour ajouter des champs personnalisés :

1. Déclarez-les dans `config.yaml` :

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. Mettez à jour votre template `prompts/write-article.md` pour demander ces champs au LLM :

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

Les réponses du LLM sont extraites du corps de l'article et fusionnées automatiquement dans le frontmatter YAML. Le frontmatter résultant ressemble à :

```yaml
---
concept: self-attention
aliases: ["scaled dot-product attention"]
sources: ["raw/transformer-paper.md"]
confidence: high
language: English
domain: machine learning
created_at: 2026-04-10T08:00:00+08:00
---
```

Les champs de vérité terrain (`concept`, `aliases`, `sources`, `created_at`) sont toujours précis — ils proviennent de la passe d'extraction, pas du LLM. Les champs sémantiques (`confidence` + vos champs personnalisés) reflètent le jugement du LLM.

## Packs de contribution

Les packs sont des profils de configuration installables qui regroupent des types d'ontologie, des prompts et des sources d'exemple pour des domaines spécifiques. sage-wiki inclut 8 packs intégrés fonctionnant hors ligne :

| Pack | Public | Ontologie clé |
|------|--------|--------------|
| `academic-research` | Chercheurs | cites, contradicts, finding, hypothesis |
| `software-engineering` | Équipes dev | implements, depends_on, adr, runbook |
| `product-management` | PM | addresses, prioritizes, user_story |
| `personal-knowledge` | Prise de notes | relates_to, inspired_by, fleeting_note |
| `study-group` | Étudiants | explains, prerequisite_of, definition |
| `meeting-organizer` | Managers | decided, assigned_to, action_item |
| `content-creation` | Rédacteurs | references, revises, draft, published |
| `legal-compliance` | Juridique | regulates, supersedes, policy, control |

```bash
sage-wiki init --pack academic-research
sage-wiki pack install academic-research
sage-wiki pack apply academic-research --mode merge
sage-wiki pack list
```

Les packs sont composables. Les packs communautaires sont distribués via le registre [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs). Voir [CONTRIBUTING.md](CONTRIBUTING.md) pour créer et publier votre propre pack.

## Parseurs externes

sage-wiki inclut des parseurs natifs pour plus de 12 formats. Pour tout autre format, vous pouvez ajouter un parseur externe sous forme de script dans n'importe quel langage. Protocole stdin/stdout.

```yaml
parsers:
  - extensions: [".rtf"]
    command: python3
    args: ["rtf_parser.py"]
    timeout: 30s
```

Sécurité : les parseurs externes s'exécutent avec timeout, suppression des variables d'environnement et isolation réseau sous Linux. Nécessite `parsers.external: true`. Voir [CONTRIBUTING.md](CONTRIBUTING.md).

## Fichiers de compétences agent

sage-wiki dispose de 17 outils MCP, mais les agents ne les utiliseront pas à moins que quelque chose dans leur contexte indique *quand* consulter le wiki. Les fichiers de compétences comblent cette lacune — des extraits générés qui enseignent aux agents quand chercher, quoi capturer et comment interroger efficacement.

```bash
# Générer lors de l'initialisation du projet
sage-wiki init --skill claude-code

# Ou ajouter à un projet existant
sage-wiki skill refresh --target claude-code

# Prévisualiser sans écrire
sage-wiki skill preview --target cursor
```

Cela ajoute une section de compétences comportementales au fichier d'instructions de l'agent (CLAUDE.md, .cursorrules, etc.) avec des déclencheurs spécifiques au projet, des directives de capture et des exemples de requêtes dérivés de votre config.yaml.

**Agents supportés :** `claude-code`, `cursor`, `windsurf`, `agents-md` (Antigravity/Codex), `gemini`, `generic`

Le fichier de compétences fournit un modèle de base générique — quand chercher, quoi capturer, comment interroger — utilisant les types d'entités et de relations de votre config.yaml. Pour un comportement d'agent spécifique au domaine, appliquez un [pack de contribution](#packs-de-contribution) :

```bash
sage-wiki init --skill claude-code --pack academic-research
```

Le répertoire `skills/` du pack ajoute des déclencheurs spécifiques au domaine aux côtés de la compétence de base. L'exécution de `skill refresh` régénère uniquement la section de compétences marquée — votre autre contenu est préservé.

## Intégration MCP

![Intégration MCP](sage-wiki-interfaces.png)

### Claude Code

Ajoutez à `.mcp.json` :

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

### SSE (clients réseau)

```bash
sage-wiki serve --transport sse --port 3333
```

## Capture de connaissances depuis les conversations IA

sage-wiki fonctionne comme un serveur MCP, vous pouvez donc capturer des connaissances directement depuis vos conversations IA. Connectez-le à Claude Code, ChatGPT, Cursor ou tout client MCP — puis demandez simplement :

> « Sauvegarde ce qu'on vient de comprendre sur le pooling de connexions dans mon wiki »

> « Capture les décisions clés de cette session de débogage »

L'outil `wiki_capture` extrait des éléments de connaissance (décisions, découvertes, corrections) du texte de conversation via votre LLM, les écrit comme fichiers sources et les met en file d'attente pour compilation. Le bruit (salutations, tentatives infructueuses, impasses) est filtré automatiquement.

Pour les faits isolés, `wiki_learn` stocke une pépite directement. Pour les documents complets, `wiki_add_source` ingère un fichier. Exécutez `wiki_compile` pour tout traiter en articles.

Consultez le guide de configuration complet : [Guide de la couche mémoire agent](docs/guides/agent-memory-layer.md)

## Configuration d'équipe

sage-wiki passe d'un wiki personnel à une base de connaissances partagée pour des équipes de 3 à 50 personnes. Trois modèles de déploiement :

**Dépôt synchronisé par Git** (3-10 personnes) — le wiki vit dans un dépôt Git. Tout le monde clone, compile localement et pousse. Le répertoire compilé `wiki/` est suivi ; la base de données est dans `.gitignore` et reconstruite à chaque compilation.

**Serveur partagé** (5-30 personnes) — exécutez sage-wiki sur un serveur avec l'interface web. Les membres de l'équipe naviguent dans le navigateur et connectent les agents via MCP sur SSE.

**Fédération de hubs** (multi-projets) — chaque projet a son propre wiki. Le système de hubs les fédère en une interface de recherche unique avec `sage-wiki hub search`.

```bash
# Hub : enregistrer et rechercher à travers plusieurs wikis
sage-wiki hub add /projects/backend-wiki
sage-wiki hub add /projects/ml-wiki
sage-wiki hub search "deployment process"
```

**Ce que les équipes obtiennent :**

- **Mémoire institutionnelle cumulative.** Ce qu'un agent apprend, tous les agents le savent. Les décisions, conventions et pièges capturés depuis n'importe quelle session sont consultables par tous.
- **Sorties avec contrôle de confiance.** Le [système de confiance des sorties](docs/guides/output-trust.md) met en quarantaine les réponses LLM jusqu'à ce qu'elles soient vérifiées par ancrage et confirmées par consensus. L'hallucination d'un agent ne peut pas empoisonner le corpus partagé.
- **Fichiers de compétences agent.** Les instructions générées enseignent à l'agent IA de chaque membre de l'équipe quand consulter le wiki, quoi capturer et comment interroger. Supporte Claude Code, Cursor, Windsurf, Codex et Gemini.
- **Authentification par abonnement par utilisateur.** Chaque développeur utilise son propre abonnement LLM — pas de clés API partagées dans le dépôt. La configuration indique `auth: subscription` ; les identifiants sont par utilisateur dans `~/.sage-wiki/auth.json`.
- **Piste d'audit complète.** `auto_commit: true` crée un commit git à chaque compilation. Qui a changé quoi, quand.

```yaml
# Configuration d'équipe recommandée
trust:
  include_outputs: verified    # quarantaine jusqu'à vérification
compiler:
  default_tier: 1              # indexer rapidement, compiler à la demande
  auto_commit: true            # piste d'audit
```

Consultez le [guide complet de configuration d'équipe](docs/guides/team-setup.md) pour l'organisation des sources, les workflows d'intégration d'agents, les pipelines de capture de connaissances, les considérations de passage à l'échelle et les recettes prêtes à l'emploi pour les startups, les laboratoires de recherche et les équipes utilisant des vaults Obsidian.

## Benchmarks

Évalué sur un wiki réel compilé à partir de 1 107 sources (base de données de 49,4 Mo, 2 832 fichiers wiki).

Exécutez `python3 eval.py .` sur votre propre projet pour reproduire. Voir [eval.py](eval.py) pour les détails.

### Performance

| Opération                            |   p50 |      Débit |
| ------------------------------------ | ----: | ---------: |
| Recherche par mots-clés FTS5 (top-10)| 411µs |  1 775 qps |
| Recherche cosinus vectorielle (2 858 × 3072d) |  81ms |   15 qps |
| Hybride RRF (BM25 + vecteur)        |  80ms |     16 qps |
| Parcours de graphe (BFS profondeur <= 5) |   1µs | 738K qps |
| Détection de cycles (graphe complet) | 1,4ms |          — |
| Insertion FTS (lot de 100)           |     — | 89 802 /s  |
| Lectures mixtes soutenues            |  77µs | 8 500+ ops/s |

La surcharge de compilation hors LLM (hachage + analyse de dépendances) est inférieure à 1 seconde. Le temps d'exécution du compilateur est entièrement dominé par les appels API LLM.

### Qualité

| Métrique                       |    Score |
| ------------------------------ | -------: |
| Rappel de recherche @10        | **100%** |
| Rappel de recherche @1         |   91,6 % |
| Taux de citation des sources   |   94,6 % |
| Couverture des alias           |   90,0 % |
| Taux d'extraction de faits     |   68,5 % |
| Connectivité du wiki           |   60,5 % |
| Intégrité des références croisées |   50,0 % |
| **Score de qualité global**    | **73,0 %** |

### Exécuter l'évaluation

```bash
# Évaluation complète (performance + qualité)
python3 eval.py /path/to/your/wiki

# Performance uniquement
python3 eval.py --perf-only .

# Qualité uniquement
python3 eval.py --quality-only .

# JSON lisible par machine
python3 eval.py --json . > report.json
```

Nécessite Python 3.10+. Installez `numpy` pour des benchmarks vectoriels ~10x plus rapides.

### Exécuter les tests

```bash
# Exécuter la suite de tests complète (génère des fixtures synthétiques, pas de données réelles nécessaires)
python3 -m unittest eval_test -v

# Générer une fixture de test autonome
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24 tests couvrant : génération de fixtures, modes CLI (`--perf-only`, `--quality-only`, `--json`), validation du schéma JSON, limites de scores, rappel de recherche, cas limites (wikis vides, grands jeux de données, chemins manquants).

## Architecture

![Architecture Sage-Wiki](sage-wiki-architecture.png)

- **Stockage :** SQLite avec FTS5 (recherche BM25) + vecteurs BLOB (similarité cosinus) + table compile_items pour le suivi palier/état par source
- **Ontologie :** Graphe entité-relation typé avec parcours BFS et détection de cycles
- **Recherche :** Pipeline amélioré avec indexation FTS5 + vecteurs au niveau des fragments, expansion de requête par LLM, re-classement par LLM, fusion RRF et expansion par graphe à 4 signaux. Les réponses de recherche signalent les sources non compilées pour la compilation à la demande.
- **Compilateur :** Pipeline par paliers (Palier 0 : indexation, Palier 1 : embedding, Palier 2 : analyse de code, Palier 3 : compilation LLM complète) avec contre-pression adaptative, extraction Pass 2 concurrente, cache de prompts, API batch (Anthropic + OpenAI + Gemini), suivi des coûts, compilation à la demande via MCP, scoring de qualité et conscience des cascades. L'embedding inclut une reprise avec backoff exponentiel, limitation de débit optionnelle et mean-pooling pour les entrées longues. 10 analyseurs de code intégrés (Go via go/ast, 8 langages via regex, extraction de clés de données structurées).
- **MCP :** 17 outils (6 lecture, 9 écriture, 2 composés) via stdio ou SSE, incluant `wiki_compile_topic` pour la compilation à la demande et `wiki_capture` pour l'extraction de connaissances
- **TUI :** tableau de bord terminal bubbletea + glamour à 4 onglets (parcourir, rechercher, Q&R, compiler) avec affichage de la distribution par paliers
- **Interface web :** Preact + Tailwind CSS intégrée via `go:embed` avec build tag (`-tags webui`)
- **Scribe :** Interface extensible pour l'ingestion de connaissances depuis les conversations. Le scribe de session traite les transcriptions JSONL de Claude Code.
- **Packs :** Système de packs de contribution — 8 packs intégrés, registre Git, cycle de vie installation/application/suppression/mise à jour, application transactionnelle avec restauration par snapshot.
- **Parseurs externes :** Parseurs de formats de fichiers enfichables à l'exécution via protocole subprocess stdin/stdout. Exécution en bac à sable avec timeout, suppression d'environnement et isolation réseau.

Zéro CGO. Go pur. Multi-plateforme.

## Licence

MIT

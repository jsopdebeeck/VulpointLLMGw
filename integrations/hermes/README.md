# LLMGW by Vulpoint.com · kit Hermes Agent

Ce kit branche [Hermes Agent](https://github.com/NousResearch/hermes-agent) sur la passerelle LLMGW :
tous les appels LLM que la passerelle peut relayer (modèle principal, tâches auxiliaires, sous-agents,
MoA) passent par elle, donc par la pseudonymisation, les quotas et le journal d'audit.

Il contient :

- `install.sh` / `uninstall.sh` : configurent un profil Hermes (et le remettent comme avant) ;
- `plugin/llmgw/` : un plugin Hermes, installé dans `~/.hermes/plugins/llmgw/`, qui
  - ajoute aux requêtes **vers la passerelle seulement** les en-têtes `X-LLMGW-Session` (identifiant de la
    conversation) et `X-LLMGW-Client: hermes/<version> llmgw-plugin/<version>` ;
  - en mode `strict` (défaut), **refuse** un appel du modèle principal qui ne va pas à la passerelle ;
  - signale une fois à la passerelle (`POST /v1/events`) chaque appel LLM qui la contourne ;
  - ajoute la commande `/llmgw` (passerelle, utilisateur, quotas, consommation du jour, valeurs
    pseudonymisées de la session, contournements vus) ;
- `tests/` : tests du plugin et de l'installateur (dossiers Hermes temporaires, fausse passerelle).

## Prérequis

- Hermes Agent **0.21.6 ou plus récent** (vérifié sur le code de 0.21.6 à 0.22.2, testé sur 0.22.2) ;
  rien d'autre : les scripts utilisent le Python fourni avec Hermes (à défaut `python3`, sans module
  supplémentaire) et la commande `hermes` elle-même pour lire et écrire la configuration.
- macOS ou Linux, bash.
- Une passerelle LLMGW joignable (1.5.0 ou plus récente) et une clé `gw-…` ; la passerelle doit autoriser
  au moins un modèle pour cette clé (`GET /v1/models`). `/v1/models` ne liste que les modèles **nommés
  exactement** dans les cibles du profil (`models = ["mistral-large-latest", …]`) : une cible qui ne
  donne qu'un motif (`"mistral-*"`) n'y apparaît pas. Avec une passerelle antérieure à 1.5.0, Hermes
  fonctionne, mais sans pseudonymes stables sur la conversation, sans signalement des contournements et
  sans détail de session dans `/llmgw`.

## Installation

```bash
integrations/hermes/install.sh --url http://127.0.0.1:8088 --key gw-… --dry-run   # voir d'abord
integrations/hermes/install.sh --url http://127.0.0.1:8088 --key gw-…
```

La clé peut aussi venir de la variable `LLMGW_API_KEY` (ou être demandée au clavier), ce qui évite de la
laisser dans l'historique du shell :

```bash
LLMGW_API_KEY=gw-… integrations/hermes/install.sh --url https://llmgw.example.org
```

| Option | Effet |
|---|---|
| `--model <modèle>` | modèle principal, parmi ceux de `GET /v1/models` (défaut : le modèle llmgw déjà choisi, sinon le premier) |
| `--warn` | plugin en mode warn : signale les appels hors passerelle sans les refuser |
| `--no-plugin` | configuration seule, sans plugin (les pseudonymes restent stables, voir plus bas) |
| `--keep-moa` | garde le fan-out MoA actif (ses modèles passent par la passerelle) |
| `--dry-run` | affiche les changements prévus, n'écrit rien |
| `--hermes-home <dossier>` | profil à configurer, par ex. `~/.hermes/profiles/travail` (défaut : celui qu'utilise `hermes`) |

L'installateur :

1. vérifie Hermes et la passerelle (`GET /v1/models` avec la clé ; `--model` doit être dans la liste) ;
2. affiche le plan, puis sauvegarde `config.yaml` en `config.yaml.bak-llmgw-<date>` (rien n'est jamais
   supprimé) ;
3. écrit la clé dans le `.env` du profil (`LLMGW_API_KEY=…`, droits 600) ; `config.yaml` n'en contient que
   la référence `${LLMGW_API_KEY}` ;
4. règle, avec `hermes config set` (l'écriture validée et atomique de Hermes) :
   - `providers.llmgw` : `api: <url>/v1`, `api_key: ${LLMGW_API_KEY}`, `transport: chat_completions`,
     `default_model`, `models` (la liste autorisée), `session_affinity_header: X-LLMGW-Session`,
     `extra_headers.X-LLMGW-Client` ;
   - `model.provider: llmgw` et `model.default` ;
   - chaque tâche auxiliaire connue de Hermes (`vision`, `compression`, `title_generation`, `approval`,
     `mcp`, `skills_hub`, `memory_query_rewrite`, `tts_audio_tags`, `voice_chat`, `triage_specifier`,
     `kanban_decomposer`, `profile_describer`, `goal_judge`, `curator`, `monitor`, et celles déjà
     présentes dans votre configuration) sur `llmgw` et le modèle principal ; un `base_url`/`api_key`
     propre à la tâche est retiré, un `fallback_chain` ne garde que les entrées llmgw. `review` et
     `background_review` restent en `auto` (= le modèle principal) : ce sont des agents complets, sous la
     garde du plugin ;
   - `delegation` (sous-agents) sur `llmgw` ;
   - MoA : modèles de référence et agrégateur de chaque préréglage sur des modèles de la passerelle ;
   - les secours (`fallback_providers`, `fallback_model`, `cron.model_provider`) hors passerelle sont
     retirés ou routés par elle ;
5. copie le plugin, écrit `plugins/llmgw/config.yaml` (`url`, `mode`) et l'active
   (`hermes plugins enable llmgw`, c'est-à-dire `plugins.enabled`) ;
6. vérifie (`hermes plugins doctor llmgw` charge le plugin) et liste ce qui reste hors passerelle.

Relancer l'installateur est sans risque : sans changement nécessaire, il ne touche à rien et ne fait pas de
nouvelle sauvegarde. Après coup, redémarrez les sessions Hermes ouvertes (`hermes gateway restart` si la
messagerie tourne).

### MoA

Par défaut, le fan-out MoA est coupé (`moa.presets.<nom>.enabled: false`) après que ses modèles ont été
routés par la passerelle : avec une seule passerelle, les « conseillers » sont souvent les mêmes
modèles, et chaque tour multiplie les appels (et la consommation des quotas). Un préréglage coupé reste
utilisable, l'agrégateur répond seul. `--keep-moa` garde le fan-out, toujours via la passerelle.

## Ce qui reste hors de la passerelle

La recherche web, le navigateur et la voix (TTS, transcription) ne sont pas des appels LLM que la passerelle
peut relayer ; c'est votre choix de les garder : l'installateur **ne les change pas**, il les liste à la fin
avec la commande pour les couper, par exemple :

```text
  - Recherche et lecture web : web.backend = nous
      pour couper : hermes tools disable web
  - Navigateur : browser.cloud_provider = nous
      pour couper : hermes tools disable browser
  - Synthèse vocale (TTS) : tts.provider = nous
      pour couper : hermes tools disable tts ; hermes config set voice.auto_tts false
  - Transcription (STT) : stt.provider = nous
      pour couper : hermes config set stt.enabled false
```

Il liste aussi la génération d'images et de vidéos, une mémoire externe, les autres fournisseurs déclarés
dans `providers`, les connexions de `auth.json` et les clés d'autres services du `.env` (noms seulement) :
elles restent utilisables via `/model`, mais en mode strict le plugin refuse leurs appels du modèle
principal. `hermes tools disable …` accepte `--platform <plateforme>` pour la messagerie.

## Modes strict et warn

Le mode se règle dans `~/.hermes/plugins/llmgw/config.yaml` (`mode: strict` ou `mode: warn`), relu à chaque
appel, ou en relançant l'installateur avec ou sans `--warn`.

- **strict** (défaut) : un appel du modèle principal (ou d'un sous-agent, d'une tâche planifiée, d'une
  revue en arrière-plan) dont l'adresse n'est pas la passerelle est refusé avant tout envoi. Hermes
  affiche une erreur non relançable dont le texte commence par `[LLMGW] Mode strict, appel refusé` et dit
  comment revenir : `/model <modèle> --provider llmgw`. Un préréglage MoA n'est accepté que si
  l'agrégateur et chaque conseiller actif passent par la passerelle.
- **warn** : rien n'est refusé ; chaque contournement est écrit une fois dans le journal de Hermes
  (`~/.hermes/logs/agent.log`) et signalé à la passerelle.

Dans les deux modes, les **tâches auxiliaires** hors passerelle (titres, compression, vision…) sont
seulement signalées : Hermes ne permet pas à un plugin de les arrêter (voir Limites). L'installateur les
route toutes par la passerelle, c'est donc le cas d'une configuration modifiée après coup.

Comment la garde refuse : le middleware `llm_execution` de Hermes est conçu pour « échouer ouvert » (un
middleware qui lève une exception est ignoré et l'appel continue). Le plugin fait donc échouer l'appel
*en aval* du middleware, avant toute entrée/sortie réseau, avec le marqueur que la chaîne de Hermes relaie
tel quel ; si une future version de Hermes renommait ce marqueur, il passe à la place une requête qui lève
l'erreur dès que le transport la lit. Le crochet `transform_api_error_classification` classe ensuite
l'erreur comme refus définitif (pas de nouvelle tentative). Une erreur interne de la garde refuse aussi
l'appel (mode strict, modèle principal seulement) ; toute autre erreur du plugin le laisse passer.

## Pseudonymes stables : l'en-tête de session

La passerelle numérote les valeurs pseudonymisées (`«PERSON_1»`, `«EMAIL_2»`…) par conversation quand la
requête porte `X-LLMGW-Session`. Sans lui, chaque requête repartirait de zéro et le modèle verrait « Jean »
devenir `PERSON_1` puis `PERSON_3` d'un message à l'autre.

- L'installateur règle `providers.llmgw.session_affinity_header: X-LLMGW-Session` : Hermes envoie alors
  lui-même la clé de la conversation (la racine de la session, stable même après compression du
  contexte) sur **chaque** appel à la passerelle, tâches auxiliaires comprises, et jamais ailleurs. Cela
  marche aussi avec `--no-plugin`.
- Le plugin garde cette valeur, la normalise (1 à 128 caractères `[A-Za-z0-9._:-]`, sinon empreinte
  sha256 de 32 caractères hexadécimaux, la même règle que la passerelle) et ajoute `X-LLMGW-Client`.
- Pour les tâches auxiliaires, que le plugin ne peut pas modifier, l'installateur met aussi
  `X-LLMGW-Client` dans `providers.llmgw.extra_headers`, que Hermes n'envoie qu'à cette adresse
  (relancez l'installateur après une mise à jour de Hermes pour en rafraîchir la version).
- La passerelle garde la table d'une conversation un temps limité après sa dernière requête (24 h par
  défaut) ; `/llmgw` montre ce qu'elle garde pour la session en cours (nombres seulement, jamais les
  valeurs).

## La commande /llmgw

```text
LLMGW by Vulpoint.com · plugin Hermes 1.0.0
Passerelle : http://127.0.0.1:8088 · version 1.5.0
Utilisateur : alice (Test) · profil « default » (Profil de test)
Mode : strict, les appels du modèle principal hors passerelle sont refusés
Modèle principal : llmgw / deepseek-flash
Quotas : 30 req/min · illimité (req/jour) · 1 000 000 jetons/jour · illimité (jetons/mois)
Consommation : aujourd'hui 12 requêtes, 34 567 jetons · ce mois 120 requêtes, 1 234 567 jetons
Session : 20261019_101500_ab12cd · 4 valeurs pseudonymisées (PERSON 3, EMAIL 1) · conservées jusqu'au 20/10/2026 10:15
Contournements vus dans ce processus : 1
  - [signalé] vision · nous · https://inference-api.nousresearch.com · modèle m · signalé à la passerelle
```

## Signalement des contournements

Un appel LLM hors passerelle est signalé par `POST <url>/v1/events` :
`{"kind": "bypass", "provider": …, "base_url": …, "model": …, "task": "main" | <tâche auxiliaire>}`, avec la
clé et les deux en-têtes. Jamais de contenu ; `base_url` réduit à `schéma://hôte:port`. Envoi en arrière-plan
(délai 3 s), une seule fois par (fournisseur, adresse, tâche) et par processus Hermes : l'agent n'attend
jamais. Un appel refusé en mode strict n'est pas un contournement et n'est pas signalé (il apparaît dans
`/llmgw`). La passerelle l'inscrit dans son journal d'audit (« contournement de la passerelle »).

## Dépannage

| Symptôme | Piste |
|---|---|
| `passerelle injoignable` à l'installation | `--url` (sans `/v1`, avec le port) ; la passerelle tourne ? `curl <url>/v1/models -H "Authorization: Bearer gw-…"` |
| `la passerelle refuse la clé (HTTP 401)` | clé erronée, désactivée ou sans profil |
| `modèle … non autorisé` | choisir dans la liste affichée ; le profil de la clé fixe les cibles |
| `Hermes utilise … et non …/config.yaml` | un profil actif (`hermes profile list`) : passer `--hermes-home ~/.hermes/profiles/<nom>` |
| `[LLMGW] Mode strict, appel refusé` dans Hermes | le modèle choisi ne passe pas par la passerelle : `/model <modèle> --provider llmgw` ; ou `mode: warn` |
| `/llmgw` : `LLMGW_API_KEY absent` | `hermes config set LLMGW_API_KEY gw-…` (va dans le `.env`), ou relancer l'installateur |
| la tâche vision échoue | le modèle principal ne lit pas les images : `hermes config set auxiliary.vision.model <modèle de la passerelle>` |
| le plugin ne se charge pas | `hermes plugins doctor llmgw`, `hermes plugins list`, `~/.hermes/logs/agent.log` (lignes `LLMGW`) |
| rien ne change après l'installation | redémarrer les sessions Hermes et `hermes gateway restart` |

## Retour arrière

```bash
integrations/hermes/uninstall.sh [--hermes-home <dossier>] [--dry-run] [--backup <fichier>]
```

- restaure la configuration d'avant le kit : la sauvegarde `config.yaml.bak-llmgw-<date>` faite par la
  **première** installation depuis la dernière désinstallation (une seconde installation sauvegarde un
  fichier déjà converti) ; `--backup` en choisit une autre ;
- garde le `config.yaml` actuel en `config.yaml.bak-before-uninstall-<date>` ;
- désactive le plugin et le déplace en `plugins/.disabled-llmgw-<date>` (son `plugin.yaml` est renommé
  `plugin.yaml.disabled` pour que Hermes l'ignore) ;
- laisse la clé dans le `.env` : `hermes config unset LLMGW_API_KEY` pour la retirer.

À la main : `cp ~/.hermes/config.yaml.bak-llmgw-<date> ~/.hermes/config.yaml` puis
`mv ~/.hermes/plugins/llmgw ~/.hermes/plugins/.disabled-llmgw-<date>`.

## Tests

```bash
integrations/hermes/tests/run_tests.sh -v          # plugin + installateur (~3 min)
integrations/hermes/tests/run_tests.sh --plugin-only
```

Les tests tournent avec le Python de Hermes (`hermes --run-module unittest`), dans des dossiers Hermes
temporaires (jamais `~/.hermes`), contre une fausse passerelle locale (`tests/fake_gateway.py`). Ils
chargent le plugin avec le vrai chargeur de Hermes et passent par ses vraies chaînes `llm_request`,
`llm_execution`, `pre_auxiliary_call` et son classement d'erreurs ; l'installateur est exécuté de bout en
bout (plan, `--dry-run`, idempotence, `.env` 600, sauvegarde, `hermes plugins doctor`, désinstallation).
Aucun agent n'est lancé, aucun fournisseur appelé. La première exécution prépare un dossier « graine »
(quelques minutes, ~1 Go dans `$TMPDIR`), réutilisé ensuite.

## Limites

- **Tâches auxiliaires** : `pre_auxiliary_call` est un observateur ; un plugin ne peut ni les bloquer ni
  modifier leurs requêtes. D'où le routage par l'installateur et l'en-tête de session natif de Hermes.
- La garde couvre la boucle d'agent de Hermes (modèle principal, sous-agents, cron, revues). Les chemins
  qui ne passent pas par elle (par exemple le runtime « codex app-server ») ne sont pas vus.
- La garde suppose `plugins.isolation: in_process` (défaut de Hermes) ; en mode `host`, le plugin tourne
  dans un autre processus et le refus n'est pas garanti (l'installateur et le plugin le signalent).
- `/llmgw` montre la session de la dernière requête vue par ce processus Hermes ; sur une passerelle de
  messagerie qui sert plusieurs conversations à la fois, ce peut être celle d'un autre fil.
- Hermes présente un refus comme une erreur de fournisseur non relançable ; selon l'interface, son texte
  générique peut parler d'une « policy » du compte : le détail `[LLMGW] …` est dans le message d'erreur et
  dans `agent.log` (le rendu exact n'a pas été vérifié dans une vraie session).
- Le web, le navigateur, la voix, les images et les vidéos ne sont pas des appels LLM relayables : le plugin
  ne les voit pas.

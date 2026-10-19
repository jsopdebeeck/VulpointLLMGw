# LLMGW by Vulpoint.com : gateway LLM avec anonymisation

[English version](README.md)

Version **1.4.0** · notes de version dans [RELEASE_NOTES.md](RELEASE_NOTES.md) · documentation intégrée sur `/docs/` (administration) et `/docs/chat.html` (guide utilisateur du chat), sources dans [docs/](docs/).

<p align="center"><img src="docs/img/architecture-fr.svg" alt="Architecture de LLMGW : utilisateurs, agents et applications de la zone de confiance ne joignent les LLM que par la passerelle, en bordure de zone ; les accès directs vers Internet sont bloqués" width="100%"></p>

Un seul binaire Go, sans dépendance externe (SQLite et dashboard embarqués), pour Mac et Linux.

```
clients (API OpenAI / Anthropic)  →  llmgw  →  LLM cibles (OpenAI, Anthropic, Mistral, Ollama, vLLM…)
          clé gateway → utilisateur → profil → cibles autorisées (ordre = repli)
          anonymisation à l'aller, restauration au retour (streaming compris)
          métriques SQLite → /dashboard
```

## Démarrer

```bash
cp config.example.toml config.toml     # puis adapter cibles, profils, utilisateurs
export MISTRAL_API_KEY=… ANTHROPIC_API_KEY=… OPENAI_API_KEY=…
./llmgw -config config.toml -check     # valide la configuration
./llmgw -config config.toml            # http://127.0.0.1:8080/dashboard
./llmgw -version                       # affiche la version
```

### App macOS (barre des menus)

`macapp/build.sh` construit une **LLMGW.app autonome** dans `dist/` (`macapp/build.sh --install` l'installe aussi dans Applications), ainsi que `dist/LLMGW-<version>.dmg` pour l'installer par glisser-déposer dans Applications (Go et les Command Line Tools d'Xcode servent à la construction, rien n'est nécessaire à l'exécution). La passerelle est embarquée dans l'app, en binaire universel (Apple Silicon et Intel) qui n'utilise que des bibliothèques fournies avec macOS. Les données sont dans `~/Library/Application Support/LLMGW` : au premier lancement, l'app y crée une configuration de départ avec un jeton d'administration aléatoire et un modèle `secrets.env` (droits 600). Le menu permet de modifier la configuration et les secrets, de copier le jeton d'administration et de choisir un autre dossier de données (par exemple une installation existante). Double-cliquer : une icône apparaît dans la barre des menus, avec une pastille d'état (verte en marche, orange au démarrage, rouge arrêtée) et un menu pour démarrer, arrêter ou redémarrer la passerelle, ouvrir le dashboard, le chat et la documentation, afficher le journal (`llmgw.log`) et ouvrir l'app à l'ouverture de session. L'app charge `secrets.env`, démarre la passerelle à son ouverture et l'arrête quand on la quitte. Menu en anglais, français ou néerlandais. Sans l'app, `start.command` lance la passerelle dans le Terminal en double-clic et ouvre le dashboard.

Construire : `go build -o llmgw .` ; `./build.sh` produit l'app macOS et les archives Linux (voir Construire plus bas).

Côté client, il suffit de changer l'URL et la clé :

| Client | Réglage |
|---|---|
| SDK OpenAI / outils compatibles | `base_url = http://gateway:8080/v1`, `api_key = <clé gateway>` |
| SDK Anthropic / Claude Code | `ANTHROPIC_BASE_URL=http://gateway:8080`, `ANTHROPIC_API_KEY=<clé gateway>` |

Modèle : `mistral-large` (routage automatique selon le profil) ou `mistral/mistral-large` (cible imposée).
`GET /v1/models` liste les modèles autorisés pour l'utilisateur, `GET /v1/me` son profil, ses quotas et sa consommation.

## Construire

```bash
./build.sh                            # tests, puis app macOS + DMG et archives Linux dans dist/
./build.sh --linux-only --skip-tests  # archives Linux seulement
./build.sh --mac-only                 # LLMGW.app et DMG seulement
```

`dist/` contient alors `LLMGW.app` et `LLMGW-<version>.dmg` (Apple Silicon et Intel), `llmgw-<version>-linux-amd64.tar.gz` et `-arm64.tar.gz` (binaire statique, `setup.sh`, service systemd, Caddyfile, `LISEZMOI.txt`), et `SHA256SUMS`.

## Déployer sur un VPS

Un VPS Linux (Ubuntu 22.04 / 24.04 ou Debian 12, systemd) suffit : 1 vCPU et 1 à 2 Go de RAM (2 vCPU pour un trafic d'agents soutenu, voir Performances). La passerelle voit les données en clair avant l'anonymisation : choisir un hébergeur UE sous droit UE (OVHcloud, Scaleway, Hetzner, Infomaniak…) et signer un contrat de sous-traitance (art. 28 RGPD). Le serverless ne convient pas : ni disque persistant, ni tâches de fond, ni streaming long garanti.

**Sur un serveur neuf**, `setup.sh` (dans l'archive Linux) installe ou met à jour tout ; il se relance sans risque :

```bash
scp dist/llmgw-<version>-linux-amd64.tar.gz admin@mon-vps:
ssh admin@mon-vps
tar xzf llmgw-<version>-linux-amd64.tar.gz && cd llmgw-<version>
sudo ./setup.sh --domain gateway.exemple.be --email it@exemple.be --admin-ip 203.0.113.7
```

- Paquets système, mises à jour de sécurité automatiques, fail2ban pour SSH, pare-feu ufw (SSH d'abord, puis 80 et 443).
- Utilisateur système `llmgw`, `/opt/llmgw` (700), service systemd durci, `secrets.env` (600) pour les clés des fournisseurs.
- Première installation : `config.toml` généré avec un jeton d'administration aléatoire (affiché une fois, gardé dans `/opt/llmgw/admin_token.txt`), des clés d'exemple aléatoires, l'écoute en local, les proxys de confiance et les adresses d'administration dans `[security]`.
- Avec `--domain` : Caddy et son certificat Let's Encrypt ; le dashboard n'est joignable que depuis les adresses `--admin-ip` (sinon par tunnel SSH : `ssh -L 8080:127.0.0.1:8080 mon-vps`).
- Le nouveau binaire valide la configuration avant de remplacer l'ancien ; s'il ne démarre pas, la version précédente est rétablie.
- Sauvegarde quotidienne de la base et de la configuration dans `/var/backups/llmgw` (14 jours).

**Depuis le Mac**, `deploy/deploy.sh` fait tout en une commande (compilation, copie, `setup.sh`) :

```bash
deploy/deploy.sh --domain gateway.exemple.be --email it@exemple.be --admin-ip 203.0.113.7 admin@mon-vps
deploy/deploy.sh admin@mon-vps       # mise à jour : configuration, base et Caddy conservés
```

Mettre ensuite les clés des fournisseurs dans `/opt/llmgw/secrets.env` (`MISTRAL_API_KEY=…`), puis `systemctl restart llmgw`. `setup.sh` ne touche pas à SSH : désactivez vous-même la connexion par mot de passe une fois votre clé en place. `setup.sh` n'a été testé qu'en environnement simulé : l'essayer d'abord sur un VPS jetable.

**Sauvegardes.** Utiliser `sqlite3 gateway.db ".backup …"`, jamais une simple copie du fichier pendant que le service tourne. Ne pas placer la base dans un dossier synchronisé (Dropbox…) ou réseau : SQLite en mode WAL s'y corrompt.

**Windows.** Le binaire compile (`GOOS=windows`), mais les chemins absolus de `words_file` sont mal gérés, il n'y a pas de service Windows natif (passer par NSSM ou WinSW) et les droits `0600` sont sans effet. Linux reste la cible recommandée.

## Authentification SSO (Entra ID, Google)

Le portail `/chat` et le dashboard acceptent une connexion **Microsoft Entra ID**, **Google** ou tout fournisseur **OpenID Connect**, en plus des clés gateway (toujours utilisées par l'API) et de `admin_token`. Section `[auth]` de `config.toml` (exemple commenté dans `config.example.toml`).

- **Profil** : un `[users.x]` avec `email = …` garde son profil et ses quotas ; sinon la première règle `mappings` qui correspond (groupe Entra, rôle d'application, motif d'email) ; sinon `default_profile` ; sinon refus.
- **Administration** : comptes `[[admins]]` avec le rôle Admin (tout) ou Support (utilisateurs, statistiques, Debug sur autorisation), ainsi que les règles `admin_roles`, `admin_groups` ou `admin_emails` des fournisseurs. Ils accèdent au dashboard sans jeton.
- **Sécurité** : code d'autorisation avec PKCE, `state` et `nonce` ; signature RS256 du jeton d'identité vérifiée sur les clés publiées par le fournisseur ; cookie de session signé, `HttpOnly`, `SameSite=Lax`, `Secure` en HTTPS ; les appels de l'interface exigent en plus l'en-tête `X-LLMGW-Session`, ce qui bloque les requêtes intersites.
- La correspondance groupes → profil est évaluée à la connexion : un changement de groupe s'applique à la session suivante (`session_hours`, 12 h par défaut).

**Entra ID** : Portail Azure → Entra ID → Inscriptions d'applications → Nouvelle inscription, type « Web », adresse de retour `https://<passerelle>/auth/callback/entra`. Noter l'ID de tenant et l'ID client, créer un secret client (`ENTRA_CLIENT_SECRET`). Pour les profils par groupe : « Configuration du jeton » → ajouter la revendication de groupes (de préférence « Groupes affectés à l'application », pour rester sous la limite de 200 groupes du jeton). Pour l'administration : déclarer un rôle d'application (par exemple `LLMGW.Admin`) et l'affecter dans « Applications d'entreprise ».

**Google** : Google Cloud Console → API et services → Identifiants → ID client OAuth, type « Application Web », URI de redirection `https://<passerelle>/auth/callback/google`. Écran de consentement « Interne » pour un Google Workspace. Google ne transmet pas les groupes : les profils s'attribuent par domaine (`allowed_domains`, `default_profile`), par email (`mappings`) ou par utilisateur déclaré.

Au démarrage, le journal rappelle l'adresse de retour de chaque fournisseur. La page Alertes et SSO du dashboard les affiche aussi.

## Fonctionnement

**Routage.** La clé identifie l'utilisateur, l'utilisateur a un profil, le profil liste les cibles autorisées dans l'ordre de préférence. La première cible qui accepte le format d'API et le modèle est utilisée. En cas d'erreur réseau, 5xx ou 429, la suivante prend le relais.

**Anonymisation.** Les détecteurs (`[[detectors]]`) repèrent emails, téléphones, IBAN, NISS, numéros INAMI / RIZIV des prestataires de soins (clé vérifiée), cartes, IP, noms précédés d'un titre, listes de noms ou d'entreprises et motifs personnalisés (numéros de dossier, identifiants…). Selon le profil, chaque type est :
- `anonymize` : remplacé par `«TYPE_n»`, puis restauré dans la réponse ;
- `block` : requête refusée (403) et comptée comme violation ;
- `log` : laissé passer, mais compté ;
- `off` : ignoré.

Détecteurs intégrés supplémentaires : adresses (`address`), localités belges et lieux de résidence (`place`), animaux (`pet`), prénoms seuls FR/NL/EN (`firstname_alone`), hôpitaux et réseaux de soins belges (`be_health_orgs`), identifiants après un mot de contexte (`ref_context`), dates de naissance (`birthdate`) ou toutes les dates (`date`). Termes jamais anonymisés : liste intégrée (produits, sigles techniques) et `[privacy] allow`.

Un nom repéré une fois (« Mme Claire Dubois ») est aussi remplacé ailleurs dans la conversation (« Dubois »). La numérotation est stable pour une requête donnée, donc le cache de prompt des fournisseurs reste efficace.

**Images et fichiers.** Par profil : `images` vaut `pass` (non changée), `blank` (remplacée par un carré blanc avant le LLM) ou `block` (refusée et journalisée) ; `files` donne une action par extension : `pass_log` (transmis et journalisé), `blank` (carré blanc), `block` (refusé et journalisé) ou `block_alert` (refusé, alerte email et journal) ; `files_default` s'applique aux extensions non listées. Le journal des fichiers (date, utilisateur, profil, LLM, nom du fichier, extension, taille, SHA-256, action) est conservé 30 jours, même sans mode Debug ; le contenu n'est jamais stocké.

**Restauration.** Les jetons sont remplacés dans la réponse, y compris en streaming SSE : un jeton coupé entre deux morceaux est retenu jusqu'à être complet. Les blocs `thinking` signés ne sont pas modifiés.

**Métriques.** Chaque requête enregistre utilisateur, cible, modèle, statut, octets, tokens et temps IN (lecture client), GATEWAY (traitement), LLM (cible) et premier octet, avec les compteurs de détection par type. **Aucune valeur sensible n'est stockée.** Après `raw_retention_days` (30 j), le détail est agrégé par jour × utilisateur × cible. Le modèle et les percentiles sont alors perdus, et les agrégats sont conservés environ 6 mois (`agg_retention_days = 200`). Le dashboard propose 48 h, 60 j, 6 semaines et 6 mois (profondeur maximale).

**Quotas.** Par profil (pour chaque utilisateur), surchargeables par utilisateur, et par LLM cible (budget partagé) : requêtes/minute, requêtes/jour, tokens/jour, tokens/mois. Si un quota est dépassé, la requête est bloquée (429) ou simplement journalisée (alerte). Une cible saturée est sautée au profit de la suivante du profil.

**Dashboard (`/dashboard`).** Pages regroupées : Pilotage (Vue d'ensemble, Reporting), Accès (Utilisateurs, Profils), Modèles (LLM cibles avec voyant de connexion, Quotas), Données (Filtrage et privacy : détecteurs, règles par profil, fichiers par extension, exceptions, banc de test ; Journal des fichiers ; Debug), Paramètres (Alertes et SSO, Comptes d'administration, Journal d'audit). Les modifications sont validées puis écrites dans `config.toml` (10 dernières versions gardées en `.bak-*`, enregistrement refusé si le fichier a changé entre-temps) et appliquées à chaud. Les clés ne sont jamais renvoyées au navigateur. Bouton Documentation et version en bas du menu.

**Chat (`/chat`).** Fenêtre de chat pour les utilisateurs : connexion SSO ou par clé gateway, choix du modèle autorisé, pièces jointes (txt, md, csv, json, PDF, docx : texte extrait dans le navigateur, puis anonymisé par le gateway ; images selon le profil), historique des 10 dernières conversations dans le navigateur. Sous chaque échange, le nombre de données pseudonymisées et le LLM qui a répondu. Bouton « ? Aide » vers le guide utilisateur. L'extraction PDF/docx charge pdf.js et mammoth depuis cdnjs.

**Prompt de comportement et alertes.** Chaque profil peut porter un `system_prompt`, ajouté en tête de toutes ses requêtes (après l'anonymisation, donc non modifié), et des `alert_emails`. Section `[alerts]` : serveur SMTP (587 STARTTLS ou 465 TLS), destinataires par défaut, déclencheurs (violation, quota, erreur LLM) et fréquence du récapitulatif. Les fichiers en `block_alert` sont toujours signalés. Les emails ne contiennent aucune valeur sensible. Bouton « Envoyer un test » dans le dashboard.

**Comptes d'administration et audit.** Rôles Admin et Support vérifiés côté serveur. Le journal d'audit trace les connexions, les enregistrements de configuration (liste des changements, sans secret), les consultations et purges Debug, les tests d'alerte ; conservé 400 jours.

**Langues de l'interface.** Le dashboard et le chat s'affichent en anglais (par défaut), français ou néerlandais, au choix dans chaque interface. Les données ne sont jamais traduites.

**Mode DEBUG (onglet Debug).** Désactivé par défaut. Une fois activé, chaque échange est archivé : la requête telle que reçue par le LLM (anonymisée), la réponse brute du LLM avant restauration, les correspondances jeton → valeur d'origine et chaque mot détecté avec l'action appliquée. La vue détaillée bascule entre « vue anonymisée » (jetons en gras) et « vue d'origine » (valeurs en gras, en bleu si anonymisées, en rouge si bloquées, en orange si journalisées). Les valeurs sont stockées **en clair** : purge automatique après `retention_hours` (24 h par défaut), bandeau « DEBUG actif » dans le dashboard, bouton pour vider l'archive.

**Configuration.** `config.toml` est rechargé à chaud : une configuration invalide est refusée et l'ancienne reste active.

**Protection web (`[security]`, page Sécurité web).** Pour une passerelle exposée sur Internet ; active par défaut. Limites par adresse (600 requêtes/min, 32 simultanées ; IPv6 par /64), blocage temporaire après 10 connexions refusées en 10 min (clé ou jeton invalide, cookie falsifié, lien inventé : 30 min, doublées à chaque récidive, 24 h au plus ; journalisé et signalé par email), liste d'adresses pour l'administration, adresse réelle du client derrière les proxys de confiance (un `X-Forwarded-For` envoyé directement par un client est ignoré), en-têtes de sécurité (CSP, HSTS en HTTPS…), HTTPS intégré en option (`tls_cert`, `tls_key`), travail d'anonymisation plafonné à deux fois le nombre de cœurs. Les appels locaux (app Mac, tunnel SSH) ne sont jamais limités. Sessions web : rôle recalculé à chaque requête, déconnexion qui révoque la session, bouton « Fermer toutes les sessions ». Une clé enregistrée n'est conservée que pour l'adresse à laquelle elle était destinée.

**Demandes d'accès (`[access]`, page Demandes d'accès).** « Demander un accès » sur l'écran de connexion du chat : nom, email, motif. La personne confirme son adresse par un lien à usage unique envoyé par email (une page avec un bouton, car les filtres de messagerie ouvrent les liens). Domaine de confiance : accès accordé tout de suite avec le profil par défaut, clé gateway affichée une fois. Autre domaine : décision dans le dashboard (Admin ou Support), refus après 14 jours sans décision ; l'approbation envoie un lien à usage unique vers la clé. La clé n'est jamais envoyée par email. Prérequis : le serveur SMTP de `[alerts]` et l'adresse publique `[auth] base_url`.

## Performances

Mesures sur Apple M5 (10 cœurs), binaire arm64 natif, corpus de prose médicale en français avec une phrase de données personnelles par Ko. L'anonymisation est la seule étape coûteuse en CPU : la passerelle ajoute bien moins d'une milliseconde pour le routage, plus le temps d'anonymisation ci-dessous ; la latence propre du LLM (plusieurs secondes) domine.

| Mesure (anonymisation, profil standard) | 1.3.0 | 1.4.0 |
|---|---|---|
| 1 Ko de texte | 1,9 ms | 0,36 ms |
| 10 Ko | 21 ms | 4,4 ms |
| 100 Ko, une requête (détecteurs en parallèle) | 250 ms | 15 ms |
| historique de 100 Ko renvoyé par un agent, tour suivant (cache) | 200 ms | 4 ms |
| 110 Ko avec 2 000 noms dans la mémoire des noms | 1,8 s | 50 ms |

| Débit de bout en bout, texte nouveau à chaque requête (pire cas) | 10 cœurs | 2 cœurs |
|---|---|---|
| requêtes de 2 Ko | ~5 200 req/s (1.3.0 : ~1 100) | ~1 700 req/s |
| requêtes de 50 Ko | ~190 req/s (1.3.0 : ~38) | ~67 req/s |
| requêtes de 50 Ko dont le texte a déjà été vu (historique d'agent) | ~1 700 req/s | ~620 req/s |

Un cœur de VPS d'entrée de gamme est plus lent qu'un cœur M5 : comptez la moitié de ces chiffres. Même ainsi, 2 vCPU absorbent plusieurs Mo de texte nouveau par seconde, bien plus que ce que les fournisseurs de LLM délivrent : la passerelle n'est pas le goulet d'étranglement. Le cache entre requêtes occupe jusqu'à ~64 Mo de mémoire.

## Limites actuelles

- Noms et entreprises : détectés s'ils figurent dans une liste ou suivent un titre (M., Mme, Dr…). Sans liste, il faudrait un modèle NER (Presidio, spaCy…) branché en service.
- Logos et texte dans les images : pas d'analyse d'image. On choisit par profil de laisser passer, remplacer par un carré blanc ou bloquer. Un modèle sans prise en charge des images peut refuser une requête qui contient le carré blanc.
- Un nom appris à un tour donné est aussi remplacé dans les messages précédents, qui changent donc au tour suivant. Les noms d'une fiche sans libellé reconnu (« Nom : Vandamme »), les prénoms qui sont aussi des mots courants (Max, Jan, Hugo) et l'Unicode décomposé (NFD) peuvent échapper à la détection.
- Le LLM doit recopier les jetons `«…»`. La consigne `placeholder_hint` le lui demande. Un jeton reformulé par le modèle (traduit, sans guillemets) n'est pas restauré.
- Pas de traduction entre formats : un client OpenAI n'est routé que vers des cibles `api = "openai"`, et un client Anthropic que vers des cibles `api = "anthropic"`.

## Fichiers

| Fichier | Rôle |
|---|---|
| `main.go` | serveur, routes, rechargement à chaud, version |
| `proxy.go` | authentification, routage, repli, relais normal et SSE |
| `anon.go` | détecteurs, anonymisation, restauration, images et fichiers |
| `names.go`, `context.go`, `places.go` | heuristiques de noms, de contexte et de lieux |
| `config.go` | chargement et validation de la configuration |
| `auth.go` | SSO OpenID Connect (Entra ID, Google, OIDC), sessions |
| `audit.go` | comptes d'administration, rôles, journal d'audit |
| `admin.go` | API d'admin : configuration, consommation, test des cibles |
| `filelog.go` | journal des fichiers (30 jours) |
| `debug.go` | archive DEBUG |
| `quota.go` | compteurs de consommation et quotas |
| `alerts.go` | alertes email |
| `store.go` | SQLite, agrégation, statistiques |
| `dashboard.html`, `chat.html`, `i18n.js` | dashboard, chat et traductions EN/FR/NL (embarqués) |
| `docs/` | documentation intégrée : administration, guide utilisateur du chat, captures |
| `RELEASE_NOTES.md` | notes de version |
| `gateway_test.go` | tests de bout en bout avec faux LLM : `go test ./...` |
| `anon_test.go` | tests de non-régression et mesures de l'anonymisation (`go test -bench Anon`) |
| `macapp/LLMGW.swift`, `macapp/build.sh` | app macOS autonome de la barre des menus (passerelle embarquée) et son script de construction (app + DMG) |
| `start.command` | démarrage en double-clic dans le Terminal (macOS) |
| `shield.go` | protection web : adresse du client, limites, blocages, en-têtes de sécurité |
| `access.go` | demandes d'accès : liens de confirmation, approbation, récupération de la clé |
| `build.sh` | app macOS + DMG et archives Linux dans `dist/` |
| `deploy/setup.sh`, `deploy/LISEZMOI.txt` | installation sur un serveur Ubuntu / Debian neuf (dans l'archive Linux) |
| `deploy/deploy.sh` | compilation, copie et `setup.sh` sur un VPS en une commande (`--help`) |
| `deploy/llmgw.service` | service systemd durci |
| `deploy/Caddyfile` | HTTPS, streaming SSE et accès admin restreint |

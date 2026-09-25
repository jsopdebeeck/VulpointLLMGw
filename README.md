# llmgw : gateway LLM avec anonymisation

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
```

Construire : `go build -o llmgw .`. Pour Linux depuis le Mac : `GOOS=linux GOARCH=amd64 go build -o dist/llmgw-linux-amd64 .`

Côté client, il suffit de changer l'URL et la clé :

| Client | Réglage |
|---|---|
| SDK OpenAI / outils compatibles | `base_url = http://gateway:8080/v1`, `api_key = <clé gateway>` |
| SDK Anthropic / Claude Code | `ANTHROPIC_BASE_URL=http://gateway:8080`, `ANTHROPIC_API_KEY=<clé gateway>` |

Modèle : `mistral-large` (routage automatique selon le profil) ou `mistral/mistral-large` (cible imposée).
`GET /v1/models` liste les modèles autorisés pour l'utilisateur, `GET /v1/me` son profil, ses quotas et sa consommation.

## Fonctionnement

**Routage.** La clé identifie l'utilisateur, l'utilisateur a un profil, le profil liste les cibles autorisées dans l'ordre de préférence. La première cible qui accepte le format d'API et le modèle est utilisée. En cas d'erreur réseau, 5xx ou 429, la suivante prend le relais.

**Anonymisation.** Les détecteurs (`[[detectors]]`) repèrent emails, téléphones, IBAN, NISS, cartes, IP, noms précédés d'un titre, listes de noms ou d'entreprises et motifs personnalisés (numéros de dossier, identifiants…). Selon le profil, chaque type est :
- `anonymize` : remplacé par `«TYPE_n»`, puis restauré dans la réponse ;
- `block` : requête refusée (403) et comptée comme violation ;
- `log` : laissé passer, mais compté ;
- `off` : ignoré.

Un nom repéré une fois (« Mme Claire Dubois ») est aussi remplacé ailleurs dans la conversation (« Dubois »). La numérotation est stable pour une requête donnée, donc le cache de prompt des fournisseurs reste efficace. Images et fichiers : `pass`, `strip` ou `block` par profil.

**Restauration.** Les jetons sont remplacés dans la réponse, y compris en streaming SSE : un jeton coupé entre deux morceaux est retenu jusqu'à être complet. Les blocs `thinking` signés ne sont pas modifiés.

**Métriques.** Chaque requête enregistre utilisateur, cible, modèle, statut, octets, tokens et temps IN (lecture client), GATEWAY (traitement), LLM (cible) et premier octet, avec les compteurs de détection par type. **Aucune valeur sensible n'est stockée.** Après `raw_retention_days` (30 j), le détail est agrégé par jour × utilisateur × cible. Le modèle et les percentiles sont alors perdus, et les agrégats sont conservés 3 ans. Le dashboard propose 48 h, 60 j, 6 semaines, 12 mois et 3 ans.

**Quotas.** Par profil (pour chaque utilisateur), surchargeables par utilisateur, et par LLM cible (budget partagé) : requêtes/minute, requêtes/jour, tokens/jour, tokens/mois. Si un quota est dépassé, la requête est bloquée (429) ou simplement journalisée (alerte). Une cible saturée est sautée au profit de la suivante du profil.

**Dashboard (`/dashboard`).** Cinq onglets : Utilisateurs & règles (clients, clés, profils, ordre des LLM), LLM cibles (connexion, clés, test `/models`), Filtrage & privacy (détecteurs et matrice type × profil, test d'anonymisation), Quotas (limites et jauges en direct), Reporting. Les modifications sont validées puis écrites dans `config.toml` (10 dernières versions gardées en `.bak-*`) et appliquées à chaud. Les clés ne sont jamais renvoyées au navigateur.

**Chat (`/chat`).** Fenêtre de chat pour les utilisateurs : connexion avec la clé gateway, choix du modèle autorisé, pièces jointes (txt, md, csv, json, PDF, docx : texte extrait dans le navigateur, puis anonymisé par le gateway ; images selon le profil). Sous chaque échange, le nombre de données pseudonymisées et le LLM qui a répondu. L'extraction PDF/docx charge pdf.js et mammoth depuis cdnjs.

**Prompt de comportement et alertes.** Chaque profil peut porter un `system_prompt`, ajouté en tête de toutes ses requêtes (après l'anonymisation, donc non modifié), et des `alert_emails`. Section `[alerts]` : serveur SMTP (587 STARTTLS ou 465 TLS), destinataires par défaut, déclencheurs (violation, quota, erreur LLM) et fréquence du récapitulatif. Les emails ne contiennent aucune valeur sensible. Bouton « Envoyer un test » dans le dashboard.

**Mode DEBUG (onglet Debug).** Désactivé par défaut. Une fois activé, chaque échange est archivé : la requête telle que reçue par le LLM (anonymisée), la réponse brute du LLM avant restauration, les correspondances jeton → valeur d'origine et chaque mot détecté avec l'action appliquée. La vue détaillée bascule entre « vue anonymisée » (jetons en gras) et « vue d'origine » (valeurs en gras, en bleu si anonymisées, en rouge si bloquées, en orange si journalisées). Les valeurs sont stockées **en clair** : purge automatique après `retention_hours` (24 h par défaut), bandeau « DEBUG actif » dans le dashboard, bouton pour vider l'archive.

**Configuration.** `config.toml` est rechargé à chaud : une configuration invalide est refusée et l'ancienne reste active.

## Limites actuelles

- Noms et entreprises : détectés s'ils figurent dans une liste ou suivent un titre (M., Mme, Dr…). Sans liste, il faudrait un modèle NER (Presidio, spaCy…) branché en service.
- Logos dans les images : pas d'analyse d'image. On choisit par profil de laisser passer, retirer ou bloquer.
- Le LLM doit recopier les jetons `«…»`. La consigne `placeholder_hint` le lui demande. Un jeton reformulé par le modèle (traduit, sans guillemets) n'est pas restauré.
- Pas de traduction entre formats : un client OpenAI n'est routé que vers des cibles `api = "openai"`, et un client Anthropic que vers des cibles `api = "anthropic"`.

## Fichiers

| Fichier | Rôle |
|---|---|
| `main.go` | serveur, rechargement à chaud |
| `admin.go` | API d'admin : configuration, quotas, test des cibles |
| `quota.go` | compteurs de consommation et quotas |
| `chat.html` | fenêtre de chat (embarquée) |
| `proxy.go` | authentification, routage, repli, relais normal et SSE |
| `anon.go` | détecteurs, anonymisation, restauration |
| `store.go` | SQLite, agrégation, statistiques |
| `config.go` | chargement et validation de la configuration |
| `dashboard.html` | dashboard (embarqué dans le binaire) |
| `gateway_test.go` | tests de bout en bout avec faux LLM : `go test ./...` |

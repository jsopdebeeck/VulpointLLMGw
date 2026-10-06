# LLMGW by Vulpoint.com : notes de version

Le numéro suit le schéma `majeur.mineur.correctif`. La version en cours s'affiche en bas du menu du dashboard, dans l'en-tête de connexion du chat, sur `GET /version` et avec `./llmgw -version`.

## 1.0.0 (7 octobre 2026)

Première version de référence.

### Passerelle
- Proxy LLM compatible OpenAI (`/v1/chat/completions`) et Anthropic (`/v1/messages`, `count_tokens`), streaming SSE compris.
- Routage par utilisateur et par profil vers les LLM cibles autorisés, dans l'ordre de préférence, avec repli automatique sur erreur 5xx ou 429.
- Alias de modèles par cible, `/v1/models` limité aux modèles réellement utilisables, conversion `json_schema` → `json_object` pour les LLM qui l'exigent.
- Cibles disponibles : OpenAI, Anthropic, Mistral, DeepSeek, GLM, Grok, Azure et LLM locaux (Ollama, vLLM, LM Studio).
- Intégrations testées : Claude Code, Claude Cowork, Hermes, chat intégré.

### Protection des données
- Pseudonymisation bidirectionnelle : les valeurs sensibles sont remplacées par des jetons «TYPE_n» avant l'envoi, puis restaurées dans la réponse, y compris en streaming.
- Détecteurs intégrés : emails, téléphones, IBAN, NISS, cartes, IPv4, dates de naissance, adresses, lieux belges, noms (titre, prénom connu FR/NL/EN), animaux, établissements de soins belges, références après « dossier », « patient », « n° »…
- Détecteurs personnalisés par expression régulière ou par liste de termes.
- Mémoire des noms par empreintes HMAC (fautes de frappe comprises), sans stockage en clair.
- Réduction des faux positifs : lexique, liste « jamais anonymiser », contexte technique, sigles sensibles à la casse.
- Action par type de donnée et par profil : anonymiser, bloquer, journaliser, ignorer.
- Images, réglées par profil : non changée, remplacée par un carré blanc, ou bloquée avec une entrée au journal.
- Fichiers, réglés par profil et par extension : transmis avec une entrée au journal, remplacés par un carré blanc, bloqués avec une entrée au journal, ou bloqués avec une alerte email et une entrée au journal.
- Journal des fichiers : nom, extension, taille, empreinte SHA-256 et action. Il est conservé 30 jours, même sans mode Debug, et ne stocke jamais le contenu.
- Prompt de comportement par profil.

### Accès et sécurité
- Clé gateway personnelle par utilisateur ou application.
- Connexion SSO OpenID Connect : Microsoft Entra ID, Google et tout fournisseur OIDC. Le profil est attribué par domaine, groupe ou rôle.
- Comptes d'administration Admin (tous les droits) et Support (utilisateurs, statistiques, Debug sur autorisation).
- Journal d'audit des connexions, des modifications et des consultations Debug, conservé 400 jours.

### Pilotage
- Quotas par profil, par utilisateur et par LLM : requêtes par minute et par jour, tokens par jour et par mois, avec blocage ou simple alerte.
- Reporting : volumes, temps de réponse (IN, passerelle, LLM), conversions et violations, par utilisateur et par LLM, sur 48 h, 60 jours, 6 semaines ou 6 mois.
- Alertes email groupées en cas de violation, de quota atteint, d'erreur LLM ou de fichier bloqué avec alerte.
- Mode DEBUG : archive temporaire des échanges, avec les valeurs détectées mises en évidence.

### Interfaces
- Dashboard d'administration : vue d'ensemble, utilisateurs, profils, LLM cibles avec voyant de connexion, quotas, filtrage, journal des fichiers, Debug, alertes et SSO, comptes, audit.
- Chat web : connexion par clé ou SSO, choix du modèle, documents joints (PDF, Word, texte), images, historique des 10 dernières conversations.
- Interface en trois langues : anglais (par défaut), français, néerlandais.
- Marque « LLMGW by Vulpoint.com », bouton Documentation dans le dashboard, bouton Aide dans le chat.
- Documentation intégrée sur `/docs/` (fonctionnelle et administration) et `/docs/chat.html` (guide utilisateur du chat).

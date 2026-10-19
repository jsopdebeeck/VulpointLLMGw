# LLMGW by Vulpoint.com : notes de version

Le numéro suit le schéma `majeur.mineur.correctif`. La version en cours s'affiche en bas du menu du dashboard, dans l'en-tête de connexion du chat, sur `GET /version` et avec `./llmgw -version`.

## 1.4.0 (19 octobre 2026)

### Nouveautés
- **Protection web** (section `[security]`, page Sécurité web du dashboard), pour une passerelle exposée sur Internet, active par défaut :
  - limites par adresse : 600 requêtes par minute et 32 simultanées (IPv6 : par /64) ;
  - blocage temporaire d'une adresse après 10 connexions refusées en 10 minutes (clé, jeton d'administration, cookie falsifié, lien inventé) : 30 minutes, doublées à chaque récidive, 24 heures au plus ; inscrit au journal d'audit et signalé par email (`on_security`), avec bouton de déblocage ;
  - administration réservée aux adresses de `admin_allow`, adresse réelle du client lue derrière les proxys de confiance (`trusted_proxies`), réseaux exemptés ;
  - en-têtes de sécurité sur toutes les réponses (Content-Security-Policy, X-Frame-Options, HSTS en HTTPS…), HTTPS intégré en option (`tls_cert`, `tls_key`) ;
  - travail d'anonymisation plafonné à deux fois le nombre de cœurs (au-delà : attente, puis 503) ;
  - les appels locaux (app Mac, tunnel SSH) ne sont jamais limités.
- **Demandes d'accès** (section `[access]`, page Demandes d'accès du dashboard) : lien « Demander un accès » sur l'écran de connexion du chat, confirmation de l'adresse par un lien à usage unique envoyé par email, accès accordé tout de suite pour les domaines de confiance (profil par défaut), décision dans le dashboard pour les autres (Admin ou Support), refus automatique après 14 jours sans décision. La clé gateway n'est jamais envoyée par email : elle s'affiche une seule fois, sur une page ouverte par un lien à usage unique. Emails en anglais, français ou néerlandais selon la langue du demandeur.
- **Sessions web** : rôle et profil recalculés à chaque requête avec la configuration en vigueur ; la déconnexion révoque la session, même si son cookie a été copié ; bouton « Fermer toutes les sessions » (nouvelle clé de signature).
- **Builds** : `build.sh` produit l'app Mac et le DMG, et les archives Linux amd64 et arm64 (binaire, `setup.sh`, service systemd, Caddyfile, `LISEZMOI.txt`) avec `SHA256SUMS`.
- **Installation Linux** : `setup.sh` installe ou met à jour la passerelle sur un serveur Ubuntu 22.04 / 24.04 ou Debian 12 neuf (mises à jour de sécurité automatiques, fail2ban, ufw, Caddy et Let's Encrypt, service durci, configuration générée avec jeton et clés aléatoires, sauvegarde quotidienne, retour à la version précédente en cas d'échec). `deploy/deploy.sh` le lance depuis le Mac en une commande.
### Sécurité
- SSO : une adresse email non vérifiée par le fournisseur est refusée (Google, OIDC ; `trust_unverified_email` pour un fournisseur sans ce marqueur) ; Entra ID : l'UPN fait foi. Auparavant, une inscription libre chez un fournisseur OIDC permettait de se présenter avec l'adresse d'un administrateur.
- Redirection après connexion limitée au chat et au dashboard (« /\t/site » menait ailleurs).
- Une clé enregistrée n'est conservée que pour l'adresse à laquelle elle était destinée : changer l'adresse d'une cible, du fournisseur SSO ou du serveur SMTP demande de ressaisir le secret ; une variable d'environnement qui alimente une destination ne peut pas être envoyée ailleurs depuis le dashboard ; les en-têtes HTTP des cibles ne sont plus renvoyés au navigateur.
- Chat : les compteurs de tokens renvoyés par le LLM sont affichés comme des nombres (un fournisseur malveillant pouvait injecter du code dans la page).
- Base de données en droits 600 ; données purgées écrasées (`secure_delete`).
- Archive Debug purgée toutes les heures, même si Debug est désactivé et que personne ne l'ouvre.
- Le message d'erreur « aucun LLM cible disponible » ne contient plus les adresses des cibles.
- **Anonymisation : fuites corrigées** (revue du moteur, chaque cas couvert par un test) :
  - texte JSON contenu dans une chaîne (résultats d'outils, arguments de fonctions) : la détection travaille sur le texte décodé (`\n`, `\"`, `\uXXXX`) et le JSON reste valide ; auparavant, « Rappel:\nClaire Vandamme » partait en clair ;
  - parties de requête désormais parcourues : documents texte Anthropic, champs `user`, `metadata`, `stop`, nom des messages, descriptions, titres et valeurs d'exemple des outils ;
  - espaces insécables, trait d'union conditionnel, caractères invisibles et chiffres pleine chasse neutralisés avant la détection (téléphone, IBAN, NISS, carte, email, noms) ; IBAN en minuscules ou séparé par des tirets ou des points ; NISS sous les formes 900101/123-95 et 90 01 01 123 95 ; emails à caractères accentués ;
  - « Mme Claude Dubois », « patient Hermes Vandamme » : la liste « jamais anonymiser » ne vaut plus que pour le terme entier (« Claude Code » reste intact) ;
  - chevauchements : l'action la plus stricte l'emporte ; un NISS interdit n'est plus masqué par une référence seulement journalisée ;
  - noms de fichiers pseudonymisés dans le journal des fichiers, les alertes et le message de blocage (`Bilan_«PERSON_1»_«NISS_1».pdf`).
### Corrections
- Quotas utilisateur vérifiés et comptés en une seule opération, avant la lecture du corps : des requêtes parallèles ne peuvent plus dépasser ensemble la limite par minute.
- Une requête interrompue par l'utilisateur (bouton Arrêter) est enregistrée en statut 499, sans alerte « erreur LLM ».
- Connexions aux LLM : délais de connexion et de négociation TLS, lecture des réponses bornée ; serveur : délais d'inactivité et de lecture.
- Métriques : un lot rejeté par une base occupée est réessayé au lieu d'être perdu ; l'archive Debug est écrite par une file bornée.
- Alias de modèles compilés une fois par configuration.
- Les anciennes sauvegardes de `config.toml` (au-delà des 10 dernières) sont déplacées dans `_archive/config/`, plus supprimées.
- Alertes : l'objet des emails mentionne aussi les fichiers bloqués.
- Restauration dans les arguments d'outils (`partial_json`, `arguments`, en streaming ou non) : les valeurs sont insérées échappées ; un nom contenant une tabulation ou un guillemet rendait le JSON invalide.
- Jeton « «_1» » impossible à restaurer quand une valeur apprise réapparaissait avec d'autres espaces ; jetons « « EMAIL_1 » » écrits avec des espaces désormais restaurés.
- Un nom ne déborde plus sur la phrase suivante (« Le patient Dupont. Demain il sort. ») ; les mots appris perdent leur ponctuation.
- Fautes de frappe : deux noms distincts ne sont plus confondus (Dupont, Dumont), les mots courants sont exclus et la forme d'origine est restaurée.
- Lieux appris repris seulement avec leur majuscule (« you can manage », « the forest ») ; Manage, Mol et Jambes retirés de la liste des localités.
- Faux positifs : carte bancaire (préfixe d'émetteur et longueur vérifiés : plus d'horodatages en millisecondes), nombres décimaux (0.0012345678), mots « une », « online », « deadline » pris pour un indice de date de naissance, numéro INAMI de 11 chiffres sans séparateur (reconnu seulement après un mot de contexte).
- Champs `encrypted_content` et `encrypted_index` jamais modifiés.
- Cache de prompt des fournisseurs : ordre de parcours fixe (outils, système, messages) et consigne `placeholder_hint` toujours ajoutée quand le profil la demande ; `count_tokens` la compte donc toujours.
### Performances
- Anonymisation environ 5 fois plus rapide : les détections du premier passage sont réutilisées au second, et les listes de termes (établissements de soins, noms appris…) ne sont essayées qu'aux débuts de mots possibles. 1 Ko : 1,9 → 0,36 ms ; 10 Ko : 21 → 4,4 ms.
- Multi-cœurs : un texte d'au moins 16 Ko est analysé par plusieurs détecteurs en parallèle (100 Ko : 250 → 15 ms sur 10 cœurs). Aucun accès concurrent détecté sous charge (`go test -race`).
- Cache entre requêtes (20 000 textes, ~64 Mo) : un historique déjà vu, renvoyé à chaque tour par Claude Code ou Hermes, n'est plus réanalysé (100 Ko : 200 → 4 ms). La mémoire des noms persistante est toujours réévaluée.
- Mémoire des noms : la recherche reste rapide avec des milliers de noms (2 000 noms, 110 Ko : 1,8 s → 50 ms) ; l'oubli périodique ne bloque plus les requêtes et ne recharge plus l'index sans raison.
- Débit de bout en bout, texte nouveau à chaque requête : environ 5 200 requêtes de 2 Ko par seconde sur 10 cœurs (1 100 en 1.3.0) et 1 700 sur 2 cœurs. Détail dans le README.

## 1.3.0 (9 octobre 2026)

### Nouveautés
- Détecteur intégré `inami` : numéro INAMI / RIZIV d'un prestataire de soins (médecin, dentiste, kinésithérapeute, infirmier…), sous les formes 1-23456-25-001, 1.23456.25.001 ou 12345625001. La clé de contrôle (chiffres 7 et 8) est vérifiée selon les modulos 97, 89, 83 et 79 de l'INAMI. La forme courte à 8 chiffres, sans code de qualification, n'est reconnue qu'après « INAMI », « RIZIV » ou « NIHII ».
- Banc de test du dashboard : le texte d'exemple contient un numéro INAMI.

## 1.2.1 (8 octobre 2026)

### Corrections
- LLMGW.app arrête la passerelle qu'elle a lancée quelle que soit la façon de la quitter (menu, commande Quitter du système, fermeture de session) ; auparavant, seul le menu l'arrêtait.
- `macapp/build.sh --install` installe l'app dans Applications et relance la copie en cours.

## 1.2.0 (7 octobre 2026)

### Nouveautés
- LLMGW.app devient **autonome** : la passerelle est embarquée dans l'app (binaire universel Apple Silicon et Intel, sans aucune dépendance hors macOS). `macapp/build.sh` produit `dist/LLMGW.app` et `dist/LLMGW-<version>.dmg`.
- Données dans `~/Library/Application Support/LLMGW` ; au premier lancement, configuration de départ avec un jeton d'administration aléatoire et modèle `secrets.env` (droits 600). Une installation existante est reprise telle quelle.
- Menu : modifier la configuration, modifier les secrets (clés API), copier le jeton d'administration, choisir le dossier de données ; textes en anglais, français et néerlandais.

## 1.1.0 (7 octobre 2026)

### Nouveautés
- App macOS **LLMGW.app** dans la barre des menus : pastille d'état (verte, orange, rouge), démarrer, arrêter, redémarrer, ouvrir le dashboard, le chat et la documentation, afficher le journal, ouverture à la session. Elle charge `secrets.env`, démarre la passerelle à son ouverture et l'arrête à sa fermeture. Menu en anglais, français et néerlandais. Construction : `macapp/build.sh`.
- `start.command` : démarrage en double-clic dans le Terminal, avec ouverture du dashboard.

## 1.0.1 (7 octobre 2026)

### Corrections
- PDF dont la police est mal encodée : quand le texte extrait contient des lettres remplacées par des symboles (« J@an S©basti@ »), ces symboles restent rattachés au mot, qui est bien détecté et masqué comme un nom.
- Chat : un avertissement « ⚠ texte extrait abîmé » s'affiche sur un document dont le texte est de mauvaise qualité, en anglais, français et néerlandais.

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

// LLMGW interface languages: English (default), French (source text), Dutch.
// Translates the displayed text nodes and the placeholder / title / aria-label attributes,
// never the data: inputs keep their values and [translate="no"] subtrees are skipped.
(function () {
  const LANGS = { en: "English", fr: "Français", nl: "Nederlands" };
  let lang = "en";
  try { lang = localStorage.getItem("gwlang") || "en"; } catch {}
  if (!LANGS[lang]) lang = "en";
  const LOC = { en: "en-GB", fr: "fr-BE", nl: "nl-BE" }[lang];

  // [French source, English, Dutch]
  const W = [
    // navigation and shell
    ["Pilotage", "Monitoring", "Monitoring"], ["Accès", "Access", "Toegang"], ["Modèles", "Models", "Modellen"], ["Données", "Data", "Gegevens"],
    ["Paramètres", "Settings", "Instellingen"], ["Vue d'ensemble", "Overview", "Overzicht"], ["Reporting", "Reporting", "Rapportering"],
    ["Utilisateurs", "Users", "Gebruikers"], ["Profils", "Profiles", "Profielen"], ["LLM cibles", "Target LLMs", "Doel-LLM's"], ["Quotas", "Quotas", "Quota"],
    ["Filtrage et privacy", "Filtering and privacy", "Filtering en privacy"], ["Debug", "Debug", "Debug"], ["Alertes et SSO", "Alerts and SSO", "Waarschuwingen en SSO"],
    ["Comptes d'administration", "Administration accounts", "Beheeraccounts"], ["Journal d'audit", "Audit log", "Auditlogboek"], ["Passerelle IA", "AI gateway", "AI-gateway"],
    ["Navigation", "Navigation", "Navigatie"], ["Thème clair / sombre", "Light / dark theme", "Licht / donker thema"], ["Thème", "Theme", "Thema"],
    ["DEBUG actif", "DEBUG on", "DEBUG actief"], ["Des échanges complets sont archivés en clair", "Full exchanges are archived in clear text", "Volledige uitwisselingen worden onversleuteld gearchiveerd"],
    ["Modifications non enregistrées", "Unsaved changes", "Niet-opgeslagen wijzigingen"], ["Annuler", "Cancel", "Annuleren"], ["Enregistrer", "Save", "Opslaan"],
    ["déconnexion", "sign out", "afmelden"], ["Déconnexion", "Sign out", "Afmelden"], ["Langue", "Language", "Taal"],
    // page headers
    ["État de la passerelle et points d'attention sur les dernières 48 heures.", "Gateway status and points of attention over the last 48 hours.", "Status van de gateway en aandachtspunten van de laatste 48 uur."],
    ["Volumes, temps de réponse, conversions et violations, par utilisateur et par LLM.", "Volumes, response times, conversions and violations, per user and per LLM.", "Volumes, responstijden, conversies en overtredingen, per gebruiker en per LLM."],
    ["Personnes et applications autorisées, leur clé gateway et leur profil.", "Authorised people and applications, their gateway key and their profile.", "Toegelaten personen en applicaties, hun gatewaysleutel en hun profiel."],
    ["Règles appliquées : LLM autorisés, prompt de comportement, images, alertes et quota.", "Applied rules: allowed LLMs, behaviour prompt, images, alerts and quota.", "Toegepaste regels: toegelaten LLM's, gedragsprompt, afbeeldingen, waarschuwingen en quota."],
    ["Fournisseurs vers lesquels la passerelle route les requêtes, avec leurs clés et leurs modèles.", "Providers the gateway routes requests to, with their keys and models.", "Providers waarnaar de gateway verzoeken doorstuurt, met hun sleutels en modellen."],
    ["Limites par profil, par utilisateur et par LLM, avec la consommation en direct.", "Limits per profile, per user and per LLM, with live consumption.", "Limieten per profiel, per gebruiker en per LLM, met live verbruik."],
    ["Ce qui est détecté, ce qui en est fait selon le profil, et un banc de test.", "What is detected, what happens to it depending on the profile, and a test bench.", "Wat gedetecteerd wordt, wat ermee gebeurt volgens het profiel, en een testbank."],
    ["Archive temporaire des échanges complets, pour le dépannage.", "Temporary archive of full exchanges, for troubleshooting.", "Tijdelijk archief van volledige uitwisselingen, voor probleemoplossing."],
    ["Alertes par email et connexion avec Microsoft Entra ID ou Google.", "Email alerts and sign-in with Microsoft Entra ID or Google.", "E-mailwaarschuwingen en aanmelden met Microsoft Entra ID of Google."],
    ["Qui peut ouvrir ce dashboard, et avec quels droits : Admin (tout) ou Support (utilisateurs, statistiques, Debug sur autorisation).", "Who can open this dashboard, and with which rights: Admin (everything) or Support (users, statistics, Debug when granted).", "Wie dit dashboard mag openen, en met welke rechten: Admin (alles) of Support (gebruikers, statistieken, Debug indien toegestaan)."],
    ["Qui a fait quoi, et quand : connexions, modifications, consultations de l'archive Debug.", "Who did what, and when: sign-ins, changes, views of the Debug archive.", "Wie deed wat, en wanneer: aanmeldingen, wijzigingen, raadplegingen van het Debug-archief."],
    // login
    ["Administration", "Administration", "Beheer"], ["Ou le jeton", "Or the token", "Of het token"], ["de la section [server] de config.toml. Il reste dans ce navigateur.", "from the [server] section of config.toml. It stays in this browser.", "uit de sectie [server] van config.toml. Het blijft in deze browser."],
    ["Entrer", "Enter", "Openen"], ["Jeton refusé", "Token refused", "Token geweigerd"], ["Connexion au gateway", "Sign in to the gateway", "Aanmelden bij de gateway"],
    ["Saisissez votre clé gateway personnelle (fournie par l'administrateur). Elle reste dans cet onglet.", "Enter your personal gateway key (provided by the administrator). It stays in this tab.", "Voer uw persoonlijke gatewaysleutel in (verstrekt door de beheerder). Ze blijft in dit tabblad."],
    ["ou avec une clé", "or with a key", "of met een sleutel"], ["Se connecter avec Microsoft", "Sign in with Microsoft", "Aanmelden met Microsoft"], ["Se connecter avec Google", "Sign in with Google", "Aanmelden met Google"],
    ["Clé refusée", "Key refused", "Sleutel geweigerd"],
    // overview
    ["Requêtes (48 h)", "Requests (48 h)", "Verzoeken (48 u)"], ["Tokens", "Tokens", "Tokens"], ["LLM cible moy.", "Target LLM avg.", "Doel-LLM gem."], ["temps de réponse", "response time", "responstijd"],
    ["Conversions", "Conversions", "Conversies"], ["valeurs pseudonymisées", "pseudonymised values", "gepseudonimiseerde waarden"], ["Violations", "Violations", "Overtredingen"],
    ["requêtes bloquées", "blocked requests", "geblokkeerde verzoeken"], ["Utilisateurs actifs", "Active users", "Actieve gebruikers"], ["Points d'attention", "Points of attention", "Aandachtspunten"],
    ["Classés par gravité.", "Sorted by severity.", "Gesorteerd op ernst."], ["Activité récente", "Recent activity", "Recente activiteit"], ["Tout le reporting", "Full reporting", "Volledige rapportering"],
    ["Utilisateurs les plus actifs (48 h)", "Most active users (48 h)", "Meest actieve gebruikers (48 u)"], ["Administration ouverte", "Administration open", "Beheer open"],
    ["Ni jeton ni SSO : quiconque atteint la passerelle peut la configurer.", "No token and no SSO: anyone who reaches the gateway can configure it.", "Geen token en geen SSO: iedereen die de gateway bereikt, kan ze configureren."],
    ["Administration protégée", "Administration protected", "Beheer beveiligd"], ["Mode DEBUG actif", "DEBUG mode on", "DEBUG-modus actief"], ["Mode DEBUG inactif", "DEBUG mode off", "DEBUG-modus uit"],
    ["Aucune archive en clair.", "No clear-text archive.", "Geen onversleuteld archief."], ["Aucune requête bloquée en 48 h", "No blocked request in 48 h", "Geen geblokkeerd verzoek in 48 u"],
    ["Aucune violation de la politique de données.", "No data policy violation.", "Geen schending van het gegevensbeleid."], ["Aucune erreur en 48 h", "No error in 48 h", "Geen fout in 48 u"],
    ["Tous les appels ont abouti.", "All calls succeeded.", "Alle oproepen zijn geslaagd."], ["Données interdites par un profil, ou quota atteint.", "Data forbidden by a profile, or quota reached.", "Gegevens verboden door een profiel, of quotum bereikt."],
    ["LLM injoignable, clé refusée, modèle inconnu… Motif dans les requêtes récentes.", "LLM unreachable, key refused, unknown model… Reason in the recent requests.", "LLM onbereikbaar, sleutel geweigerd, onbekend model… Reden in de recente verzoeken."],
    ["Quotas sous les seuils", "Quotas below thresholds", "Quota onder de drempels"], ["Aucune limite proche d'être atteinte.", "No limit close to being reached.", "Geen limiet bijna bereikt."],
    ["Alertes email actives", "Email alerts on", "E-mailwaarschuwingen aan"], ["Alertes email inactives", "Email alerts off", "E-mailwaarschuwingen uit"],
    ["Les violations et les pannes ne sont pas signalées par email.", "Violations and outages are not reported by email.", "Overtredingen en storingen worden niet per e-mail gemeld."],
    ["Toutes les cibles cloud ont une clé.", "All cloud targets have a key.", "Alle clouddoelen hebben een sleutel."], ["Aucune requête pour l'instant.", "No request yet.", "Nog geen verzoeken."],
    ["Pas encore d'activité.", "No activity yet.", "Nog geen activiteit."], ["jeton d'administration", "admin token", "beheertoken"], ["et", "and", "en"],
    // reporting
    ["Période", "Period", "Periode"], ["48 h", "48 h", "48 u"], ["60 j", "60 d", "60 d"], ["6 sem", "6 wk", "6 wk"], ["6 mois", "6 months", "6 maanden"],
    ["Détail par requête sur 30 j, puis agrégats journaliers lissés.", "Per-request detail for 30 days, then smoothed daily aggregates.", "Detail per verzoek gedurende 30 dagen, daarna afgevlakte dagaggregaten."],
    ["Requêtes", "Requests", "Verzoeken"], ["Volume", "Volume", "Volume"], ["Temps total moy.", "Avg. total time", "Gem. totale tijd"], ["Gateway moy.", "Avg. gateway", "Gem. gateway"],
    ["valeurs anonymisées ou retirées", "values anonymised or removed", "waarden geanonimiseerd of verwijderd"], ["Requêtes par LLM cible", "Requests per target LLM", "Verzoeken per doel-LLM"],
    ["Volumétrie par destination", "Volume per destination", "Volume per bestemming"], ["Tokens par utilisateur", "Tokens per user", "Tokens per gebruiker"], ["Entrée + sortie, par source", "Input + output, per source", "Invoer + uitvoer, per bron"],
    ["Temps de réponse moyen", "Average response time", "Gemiddelde responstijd"], ["IN = lecture client · Gateway = traitement · LLM = cible jusqu'à la fin de réponse", "IN = reading the client · Gateway = processing · LLM = target until the end of the answer", "IN = client lezen · Gateway = verwerking · LLM = doel tot het einde van het antwoord"],
    ["Filtrage", "Filtering", "Filtering"], ["Conversions (anonymisées ou retirées) et violations (bloquées : données ou quotas)", "Conversions (anonymised or removed) and violations (blocked: data or quotas)", "Conversies (geanonimiseerd of verwijderd) en overtredingen (geblokkeerd: gegevens of quota)"],
    ["Par utilisateur (source)", "Per user (source)", "Per gebruiker (bron)"], ["Par LLM (destination)", "Per LLM (destination)", "Per LLM (bestemming)"], ["Détections et quotas par type", "Detections and quotas by type", "Detecties en quota per type"],
    ["Aucune valeur sensible n'est stockée, seulement des compteurs", "No sensitive value is stored, only counters", "Er wordt geen gevoelige waarde opgeslagen, alleen tellers"], ["Requêtes récentes", "Recent requests", "Recente verzoeken"],
    ["Utilisateur", "User", "Gebruiker"], ["Tokens in", "Tokens in", "Tokens in"], ["Tokens out", "Tokens out", "Tokens uit"], ["Volume in/out", "Volume in/out", "Volume in/uit"], ["Moy. totale", "Avg. total", "Gem. totaal"],
    ["Moy. LLM", "Avg. LLM", "Gem. LLM"], ["Moy. gateway", "Avg. gateway", "Gem. gateway"], ["Conv.", "Conv.", "Conv."], ["Viol.", "Viol.", "Overtr."], ["Erreurs", "Errors", "Fouten"],
    ["LLM", "LLM", "LLM"], ["Type", "Type", "Type"], ["Action", "Action", "Actie"], ["Occurrences", "Occurrences", "Voorkomens"], ["Principaux utilisateurs", "Main users", "Belangrijkste gebruikers"],
    ["Heure", "Time", "Tijd"], ["Modèle", "Model", "Model"], ["Statut", "Status", "Status"], ["Total", "Total", "Totaal"], ["Gateway", "Gateway", "Gateway"], ["Remarque", "Note", "Opmerking"],
    ["Aucune donnée sur la période.", "No data for this period.", "Geen gegevens voor deze periode."], ["Aucune détection sur la période.", "No detection for this period.", "Geen detectie voor deze periode."],
    ["anonymisé", "anonymised", "geanonimiseerd"], ["bloqué", "blocked", "geblokkeerd"], ["journalisé", "logged", "gelogd"], ["retiré", "removed", "verwijderd"], ["laissé passer", "let through", "doorgelaten"],
    // users
    ["Utilisateurs et applications", "Users and applications", "Gebruikers en applicaties"], ["+ Utilisateur", "+ User", "+ Gebruiker"],
    ["Chaque ligne a sa clé gateway ; son profil fixe les LLM autorisés, les règles de données et le quota.", "Each row has its gateway key; its profile sets the allowed LLMs, the data rules and the quota.", "Elke rij heeft een gatewaysleutel; het profiel bepaalt de toegelaten LLM's, de gegevensregels en het quotum."],
    ["Chaque ligne a sa clé gateway ; son profil fixe les LLM autorisés, les règles de données et le quota. Les personnes connectées en SSO n'ont pas besoin d'y figurer : leur profil vient de l'annuaire (voir Alertes et SSO).", "Each row has its gateway key; its profile sets the allowed LLMs, the data rules and the quota. People signed in with SSO do not need to be listed: their profile comes from the directory (see Alerts and SSO).", "Elke rij heeft een gatewaysleutel; het profiel bepaalt de toegelaten LLM's, de gegevensregels en het quotum. Personen die via SSO aanmelden, hoeven hier niet te staan: hun profiel komt uit de directory (zie Waarschuwingen en SSO)."],
    ["Nom", "Name", "Naam"], ["Description", "Description", "Beschrijving"], ["Profil", "Profile", "Profiel"], ["LLM accessibles", "Accessible LLMs", "Toegankelijke LLM's"], ["Clé gateway", "Gateway key", "Gatewaysleutel"],
    ["Aujourd'hui", "Today", "Vandaag"], ["Personne", "Person", "Persoon"], ["Application", "Application", "Applicatie"], ["équipe, email, usage…", "team, email, purpose…", "team, e-mail, gebruik…"],
    ["Générer une nouvelle clé", "Generate a new key", "Nieuwe sleutel genereren"], ["Supprimer", "Delete", "Verwijderen"], ["Aucun utilisateur", "No user", "Geen gebruikers"], ["Copier", "Copy", "Kopiëren"],
    ["🔑 Nouvelle(s) clé(s) affichée(s) en entier : copiez-les maintenant et transmettez-les à l'utilisateur. Après rechargement de la page, seul un aperçu reste visible.", "🔑 New key(s) shown in full: copy them now and pass them on to the user. After the page is reloaded, only a preview remains visible.", "🔑 Nieuwe sleutel(s) volledig getoond: kopieer ze nu en bezorg ze aan de gebruiker. Na het herladen van de pagina blijft alleen een voorbeeld zichtbaar."],
    ["Clé copiée", "Key copied", "Sleutel gekopieerd"],
    // profiles
    ["+ Profil", "+ Profile", "+ Profiel"], ["Un profil regroupe les LLM autorisés (dans l'ordre de repli), le prompt de comportement, la politique d'images, les alertes et le quota. Les règles par type de donnée se règlent dans Filtrage et privacy.", "A profile groups the allowed LLMs (in fallback order), the behaviour prompt, the image policy, the alerts and the quota. Rules per data type are set in Filtering and privacy.", "Een profiel groepeert de toegelaten LLM's (in terugvalvolgorde), de gedragsprompt, het afbeeldingenbeleid, de waarschuwingen en het quotum. Regels per gegevenstype stel je in onder Filtering en privacy."],
    ["Tout déplier", "Expand all", "Alles uitklappen"], ["Tout replier", "Collapse all", "Alles inklappen"], ["Vert", "Green", "Groen"], ["gris", "grey", "grijs"], [": profil utilisé ·", ": profile in use ·", ": profiel in gebruik ·"],
    [": aucun utilisateur ni règle SSO", ": no user and no SSO rule", ": geen gebruiker en geen SSO-regel"], ["non utilisé", "not used", "niet gebruikt"], ["Afficher les paramètres", "Show settings", "Instellingen tonen"], ["Replier", "Collapse", "Inklappen"],
    ["Supprimer le profil", "Delete profile", "Profiel verwijderen"], ["LLM autorisés", "Allowed LLMs", "Toegelaten LLM's"], ["ordre = priorité, repli si panne", "order = priority, fallback on failure", "volgorde = prioriteit, terugval bij storing"],
    ["+ ajouter un LLM…", "+ add an LLM…", "+ een LLM toevoegen…"], ["Prompt de comportement", "Behaviour prompt", "Gedragsprompt"], ["ajouté en tête de chaque requête (non anonymisé)", "added at the top of every request (not anonymised)", "bovenaan elk verzoek toegevoegd (niet geanonimiseerd)"],
    ["ex. Réponds en français, ton professionnel. Ne donne jamais de conseil médical ou juridique définitif.", "e.g. Answer in English, professional tone. Never give definitive medical or legal advice.", "bv. Antwoord in het Nederlands, professionele toon. Geef nooit definitief medisch of juridisch advies."],
    ["Alerter", "Alert", "Waarschuwen"], ["emails, séparés par des virgules", "emails, separated by commas", "e-mails, gescheiden door komma's"], ["Images, fichiers", "Images, files", "Afbeeldingen, bestanden"],
    ["Données sensibles", "Sensitive data", "Gevoelige gegevens"], ["→ Filtrage", "→ Filtering", "→ Filtering"], ["Quota / utilisateur", "Quota / user", "Quotum / gebruiker"], ["→ Quotas", "→ Quotas", "→ Quota"], ["aucun", "none", "geen"],
    ["Utilisé par", "Used by", "Gebruikt door"], ["personne", "nobody", "niemand"], ["Laisser passer", "Let through", "Doorlaten"], ["Retirer", "Remove", "Verwijderen"], ["Bloquer", "Block", "Blokkeren"],
    ["Lecture seule : votre rôle Support permet de consulter cette page, pas de la modifier.", "Read only: your Support role lets you view this page, not change it.", "Alleen lezen: met uw rol Support kunt u deze pagina bekijken, niet wijzigen."],
    // targets
    ["Gateways de sortie : LLM cibles", "Outbound gateways: target LLMs", "Uitgaande gateways: doel-LLM's"], ["+ Cible", "+ Target", "+ Doel"],
    ["OpenAI, Mistral, Azure, Ollama, vLLM, LM Studio… au format OpenAI ; Claude au format Anthropic. Un client OpenAI n'est routé que vers des cibles OpenAI, et inversement.", "OpenAI, Mistral, Azure, Ollama, vLLM, LM Studio… in OpenAI format; Claude in Anthropic format. An OpenAI client is only routed to OpenAI targets, and vice versa.", "OpenAI, Mistral, Azure, Ollama, vLLM, LM Studio… in OpenAI-formaat; Claude in Anthropic-formaat. Een OpenAI-client wordt alleen naar OpenAI-doelen gerouteerd, en omgekeerd."],
    ["Tout retester", "Retest all", "Alles opnieuw testen"], ["connectée", "connected", "verbonden"], ["injoignable ou clé refusée", "unreachable or key refused", "onbereikbaar of sleutel geweigerd"], ["désactivée ou non testée", "disabled or not tested", "uitgeschakeld of niet getest"],
    ["nom", "name", "naam"], ["vert", "green", "groen"], [": utilisée par un profil", ": used by a profile", ": gebruikt door een profiel"], ["Tester", "Test", "Testen"], ["Supprimer la cible", "Delete target", "Doel verwijderen"],
    ["désactivée", "disabled", "uitgeschakeld"], ["non testée", "not tested", "niet getest"], ["test en cours…", "testing…", "bezig met testen…"], ["à enregistrer", "to be saved", "op te slaan"], ["Format d'API", "API format", "API-formaat"],
    ["OpenAI (compatible)", "OpenAI (compatible)", "OpenAI (compatibel)"], ["URL de base", "Base URL", "Basis-URL"], ["Modèles autorisés", "Allowed models", "Toegelaten modellen"], ["séparés par des virgules, * accepté", "comma-separated, * allowed", "gescheiden door komma's, * toegestaan"],
    ["Alias de modèles", "Model aliases", "Modelaliassen"], ["« demandé : envoyé », * accepté", "« requested: sent », * allowed", "« gevraagd: verzonden », * toegestaan"], ["Clé : variable d'env.", "Key: env. variable", "Sleutel: omgevingsvariabele"],
    ["ex. MISTRAL_API_KEY (recommandé)", "e.g. MISTRAL_API_KEY (recommended)", "bv. MISTRAL_API_KEY (aanbevolen)"], ["Clé : valeur directe", "Key: direct value", "Sleutel: directe waarde"], ["enregistrée (vide = conserver)", "saved (empty = keep)", "opgeslagen (leeg = behouden)"],
    ["écrite dans config.toml", "written to config.toml", "geschreven in config.toml"], ["État", "Status", "Status"], ["clé active", "key active", "sleutel actief"], ["aucune clé active", "no active key", "geen actieve sleutel"],
    ["En-têtes HTTP", "HTTP headers", "HTTP-headers"], ["une ligne « nom: valeur »", "one « name: value » line", "één regel « naam: waarde »"], ["Options", "Options", "Opties"], ["tokens en streaming", "tokens when streaming", "tokens bij streaming"],
    ["Pour les LLM qui ne gèrent que le mode JSON simple (DeepSeek…)", "For LLMs that only support plain JSON mode (DeepSeek…)", "Voor LLM's die alleen de eenvoudige JSON-modus ondersteunen (DeepSeek…)"], ["Utilisée par", "Used by", "Gebruikt door"],
    ["aucun profil", "no profile", "geen profiel"], ["Connexion", "Connection", "Verbinding"], ["« Tester » interroge /models avec la clé enregistrée", "« Test » queries /models with the saved key", "« Testen » vraagt /models op met de opgeslagen sleutel"], ["Aucune cible", "No target", "Geen doel"],
    // quotas
    ["Vide = illimité. Quand un quota est atteint :", "Empty = unlimited. When a quota is reached:", "Leeg = onbeperkt. Wanneer een quotum bereikt is:"], ["Par profil", "Per profile", "Per profiel"], ["Limite appliquée à", "Limit applied to", "Limiet toegepast op"],
    ["chaque", "each", "elke"], ["utilisateur du profil.", "user of the profile.", "gebruiker van het profiel."], ["Par utilisateur", "Per user", "Per gebruiker"], ["Surcharge le profil champ par champ ; −1 = illimité pour cet utilisateur.", "Overrides the profile field by field; −1 = unlimited for this user.", "Overschrijft het profiel veld per veld; −1 = onbeperkt voor deze gebruiker."],
    ["Par LLM cible", "Per target LLM", "Per doel-LLM"], ["Limite totale, tous utilisateurs confondus (budget fournisseur).", "Total limit, all users combined (provider budget).", "Totale limiet, alle gebruikers samen (providerbudget)."], ["Req./minute", "Req./minute", "Verz./minuut"],
    ["Req./jour", "Req./day", "Verz./dag"], ["Tokens/jour", "Tokens/day", "Tokens/dag"], ["Tokens/mois", "Tokens/month", "Tokens/maand"], ["Si dépassé", "If exceeded", "Bij overschrijding"], ["illimité", "unlimited", "onbeperkt"], ["Alerter", "Alert", "Waarschuwen"],
    ["Minute en cours", "Current minute", "Huidige minuut"], ["Req. aujourd'hui", "Req. today", "Verz. vandaag"], ["Tokens aujourd'hui", "Tokens today", "Tokens vandaag"], ["Tokens ce mois", "Tokens this month", "Tokens deze maand"], ["Cible", "Target", "Doel"],
    // privacy
    ["Détecteurs", "Detectors", "Detectoren"], ["Règles par profil", "Rules per profile", "Regels per profiel"], ["Fichiers", "Files", "Bestanden"], ["Exceptions et mémoire", "Exceptions and memory", "Uitzonderingen en geheugen"], ["Tester", "Test", "Testen"],
    ["Détecteurs de données sensibles", "Sensitive data detectors", "Detectoren voor gevoelige gegevens"], ["+ Détecteur", "+ Detector", "+ Detector"],
    ["Le type (ex. PERSON) donne le nom des jetons «PERSON_1». Plusieurs détecteurs peuvent partager un type. En cas de chevauchement, la correspondance la plus longue l'emporte, puis l'ordre.", "The type (e.g. PERSON) gives the token name «PERSON_1». Several detectors can share a type. When matches overlap, the longest wins, then the order.", "Het type (bv. PERSON) bepaalt de tokennaam «PERSON_1». Meerdere detectoren kunnen een type delen. Bij overlapping wint de langste overeenkomst, daarna de volgorde."],
    ["Méthode", "Method", "Methode"], ["Paramètre", "Parameter", "Parameter"], ["Note", "Note", "Notitie"], ["note", "note", "notitie"], ["Intégré", "Built-in", "Ingebouwd"], ["Expression régulière", "Regular expression", "Reguliere expressie"], ["Liste de termes", "Term list", "Termenlijst"],
    ["groupe", "group", "groep"], ["un terme par ligne ; un terme en MAJUSCULES (sigle) n'est reconnu qu'en majuscules", "one term per line; a term in CAPITALS (acronym) only matches in capitals", "één term per regel; een term in HOOFDLETTERS (acroniem) wordt alleen in hoofdletters herkend"],
    ["fichier (optionnel), ex. noms.txt", "file (optional), e.g. names.txt", "bestand (optioneel), bv. namen.txt"],
    ["Anonymiser : remplacé par un jeton puis restauré dans la réponse · Bloquer : requête refusée (violation) · Journaliser : laissé passer mais compté · Ignorer : non détecté", "Anonymise: replaced by a token, then restored in the answer · Block: request refused (violation) · Log: let through but counted · Ignore: not detected", "Anonimiseren: vervangen door een token, daarna hersteld in het antwoord · Blokkeren: verzoek geweigerd (overtreding) · Loggen: doorgelaten maar geteld · Negeren: niet gedetecteerd"],
    ["Les cases en gras s'écartent de l'action par défaut du profil ; en rouge, les blocages.", "Cells in bold differ from the profile's default action; blocks are in red.", "Vetgedrukte cellen wijken af van de standaardactie van het profiel; blokkeringen staan in het rood."],
    ["Type de donnée", "Data type", "Gegevenstype"], ["Action par défaut", "Default action", "Standaardactie"], ["Anonymiser", "Anonymise", "Anonimiseren"], ["Journaliser", "Log", "Loggen"], ["Ignorer", "Ignore", "Negeren"], ["IMAGE / FICHIER", "IMAGE / FILE", "AFBEELDING / BESTAND"],
    ["Consigne « jetons »", "« Tokens » instruction", "Instructie « tokens »"], ["ajouter", "add", "toevoegen"], ["Fichiers bloqués", "Blocked files", "Geblokkeerde bestanden"],
    ["Une requête qui contient un fichier d'un type coché est refusée (violation de type FILE) : fichier joint via l'API, document joint dans le chat, ou image. Le chat refuse le fichier dès qu'on le joint.", "A request containing a file of a ticked type is refused (FILE violation): file attached through the API, document attached in the chat, or image. The chat refuses the file as soon as it is attached.", "Een verzoek met een bestand van een aangevinkt type wordt geweigerd (overtreding FILE): bestand via de API, document in de chat of afbeelding. De chat weigert het bestand zodra het wordt toegevoegd."],
    ["Type de fichier", "File type", "Bestandstype"], ["PDF", "PDF", "PDF"], ["Word et texte riche", "Word and rich text", "Word en opgemaakte tekst"], ["Tableurs", "Spreadsheets", "Rekenbladen"], ["Présentations", "Presentations", "Presentaties"], ["Archives", "Archives", "Archieven"],
    ["Images", "Images", "Afbeeldingen"], ["Texte et code", "Text and code", "Tekst en code"], ["Autres extensions", "Other extensions", "Andere extensies"], ["séparées par des virgules", "comma-separated", "gescheiden door komma's"], ["autorisés", "allowed", "toegelaten"], ["bloqués", "blocked", "geblokkeerd"],
    ["ex. exe, iso", "e.g. exe, iso", "bv. exe, iso"], ["Jamais anonymiser", "Never anonymise", "Nooit anonimiseren"],
    ["Produits, marques, personnalités publiques… Un terme par ligne, sans accents ni casse. Pour un nom, il suffit que le premier mot soit listé : « Claude » couvre « Claude Code ».", "Products, brands, public figures… One term per line, case and accents ignored. For a name, listing the first word is enough: « Claude » covers « Claude Code ».", "Producten, merken, publieke figuren… Eén term per regel, zonder rekening te houden met accenten of hoofdletters. Voor een naam volstaat het eerste woord: « Claude » dekt « Claude Code »."],
    ["Mémoire des noms", "Name memory", "Namengeheugen"], ["Tout oublier", "Forget all", "Alles vergeten"],
    ["Actif si un détecteur « person_learned » est configuré. Chaque nom repéré avec certitude (titre, prénom connu, liste) est mémorisé sous forme d'empreintes HMAC, fautes de frappe comprises : il est ensuite reconnu seul (« Keizer », « Kiezer »). Aucun nom n'est stocké en clair ; oubli automatique après 1 an sans usage.", "Active if a « person_learned » detector is configured. Each name found with certainty (title, known first name, list) is remembered as HMAC fingerprints, typos included: it is then recognised on its own (« Keizer », « Kiezer »). No name is stored in clear; forgotten automatically after 1 year without use.", "Actief als een detector « person_learned » is ingesteld. Elke met zekerheid herkende naam (titel, gekende voornaam, lijst) wordt onthouden als HMAC-vingerafdrukken, tikfouten inbegrepen: daarna wordt hij ook alleen herkend (« Keizer », « Kiezer »). Geen enkele naam wordt onversleuteld opgeslagen; automatisch vergeten na 1 jaar zonder gebruik."],
    ["Tester l'anonymisation", "Test anonymisation", "Anonimisering testen"], ["Utilise la configuration", "Uses the", "Gebruikt de"], ["enregistrée", "saved configuration", "opgeslagen configuratie"], [". Rien n'est envoyé à un LLM.", ". Nothing is sent to an LLM.", ". Er wordt niets naar een LLM gestuurd."],
    ["modifications non enregistrées ignorées", "unsaved changes ignored", "niet-opgeslagen wijzigingen genegeerd"], ["⛔ Cette requête serait bloquée par ce profil.", "⛔ This request would be blocked by this profile.", "⛔ Dit verzoek zou door dit profiel geblokkeerd worden."], ["Aucune détection", "No detection", "Geen detectie"],
    ["Effacer toute la mémoire des noms ?", "Erase the whole name memory?", "Het volledige namengeheugen wissen?"], ["Mémoire des noms effacée", "Name memory erased", "Namengeheugen gewist"],
    // debug
    ["Mode DEBUG", "DEBUG mode", "DEBUG-modus"], ["actif", "on", "actief"], ["inactif", "off", "uit"],
    ["Archive chaque échange : requête telle que reçue par le LLM (anonymisée), réponse brute du LLM, correspondances jeton → valeur d'origine et mots détectés.", "Archives every exchange: request as received by the LLM (anonymised), raw LLM answer, token → original value mapping and detected words.", "Archiveert elke uitwisseling: verzoek zoals ontvangen door de LLM (geanonimiseerd), ruw LLM-antwoord, koppeling token → oorspronkelijke waarde en gedetecteerde woorden."],
    ["Les valeurs d'origine sont stockées en clair", "Original values are stored in clear text", "Oorspronkelijke waarden worden onversleuteld opgeslagen"], [": à activer le temps d'un diagnostic seulement. Purge automatique après la durée choisie.", ": enable only for the duration of a diagnosis. Automatic purge after the chosen duration.", ": alleen inschakelen tijdens een diagnose. Automatische opschoning na de gekozen duur."],
    ["Activer", "Enable", "Inschakelen"], ["archiver les échanges", "archive exchanges", "uitwisselingen archiveren"], ["à enregistrer", "to be saved", "op te slaan"], ["Conservation", "Retention", "Bewaring"], ["heures", "hours", "uur"], ["Archive", "Archive", "Archief"],
    ["Vider", "Empty", "Leegmaken"], ["Rafraîchir", "Refresh", "Vernieuwen"], ["Échanges archivés", "Archived exchanges", "Gearchiveerde uitwisselingen"], ["Cliquer une ligne pour le détail. Légende :", "Click a row for details. Legend:", "Klik op een rij voor details. Legende:"],
    ["LLM / modèle", "LLM / model", "LLM / model"], ["Détections", "Detections", "Detecties"], ["Taille", "Size", "Grootte"], ["Mode DEBUG inactif.", "DEBUG mode off.", "DEBUG-modus uit."], ["Aucun échange archivé pour l'instant.", "No archived exchange yet.", "Nog geen gearchiveerde uitwisseling."],
    ["Vue anonymisée", "Anonymised view", "Geanonimiseerde weergave"], ["Vue d'origine", "Original view", "Oorspronkelijke weergave"], ["Fermer", "Close", "Sluiten"], ["Détecté dans cet échange", "Detected in this exchange", "Gedetecteerd in deze uitwisseling"],
    ["Valeur d'origine", "Original value", "Oorspronkelijke waarde"], ["Jeton", "Token", "Token"], ["Occ.", "Occ.", "Voork."], ["Où", "Where", "Waar"], ["réponse", "answer", "antwoord"], ["Rien de détecté dans cet échange", "Nothing detected in this exchange", "Niets gedetecteerd in deze uitwisseling"],
    ["Message de cet échange (tel que reçu par le LLM)", "Message of this exchange (as received by the LLM)", "Bericht van deze uitwisseling (zoals ontvangen door de LLM)"], ["Messages de cet échange (tel que reçu par le LLM)", "Messages of this exchange (as received by the LLM)", "Berichten van deze uitwisseling (zoals ontvangen door de LLM)"],
    ["Message de cet échange (valeurs d'origine en gras)", "Message of this exchange (original values in bold)", "Bericht van deze uitwisseling (oorspronkelijke waarden vetgedrukt)"], ["Messages de cet échange (valeurs d'origine en gras)", "Messages of this exchange (original values in bold)", "Berichten van deze uitwisseling (oorspronkelijke waarden vetgedrukt)"],
    ["Réponse du LLM (brute, avant restauration)", "LLM answer (raw, before restoration)", "LLM-antwoord (ruw, vóór herstel)"], ["Réponse du LLM (restaurée)", "LLM answer (restored)", "LLM-antwoord (hersteld)"], ["vide", "empty", "leeg"],
    ["Contexte complet envoyé au LLM", "Full context sent to the LLM", "Volledige context verstuurd naar de LLM"], ["Le client (Hermes, chat…) renvoie à chaque appel son prompt système et tout l'historique : ils sont anonymisés à chaque fois.", "The client (Hermes, chat…) resends its system prompt and the whole history with every call: they are anonymised each time.", "De client (Hermes, chat…) stuurt bij elke oproep zijn systeemprompt en de volledige geschiedenis opnieuw: die worden telkens geanonimiseerd."],
    ["JSON brut envoyé au LLM", "Raw JSON sent to the LLM", "Ruwe JSON verstuurd naar de LLM"], ["Vue globale : tout ce qui a été détecté", "Global view: everything detected", "Globaal overzicht: alles wat gedetecteerd werd"], ["filtrer…", "filter…", "filteren…"], ["Échanges", "Exchanges", "Uitwisselingen"],
    ["Dernier", "Last", "Laatste"], ["Ouvrir l'échange", "Open the exchange", "Uitwisseling openen"], ["Rien", "Nothing", "Niets"], ["Vider toute l'archive DEBUG ?", "Empty the whole DEBUG archive?", "Het volledige DEBUG-archief leegmaken?"], ["Archive vidée", "Archive emptied", "Archief leeggemaakt"],
    // settings: alerts and SSO
    ["Alertes par email", "Email alerts", "E-mailwaarschuwingen"], ["Un récapitulatif toutes les N minutes, envoyé aux emails « Alerter » du profil concerné et aux destinataires par défaut ci-dessous. Aucune valeur sensible dans les messages.", "A summary every N minutes, sent to the « Alert » emails of the profile concerned and to the default recipients below. No sensitive value in the messages.", "Een samenvatting om de N minuten, verstuurd naar de e-mails « Waarschuwen » van het betrokken profiel en naar de standaardontvangers hieronder. Geen gevoelige waarden in de berichten."],
    ["actives", "on", "aan"], ["inactives", "off", "uit"], ["Envoyer un test", "Send a test", "Test versturen"], ["envoyer des alertes", "send alerts", "waarschuwingen versturen"], ["Déclencheurs", "Triggers", "Triggers"], ["violation", "violation", "overtreding"], ["quota", "quota", "quotum"],
    ["erreur LLM", "LLM error", "LLM-fout"], ["Destinataires par défaut", "Default recipients", "Standaardontvangers"], ["Fréquence", "Frequency", "Frequentie"], ["minutes (récapitulatif)", "minutes (summary)", "minuten (samenvatting)"], ["Serveur SMTP", "SMTP server", "SMTP-server"], ["Compte", "Account", "Account"],
    ["Mot de passe : env.", "Password: env.", "Wachtwoord: omgeving"], ["Mot de passe : valeur", "Password: value", "Wachtwoord: waarde"], ["SMTP_PASSWORD (recommandé)", "SMTP_PASSWORD (recommended)", "SMTP_PASSWORD (aanbevolen)"], ["enregistré (vide = conserver)", "saved (empty = keep)", "opgeslagen (leeg = behouden)"], ["Expéditeur", "Sender", "Afzender"],
    ["Enregistrez d'abord la configuration", "Save the configuration first", "Sla eerst de configuratie op"], ["Authentification (SSO)", "Authentication (SSO)", "Authenticatie (SSO)"],
    ["Connexion au portail et au dashboard avec Microsoft Entra ID, Google ou un fournisseur OpenID Connect, en plus des clés gateway et du jeton d'administration. Se paramètre dans la section [auth] de config.toml.", "Sign-in to the portal and dashboard with Microsoft Entra ID, Google or an OpenID Connect provider, in addition to gateway keys and the admin token. Configured in the [auth] section of config.toml.", "Aanmelden bij het portaal en het dashboard met Microsoft Entra ID, Google of een OpenID Connect-provider, naast gatewaysleutels en het beheertoken. In te stellen in de sectie [auth] van config.toml."],
    ["Fournisseur", "Provider", "Provider"], ["Adresse de retour à déclarer", "Redirect URI to register", "Te registreren redirect-URI"], ["Domaines", "Domains", "Domeinen"], ["Profil attribué", "Assigned profile", "Toegewezen profiel"], ["Administrateurs", "Administrators", "Beheerders"], ["tous", "all", "alle"],
    ["Aucun fournisseur configuré. Exemple dans config.example.toml, section [auth].", "No provider configured. Example in config.example.toml, [auth] section.", "Geen provider ingesteld. Voorbeeld in config.example.toml, sectie [auth]."],
    // accounts
    ["Comptes", "Accounts", "Accounts"], ["+ Compte", "+ Account", "+ Account"], ["Connexion par SSO : l'adresse email identifie le compte. Un compte listé ici prime sur les règles SSO ; le désactiver coupe l'accès immédiatement.", "Sign-in with SSO: the email address identifies the account. An account listed here takes precedence over SSO rules; disabling it cuts access immediately.", "Aanmelden via SSO: het e-mailadres identificeert het account. Een hier vermeld account heeft voorrang op de SSO-regels; uitschakelen ontzegt de toegang meteen."],
    ["Email", "Email", "E-mail"], ["Rôle", "Role", "Rol"], ["Accès Debug", "Debug access", "Debug-toegang"], ["Dernière connexion", "Last sign-in", "Laatste aanmelding"], ["Admin (tout)", "Admin (everything)", "Admin (alles)"], ["Support", "Support", "Support"], ["Admin", "Admin", "Admin"],
    ["inclus", "included", "inbegrepen"], ["autorisé", "granted", "toegestaan"], ["désactivé", "disabled", "uitgeschakeld"], ["nom affiché", "display name", "weergavenaam"],
    ["Aucun compte. Ajoutez au moins un Admin avant de retirer le jeton d'administration.", "No account. Add at least one Admin before removing the admin token.", "Geen account. Voeg minstens één Admin toe voordat u het beheertoken verwijdert."],
    ["Droits par rôle", "Rights per role", "Rechten per rol"], ["Écran", "Screen", "Scherm"], ["Vue d'ensemble, Reporting", "Overview, Reporting", "Overzicht, Rapportering"], ["✓ consultation", "✓ view", "✓ raadplegen"], ["✓ créer, bloquer, clés, profil", "✓ create, block, keys, profile", "✓ aanmaken, blokkeren, sleutels, profiel"],
    ["Profils, Quotas", "Profiles, Quotas", "Profielen, Quota"], ["consultation", "view", "raadplegen"], ["Debug (données en clair)", "Debug (clear-text data)", "Debug (onversleutelde gegevens)"], ["si autorisé, chaque consultation journalisée", "if granted, every view logged", "indien toegestaan, elke raadpleging gelogd"],
    ["LLM cibles, Filtrage, Alertes et SSO, Comptes, Audit", "Target LLMs, Filtering, Alerts and SSO, Accounts, Audit", "Doel-LLM's, Filtering, Waarschuwingen en SSO, Accounts, Audit"], ["Autres accès", "Other access", "Andere toegang"],
    ["Règles des fournisseurs SSO (section [auth]), utilisées pour les adresses absentes de la liste :", "SSO provider rules ([auth] section), used for addresses not in the list:", "Regels van de SSO-providers (sectie [auth]), gebruikt voor adressen die niet in de lijst staan:"], ["Aucune.", "None.", "Geen."],
    ["Jeton d'administration (", "Admin token (", "Beheertoken ("], [") : accès de secours avec le rôle Admin. Défini.", "): emergency access with the Admin role. Set.", "): noodtoegang met de rol Admin. Ingesteld."], [") : accès de secours avec le rôle Admin.", "): emergency access with the Admin role.", "): noodtoegang met de rol Admin."], ["Non défini.", "Not set.", "Niet ingesteld."],
    ["Ce compte existe déjà", "This account already exists", "Dit account bestaat al"], ["Adresse email invalide", "Invalid email address", "Ongeldig e-mailadres"], ["Adresse email du compte (celle de sa connexion SSO)", "Account email address (the one used for SSO sign-in)", "E-mailadres van het account (dat van de SSO-aanmelding)"],
    // audit
    ["Événements", "Events", "Gebeurtenissen"], ["Date", "Date", "Datum"], ["Acteur", "Actor", "Actor"], ["Détail", "Detail", "Detail"], ["Aucun événement", "No event", "Geen gebeurtenissen"],
    ["connexion", "sign-in", "aanmelding"], ["configuration enregistrée", "configuration saved", "configuratie opgeslagen"], ["utilisateurs enregistrés", "users saved", "gebruikers opgeslagen"], ["consultation Debug", "Debug view", "Debug-raadpleging"],
    ["archive Debug vidée", "Debug archive emptied", "Debug-archief leeggemaakt"], ["mémoire des noms vidée", "name memory emptied", "namengeheugen gewist"], ["test des alertes", "alert test", "waarschuwingstest"], ["configuration rechargée", "configuration reloaded", "configuratie herladen"],
    // toasts, prompts and confirmations
    ["Configuration enregistrée et appliquée.", "Configuration saved and applied.", "Configuratie opgeslagen en toegepast."], ["Nom de l'utilisateur ou de l'application", "Name of the user or application", "Naam van de gebruiker of applicatie"], ["Nom du profil", "Profile name", "Profielnaam"],
    ["Créez d'abord un profil", "Create a profile first", "Maak eerst een profiel aan"],
    // chat
    ["Gateway Chat", "Gateway Chat", "Gateway Chat"], ["Nouvelle conversation", "New conversation", "Nieuw gesprek"], ["Joindre un document ou une image", "Attach a document or an image", "Een document of afbeelding toevoegen"], ["📎 Joindre", "📎 Attach", "📎 Toevoegen"],
    ["Arrêter", "Stop", "Stoppen"], ["Envoyer", "Send", "Versturen"], ["Votre message… (Entrée pour envoyer, Maj+Entrée pour une nouvelle ligne)", "Your message… (Enter to send, Shift+Enter for a new line)", "Uw bericht… (Enter om te versturen, Shift+Enter voor een nieuwe regel)"],
    ["Modèle", "Model", "Model"], ["Autre modèle", "Other model", "Ander model"], ["nom du modèle", "model name", "modelnaam"], ["Retirer", "Remove", "Verwijderen"], ["lecture…", "reading…", "lezen…"], ["(voir document joint)", "(see attached document)", "(zie bijgevoegd document)"],
    ["Vos messages passent par le gateway : noms, emails, numéros de dossier… sont remplacés par des jetons avant d'atteindre le LLM, puis restaurés dans la réponse.", "Your messages go through the gateway: names, emails, file numbers… are replaced by tokens before reaching the LLM, then restored in the answer.", "Uw berichten gaan via de gateway: namen, e-mails, dossiernummers… worden vervangen door tokens voordat ze de LLM bereiken, en daarna hersteld in het antwoord."],
    ["aucune donnée sensible détectée", "no sensitive data detected", "geen gevoelige gegevens gedetecteerd"], ["Ces valeurs ont été remplacées par des jetons avant envoi, puis restaurées dans la réponse", "These values were replaced by tokens before sending, then restored in the answer", "Deze waarden werden vóór verzending door tokens vervangen en daarna hersteld in het antwoord"],
    ["Données sensibles pseudonymisées avant envoi · images retirées avant envoi", "Sensitive data pseudonymised before sending · images removed before sending", "Gevoelige gegevens gepseudonimiseerd vóór verzending · afbeeldingen verwijderd vóór verzending"],
    ["Données sensibles pseudonymisées avant envoi · images transmises", "Sensitive data pseudonymised before sending · images sent", "Gevoelige gegevens gepseudonimiseerd vóór verzending · afbeeldingen verzonden"],
    ["Données sensibles pseudonymisées avant envoi · images interdites", "Sensitive data pseudonymised before sending · images forbidden", "Gevoelige gegevens gepseudonimiseerd vóór verzending · afbeeldingen verboden"],
    ["aucun texte extrait (document scanné ?)", "no text extracted (scanned document?)", "geen tekst geëxtraheerd (gescand document?)"], ["(tronqué)", "(truncated)", "(ingekort)"],
    // builtin detectors (admin.go builtinInfo)
    ["Adresse email", "Email address", "E-mailadres"], ["Numéro de téléphone (international ou national)", "Phone number (international or national)", "Telefoonnummer (internationaal of nationaal)"],
    ["IBAN (clé de contrôle vérifiée)", "IBAN (check digits verified)", "IBAN (controlegetal geverifieerd)"], ["Numéro de registre national belge (clé vérifiée)", "Belgian national register number (checksum verified)", "Belgisch rijksregisternummer (controlegetal geverifieerd)"],
    ["Carte bancaire (Luhn)", "Bank card (Luhn)", "Bankkaart (Luhn)"], ["Adresse IPv4", "IPv4 address", "IPv4-adres"],
    ["Nom après un titre (Monsieur, Mme, Dr, Dhr… fautes de frappe tolérées) ou un mot de contexte (patient, client, nommé, né…)", "Name after a title (Monsieur, Mme, Dr, Dhr… typos tolerated) or a context word (patient, client, named, born…)", "Naam na een titel (Monsieur, Mme, Dr, Dhr… tikfouten toegestaan) of een contextwoord (patiënt, klant, genaamd, geboren…)"],
    ["Prénom connu suivi d'un nom (Gérald Keizer) ou NOM + prénom (KEIZER Gérald) ; dictionnaire FR/NL/EN intégré", "Known first name followed by a surname (Gérald Keizer) or SURNAME + first name (KEIZER Gérald); built-in FR/NL/EN dictionary", "Bekende voornaam gevolgd door een achternaam (Gérald Keizer) of ACHTERNAAM + voornaam (KEIZER Gérald); ingebouwd FR/NL/EN-woordenboek"],
    ["Identifiant après un mot de contexte : dossier, patient, n°, réf., contrat, facture, matricule, client, sinistre… (Dossier patient 676888-67)", "Identifier after a context word: file, patient, no., ref., contract, invoice, staff number, client, claim… (Dossier patient 676888-67)", "Identificatie na een contextwoord: dossier, patiënt, nr., ref., contract, factuur, stamnummer, klant, schadegeval… (Dossier patient 676888-67)"],
    ["Date après « né(e) le », « date de naissance », DOB, geboren… (30/12/2006, 2006-12-30, 30 décembre 2006)", "Date after “né(e) le”, “date de naissance”, DOB, geboren… (30/12/2006, 2006-12-30, 30 December 2006)", "Datum na „né(e) le”, „date de naissance”, DOB, geboren… (30/12/2006, 2006-12-30, 30 december 2006)"],
    ["Toutes les dates (30/12/2006, 2006-12-30, 30 décembre 2006)", "All dates (30/12/2006, 2006-12-30, 30 December 2006)", "Alle datums (30/12/2006, 2006-12-30, 30 december 2006)"],
    ["Adresse : rue/avenue/chaussée/straat/laan + numéro, code postal + localité (1000 Bruxelles, B-6041 Gosselies)", "Address: rue/avenue/chaussée/straat/laan + number, postcode + town (1000 Bruxelles, B-6041 Gosselies)", "Adres: rue/avenue/chaussée/straat/laan + nummer, postcode + plaats (1000 Brussel, B-6041 Gosselies)"],
    ["Localité belge (liste intégrée : communes et sections courantes) ou lieu après « domicilié à », « habite à », « né à », « woont in »…", "Belgian town (built-in list: municipalities and common districts) or place after “domicilié à”, “habite à”, “né à”, “woont in”…", "Belgische plaats (ingebouwde lijst: gemeenten en gangbare deelgemeenten) of plaats na „domicilié à”, „habite à”, „né à”, „woont in”…"],
    ["Nom d'animal après chien, chat, cheval, hond, kat, dog, cat… (mon chien Rex)", "Pet name after chien, chat, cheval, hond, kat, dog, cat… (my dog Rex)", "Dierennaam na chien, chat, cheval, hond, kat, dog, cat… (mijn hond Rex)"],
    ["Prénom connu seul (Luc, Els, Tom, Liz…), FR/NL/EN ; sauf prénoms qui sont aussi des mots (Rose, Pierre, May, Will…)", "Known first name alone (Luc, Els, Tom, Liz…), FR/NL/EN; except first names that are also words (Rose, Pierre, May, Will…)", "Bekende voornaam alleen (Luc, Els, Tom, Liz…), FR/NL/EN; behalve voornamen die ook woorden zijn (Rose, Pierre, May, Will…)"],
    ["Hôpitaux et réseaux de soins belges : HUB (aussi H.U.B.), Erasme, Bordet, HUDERF, CHU, UZ, AZ, Chirec, Humani, HELORA…", "Belgian hospitals and care networks: HUB (also H.U.B.), Erasme, Bordet, HUDERF, CHU, UZ, AZ, Chirec, Humani, HELORA…", "Belgische ziekenhuizen en zorgnetwerken: HUB (ook H.U.B.), Erasme, Bordet, HUDERF, CHU, UZ, AZ, Chirec, Humani, HELORA…"],
    ["Mémoire des noms déjà repérés, fautes de frappe comprises ; seules des empreintes HMAC sont stockées", "Memory of names already found, typos included; only HMAC fingerprints are stored", "Geheugen van reeds gevonden namen, tikfouten inbegrepen; alleen HMAC-vingerafdrukken worden bewaard"],
    // fragments of mixed sentences (text split by <b>, <span>…)
    ["connectée ·", "connected ·", "verbonden ·"], ["injoignable ou clé refusée ·", "unreachable or key refused ·", "onbereikbaar of sleutel geweigerd ·"],
    ["désactivée ou non testée · nom", "disabled or not tested · name", "uitgeschakeld of niet getest · naam"],
    ["renvoie une erreur 429 (comptée comme violation),", "returns a 429 error (counted as a violation),", "geeft een 429-fout terug (geteld als overtreding),"],
    ["laisse passer et journalise. Une cible LLM au-dessus de son quota est sautée : la suivante du profil prend le relais. Consommation en direct (jauges) selon la configuration", "lets it through and logs it. A target LLM above its quota is skipped: the next one in the profile takes over. Live consumption (gauges) according to the", "laat door en logt. Een doel-LLM boven zijn quotum wordt overgeslagen: de volgende van het profiel neemt over. Live verbruik (meters) volgens de"],
    ["sinon refus", "otherwise refused", "anders geweigerd"],
    ["ex. Réponds en français, ton professionnel. Ne donne jamais de conseil médical ou juridique définitif.", "e.g. Answer in English, professional tone. Never give definitive medical or legal advice.", "bv. Antwoord in het Nederlands, professionele toon. Geef nooit definitief medisch of juridisch advies."],
    ["un terme par ligne ; un terme en MAJUSCULES (sigle) n'est reconnu qu'en majuscules", "one term per line; a term in CAPITALS (acronym) is only matched in capitals", "één term per regel; een term in HOOFDLETTERS (acroniem) wordt alleen in hoofdletters herkend"],
    ["écrit dans config.toml", "written to config.toml", "geschreven in config.toml"],
    // chat history panel
    ["Conversations", "Conversations", "Gesprekken"], ["+ Nouvelle conversation", "+ New conversation", "+ Nieuw gesprek"],
    ["Les 10 dernières, conservées dans ce navigateur.", "The last 10, kept in this browser.", "De laatste 10, bewaard in deze browser."],
    ["Aucune conversation enregistrée", "No saved conversation", "Geen opgeslagen gesprek"], ["Afficher", "Show", "Tonen"], ["Actions", "Actions", "Acties"],
    ["Afficher l'historique", "Show history", "Geschiedenis tonen"], ["Supprimer cette conversation ?", "Delete this conversation?", "Dit gesprek verwijderen?"],
  ];

  // dynamic texts: [regex on the French text, English, Dutch] (string with $1… or function)
  const tr = s => T(s);
  const R = [
    [/^(.+) · LLMGW$/, (m, a) => `${tr(a)} · LLMGW`, (m, a) => `${tr(a)} · LLMGW`],
    [/^(:\s*)?rôle (.+)$/, "$1role $2", "$1rol $2"], [/^(:\s*)?groupe (.+)$/, "$1group $2", "$1groep $2"],
    [/^sinon (.+)$/, (m, a) => `otherwise ${tr(a)}`, (m, a) => `anders ${tr(a)}`],
    [/^(\d+) modèles \(cliquer pour autoriser\) :$/, "$1 models (click to allow):", "$1 modellen (klik om toe te laten):"],
    [/^échange #(\d+)$/, "exchange #$1", "uitwisseling #$1"],
    [/^jeton d'administration et SSO (.+)$/, "administration token and SSO $1", "beheertoken en SSO $1"],
    [/^Mis à jour (.+)$/, "Updated $1", "Bijgewerkt $1"],
    [/^(\d+) utilisateurs?$/, "$1 user(s)", "$1 gebruiker(s)"],
    [/^(\d+) utilisateurs? · SSO (.+)$/, "$1 user(s) · SSO $2", "$1 gebruiker(s) · SSO $2"],
    [/^(.+) par défaut · (\d+) règle\(s\) spécifique\(s\)$/, (m, a, n) => `${tr(a)} by default · ${n} specific rule(s)`, (m, a, n) => `${tr(a)} standaard · ${n} specifieke regel(s)`],
    [/^connectée · (\d+) ms$/, "connected · $1 ms", "verbonden · $1 ms"],
    [/^injoignable(.*)$/, "unreachable$1", "onbereikbaar$1"],
    [/^(.*) · profils : (.+)$/, "$1 · profiles: $2", "$1 · profielen: $2"],
    [/^(.*) · aucun profil$/, "$1 · no profile", "$1 · geen profiel"],
    [/^(\d+) erreur\(s\)$/, "$1 error(s)", "$1 fout(en)"], [/^(\d+) erreurs$/, "$1 errors", "$1 fouten"],
    [/^(\d+) déclaré\(s\)$/, "$1 declared", "$1 aangemeld"],
    [/^(.+) erreur\(s\) en 48 h$/, "$1 error(s) in 48 h", "$1 fout(en) in 48 u"],
    [/^(.+) requête\(s\) bloquée\(s\) en 48 h$/, "$1 blocked request(s) in 48 h", "$1 geblokkeerd(e) verzoek(en) in 48 u"],
    [/^(\d+) LLM cible\(s\) configurée\(s\)$/, "$1 target LLM(s) configured", "$1 doel-LLM('s) ingesteld"],
    [/^(\d+) LLM cible\(s\) sur (\d+) utilisable\(s\)$/, "$1 of $2 target LLM(s) usable", "$1 van $2 doel-LLM('s) bruikbaar"],
    [/^(\d+) quota\(s\) à plus de 80 %$/, "$1 quota(s) above 80 %", "$1 quotum/quota boven 80 %"],
    [/^Sans clé : (.+)$/, "Without key: $1", "Zonder sleutel: $1"],
    [/^Récapitulatif toutes les (\d+) min\.$/, "Summary every $1 min.", "Samenvatting om de $1 min."],
    [/^Échanges archivés en clair pendant (\d+) h\. À désactiver après le diagnostic\.$/, "Exchanges archived in clear text for $1 h. Disable after the diagnosis.", "Uitwisselingen $1 u onversleuteld gearchiveerd. Uitschakelen na de diagnose."],
    [/^(.+) in · (.+) out$/, "$1 in · $2 out", "$1 in · $2 uit"],
    [/^p50 (.+) · p95 (.+)$/, "p50 $1 · p95 $2", "p50 $1 · p95 $2"],
    [/^1er octet p50 (.+)$/, "first byte p50 $1", "eerste byte p50 $1"],
    [/^p95 (.+) · IN moy\. (.+)$/, "p95 $1 · avg. IN $2", "p95 $1 · gem. IN $2"],
    [/^(\d+) req$/, "$1 req", "$1 verz."],
    [/^profil : (.+)$/, "profile: $1", "profiel: $1"],
    [/^(.+) · profil (.+)$/, "$1 · profile $2", "$1 · profiel $2"],
    [/^Bonjour (.+)$/, "Hello $1", "Hallo $1"],
    [/^(.+) \(pas de clé\)$/, "$1 (no key)", "$1 (geen sleutel)"],
    [/^(.+) : autre…$/, "$1: other…", "$1: ander…"],
    [/^(\d+) donnée\(s\) pseudonymisée\(s\) : (.+)$/, "$1 value(s) pseudonymised: $2", "$1 waarde(n) gepseudonimiseerd: $2"],
    [/^(.+) · (\d+) occurrences dans la conversation$/, "$1 · $2 occurrences in the conversation", "$1 · $2 voorkomens in het gesprek"],
    [/^(\d+) k car\.(.*)$/, (m, n, rest) => `${n} k chars${rest ? " " + tr(rest.trim()) : ""}`, (m, n, rest) => `${n} k tekens${rest ? " " + tr(rest.trim()) : ""}`],
    [/^fichiers \.(\w+) interdits par votre profil$/, ".$1 files forbidden by your profile", ".$1-bestanden verboden door uw profiel"],
    [/^Échange #(\d+)$/, "Exchange #$1", "Uitwisseling #$1"],
    [/^(\d+) échange\(s\)$/, "$1 exchange(s)", "$1 uitwisseling(en)"],
    [/^(.+) · (\d+) car\.$/, "$1 · $2 chars", "$1 · $2 tekens"],
    [/^(.*) · statut (\d+)(.*)$/, (m, a, b, c) => `${a} · status ${b}${c.replace(" · BLOQUÉ", " · BLOCKED")}`, (m, a, b, c) => `${a} · status ${b}${c.replace(" · BLOQUÉ", " · GEBLOKKEERD")}`],
    [/^Nouveaux messages de cet échange \((\d+)\) et réponse du LLM\. Le contexte renvoyé par le client est en bas de page\.$/, "New messages of this exchange ($1) and the LLM answer. The context resent by the client is at the bottom of the page.", "Nieuwe berichten van deze uitwisseling ($1) en het LLM-antwoord. De door de client opnieuw verstuurde context staat onderaan de pagina."],
    [/^· (\d+) message\(s\) antérieurs ou système · (\d+) valeur\(s\) détectée\(s\) au total · (\d+) conversion\(s\)$/, "· $1 earlier or system message(s) · $2 value(s) detected in total · $3 conversion(s)", "· $1 eerdere of systeembericht(en) · $2 waarde(n) in totaal gedetecteerd · $3 conversie(s)"],
    [/^Toutes les valeurs détectées dans l'archive \((\d+)\), tous échanges et contextes confondus, triées par nombre d'échanges\.$/, "All values detected in the archive ($1), all exchanges and contexts combined, sorted by number of exchanges.", "Alle in het archief gedetecteerde waarden ($1), alle uitwisselingen en contexten samen, gesorteerd op aantal uitwisselingen."],
    [/^Un utilisateur déclaré ci-dessus avec la même adresse email garde son profil et ses quotas\. Session : (\d+) h\. Connexion par clé sur le portail : (active|désactivée)\.$/, (m, h, k) => `A user declared above with the same email address keeps their profile and quotas. Session: ${h} h. Key sign-in on the portal: ${k === "active" ? "on" : "off"}.`, (m, h, k) => `Een hierboven aangemelde gebruiker met hetzelfde e-mailadres behoudt profiel en quota. Sessie: ${h} u. Aanmelden met sleutel op het portaal: ${k === "active" ? "aan" : "uit"}.`],
    [/^(\d+) événement\(s\) récents, conservés 400 jours\. Aucune valeur sensible n'y figure\.$/, "$1 recent event(s), kept 400 days. No sensitive value appears.", "$1 recente gebeurtenis(sen), 400 dagen bewaard. Er staat geen gevoelige waarde in."],
    [/^(\d+) nom\(s\) mémorisé\(s\)$/, "$1 name(s) remembered", "$1 naam/namen onthouden"],
    [/^([A-Z_]+) · (anonymisé|bloqué|journalisé|retiré|laissé passer) ×(\d+)$/, (m, k, a, n) => `${k} · ${tr(a)} ×${n}`, (m, k, a, n) => `${k} · ${tr(a)} ×${n}`],
    [/^(\d+)\/(\d+) bloqué\(s\)$/, "$1/$2 blocked", "$1/$2 geblokkeerd"],
    [/^Email de test envoyé à : (.+)$/, "Test email sent to: $1", "Testmail verstuurd naar: $1"],
    [/^Non enregistré : (.+)$/, "Not saved: $1", "Niet opgeslagen: $1"],
    [/^Échec : (.+)$/, "Failed: $1", "Mislukt: $1"],
    [/^Profil utilisé par : (.+)$/, "Profile used by: $1", "Profiel gebruikt door: $1"],
    [/^Cible utilisée par les profils : (.+)$/, "Target used by the profiles: $1", "Doel gebruikt door de profielen: $1"],
    [/^Supprimer (le profil |la cible |le compte )?(.+) \?$/, "Delete $2?", "$2 verwijderen?"],
    [/^Générer une nouvelle clé pour (.+) \? L'ancienne cessera de fonctionner après enregistrement\.$/, "Generate a new key for $1? The old one will stop working once saved.", "Een nieuwe sleutel genereren voor $1? De oude werkt niet meer na het opslaan."],
    [/^Connecté en tant que (.+), sans droit d'administration\.$/, "Signed in as $1, without administration rights.", "Aangemeld als $1, zonder beheerrechten."],
    [/^Connecté en tant que (.+), mais aucun profil de chat ne vous est attribué\.$/, "Signed in as $1, but no chat profile is assigned to you.", "Aangemeld als $1, maar er is u geen chatprofiel toegewezen."],
    [/^Accès refusé pour (.+)$/, "Access refused for $1", "Toegang geweigerd voor $1"],
    // messages from the gateway
    [/^fichier interdit par votre profil : (.+)$/, "file forbidden by your profile: $1", "bestand verboden door uw profiel: $1"],
    [/^requête bloquée par la politique de données : (.+)$/, "request blocked by the data policy: $1", "verzoek geblokkeerd door het gegevensbeleid: $1"],
    [/^modèle "(.*)" non autorisé pour le profil "(.*)"$/, "model \"$1\" not allowed for profile \"$2\"", "model \"$1\" niet toegelaten voor profiel \"$2\""],
    [/^clé API gateway invalide ou utilisateur désactivé$/, "invalid gateway API key or user disabled", "ongeldige gateway-API-sleutel of gebruiker uitgeschakeld"],
    [/^aucun LLM cible disponible(.*)$/, "no target LLM available$1", "geen doel-LLM beschikbaar$1"],
    [/^quota (.+) atteint pour (.+)$/, "quota reached ($1) for $2", "quotum bereikt ($1) voor $2"],
  ];

  const D = { en: {}, nl: {} };
  for (const [fr, en, nl] of W) { D.en[fr] = en; D.nl[fr] = nl; }

  function T(s) {
    if (lang === "fr" || !s) return s;
    const t = s.trim();
    if (!t) return s;
    const d = D[lang][t];
    if (d !== undefined) return s.replace(t, d);
    const i = lang === "en" ? 1 : 2;
    for (const r of R) if (r[0].test(t)) return s.replace(t, t.replace(r[0], r[i]));
    return s;
  }

  const SKIP = new Set(["SCRIPT", "STYLE", "TEXTAREA", "CODE", "PRE"]);
  function translate(root) {
    if (lang === "fr" || !root) return;
    if (root.nodeType === 3) { const p = root.parentElement; if (p && !SKIP.has(p.tagName) && !p.closest('[translate="no"]')) { const v = T(root.nodeValue); if (v !== root.nodeValue) root.nodeValue = v; } return; }
    if (root.nodeType !== 1 || root.closest?.('[translate="no"]')) return;
    if (root.tagName === "TEXTAREA") { const v = root.getAttribute("placeholder"), t = v && T(v); if (t && t !== v) root.setAttribute("placeholder", t); return; }
    if (SKIP.has(root.tagName)) return;
    const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT, {
      acceptNode: n => n.nodeType === 1 && (SKIP.has(n.tagName) || n.getAttribute("translate") === "no") ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT });
    const els = [root];
    for (let n = w.nextNode(); n; n = w.nextNode()) {
      if (n.nodeType === 3) { const v = T(n.nodeValue); if (v !== n.nodeValue) n.nodeValue = v; } else els.push(n);
    }
    for (const e of root.querySelectorAll("textarea[placeholder]")) if (!e.closest('[translate="no"]')) els.push(e);
    for (const e of els) for (const a of ["placeholder", "title", "aria-label"]) { const v = e.getAttribute?.(a); if (v) { const t = T(v); if (t !== v) e.setAttribute(a, t); } }
  }

  // native dialogs
  const c0 = window.confirm.bind(window), p0 = window.prompt.bind(window);
  window.confirm = m => c0(T(String(m)));
  window.prompt = (m, d) => p0(T(String(m)), d);

  function start() {
    document.documentElement.lang = lang;
    translate(document.body);
    if (lang === "fr") return;
    new MutationObserver(ms => {
      for (const m of ms) {
        if (m.type === "characterData") translate(m.target);
        else for (const n of m.addedNodes) translate(n);
        if (m.type === "attributes") translate(m.target);
      }
      const t = T(document.title); if (t !== document.title) document.title = t;
    }).observe(document.body, { childList: true, subtree: true, characterData: true, attributes: true, attributeFilter: ["placeholder", "title", "aria-label"] });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start); else start();

  window.I18N = {
    lang, loc: LOC, T,
    // fills a <select> with the languages; changing it reloads the page in the chosen language
    select(el) {
      if (!el) return;
      el.innerHTML = Object.entries(LANGS).map(([k, v]) => `<option value="${k}" ${k === lang ? "selected" : ""}>${k.toUpperCase()} · ${v}</option>`).join("");
      el.setAttribute("translate", "no");
      el.onchange = () => { try { localStorage.setItem("gwlang", el.value); } catch {} location.reload(); };
    },
  };
})();

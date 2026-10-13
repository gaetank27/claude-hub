// Configuration namespace: appearance/theme picker, Claude Code subscription
// management, deleted-projects vault, and the default session settings form
// (permission mode, model, limits, systemd caps).
export const configuration = {
  en: {
    title: "Configuration",
    intro_pre: "Settings applied to ",
    introStrong: "new",
    intro_post: " sessions. Running sessions keep their own.",

    // Session settings form
    permissionModeLabel: "Default permission mode",
    permissionModeHint:
      "Mode for interactive sessions (autonomous sessions start in plan).",
    modelLabel: "Model",
    modelHint: "Claude model used for new sessions. Sonnet/Haiku use fewer usage limits than Opus.",
    model_opus: "Opus — most capable (default)",
    model_sonnet: "Sonnet — faster, lighter on usage",
    model_haiku: "Haiku — fastest, most economical",
    maxTurnsLabel: "Max turns",
    maxTurnsHint: "Hand back beyond N turns.",
    inactivityLabel: "Inactivity (s)",
    inactivityHint: "Hand back after N s of inactivity.",
    memoryMaxLabel: "Max memory",
    memoryMaxHint: "systemd cap per session (e.g. 4G; empty = none).",
    cpuQuotaLabel: "CPU quota",
    cpuQuotaHint: "systemd cap per session (e.g. 200%; empty = none).",
    capPlaceholder: "(none)",
    savedMark: "Saved ✓",
    handsFreeVoiceLabel: "Hands-free voice mode",
    handsFreeVoiceHint:
      "Shows the live voice conversation (Assistant page and sessions). Needs a fast server: transcription runs locally (faster-whisper). Off by default. Dictation (mic button) and WhatsApp voice notes are not affected.",
    outputTruncationLabel: "Truncate large outputs",
    outputTruncationHint:
      "Automatically truncate Bash outputs over ~40 KB (keeps first 120 + last 60 lines). Saves 60–90% of tokens on git diff, npm test, cat — ~40% savings on average sessions. The hook script is written to <DataDir>/hooks/truncate-output.py and registered in ~/.claude/settings.json.",

    // Claude accounts
    accountsTitle: "Claude Code subscription management",
    accountsIntro_pre: "Each subscription is isolated in its own ",
    accountsIntro_mid: ". The",
    accountsIntroStrong: " default",
    accountsIntro_post:
      " subscription is the one used by new projects and by the usage counter at the top of the page when out of session. Each project can override this choice from its page.",
    badgeDefault: "default",
    badgeLoggedOut: "logged out",
    setDefault: "Set as default",
    signIn: "Sign in",
    disconnect: "Disconnect",
    deleteTitle: "Delete",
    noAccounts: "No subscription.",
    confirmLogout:
      "Disconnect this subscription? Its running sessions will be stopped.",
    confirmRemoveActive:
      "This subscription is active. Removing it will stop its running sessions and erase its configuration directory. Continue?",
    confirmRemoveDefault:
      "Remove “default” from the list? Its running sessions will be stopped. The ~/.claude folder is not deleted; it will be re-detected on next start if no subscription remains.",
    confirmRemove:
      "Delete this subscription? Its running sessions will be stopped and its configuration directory erased.",

    // OAuth sign-in flow
    oauthStep1Prefix: "Open the authorization page: ",
    oauthStep1Link: "authorize access ↗",
    oauthStep2: "Sign in to the desired account and authorize.",
    oauthStep3: "Paste the obtained code below, then “Validate”.",
    codePlaceholder: "Authorization code",
    validate: "Validate",
    loginStepConnecting: "Retrieving authorization URL…",
    loginStepValidating: "Validating code, please wait…",
    tokenExpiresAt: "valid until {{date}}",

    // Connect new subscription
    connectTitle: "Connect a new subscription",
    labelPlaceholder: "Label (e.g. Personal Max)",
    connect: "Connect",
    connectHint:
      "An authorization link will appear: open it, sign in to the desired account, then paste the obtained code.",

    // Signed-in devices
    mcpTitle: "Claude remote connector (claude.ai / mobile app)",
    mcpIntro:
      "Lets claude.ai and the Claude mobile app use your Hub tools through a custom connector. Off by default. Sign-in uses your Hub password AND a TOTP code; every call is audited and rate-limited.",
    mcpEnable: "Enable the remote connector",
    mcpUrlLabel: "Public HTTPS origin of the connector",
    mcpUrlHint: "Dedicated host exposed by your reverse proxy (see the Help), e.g. https://mcp.example.com — no path.",
    mcpConnectorUrl: "Connector URL to paste in Claude",
    mcpSave: "Save",
    mcpWarn:
      "Mail, WhatsApp and script contents are untrusted data (prompt-injection risk). Sensitive tools stay off until you enable them one by one, and Claude must ask for your confirmation in the conversation before each use.",
    mcpTools: "Tools exposed",
    mcpClassRead: "read",
    mcpClassSafe: "safe action",
    mcpClassSensitive: "sensitive · confirmation",
    mcpClassBlocked: "never remote",
    mcpGrants: "Connections (revoke in one click)",
    mcpNoGrants: "No connected client.",
    mcpRevoke: "Revoke",
    mcpRevokeAll: "Revoke everything",
    mcpConfirmRevokeAll: "Revoke every remote connection?",
    mcpLastUsed: "last used {{date}}",
    mcpNeverUsed: "never used",
    mcpAudit: "Audit log (latest calls)",
    mcpAuditEmpty: "No call yet.",
    devicesTitle: "Signed-in devices",
    devicesIntro:
      "Every browser or app currently signed in to the Hub. Signing a device out takes effect immediately; it will have to sign in again (password + code).",
    devicesEmpty: "No signed-in device.",
    devicesUnknown: "Unknown device",
    devicesCurrent: "this device",
    devicesSignedIn: "signed in {{date}}",
    devicesLastSeen: "last seen {{date}}",
    devicesExpires: "expires {{date}}",
    devicesNoExpiry: "stays signed in",
    devicesRevoke: "Sign out",
    devicesRevokeOthers: "Sign out all other devices",
    devicesConfirmCurrent: "This is the device you are using. Sign it out?",
    devicesConfirmOthers: "Sign out every device except this one?",

    // Project vault
    vaultTitle: "Deleted projects (vault)",
    vaultIntro:
      "Compressed archives of projects deleted from Archiving. Kept indefinitely. Restore a project, or permanently destroy its archive (irreversible).",
    vaultEmpty:
      "No archive. Projects deleted from Archiving will appear here.",
    vaultDeletedAt: "deleted {{date}}",
    vaultSessions: "{{count}} session(s)",
    restore: "Restore",
    destroy: "Delete permanently",
    confirmDestroy:
      "PERMANENT and irreversible destruction of “{{name}}”: the compressed archive will be erased from the server, with no way back.\n\nTo confirm, retype the project name exactly:",
    destroyWrongName: "Wrong name — destruction cancelled.",

    // Network settings
    networkTitle: "Network",
    publicBaseUrlLabel: "Public base URL",
    publicBaseUrlHint: "External origin used to build container proxy links (e.g. https://hub.example.com).",
    publicBaseUrlPlaceholder: "https://hub.example.com",
    dnsServerIpLabel: "DNS server IP",
    dnsServerIpHint: "IP address used when provisioning DNS A records for containers.",
    dnsServerIpPlaceholder: "192.0.2.10",
    dnsIpModeLocal: "Local IP",
    dnsIpModeLocalHint: "Detected LAN address of this server",
    dnsIpModePublic: "Public IP",
    dnsIpModePublicHint: "Fetched dynamically, refreshed hourly",
    dnsIpModeCustom: "Custom IP",

    // Appearance
    appearanceTitle: "Appearance",
    appearanceIntro:
      "Interface color theme. Applied immediately, remembered on this device.",
  },
  fr: {
    title: "Configuration",
    intro_pre: "Réglages appliqués aux ",
    introStrong: "nouvelles",
    intro_post: " sessions. Les sessions en cours conservent les leurs.",

    permissionModeLabel: "Mode de permission par défaut",
    permissionModeHint:
      "Mode des sessions interactives (les sessions autonomes démarrent en plan).",
    modelLabel: "Modèle",
    modelHint: "Modèle Claude utilisé pour les nouvelles sessions. Sonnet/Haiku consomment moins de quota qu'Opus.",
    model_opus: "Opus — le plus capable (défaut)",
    model_sonnet: "Sonnet — plus rapide, moins gourmand",
    model_haiku: "Haiku — le plus rapide et économique",
    maxTurnsLabel: "Max turns",
    maxTurnsHint: "Hand-back au-delà de N tours.",
    inactivityLabel: "Inactivité (s)",
    inactivityHint: "Hand-back après N s d'inactivité.",
    memoryMaxLabel: "Mémoire max",
    memoryMaxHint: "Cap systemd par session (ex. 4G ; vide = aucun).",
    cpuQuotaLabel: "Quota CPU",
    cpuQuotaHint: "Cap systemd par session (ex. 200% ; vide = aucun).",
    capPlaceholder: "(aucun)",
    savedMark: "Enregistré ✓",
    handsFreeVoiceLabel: "Mode vocal mains libres",
    handsFreeVoiceHint:
      "Affiche la conversation vocale en direct (page Assistant et sessions). Demande un serveur rapide : la transcription est locale (faster-whisper). Désactivé par défaut. La dictée (bouton micro) et les vocaux WhatsApp ne sont pas concernés.",
    outputTruncationLabel: "Tronquer les grosses sorties",
    outputTruncationHint:
      "Tronque automatiquement les sorties Bash de plus de ~40 Ko (garde les 120 premières + 60 dernières lignes). Économise 60–90 % des tokens sur git diff, npm test, cat — ~40 % d'économie en moyenne. Le script est écrit dans <DataDir>/hooks/truncate-output.py et enregistré dans ~/.claude/settings.json.",

    accountsTitle: "Gestion de l'abonnement Claude Code",
    accountsIntro_pre: "Chaque abonnement est isolé dans son propre ",
    accountsIntro_mid: ". L'abonnement",
    accountsIntroStrong: " par défaut",
    accountsIntro_post:
      " est celui qu'utilisent les nouveaux projets et le compteur d'usage en haut de page hors session. Chaque projet peut surcharger ce choix depuis sa page.",
    badgeDefault: "par défaut",
    badgeLoggedOut: "déconnecté",
    setDefault: "Définir par défaut",
    signIn: "Se connecter",
    disconnect: "Déconnecter",
    deleteTitle: "Supprimer",
    noAccounts: "Aucun abonnement.",
    confirmLogout:
      "Déconnecter cet abonnement ? Ses sessions en cours seront stoppées.",
    confirmRemoveActive:
      "Cet abonnement est actif. Le supprimer stoppera ses sessions en cours et effacera son répertoire de configuration. Continuer ?",
    confirmRemoveDefault:
      "Retirer « default » de la liste ? Ses sessions en cours seront stoppées. Le dossier ~/.claude n'est pas supprimé ; il sera re-détecté au prochain démarrage s'il ne reste aucun abonnement.",
    confirmRemove:
      "Supprimer cet abonnement ? Ses sessions en cours seront stoppées et son répertoire de configuration effacé.",

    oauthStep1Prefix: "Ouvre la page d'autorisation : ",
    oauthStep1Link: "autoriser l'accès ↗",
    oauthStep2: "Connecte-toi au compte voulu et autorise.",
    oauthStep3: "Colle le code obtenu ci-dessous, puis « Valider ».",
    codePlaceholder: "Code d'autorisation",
    validate: "Valider",
    loginStepConnecting: "Récupération de l'URL d'autorisation…",
    loginStepValidating: "Validation du code, veuillez patienter…",
    tokenExpiresAt: "valide jusqu'au {{date}}",

    connectTitle: "Connecter un nouvel abonnement",
    labelPlaceholder: "Libellé (ex. Perso Max)",
    connect: "Connecter",
    connectHint:
      "Un lien d'autorisation s'affichera : ouvre-le, connecte-toi au compte voulu, puis colle le code obtenu.",

    mcpTitle: "Connecteur Claude distant (claude.ai / app mobile)",
    mcpIntro:
      "Permet à claude.ai et à l'app mobile Claude d'utiliser les outils du Hub via un connecteur personnalisé. Désactivé par défaut. La connexion exige votre mot de passe Hub ET un code TOTP ; chaque appel est journalisé et limité en débit.",
    mcpEnable: "Activer le connecteur distant",
    mcpUrlLabel: "Origine HTTPS publique du connecteur",
    mcpUrlHint: "Hôte dédié exposé par votre reverse proxy (voir l'Aide), ex. https://mcp.exemple.com — sans chemin.",
    mcpConnectorUrl: "URL du connecteur à coller dans Claude",
    mcpSave: "Enregistrer",
    mcpWarn:
      "Le contenu des mails, messages WhatsApp et scripts est une donnée non fiable (risque d'injection de prompt). Les outils sensibles restent désactivés tant que vous ne les activez pas un par un, et Claude doit demander votre confirmation dans la conversation avant chaque usage.",
    mcpTools: "Outils exposés",
    mcpClassRead: "lecture",
    mcpClassSafe: "action sans danger",
    mcpClassSensitive: "sensible · confirmation",
    mcpClassBlocked: "jamais à distance",
    mcpGrants: "Connexions (révocation en un clic)",
    mcpNoGrants: "Aucun client connecté.",
    mcpRevoke: "Révoquer",
    mcpRevokeAll: "Tout révoquer",
    mcpConfirmRevokeAll: "Révoquer toutes les connexions distantes ?",
    mcpLastUsed: "dernier usage {{date}}",
    mcpNeverUsed: "jamais utilisée",
    mcpAudit: "Journal d'audit (derniers appels)",
    mcpAuditEmpty: "Aucun appel pour l'instant.",
    devicesTitle: "Appareils connectés",
    devicesIntro:
      "Tous les navigateurs et applications actuellement connectés au Hub. La déconnexion d'un appareil est immédiate ; il devra se reconnecter (mot de passe + code).",
    devicesEmpty: "Aucun appareil connecté.",
    devicesUnknown: "Appareil inconnu",
    devicesCurrent: "cet appareil",
    devicesSignedIn: "connecté {{date}}",
    devicesLastSeen: "vu {{date}}",
    devicesExpires: "expire {{date}}",
    devicesNoExpiry: "reste connecté",
    devicesRevoke: "Déconnecter",
    devicesRevokeOthers: "Déconnecter tous les autres appareils",
    devicesConfirmCurrent: "C'est l'appareil que tu utilises. Le déconnecter ?",
    devicesConfirmOthers: "Déconnecter tous les appareils sauf celui-ci ?",
    vaultTitle: "Projets supprimés (coffre-fort)",
    vaultIntro:
      "Archives compressées des projets supprimés depuis l'Archivage. Conservées indéfiniment. Restaure un projet, ou détruis définitivement son archive (irréversible).",
    vaultEmpty:
      "Aucune archive. Les projets supprimés depuis l'Archivage apparaîtront ici.",
    vaultDeletedAt: "supprimé {{date}}",
    vaultSessions: "{{count}} session(s)",
    restore: "Restaurer",
    destroy: "Supprimer définitivement",
    confirmDestroy:
      "Destruction DÉFINITIVE et irréversible de « {{name}} » : l'archive compressée sera effacée du serveur, sans retour possible.\n\nPour confirmer, retape exactement le nom du projet :",
    destroyWrongName: "Nom incorrect — destruction annulée.",

    // Réseau
    networkTitle: "Réseau",
    publicBaseUrlLabel: "URL publique de base",
    publicBaseUrlHint: "Origine externe utilisée pour les liens proxy des conteneurs (ex. https://hub.example.com).",
    publicBaseUrlPlaceholder: "https://hub.exemple.com",
    dnsServerIpLabel: "IP du serveur DNS",
    dnsServerIpHint: "Adresse IP utilisée lors du provisionnement des enregistrements DNS A pour les conteneurs.",
    dnsServerIpPlaceholder: "192.0.2.10",
    dnsIpModeLocal: "IP locale",
    dnsIpModeLocalHint: "Adresse LAN détectée de ce serveur",
    dnsIpModePublic: "IP publique",
    dnsIpModePublicHint: "Récupérée dynamiquement, rafraîchie toutes les heures",
    dnsIpModeCustom: "IP personnalisée",

    appearanceTitle: "Apparence",
    appearanceIntro:
      "Thème de couleur de l'interface. Appliqué immédiatement, mémorisé sur cet appareil.",
  },
};

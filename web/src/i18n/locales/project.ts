// Per-project session view (ProjectView): sidebar, sessions list, RAG panel,
// editors (category/comment/subscription) and session header.
export const project = {
  en: {
    // RAG panel
    ragTitle: "Project RAG",
    ragTooltip: "Private project RAG index",
    ragIndexing: "indexing… ({{count}})",
    ragFilesChunks: "{{files}} files · {{chunks}} chunks",
    ragWaiting: "waiting",
    ragLastIndexed: "last indexed: {{date}}",
    ragNeverIndexed: "never indexed",
    ragReindexing: "Reindexing…",
    ragReindexNow: "Reindex now",
    ragAutoOnCommit: "Reindex after every commit/push",
    ragEvery: "Every",
    ragMinutesOff: "min (0 = off)",
    ragIndexEnabled: "Index enabled",

    // Category editor
    categoryChangeTooltip: "Change the project category",
    categoryLabel: "Category:",
    categoryNone: "no category",
    categoryPlaceholder: "(no category)",

    // Comment editor
    commentEditTooltip: "Edit the project comment",
    commentLabel: "Note:",
    commentNone: "no note",
    commentPlaceholder: "Free note about the project…",

    // Subscription editor
    subscription: "Subscription",
    subscriptionDefault: "Default",
    subscriptionDisconnected: "disconnected",
    modelLabel: "Model",
    modelDefault: "Global default",
    sessionModelLabel: "Model for next session",
    sessionModelDefault: "Project default",

    // Sidebar
    protected: "protected",
    archive: "Archive project",
    archiveTooltip: "Remove the project from the dashboard (reversible, data kept).",
    newSession: "+ New session",
    newSessionTooltip:
      "Interactive session: you drive Claude message by message, direct execution without plan validation (bypassPermissions).",
    autonomousSession: "+ Autonomous session",
    autonomousSessionTooltip:
      "Plan mode: Claude proposes a plan, you approve it in one click, then it runs on its own. Control returns to you on a question, max-turns or inactivity.",
    sessions: "Sessions",
    noSessions: "No session.",
    scheduledSessions: "Scheduled sessions",

    // Session list item
    autonomousTooltip: "Autonomous session",
    sessionRange: "Session start → end",
    inProgress: "in progress",
    idleTurn: "idle · turn finished",
    usageTooltip: "Tokens (input + output) · session cost",
    tokensSuffix: "tok",
    stop: "stop",
    deleteLink: "delete",
    archiveSessionLink: "archive",
    unarchiveSessionLink: "restore",
    showArchived: "Show archived ({{count}})",
    hideArchived: "Hide archived",
    archivedSessions: "Archived sessions",
    noArchivedSessions: "No archived session.",

    // Usage warning dialog (shown when session window ≥ 80% before launch)
    usageWarnTitle: "Usage at {{pct}}% — limit approaching",
    usageWarnReset: "Window resets at {{time}}",
    usageWarnStartAnyway: "Start anyway",
    usageWarnDefer: "Wait for reset (at {{time}})",
    usageWarnTonight: "Schedule for tonight at 2:00",

    // Empty state
    launchToStart: "Launch a session to drive Claude Code on this project.",

    // Confirmations
    archiveConfirm:
      "Archive “{{name}}”? It will leave the dashboard (reversible).{{warn}}",
    archiveWarnSessions_one:
      " {{count}} active session will be stopped (resumable).",
    archiveWarnSessions_other:
      " {{count}} active sessions will be stopped (resumable).",
    deleteSessionConfirm:
      "Permanently delete this session and all its history?",

    // Session header
    mobileSessions: "← Sessions",
    notePlaceholder: "Add a note…",
    branchTooltip: "Execution branch",
    unreadTooltip: "Unread reply",
    showAutoSessions: "Show bot sessions on the Dashboard",
    showAutoSessionsHint: "WhatsApp / API-key (ha-ia) sessions of this project also appear in the Dashboard and the Recent bar. Hidden by default; always visible inside the project.",
    accessTitle: "Access",
    accessTools: "Tools usable by this project",
    accessToolsAll: "All tools",
    accessToolsList: "Selected tools only",
    accessNoTools: "No tool configured.",
    accessProjects: "Other projects whose code it can search",
    accessNoProjects: "No other project.",
    accessHint: "Tools apply immediately; searchable projects apply to new sessions.",
  },
  fr: {
    // RAG panel
    ragTitle: "RAG du projet",
    ragTooltip: "Index RAG privé du projet",
    ragIndexing: "indexation… ({{count}})",
    ragFilesChunks: "{{files}} fichiers · {{chunks}} chunks",
    ragWaiting: "en attente",
    ragLastIndexed: "dernière indexation : {{date}}",
    ragNeverIndexed: "jamais indexé",
    ragReindexing: "Indexation en cours…",
    ragReindexNow: "Réindexer maintenant",
    ragAutoOnCommit: "Réindexer après chaque commit/push",
    ragEvery: "Toutes les",
    ragMinutesOff: "min (0 = off)",
    ragIndexEnabled: "Index activé",

    // Category editor
    categoryChangeTooltip: "Changer la catégorie du projet",
    categoryLabel: "Catégorie :",
    categoryNone: "sans catégorie",
    categoryPlaceholder: "(sans catégorie)",

    // Comment editor
    commentEditTooltip: "Modifier le commentaire du projet",
    commentLabel: "Note :",
    commentNone: "aucune note",
    commentPlaceholder: "Note libre sur le projet…",

    // Subscription editor
    subscription: "Abonnement",
    subscriptionDefault: "Défaut",
    subscriptionDisconnected: "déconnecté",
    modelLabel: "Modèle",
    modelDefault: "Défaut global",
    sessionModelLabel: "Modèle de la prochaine session",
    sessionModelDefault: "Défaut du projet",

    // Sidebar
    protected: "protégé",
    archive: "Archiver le projet",
    archiveTooltip: "Retire le projet du tableau de bord (réversible, données conservées).",
    newSession: "+ Nouvelle session",
    newSessionTooltip:
      "Session interactive : tu pilotes Claude message par message, exécution directe sans validation de plan (bypassPermissions).",
    autonomousSession: "+ Session autonome",
    autonomousSessionTooltip:
      "Mode plan : Claude propose un plan, tu valides en un clic, puis il exécute seul. La main te revient sur question, max-turns ou inactivité.",
    sessions: "Sessions",
    noSessions: "Aucune session.",
    scheduledSessions: "Sessions planifiées",

    // Session list item
    autonomousTooltip: "Session autonome",
    sessionRange: "Début → fin de session",
    inProgress: "en cours",
    idleTurn: "au repos · tour terminé",
    usageTooltip: "Tokens (entrée + sortie) · coût de la session",
    tokensSuffix: "tok",
    stop: "arrêter",
    deleteLink: "supprimer",
    archiveSessionLink: "archiver",
    unarchiveSessionLink: "restaurer",
    showArchived: "Voir les archivées ({{count}})",
    hideArchived: "Masquer les archivées",
    archivedSessions: "Sessions archivées",
    noArchivedSessions: "Aucune session archivée.",

    // Usage warning dialog
    usageWarnTitle: "Utilisation à {{pct}}% — limite imminente",
    usageWarnReset: "Fenêtre réinitialisée à {{time}}",
    usageWarnStartAnyway: "Démarrer quand même",
    usageWarnDefer: "Attendre le reset (à {{time}})",
    usageWarnTonight: "Planifier cette nuit à 2h",

    // Empty state
    launchToStart: "Lance une session pour piloter Claude Code sur ce projet.",

    // Confirmations
    archiveConfirm:
      "Archiver « {{name}} » ? Il quittera le tableau de bord (réversible).{{warn}}",
    archiveWarnSessions_one:
      " {{count}} session active sera arrêtée (reprenable).",
    archiveWarnSessions_other:
      " {{count}} sessions actives seront arrêtées (reprenables).",
    deleteSessionConfirm:
      "Supprimer définitivement cette session et tout son historique ?",

    // Session header
    mobileSessions: "← Sessions",
    notePlaceholder: "Ajouter une note…",
    branchTooltip: "Branche d'exécution",
    unreadTooltip: "Réponse non lue",
    showAutoSessions: "Afficher les sessions bot sur le Dashboard",
    showAutoSessionsHint: "Les sessions WhatsApp / clé d'API (ha-ia) de ce projet apparaissent aussi dans le Dashboard et la barre Récentes. Masquées par défaut ; toujours visibles dans le projet.",
    accessTitle: "Accès",
    accessTools: "Outils utilisables par ce projet",
    accessToolsAll: "Tous les outils",
    accessToolsList: "Outils sélectionnés uniquement",
    accessNoTools: "Aucun outil configuré.",
    accessProjects: "Autres projets dont il peut consulter le code",
    accessNoProjects: "Aucun autre projet.",
    accessHint: "Les outils s'appliquent immédiatement ; les projets consultables, aux nouvelles sessions.",
  },
};

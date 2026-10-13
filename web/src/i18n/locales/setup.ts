// Strings for the first-boot setup wizard (admin, TOTP, options, Claude, finish).
export const setup = {
  en: {
    title: "Claude Hub — initial setup",

    // Admin step.
    adminIntro: "Create the administrator account.",
    username: "Username",
    emailOptional: "Email (optional)",
    passwordMin: "Password (min. 8 characters)",
    confirmPassword: "Confirm password",
    passwordsMismatch: "passwords do not match",
    next: "Next",

    // TOTP step.
    totpIntro:
      "Scan this QR code with your authenticator app (Google Authenticator, Bitwarden, …), then enter the generated code to confirm.",
    qrAlt: "TOTP QR code",
    manualKey: "Manual key:",
    codePlaceholder: "000000",
    confirm2fa: "Confirm 2FA",

    // Options step.
    publicBaseUrl: "Public base URL",
    publicBaseUrlPlaceholder: "https://claude-hub.example.com",
    enableOidc: "Enable OIDC single sign-on (optional)",
    issuerPlaceholder: "Issuer URL (https://id.example.com)",
    clientIdPlaceholder: "Client ID",
    clientSecretPlaceholder: "Client secret",
    redirectUrlPlaceholder: "Redirect URL",
    allowedEmailsPlaceholder: "Allowed emails (comma-separated, optional)",
    saveContinue: "Save & continue",

    // Claude step.
    claudeIntro:
      "Connect a Claude account so the hub can run Claude Code sessions. Optional — you can also do this later from the Configuration page.",
    kindSubscription: "Subscription",
    kindApiKey: "API key",
    apiKeyLabel: "Anthropic API key",
    apiKeyPlaceholder: "sk-ant-…",
    connect: "Connect",
    connectSubscription: "Connect subscription",
    skipForNow: "Skip for now",
    oauthIntro:
      "Open the authorization page, sign in to your Anthropic account, then paste the code back here.",
    openAuthPage: "Open authorization page ↗",
    pasteCodePlaceholder: "Paste the code",
    validateContinue: "Validate & continue",
    apiKeyConnected: "API key connected.",
    subscriptionConnected: "Subscription connected.",

    // Finish step.
    complete: "Setup complete.",
    finishHint:
      "The administrator account is ready and protected by two-factor authentication. Click below to enter the hub.",
    enterHub: "Enter Claude Hub",
  },
  fr: {
    title: "Claude Hub — configuration initiale",

    // Admin step.
    adminIntro: "Crée le compte administrateur.",
    username: "Nom d'utilisateur",
    emailOptional: "Email (optionnel)",
    passwordMin: "Mot de passe (min. 8 caractères)",
    confirmPassword: "Confirme le mot de passe",
    passwordsMismatch: "les mots de passe ne correspondent pas",
    next: "Suivant",

    // TOTP step.
    totpIntro:
      "Scanne ce QR code avec ton application d'authentification (Google Authenticator, Bitwarden, …), puis saisis le code généré pour confirmer.",
    qrAlt: "QR code TOTP",
    manualKey: "Clé manuelle :",
    codePlaceholder: "000000",
    confirm2fa: "Confirmer la 2FA",

    // Options step.
    publicBaseUrl: "URL de base publique",
    publicBaseUrlPlaceholder: "https://claude-hub.example.com",
    enableOidc: "Activer le SSO OIDC (optionnel)",
    issuerPlaceholder: "URL de l'issuer (https://id.example.com)",
    clientIdPlaceholder: "Client ID",
    clientSecretPlaceholder: "Client secret",
    redirectUrlPlaceholder: "URL de redirection",
    allowedEmailsPlaceholder: "Emails autorisés (séparés par des virgules, optionnel)",
    saveContinue: "Enregistrer et continuer",

    // Claude step.
    claudeIntro:
      "Connecte un compte Claude pour que le hub puisse exécuter des sessions Claude Code. Optionnel — tu peux aussi le faire plus tard depuis la page Configuration.",
    kindSubscription: "Abonnement",
    kindApiKey: "Clé API",
    apiKeyLabel: "Clé API Anthropic",
    apiKeyPlaceholder: "sk-ant-…",
    connect: "Connecter",
    connectSubscription: "Connecter l'abonnement",
    skipForNow: "Ignorer pour l'instant",
    oauthIntro:
      "Ouvre la page d'autorisation, connecte-toi à ton compte Anthropic, puis colle le code ici.",
    openAuthPage: "Ouvrir la page d'autorisation ↗",
    pasteCodePlaceholder: "Colle le code",
    validateContinue: "Valider et continuer",
    apiKeyConnected: "Clé API connectée.",
    subscriptionConnected: "Abonnement connecté.",

    // Finish step.
    complete: "Configuration terminée.",
    finishHint:
      "Le compte administrateur est prêt et protégé par l'authentification à deux facteurs. Clique ci-dessous pour entrer dans le hub.",
    enterHub: "Entrer dans Claude Hub",
  },
};

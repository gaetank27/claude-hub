// Assembled translation resources. Each UI domain owns a namespace file under
// ./locales exporting `{ en, fr }`; this barrel merges them into the two flat
// language bundles consumed by i18next (single "translation" namespace, keyed
// as "<domain>.<key>"). Add a new domain by importing it and adding it to both
// maps below.
import { common } from "./locales/common";
import { nav } from "./locales/nav";
import { login } from "./locales/login";
import { setup } from "./locales/setup";
import { dashboard } from "./locales/dashboard";
import { project } from "./locales/project";
import { skills } from "./locales/skills";
import { environments } from "./locales/environments";
import { archivage } from "./locales/archivage";
import { rag } from "./locales/rag";
import { tools } from "./locales/tools";
import { monitoring } from "./locales/monitoring";
import { configuration } from "./locales/configuration";
import { session } from "./locales/session";
import { ui } from "./locales/ui";
import { help } from "./locales/help";
import { tasks } from "./locales/tasks";
import { scripts } from "./locales/scripts";
import { voice } from "./locales/voice";
import { assistant } from "./locales/assistant";

export const en = {
  common: common.en,
  nav: nav.en,
  login: login.en,
  setup: setup.en,
  dashboard: dashboard.en,
  project: project.en,
  skills: skills.en,
  environments: environments.en,
  archivage: archivage.en,
  rag: rag.en,
  tools: tools.en,
  tasks: tasks.en,
  scripts: scripts.en,
  voice: voice.en,
  assistant: assistant.en,
  monitoring: monitoring.en,
  configuration: configuration.en,
  session: session.en,
  ui: ui.en,
  help: help.en,
};

export const fr = {
  common: common.fr,
  nav: nav.fr,
  login: login.fr,
  setup: setup.fr,
  dashboard: dashboard.fr,
  project: project.fr,
  skills: skills.fr,
  environments: environments.fr,
  archivage: archivage.fr,
  rag: rag.fr,
  tools: tools.fr,
  tasks: tasks.fr,
  scripts: scripts.fr,
  voice: voice.fr,
  assistant: assistant.fr,
  monitoring: monitoring.fr,
  configuration: configuration.fr,
  session: session.fr,
  ui: ui.fr,
  help: help.fr,
};

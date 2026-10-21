// The connector gallery: what Grantra can connect to, and the settings each
// connector asks for. The core checks every setting again when it is saved.

import { send } from "./api";

export interface ConnectorField {
  key: string;
  label: string;
  /**
   * "secret" is typed once and kept encrypted by the server, which hands
   * back a "secret:<id>" reference; an "env:NAME" reference to the server's
   * environment works too.
   */
  type: "text" | "url" | "secret" | "checkbox" | "select";
  required?: boolean;
  placeholder?: string;
  help?: string;
  options?: readonly (readonly [string, string])[];
  /** A secret too long for one line, such as a JSON key. */
  multiline?: boolean;
  /** A checkbox the core treats as on unless it is turned off. */
  defaultOn?: boolean;
}

export interface ConnectorType {
  /** The core's connector kind; none while it is planned. */
  kind?: string;
  name: string;
  blurb: string;
  /** Two letters standing in for a logo. */
  mark: string;
  fields: ConnectorField[];
  /** Where docs/connectors.md explains it. */
  docs: string;
  /** A planned connector, and what to use until it exists. */
  planned?: string;
}

const secretHelp =
  "Grantra keeps it encrypted and never shows it again. Or give a reference such as env:GRANTRA_SECRET_CRM_TOKEN to a variable in the server's environment.";

/** Whether a secret field holds a reference rather than the secret itself. */
export const isSecretRef = (v: string) => /^(env|secret):/.test(v.trim());

/** What the console shows for a secret reference. */
export const secretShown = (v: string) =>
  v.startsWith("secret:") ? "Kept encrypted by Grantra" : v;

export const connectorTypes: ConnectorType[] = [
  {
    kind: "scim",
    name: "SCIM 2.0",
    mark: "SC",
    blurb: "Any app that speaks SCIM 2.0: Slack, Zoom, GitHub Enterprise and most SaaS tools.",
    docs: "#scim-20",
    fields: [
      {
        key: "base_url",
        label: "SCIM endpoint",
        type: "url",
        required: true,
        placeholder: "https://crm.example.com/scim/v2",
      },
      {
        key: "token",
        label: "Bearer token",
        type: "secret",
        required: true,
        help: secretHelp,
      },
    ],
  },
  {
    kind: "ldap",
    name: "LDAP / Active Directory",
    mark: "AD",
    blurb: "Active Directory or OpenLDAP: who is in which directory group.",
    docs: "#ldap-and-active-directory",
    fields: [
      {
        key: "url",
        label: "Server",
        type: "url",
        required: true,
        placeholder: "ldaps://dc1.corp.example.com",
        help: "ldaps://, or ldap:// with StartTLS below.",
      },
      {
        key: "directory",
        label: "Directory",
        type: "select",
        options: [
          ["active_directory", "Active Directory"],
          ["openldap", "OpenLDAP"],
        ],
      },
      {
        key: "bind_dn",
        label: "Service account",
        type: "text",
        required: true,
        placeholder: "CN=grantra,OU=Service Accounts,DC=corp,DC=example,DC=com",
      },
      {
        key: "password",
        label: "Its password",
        type: "secret",
        required: true,
        help: secretHelp,
      },
      {
        key: "base_dn",
        label: "Search under",
        type: "text",
        required: true,
        placeholder: "DC=corp,DC=example,DC=com",
      },
      { key: "start_tls", label: "Use StartTLS on ldap://", type: "checkbox" },
    ],
  },
  {
    kind: "grantra-login",
    name: "Grantra Login",
    mark: "GL",
    blurb: "The sign-in that ships with Grantra: its users, groups and roles.",
    docs: "#grantra-login",
    fields: [
      {
        key: "url",
        label: "Grantra Login address",
        type: "url",
        required: true,
        placeholder: "https://login.example.com",
      },
      {
        key: "client_secret",
        label: "Client secret of grantra-connector",
        type: "secret",
        required: true,
        help: secretHelp,
      },
    ],
  },
  {
    kind: "entra-id",
    name: "Microsoft Entra ID",
    mark: "EN",
    blurb: "Users, groups and admin roles of your Microsoft tenant, with last sign-ins.",
    docs: "#microsoft-entra-id",
    fields: [
      {
        key: "tenant_id",
        label: "Directory (tenant) id",
        type: "text",
        required: true,
        placeholder: "contoso.onmicrosoft.com",
        help: "The tenant id or a verified domain.",
      },
      {
        key: "client_id",
        label: "Application (client) id",
        type: "text",
        required: true,
        placeholder: "00000000-0000-0000-0000-000000000000",
        help: "Of the app registration Grantra signs in as, with the Graph permissions in the docs.",
      },
      {
        key: "client_secret",
        label: "Client secret",
        type: "secret",
        required: true,
        help: secretHelp,
      },
    ],
  },
  {
    kind: "google-workspace",
    name: "Google Workspace",
    mark: "GW",
    blurb: "Users, groups and admin roles of your Google domain, with last sign-ins.",
    docs: "#google-workspace",
    fields: [
      {
        key: "admin_email",
        label: "Administrator to act as",
        type: "text",
        required: true,
        placeholder: "admin@example.com",
      },
      {
        key: "service_account_key",
        label: "Service account key (JSON)",
        type: "secret",
        required: true,
        multiline: true,
        placeholder: '{"type": "service_account", ...}',
        help: "The JSON key of a service account with domain-wide delegation. " + secretHelp,
      },
    ],
  },
  {
    kind: "okta",
    name: "Okta",
    mark: "OK",
    blurb: "Users, groups, app assignments and admin roles, with last sign-ins.",
    docs: "#okta",
    fields: [
      {
        key: "org_url",
        label: "Okta address",
        type: "url",
        required: true,
        placeholder: "https://acme.okta.com",
      },
      {
        key: "token",
        label: "API token",
        type: "secret",
        required: true,
        help:
          "Of a read-only administrator (a group administrator to also change groups). " +
          secretHelp,
      },
      {
        key: "apps",
        label: "Also read who is assigned to each application",
        type: "checkbox",
        defaultOn: true,
      },
    ],
  },
  {
    kind: "csv",
    name: "CSV file",
    mark: "CS",
    blurb: "For apps with no API: upload their user list as a CSV export.",
    docs: "#csv-files",
    fields: [],
  },
  ...(
    [
      ["AWS IAM Identity Center", "AW", "Who can reach which AWS accounts, and how."],
      ["GitHub", "GH", "Organization members, teams and repository roles."],
      ["Salesforce", "SF", "Users, profiles and permission sets."],
      ["Workday", "WD", "Joiners, movers and leavers straight from HR."],
      ["BambooHR", "BH", "Joiners, movers and leavers straight from HR."],
    ] as const
  ).map(([name, mark, blurb]) => ({
    name,
    mark,
    blurb,
    docs: "#scim-20",
    fields: [],
    planned:
      mark === "WD" || mark === "BH"
        ? "Meanwhile, send its export to an HR source as a CSV file, by hand or with a service token."
        : "Meanwhile, use SCIM 2.0 if it offers it, or upload a CSV export.",
  })),
];

/** The connector settings to save: filled-in fields only, trimmed, booleans when on. */
export function connectorConfig(type: ConnectorType, values: Record<string, string | boolean>) {
  const config: Record<string, string | boolean> = {};
  for (const f of type.fields) {
    const v = values[f.key];
    if (f.type === "checkbox") {
      if (v === true) config[f.key] = true;
    } else if (typeof v === "string" && v.trim()) {
      config[f.key] = v.trim();
    }
  }
  return config;
}

/** The fields still empty that the connector needs; secrets kept from before count as filled. */
export const missingFields = (
  type: ConnectorType,
  values: Record<string, string | boolean>,
  kept?: Record<string, unknown> | null,
) =>
  type.fields.filter(
    (f) =>
      f.required &&
      !(typeof values[f.key] === "string" && String(values[f.key]).trim()) &&
      !(f.type === "secret" && typeof kept?.[f.key] === "string"),
  );

/**
 * The values to show when changing a connector: its config, with secrets
 * blank (left blank, the secret saved before is kept).
 */
export function connectorValues(type: ConnectorType, config: Record<string, unknown> | null) {
  const values: Record<string, string | boolean> = { ...defaultValues(type) };
  for (const f of type.fields) {
    const v = config?.[f.key];
    if (f.type === "secret") continue;
    if (typeof v === "string" || typeof v === "boolean") values[f.key] = v;
  }
  return values;
}

/** The values a new connector starts with: the first option of each list, boxes ticked by default. */
export function defaultValues(type: ConnectorType) {
  const values: Record<string, string | boolean> = {};
  for (const f of type.fields) {
    const first = f.options?.[0]?.[0];
    if (f.type === "select" && first) values[f.key] = first;
    if (f.type === "checkbox" && f.defaultOn) values[f.key] = true;
  }
  return values;
}

/**
 * The config to save: secrets typed in are stored first and replaced by
 * their reference, and secrets left blank keep the reference in kept.
 */
export async function saveableConfig(
  type: ConnectorType,
  values: Record<string, string | boolean>,
  label: string,
  kept?: Record<string, unknown> | null,
) {
  const config: Record<string, string | boolean> = connectorConfig(type, values);
  for (const f of type.fields) {
    if (f.type !== "secret") continue;
    const v = config[f.key];
    if (typeof v !== "string") {
      if (typeof kept?.[f.key] === "string") config[f.key] = kept[f.key] as string;
    } else if (!isSecretRef(v)) {
      const { ref } = await send<{ ref: string }>("POST", "/api/v1/connector-secrets", {
        label: `${label}: ${f.label}`,
        value: v,
      });
      config[f.key] = ref;
    }
  }
  // Off-by-default boxes the user left on their default are dropped by connectorConfig.
  for (const f of type.fields) {
    if (f.type === "checkbox" && f.defaultOn && values[f.key] !== true) config[f.key] = false;
  }
  return config;
}

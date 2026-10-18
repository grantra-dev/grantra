// The connector gallery: what Grantra can connect to, and the settings each
// connector asks for. The core checks every setting again when it is saved.

export interface ConnectorField {
  key: string;
  label: string;
  /** "secret" holds a reference to a secret in the server's environment, never the secret. */
  type: "text" | "url" | "secret" | "checkbox" | "select";
  required?: boolean;
  placeholder?: string;
  help?: string;
  options?: readonly (readonly [string, string])[];
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
  "A reference such as env:GRANTRA_SECRET_CRM_TOKEN, not the secret itself: the secret goes in the server's environment, so it never shows in the console or the audit log.";

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
        placeholder: "env:GRANTRA_SECRET_CRM_SCIM_TOKEN",
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
        placeholder: "env:GRANTRA_SECRET_AD_PASSWORD",
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
        placeholder: "env:GRANTRA_SECRET_GRANTRA_LOGIN",
        help: secretHelp,
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
      ["Microsoft Entra ID", "EN", "Users, groups and app roles of your Microsoft tenant."],
      ["Google Workspace", "GW", "Users and groups of your Google domain."],
      ["AWS IAM Identity Center", "AW", "Who can reach which AWS accounts, and how."],
      ["GitHub", "GH", "Organization members, teams and repository roles."],
      ["Okta", "OK", "Users, groups and app assignments."],
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

/** The fields still empty that the connector needs. */
export const missingFields = (type: ConnectorType, values: Record<string, string | boolean>) =>
  type.fields.filter(
    (f) => f.required && !(typeof values[f.key] === "string" && String(values[f.key]).trim()),
  );

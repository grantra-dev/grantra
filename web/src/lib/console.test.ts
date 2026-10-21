import { describe, expect, it } from "vitest";
import type { ReviewItem } from "./api";
import { serve } from "@/test-utils";
import {
  connectorConfig,
  connectorTypes,
  connectorValues,
  missingFields,
  saveableConfig,
} from "./connectors";
import { filterReviewItems, sortReviewItems } from "./reviews";
import { hitLink } from "./search";
import { hasWords } from "./utils";

const item = (over: Partial<ReviewItem>): ReviewItem => ({
  id: "i",
  campaign_id: "c1",
  campaign_name: "Q4",
  identity_id: "p",
  identity_name: "Pedro",
  type: "entitlement",
  target_id: "t",
  target_name: "Admins",
  application: "ERP",
  risk: "low",
  origin: "request",
  granted_at: "2026-01-01T00:00:00Z",
  reviewer_id: "m",
  reviewer_name: "Marta",
  decision: "pending",
  ...over,
});

describe("review filters", () => {
  const items = [
    item({ id: "1", identity_name: "Laura", target_name: "Payments", risk: "critical" }),
    item({
      id: "2",
      identity_name: "Ana",
      target_name: "Reports",
      granted_at: "2025-01-01T00:00:00Z",
    }),
    item({
      id: "3",
      identity_name: "Ana",
      target_name: "Admins",
      campaign_id: "c2",
      origin: "eligibility",
    }),
  ];

  it("keeps what matches every part", () => {
    const ids = (f: Parameters<typeof filterReviewItems>[1]) =>
      filterReviewItems(items, f).map((i) => i.id);
    expect(ids({})).toEqual(["1", "2", "3"]);
    expect(ids({ campaign: "c2" })).toEqual(["3"]);
    expect(ids({ text: "ana erp" })).toEqual(["2", "3"]);
    expect(ids({ text: "ana pay" })).toEqual([]);
    expect(ids({ risk: "critical" })).toEqual(["1"]);
    expect(ids({ origin: "eligibility" })).toEqual(["3"]);
  });

  it("sorts by person, access, risk or age", () => {
    const ids = (by: Parameters<typeof sortReviewItems>[1]) =>
      sortReviewItems(items, by).map((i) => i.id);
    expect(ids("person")).toEqual(["3", "2", "1"]);
    expect(ids("access")).toEqual(["3", "1", "2"]);
    expect(ids("risk")).toEqual(["1", "3", "2"]);
    expect(ids("oldest")).toEqual(["2", "3", "1"]);
    expect(items.map((i) => i.id)).toEqual(["1", "2", "3"]);
  });
});

describe("search hits", () => {
  const admin = () => true;
  const employee = () => false;
  it("open the page that shows each thing", () => {
    const hit = { id: "x", title: "Sales reps", application_id: "app" };
    expect(hitLink({ ...hit, kind: "entitlement" }, admin)).toBe("/governance/applications/app");
    expect(hitLink({ ...hit, kind: "entitlement" }, employee)).toBe("/request?q=Sales%20reps");
    expect(hitLink({ ...hit, kind: "role" }, admin)).toBe("/governance/roles");
    expect(hitLink({ ...hit, kind: "role" }, employee)).toBe("/request?q=Sales%20reps");
    expect(hitLink({ ...hit, kind: "person" }, employee)).toBe("/people/x");
    expect(hitLink({ ...hit, kind: "vault_account" }, employee)).toBe("/vault?account=x");
    expect(hitLink({ ...hit, kind: "server" }, employee)).toBe("/servers?target=x");
    expect(hitLink({ ...hit, kind: "remote_target" }, employee)).toBe("/remote-access?target=x");
  });
});

describe("connector settings", () => {
  const ldap = connectorTypes.find((t) => t.kind === "ldap")!;
  it("saves only what is filled in, trimmed", () => {
    expect(
      connectorConfig(ldap, {
        url: " ldaps://dc1 ",
        bind_dn: "",
        start_tls: false,
        directory: "openldap",
        stray: "x",
      }),
    ).toEqual({ url: "ldaps://dc1", directory: "openldap" });
    expect(connectorConfig(ldap, { start_tls: true })).toEqual({ start_tls: true });
  });

  it("names the required fields still empty", () => {
    expect(missingFields(ldap, { url: "ldaps://dc1", bind_dn: "  " }).map((f) => f.key)).toEqual([
      "bind_dn",
      "password",
      "base_dn",
    ]);
  });

  it("stores typed secrets on the server and saves only their references", async () => {
    const stored: unknown[] = [];
    serve({
      "POST /api/v1/connector-secrets": (init) => {
        stored.push(JSON.parse(init!.body as string));
        return { status: 201, body: { ref: "secret:s1" } };
      },
    });
    const okta = connectorTypes.find((t) => t.kind === "okta")!;
    const org = "https://acme.okta.com";
    expect(
      await saveableConfig(okta, { org_url: org, token: " 00abc ", apps: false }, "Okta"),
    ).toEqual({ org_url: org, token: "secret:s1", apps: false });
    expect(stored).toEqual([{ label: "Okta: API token", value: "00abc" }]);
    // References go as they are, and a secret left blank keeps the saved one.
    expect(
      await saveableConfig(
        okta,
        { org_url: org, token: "env:GRANTRA_SECRET_OKTA", apps: true },
        "Okta",
      ),
    ).toEqual({ org_url: org, token: "env:GRANTRA_SECRET_OKTA", apps: true });
    const kept = { org_url: org, token: "secret:old" };
    expect(await saveableConfig(okta, connectorValues(okta, kept), "Okta", kept)).toEqual({
      org_url: org,
      token: "secret:old",
      apps: true,
    });
    expect(missingFields(okta, connectorValues(okta, kept), kept)).toEqual([]);
    expect(stored).toHaveLength(1);
  });
});

describe("filter boxes", () => {
  it("match every word, in any of the texts, whatever the case", () => {
    expect(hasWords("", "anything")).toBe(true);
    expect(hasWords("  ", undefined)).toBe(true);
    expect(hasWords("aws admin", "Admins", "AWS")).toBe(true);
    expect(hasWords("PAY", "Payments")).toBe(true);
    expect(hasWords("aws sales", "Admins", "AWS")).toBe(false);
    expect(hasWords("x", undefined, undefined)).toBe(false);
  });
});

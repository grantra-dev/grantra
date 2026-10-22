import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { useState } from "react";
import { FormActions } from "@/components/FormActions";
import { PickPerson, type Picked } from "@/components/PersonSearch";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/field";
import { useDebounced } from "@/lib/hooks";
import {
  idsOf,
  listAttributes,
  previewAccessRule,
  saveAccessRule,
  saveRole,
  type AccessRule,
  type BusinessRole,
  type RuleChanges,
} from "@/lib/admin";
import { AccessPicker } from "./shared";

/** Refreshes everything that shows roles, rules and what people hold. */
function useAccessRefresh() {
  const client = useQueryClient();
  return () =>
    Promise.all(
      ["roles", "access-rules", "entitlements", "catalog-applications"].map((k) =>
        client.invalidateQueries({ queryKey: [k] }),
      ),
    );
}

/** The required name field both editors open with. */
function NameField({
  id,
  value,
  onChange,
  placeholder,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <div>
      <Label htmlFor={id}>Name</Label>
      <Input
        id={id}
        required
        maxLength={200}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </div>
  );
}

/** A business role's name, owner and entitlements; new, or replacing role. */
export function RoleForm({ role, onDone }: { role?: BusinessRole; onDone: () => void }) {
  const refresh = useAccessRefresh();
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [owner, setOwner] = useState<Picked | undefined>(
    role?.owner_id ? { id: role.owner_id, name: role.owner_name ?? "" } : undefined,
  );
  const [picked, setPicked] = useState(role?.entitlements.map((e) => `entitlement:${e.id}`) ?? []);
  const save = useMutation({
    mutationFn: () =>
      saveRole(
        {
          name,
          description,
          owner_id: owner?.id ?? "",
          entitlement_ids: idsOf(picked, "entitlement"),
        },
        role?.id,
      ),
    onSuccess: async () => {
      await refresh();
      onDone();
    },
  });
  return (
    <form
      className="grid gap-4 md:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <NameField id="role-name" value={name} onChange={setName} placeholder="Sales rep" />
      <div>
        <Label htmlFor="role-owner">Owner</Label>
        <PickPerson
          id="role-owner"
          value={owner}
          onChange={setOwner}
          placeholder="Who answers for it"
        />
      </div>
      <div className="md:col-span-2">
        <Label htmlFor="role-description">Description</Label>
        <Input
          id="role-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="What people with this job need"
        />
      </div>
      <div className="md:col-span-2">
        <AccessPicker
          id="role-entitlements"
          label="Entitlements it bundles"
          value={picked}
          onChange={setPicked}
          roles={false}
        />
      </div>
      {role && (
        <p className="text-sm text-slate-500 md:col-span-2">
          Whoever holds the role gets what you add and loses what you take out, right away.
        </p>
      )}
      <FormActions
        action={save}
        submit={role ? "Save role" : "Add role"}
        onCancel={onDone}
        className="md:col-span-2"
      />
    </form>
  );
}

type Condition = { attr: string; values: string };

const toConditions = (rows: Condition[]) =>
  Object.fromEntries(
    rows
      .map(
        (r) =>
          [
            r.attr.trim(),
            r.values
              .split(",")
              .map((v) => v.trim())
              .filter(Boolean),
          ] as const,
      )
      .filter(([attr, values]) => attr && values.length > 0),
  );

/**
 * A birthright rule: who it is for (HR attributes), what it gives, and
 * whether it is on, with who matches now as the conditions change. Saving
 * applies it at once. initial fills a new rule, such as access picked
 * elsewhere.
 */
export function RuleForm({
  rule,
  initial,
  onDone,
  onCancel,
  submit,
}: {
  rule?: AccessRule;
  initial?: { name?: string; access?: string[] };
  /** After saving, with how many people gained and lost access. */
  onDone: (changes: RuleChanges) => void;
  onCancel?: () => void;
  submit?: string;
}) {
  const refresh = useAccessRefresh();
  const [name, setName] = useState(rule?.name ?? initial?.name ?? "");
  const [description, setDescription] = useState(rule?.description ?? "");
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [rows, setRows] = useState<Condition[]>(
    rule
      ? Object.entries(rule.conditions).map(([attr, values]) => ({
          attr,
          values: values.join(", "),
        }))
      : [{ attr: "department", values: "" }],
  );
  const [picked, setPicked] = useState(
    rule
      ? [
          ...rule.roles.map((r) => `role:${r.id}`),
          ...rule.entitlements.map((e) => `entitlement:${e.id}`),
        ]
      : (initial?.access ?? []),
  );
  const attributes = useQuery({ queryKey: ["access-rule-attributes"], queryFn: listAttributes });
  const conditions = toConditions(rows);
  const debounced = useDebounced(JSON.stringify(conditions));
  const preview = useQuery({
    queryKey: ["access-rule-preview", debounced],
    queryFn: () => previewAccessRule(JSON.parse(debounced) as Record<string, string[]>),
  });
  const save = useMutation({
    mutationFn: () =>
      saveAccessRule(
        {
          name,
          description,
          conditions,
          enabled,
          role_ids: idsOf(picked, "role"),
          entitlement_ids: idsOf(picked, "entitlement"),
        },
        rule?.id,
      ),
    onSuccess: async (changes) => {
      await refresh();
      onDone(changes);
    },
  });
  const set = (i: number, row: Partial<Condition>) =>
    setRows(rows.map((r, j) => (j === i ? { ...r, ...row } : r)));
  const valuesOf = (attr: string) =>
    attributes.data?.find((a) => a.name === attr.trim())?.values ?? [];

  return (
    <form
      className="grid gap-4 md:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <NameField id="rule-name" value={name} onChange={setName} placeholder="Everyone in Sales" />
      <div>
        <Label htmlFor="rule-description">Description</Label>
        <Input
          id="rule-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      <fieldset className="md:col-span-2">
        <legend className="mb-1 text-sm font-medium">Who it is for</legend>
        <p className="mb-2 text-xs text-slate-500">
          Every line must match, with any of its values (comma separated). No lines means everyone.
        </p>
        <datalist id="rule-attributes">
          {attributes.data?.map((a) => (
            <option key={a.name} value={a.name} />
          ))}
        </datalist>
        <div className="flex flex-col gap-2">
          {rows.map((r, i) => (
            <div key={i} className="flex flex-wrap items-center gap-2">
              <Input
                aria-label={`Attribute ${i + 1}`}
                list="rule-attributes"
                className="w-40"
                value={r.attr}
                onChange={(e) => set(i, { attr: e.target.value })}
                placeholder="department"
              />
              <span className="text-sm text-slate-500">is</span>
              <Input
                aria-label={`Values ${i + 1}`}
                list={`rule-values-${i}`}
                className="min-w-48 flex-1"
                value={r.values}
                onChange={(e) => set(i, { values: e.target.value })}
                placeholder="Sales, Marketing"
              />
              <datalist id={`rule-values-${i}`}>
                {valuesOf(r.attr).map((v) => (
                  <option key={v} value={v} />
                ))}
              </datalist>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                aria-label={`Remove condition ${i + 1}`}
                onClick={() => setRows(rows.filter((_, j) => j !== i))}
              >
                <X className="size-4" aria-hidden />
              </Button>
            </div>
          ))}
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="mt-2"
          onClick={() => setRows([...rows, { attr: "", values: "" }])}
        >
          <Plus className="size-4" aria-hidden /> Add condition
        </Button>
        {preview.data && (
          <p className="mt-2 text-sm" aria-live="polite">
            Matches {preview.data.matches} {preview.data.matches === 1 ? "person" : "people"} now
            {preview.data.sample.length > 0 &&
              `: ${preview.data.sample
                .slice(0, 5)
                .map((p) => p.display_name)
                .join(", ")}${preview.data.matches > 5 ? "…" : ""}`}
          </p>
        )}
      </fieldset>
      <div className="md:col-span-2">
        <AccessPicker
          id="rule-access"
          label="What it gives"
          value={picked}
          onChange={setPicked}
          required
        />
      </div>
      <label className="flex items-center gap-2 text-sm md:col-span-2">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        On: give and take access as people match and stop matching
      </label>
      <FormActions
        action={save}
        submit={submit ?? (rule ? "Save rule" : "Add rule")}
        onCancel={onCancel}
        className="md:col-span-2"
      />
    </form>
  );
}

/** What saving a rule did to people's access. */
export function RuleOutcome({ changes }: { changes: RuleChanges }) {
  return (
    <Alert variant="success">
      Saved. {changes.added} {changes.added === 1 ? "person got" : "people got"} access and{" "}
      {changes.removed} lost it.
    </Alert>
  );
}

import { useMutation } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { FormActions } from "@/components/FormActions";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { setConnector } from "@/lib/admin";
import {
  useConnection,
  type Connection,
  type ConnectorField,
  type ConnectorType,
} from "@/lib/connectors";

const docsBase = "https://github.com/grantra-dev/grantra/blob/main/docs/";

/**
 * A connector's settings, for a new connection or to change one. Secrets
 * typed here go to the server once and come back as references; when
 * changing a connector, a secret left blank keeps the one saved before.
 */
export function ConnectorSettings({
  type,
  label,
  current,
  target,
  onSaved,
  children,
}: {
  type: ConnectorType;
  /** What the secrets are for, as the audit log names them: the application's name. */
  label: string;
  /** The config saved before, when changing a connector. */
  current?: Record<string, unknown> | null;
  /** The application to save the settings on, made first if need be. */
  target: () => Promise<string>;
  onSaved: () => void;
  /** Buttons next to Save. */
  children?: React.ReactNode;
}) {
  const connection = useConnection(type, current);
  const save = useMutation({
    mutationFn: async () => {
      const appId = await target();
      await setConnector(appId, type.kind, await connection.saveable(label));
    },
    onSuccess: onSaved,
  });
  return (
    <form
      className="flex max-w-lg flex-col gap-4 text-sm"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      {type.fields.length === 0 && (
        <p className="text-slate-600">
          Nothing to fill in: you upload the files from the application&apos;s page, where you can
          also say which column is which.
        </p>
      )}
      <ConnectionFields connection={connection} />
      <FormActions
        action={save}
        submit={save.isPending ? "Saving…" : current ? "Save settings" : "Save and continue"}
        disabled={!connection.complete}
      >
        {children}
      </FormActions>
    </form>
  );
}

/** The fields of a connection, and where every setting is explained. */
export function ConnectionFields({ connection }: { connection: Connection }) {
  const { type, current, values, setValues } = connection;
  return (
    <>
      {type.fields.map((f) => (
        <Field
          key={f.key}
          f={f}
          value={values[f.key]}
          kept={f.type === "secret" && typeof current?.[f.key] === "string"}
          onChange={(v) => setValues({ ...values, [f.key]: v })}
        />
      ))}
      <a
        href={docsBase + type.docs}
        target="_blank"
        rel="noreferrer"
        className="inline-flex items-center gap-1 text-brand-600"
      >
        Every setting, explained <ExternalLink className="size-3.5" aria-hidden />
      </a>
    </>
  );
}

function Field({
  f,
  value,
  kept,
  onChange,
}: {
  f: ConnectorField;
  value: string | boolean | undefined;
  /** A secret is saved already, and stays unless a new one is typed. */
  kept: boolean;
  onChange: (v: string | boolean) => void;
}) {
  const id = `connector-${f.key}`;
  if (f.type === "checkbox") {
    return (
      <label className="flex items-center gap-2">
        <input
          id={id}
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
        {f.label}
      </label>
    );
  }
  const common = {
    id,
    required: f.required && !kept,
    value: String(value ?? ""),
    placeholder: kept ? "Saved. Type a new one to replace it" : f.placeholder,
    spellCheck: false,
    autoComplete: "off",
  };
  return (
    <div>
      <Label htmlFor={id}>{f.label}</Label>
      {f.type === "select" ? (
        <Select id={id} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
          {f.options?.map(([v, label]) => (
            <option key={v} value={v}>
              {label}
            </option>
          ))}
        </Select>
      ) : f.multiline ? (
        <Textarea {...common} rows={4} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <Input
          {...common}
          type={f.type === "url" ? "url" : f.type === "secret" ? "password" : "text"}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {f.help && <p className="mt-1 text-xs text-slate-500">{f.help}</p>}
    </div>
  );
}

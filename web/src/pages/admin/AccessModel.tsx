import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  deleteAccessRule,
  listAccessRules,
  listRoles,
  type AccessRule,
  type BusinessRole,
  type RuleChanges,
} from "@/lib/admin";
import { RoleForm, RuleForm, RuleOutcome } from "./AccessEditors";

/** Business roles and the birthright rules that hand out access, to add and change. */
export function AccessModel() {
  const client = useQueryClient();
  const roles = useQuery({ queryKey: ["roles"], queryFn: listRoles });
  const rules = useQuery({ queryKey: ["access-rules"], queryFn: listAccessRules });
  const [rule, setRule] = useState<AccessRule | "new" | null>(null);
  const [role, setRole] = useState<BusinessRole | "new" | null>(null);
  const [outcome, setOutcome] = useState<RuleChanges | null>(null);
  const remove = useMutation({
    mutationFn: (id: string) => deleteAccessRule(id),
    onSuccess: (changes) => {
      setOutcome(changes);
      void client.invalidateQueries({ queryKey: ["access-rules"] });
    },
  });

  return (
    <div className="flex flex-col gap-6">
      <section>
        <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="font-semibold">Birthright rules</h2>
            <p className="text-sm text-slate-500">
              Who gets what automatically, from their HR attributes. People who stop matching lose
              it.
            </p>
          </div>
          {!rule && (
            <Manage>
              <Button
                size="sm"
                onClick={() => {
                  setOutcome(null);
                  setRule("new");
                }}
              >
                Add rule
              </Button>
            </Manage>
          )}
        </div>
        {rule && (
          <Card className="mb-4">
            <CardHeader>
              <CardTitle>
                {rule === "new" ? "New birthright rule" : `Change ${rule.name}`}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <RuleForm
                key={rule === "new" ? "new" : rule.id}
                rule={rule === "new" ? undefined : rule}
                onDone={(changes) => {
                  setOutcome(changes);
                  setRule(null);
                }}
                onCancel={() => setRule(null)}
              />
            </CardContent>
          </Card>
        )}
        {outcome && (
          <div className="mb-4">
            <RuleOutcome changes={outcome} />
          </div>
        )}
        <ErrorAlert error={remove.error} className="mb-4" />
        <Loaded
          loading={rules.isPending}
          error={rules.error}
          empty={rules.data?.length === 0 && "No birthright rules yet."}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Rule</TableHead>
                <TableHead>Applies to</TableHead>
                <TableHead>Gives</TableHead>
                <TableHead>People</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rules.data?.map((r) => (
                <TableRow key={r.id} className="align-top">
                  <TableCell>
                    <div className="font-medium">{r.name}</div>
                    {!r.enabled && <Badge className="mt-1">Off</Badge>}
                    {r.description && <div className="text-slate-500">{r.description}</div>}
                  </TableCell>
                  <TableCell>
                    {Object.keys(r.conditions).length === 0 ? (
                      "Everyone"
                    ) : (
                      <ul>
                        {Object.entries(r.conditions).map(([attr, values]) => (
                          <li key={attr}>
                            <span className="text-slate-500">{attr}:</span> {values.join(" or ")}
                          </li>
                        ))}
                      </ul>
                    )}
                  </TableCell>
                  <TableCell>
                    <ul>
                      {r.roles.map((x) => (
                        <li key={x.id}>Role: {x.name}</li>
                      ))}
                      {r.entitlements.map((x) => (
                        <li key={x.id}>
                          {x.name}
                          {x.application && (
                            <span className="text-slate-500"> · {x.application}</span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </TableCell>
                  <TableCell>{r.holders}</TableCell>
                  <TableCell>
                    <Manage>
                      <div className="flex gap-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={`Edit ${r.name}`}
                          onClick={() => {
                            setOutcome(null);
                            setRule(r);
                          }}
                        >
                          Edit
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-red-700"
                          aria-label={`Delete ${r.name}`}
                          disabled={remove.isPending}
                          onClick={() => {
                            if (
                              window.confirm(
                                `Delete ${r.name}? The ${r.holders} people who hold access through it lose it now.`,
                              )
                            )
                              remove.mutate(r.id);
                          }}
                        >
                          Delete
                        </Button>
                      </div>
                    </Manage>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Loaded>
      </section>
      <section>
        <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="font-semibold">Business roles</h2>
            <p className="text-sm text-slate-500">Bundles of entitlements given together.</p>
          </div>
          {!role && (
            <Manage>
              <Button size="sm" onClick={() => setRole("new")}>
                Add role
              </Button>
            </Manage>
          )}
        </div>
        {role && (
          <Card className="mb-4">
            <CardHeader>
              <CardTitle>{role === "new" ? "New business role" : `Change ${role.name}`}</CardTitle>
            </CardHeader>
            <CardContent>
              <RoleForm
                key={role === "new" ? "new" : role.id}
                role={role === "new" ? undefined : role}
                onDone={() => setRole(null)}
              />
            </CardContent>
          </Card>
        )}
        <Loaded
          loading={roles.isPending}
          error={roles.error}
          empty={roles.data?.length === 0 && "No business roles yet."}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Role</TableHead>
                <TableHead>Owner</TableHead>
                <TableHead>Entitlements</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {roles.data?.map((r) => (
                <TableRow key={r.id} className="align-top">
                  <TableCell>
                    <div className="font-medium">{r.name}</div>
                    {r.description && <div className="text-slate-500">{r.description}</div>}
                  </TableCell>
                  <TableCell>{r.owner_name ?? "—"}</TableCell>
                  <TableCell>
                    {r.entitlements.length === 0
                      ? "—"
                      : r.entitlements.map((e) => `${e.name} (${e.application_name})`).join(", ")}
                  </TableCell>
                  <TableCell>
                    <Manage>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Edit ${r.name}`}
                        onClick={() => setRole(r)}
                      >
                        Edit
                      </Button>
                    </Manage>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Loaded>
      </section>
    </div>
  );
}

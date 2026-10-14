import { useQuery } from "@tanstack/react-query";
import { Loaded } from "@/components/Loaded";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { listAccessRules, listRoles } from "@/lib/admin";

/** Business roles and the birthright rules that hand out access. Read only for now. */
export function AccessModel() {
  const roles = useQuery({ queryKey: ["roles"], queryFn: listRoles });
  const rules = useQuery({ queryKey: ["access-rules"], queryFn: listAccessRules });
  return (
    <div className="flex flex-col gap-6">
      <section>
        <div className="mb-3">
          <h2 className="font-semibold">Birthright rules</h2>
          <p className="text-sm text-slate-500">
            Who gets what automatically, from their HR attributes. People who stop matching lose it.
          </p>
        </div>
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
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Loaded>
      </section>
      <section>
        <div className="mb-3">
          <h2 className="font-semibold">Business roles</h2>
          <p className="text-sm text-slate-500">Bundles of entitlements given together.</p>
        </div>
        <Loaded
          loading={roles.isPending}
          error={roles.error}
          empty={roles.data?.length === 0 && "No business roles yet."}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Role</TableHead>
                <TableHead>Entitlements</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {roles.data?.map((r) => (
                <TableRow key={r.id} className="align-top">
                  <TableCell>
                    <div className="font-medium">{r.name}</div>
                    {r.description && <div className="text-slate-500">{r.description}</div>}
                  </TableCell>
                  <TableCell>
                    {r.entitlements.length === 0
                      ? "—"
                      : r.entitlements.map((e) => `${e.name} (${e.application_name})`).join(", ")}
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

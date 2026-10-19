import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { discoverVaultAccounts, type VaultFinding } from "@/lib/vault";

const reasonLabels = {
  privileged: "Privileged access",
  orphan: "Nobody's",
  service: "Service account",
};

export function Discovery({ admin, onAdd }: { admin: boolean; onAdd: (f: VaultFinding) => void }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["vault-discovery"],
    queryFn: discoverVaultAccounts,
  });
  return (
    <section className="flex flex-col gap-3">
      <p className="text-sm text-slate-500">
        Accounts in your applications that hold high or critical access, belong to nobody, or belong
        to a service. Grantra finds them in what it last read from each application.
      </p>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 && "Nothing found. Connect and read applications to find accounts."
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Account</TableHead>
              <TableHead>Why</TableHead>
              <TableHead>Belongs to</TableHead>
              <TableHead className="relative">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((f) => (
              <TableRow key={f.account_id} className="align-top">
                <TableCell>
                  <div className="font-medium">{f.username || f.native_id}</div>
                  <div className="text-slate-500">{f.application_name}</div>
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {f.reasons.map((r) => (
                      <Badge key={r} variant={r === "privileged" ? "red" : "amber"}>
                        {reasonLabels[r]}
                      </Badge>
                    ))}
                  </div>
                  {f.entitlements.length > 0 && (
                    <div className="text-slate-500">{f.entitlements.join(", ")}</div>
                  )}
                </TableCell>
                <TableCell>
                  {f.identity_id ? (
                    <Link className="underline" to={`/people/${f.identity_id}`}>
                      {f.identity_name}
                    </Link>
                  ) : (
                    <span className="text-slate-500">Nobody</span>
                  )}
                </TableCell>
                <TableCell className="text-right">
                  {f.vault_account_id ? (
                    <Badge variant="green">In the vault</Badge>
                  ) : (
                    admin && (
                      <Button size="sm" variant="outline" onClick={() => onAdd(f)}>
                        Add to vault
                      </Button>
                    )
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </section>
  );
}

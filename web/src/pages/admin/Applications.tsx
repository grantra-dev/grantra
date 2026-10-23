import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button-variants";
import { Manage } from "@/components/Manage";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { connectorLabels, listCatalogApplications } from "@/lib/admin";

export function Applications() {
  const { data, error, isPending } = useQuery({
    queryKey: ["catalog-applications"],
    queryFn: listCatalogApplications,
  });
  const apps = data?.filter((a) => a.connector !== "grantra");
  return (
    <div className="flex flex-col gap-4">
      <Manage>
        <div>
          <Link to="/governance/applications/new" className={buttonVariants()}>
            Add application
          </Link>
        </div>
      </Manage>
      <Loaded
        loading={isPending}
        error={error}
        empty={apps?.length === 0 && "No applications yet. Add the first one above."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Application</TableHead>
              <TableHead>Owner</TableHead>
              <TableHead>Connector</TableHead>
              <TableHead>Provisioning</TableHead>
              <TableHead>Entitlements</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {apps?.map((a) => (
              <TableRow key={a.id}>
                <TableCell>
                  <Link
                    to={`/governance/applications/${a.id}`}
                    className="font-medium text-brand-700 hover:underline"
                  >
                    {a.name}
                  </Link>
                </TableCell>
                <TableCell>
                  {a.owner_name ?? <span className="text-slate-500">Nobody yet</span>}
                </TableCell>
                <TableCell>{connectorLabels[a.connector] ?? a.connector}</TableCell>
                <TableCell>
                  {a.retired_at ? (
                    <Badge>Retired</Badge>
                  ) : a.provisioning?.mode === "on" ? (
                    <Badge variant="green">On</Badge>
                  ) : (
                    <Badge>Off, read only</Badge>
                  )}
                </TableCell>
                <TableCell>{a.entitlement_count}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}

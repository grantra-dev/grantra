import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { FormActions } from "@/components/FormActions";
import { PickPerson, type Picked } from "@/components/PersonSearch";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Manage } from "@/components/Manage";
import { Input, Label } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { createApplication, connectorLabels, listCatalogApplications } from "@/lib/admin";

export function Applications() {
  const { data, error, isPending } = useQuery({
    queryKey: ["catalog-applications"],
    queryFn: listCatalogApplications,
  });
  const apps = data?.filter((a) => a.connector !== "grantra");
  return (
    <div className="flex flex-col gap-4">
      <Manage>
        <NewApplication />
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

function NewApplication() {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [owner, setOwner] = useState<Picked>();
  const create = useMutation({
    mutationFn: () => createApplication({ name, owner_id: owner?.id }),
    onSuccess: () => {
      setName("");
      setOwner(undefined);
      void client.invalidateQueries({ queryKey: ["catalog-applications"] });
    },
  });
  return (
    <Card>
      <CardContent className="pt-5">
        <form
          className="flex flex-wrap items-start gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div className="w-full max-w-xs">
            <Label htmlFor="app-name">New application</Label>
            <Input
              id="app-name"
              required
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Salesforce"
            />
          </div>
          <div className="w-full max-w-xs">
            <Label htmlFor="app-owner">Owner</Label>
            <PickPerson
              id="app-owner"
              value={owner}
              onChange={setOwner}
              placeholder="Who answers for it"
            />
          </div>
          <FormActions action={create} submit="Add application" className="w-full" />
          <p className="w-full text-sm text-slate-500">
            The owner approves requests for its access under the default approval flow. To read its
            accounts, connect it from Settings, Connectors, or from its own page once it exists.
          </p>
        </form>
      </CardContent>
    </Card>
  );
}

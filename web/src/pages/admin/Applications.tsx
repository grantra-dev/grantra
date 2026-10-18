import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
      <NewApplication />
      <Loaded
        loading={isPending}
        error={error}
        empty={apps?.length === 0 && "No applications yet. Add the first one above."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Application</TableHead>
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
                <TableCell>{connectorLabels[a.connector] ?? a.connector}</TableCell>
                <TableCell>
                  {a.provisioning?.mode === "on" ? (
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
  const create = useMutation({
    mutationFn: () => createApplication({ name }),
    onSuccess: () => {
      setName("");
      void client.invalidateQueries({ queryKey: ["catalog-applications"] });
    },
  });
  return (
    <Card>
      <CardContent className="pt-5">
        <form
          className="flex flex-wrap items-end gap-2"
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
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Salesforce"
            />
          </div>
          <Button type="submit" disabled={create.isPending}>
            Add application
          </Button>
          <p className="w-full text-sm text-slate-500">
            Connectors (how Grantra reads an application) hold secret references and are set up
            through the API; their settings show on each application.
          </p>
          {create.error && (
            <Alert variant="error" className="w-full">
              {create.error.message}
            </Alert>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { convertToEligibility, listStandingAccess, type Standing } from "@/lib/jit";
import { formatDate } from "@/lib/utils";

/**
 * Who holds high and critical risk access with no end date, and a way to
 * turn it into eligibility so nobody keeps privileges they are not using.
 */
export function StandingAccess() {
  const { data, error, isPending } = useQuery({
    queryKey: ["standing-access"],
    queryFn: listStandingAccess,
  });
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          High and critical risk access held with no end date. Converting it removes the access and
          makes the person eligible instead: they activate it for a few hours when they need it.
          Birthright access comes from a rule; change the rule instead.
        </CardContent>
      </Card>
      <Loaded
        loading={isPending}
        error={error}
        empty={data?.length === 0 && "Nobody holds privileged access for good. Nice."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Person</TableHead>
              <TableHead>Access</TableHead>
              <TableHead>Risk</TableHead>
              <TableHead>Since</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((s) => (
              <Row key={s.assignment_id} s={s} />
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}

function Row({ s }: { s: Standing }) {
  const client = useQueryClient();
  const convert = useMutation({
    mutationFn: () => convertToEligibility(s.assignment_id),
    onSuccess: () => client.invalidateQueries({ queryKey: ["standing-access"] }),
  });
  return (
    <TableRow>
      <TableCell>
        <Link
          to={`/people/${s.identity_id}`}
          className="font-medium text-brand-700 hover:underline"
        >
          {s.identity_name}
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{s.name}</span>
          {s.jit_only && <Badge variant="amber">Just in time only</Badge>}
        </div>
        <div className="text-slate-500">
          {s.type === "role" ? "Role" : s.application || "Entitlement"}
          {s.rule_name && <> · birthright rule {s.rule_name}</>}
        </div>
      </TableCell>
      <TableCell>
        <RiskBadge risk={s.risk} />
      </TableCell>
      <TableCell className="whitespace-nowrap">{formatDate(s.granted_at)}</TableCell>
      <TableCell className="text-right">
        {s.convertible ? (
          <Button
            size="sm"
            variant="outline"
            onClick={() => convert.mutate()}
            disabled={convert.isPending}
          >
            Make it just in time
          </Button>
        ) : (
          <span className="text-slate-500">Change the rule</span>
        )}
        {convert.error && (
          <Alert variant="error" className="mt-2 text-left">
            {convert.error.message}
          </Alert>
        )}
      </TableCell>
    </TableRow>
  );
}
